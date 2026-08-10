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

// checkBypassSets 巡检 LAN 客户端 bypass 的三个 ipset。只读。
//
// 只管 set 本身——引用它们的 RETURN 规则由 checkIptablesChains 在遍历
// nat/sing-box 等子链时就地检查（见 checkBypassReturns），这样输出里
// "链有哪些规则" 与 "bypass RETURN 装在链的哪一行" 是挨着的，不用在
// 报告末尾另起一段回头找上下文。
func checkBypassSets(b config.Bypass) []doctorCheck {
	if !b.Enabled {
		return []doctorCheck{{
			Name:   "ipset client_bypass*",
			Status: "info",
			Detail: "disabled ([bypass].enabled = false)",
		}}
	}
	out := make([]doctorCheck, 0, len(bypassSets))
	for _, spec := range bypassSets {
		out = append(out, checkBypassSet(spec, b))
	}
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
	detail := fmt.Sprintf("%s [%s]", plural(len(entries), "entry", "entries"), spec.role)
	if len(entries) > 0 {
		detail += ": " + strings.Join(entries, ", ")
	}
	return doctorCheck{Name: name, Status: "pass", Detail: detail}
}

// checkBypassReturns 检查单条子链里的 bypass RETURN 规则，输出格式与
// checkIptablesChains 的其余条目一致（`iptables <table>/<chain> <spec>` +
// `line N`），调用方已把该链 `iptables -S` 的解析结果传进来，不重复取。
//
// 每个"按配置应当存在"的 set 一条 RETURN（startup.sh 的
// add_client_bypass_returns 就是这么装的：一条规则只能引用一个 set）。
// 除了存在性，还要确认 RETURN 排在该链的终结规则（REDIRECT / MARK）之前
// ——装在其后等于完全不生效，而从客户端表现上完全看不出来（只是"没被
// 放行"），必须靠这段位置判定兜底。
func checkBypassReturns(table, chain string, rules []iptRule, b config.Bypass) []doctorCheck {
	terminal := -1
	for _, rl := range rules {
		if rl.Target == "REDIRECT" || rl.Target == "MARK" {
			terminal = rl.Index
			break
		}
	}
	var out []doctorCheck
	for _, spec := range bypassSets {
		if !spec.expected(b) {
			continue
		}
		// 尾部的 " src" 不能省：否则 client_bypass 会误匹配 client_bypass_static。
		match := "--match-set " + spec.name + " src"
		label := fmt.Sprintf("iptables %s/%s -m set %s -j RETURN", table, chain, match)
		var ours *iptRule
		for i := range rules {
			if rules[i].Target == "RETURN" && strings.Contains(rules[i].Spec, match) {
				ours = &rules[i]
				break
			}
		}
		if ours == nil {
			out = append(out, doctorCheck{Name: label, Status: "fail",
				Detail: fmt.Sprintf("rule not found (%s)", spec.role)})
			continue
		}
		switch {
		case terminal < 0:
			// 链里没有终结规则，"RETURN 在其前面"这个断言无从谈起；链本身是否
			// 完整由上面的规则数下界负责，这里只如实报告"无法判断顺序"。
			out = append(out, doctorCheck{Name: label, Status: "warn",
				Detail: fmt.Sprintf("line %d, but chain has no terminal rule (REDIRECT/MARK); "+
					"cannot judge RETURN's relative position", ours.Index)})
		case ours.Index > terminal:
			out = append(out, doctorCheck{Name: label, Status: "fail",
				Detail: fmt.Sprintf("line %d is after the terminal rule at line %d; it will never be reached",
					ours.Index, terminal)})
		default:
			out = append(out, doctorCheck{Name: label, Status: "pass",
				Detail: fmt.Sprintf("line %d", ours.Index)})
		}
	}
	return out
}

// parseIpsetListEntries 提取 `ipset list` 输出中 Members: 之后的非空行。
func parseIpsetListEntries(out string) []string {
	var entries []string
	inMembers := false
	for line := range strings.SplitSeq(out, "\n") {
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
