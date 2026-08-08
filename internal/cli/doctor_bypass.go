package cli

import (
	"fmt"
	"strings"

	"github.com/moonfruit/sing-router/internal/config"
)

// bypassSets 是三个 set 与各自"无配置时不该存在"的判断依据。
// 动态 set 只要功能启用就必须存在；两个静态 set 仅在配置非空时才创建。
type bypassSetSpec struct {
	name     string
	expected func(config.Bypass) bool
	role     string
}

var bypassSets = []bypassSetSpec{
	{"client_bypass", func(config.Bypass) bool { return true }, "dynamic leases"},
	{"client_bypass_static", func(b config.Bypass) bool { return len(b.StaticIPs) > 0 }, "static IPs"},
	{"client_bypass_mac", func(b config.Bypass) bool { return len(b.StaticMACs) > 0 }, "static MACs"},
}

// checkClientBypass 巡检 LAN 客户端 bypass 的 ipset 与链规则。只读。
func checkClientBypass(b config.Bypass) []doctorCheck {
	if !b.Enabled {
		return []doctorCheck{{
			Name:   "client bypass",
			Status: "info",
			Detail: "disabled ([bypass].enabled = false)",
		}}
	}
	var out []doctorCheck
	for _, spec := range bypassSets {
		out = append(out, checkBypassSet(spec, b))
	}
	out = append(out, checkBypassChainRules()...)
	return out
}

func checkBypassSet(spec bypassSetSpec, b config.Bypass) doctorCheck {
	name := "ipset " + spec.name
	out, code, err := runCmd("ipset", "list", spec.name)
	want := spec.expected(b)
	// code == -1：ipset 命令本身跑不起来（未安装/PATH 里没有），与"set 不存在"
	// 是两回事——照抄 checkIptablesChains 的约定，不能把工具缺失误判成
	// "符合预期的不存在"，否则会把真正的环境问题吞成一条 pass。
	if code == -1 {
		return doctorCheck{Name: name, Status: "warn",
			Detail: "ipset unavailable: " + err.Error()}
	}
	if code != 0 {
		if !want {
			return doctorCheck{Name: name, Status: "pass",
				Detail: fmt.Sprintf("absent as expected (no %s configured)", spec.role)}
		}
		return doctorCheck{Name: name, Status: "fail",
			Detail: fmt.Sprintf("missing (%s); is startup.sh installed?", spec.role)}
	}
	entries := parseIpsetListEntries(out)
	if !want {
		return doctorCheck{Name: name, Status: "warn",
			Detail: fmt.Sprintf("exists but no %s configured; stale set from an earlier config", spec.role)}
	}
	detail := fmt.Sprintf("%d entries [%s]", len(entries), spec.role)
	if len(entries) > 0 {
		detail += ": " + strings.Join(entries, ", ")
	}
	return doctorCheck{Name: name, Status: "pass", Detail: detail}
}

// checkBypassChainRules 确认三条链各有 client_bypass 的 RETURN，且位置在
// 该链的终结规则（REDIRECT / MARK）之前——装在其后等于完全不生效。
//
// 不接受 config.Bypass：动态 set 的 RETURN 只要功能启用就必须存在，与静态
// 配置无关，调用方已在 checkClientBypass 里判过 Enabled。
func checkBypassChainRules() []doctorCheck {
	targets := []struct{ table, chain string }{
		{"nat", chainSingBox},
		{"mangle", chainSingBoxMark},
		{"nat", chainSingBoxDNS},
	}
	var out []doctorCheck
	for _, tgt := range targets {
		name := fmt.Sprintf("%s/%s bypass RETURN", tgt.table, tgt.chain)
		listing, code, err := runCmd("iptables", "-t", tgt.table, "-S", tgt.chain)
		// 同上：区分"iptables 命令跑不起来"与"链本身取不到"，避免误导排查方向。
		if code == -1 {
			out = append(out, doctorCheck{Name: name, Status: "warn",
				Detail: "iptables unavailable: " + err.Error()})
			continue
		}
		if code != 0 {
			out = append(out, doctorCheck{Name: name, Status: "fail",
				Detail: "chain not found; sing-router rules not installed"})
			continue
		}
		returnAt, terminalAt := -1, -1
		for i, line := range strings.Split(listing, "\n") {
			if returnAt < 0 && strings.Contains(line, "--match-set client_bypass src") &&
				strings.Contains(line, "-j RETURN") {
				returnAt = i
			}
			if terminalAt < 0 && (strings.Contains(line, "-j REDIRECT") ||
				strings.Contains(line, "-j MARK")) {
				terminalAt = i
			}
		}
		switch {
		case returnAt < 0:
			out = append(out, doctorCheck{Name: name, Status: "fail",
				Detail: "no RETURN rule for the client_bypass set"})
		case terminalAt < 0:
			// 链里没有终结规则，"RETURN 在其前面"这个断言无从谈起；链本身是否
			// 完整由 checkIptablesChains 负责，这里只如实报告"无法判断顺序"。
			out = append(out, doctorCheck{Name: name, Status: "warn",
				Detail: "no terminal rule (REDIRECT/MARK) found in chain; cannot judge RETURN's " +
					"relative position, chain may not be fully installed"})
		case returnAt > terminalAt:
			out = append(out, doctorCheck{Name: name, Status: "fail",
				Detail: fmt.Sprintf("RETURN is at position %d, after the terminal rule at %d; "+
					"it will never be reached", returnAt, terminalAt)})
		default:
			out = append(out, doctorCheck{Name: name, Status: "pass",
				Detail: "RETURN installed ahead of the terminal rule"})
		}
	}
	return out
}

// parseIpsetListEntries 提取 `ipset list` 输出中 Members: 之后的非空行。
func parseIpsetListEntries(out string) []string {
	var entries []string
	inMembers := false
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if line == "Members:" {
			inMembers = true
			continue
		}
		if inMembers && line != "" {
			entries = append(entries, line)
		}
	}
	return entries
}
