package cli

import (
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/moonfruit/sing-router/internal/config"
)

const (
	chainSingBox     = "sing-box"
	chainSingBoxMark = "sing-box-mark"
	chainSingBoxDNS  = "sing-box-dns"
)

// runReadOnly is a function variable so tests can stub it out.
// Returns (stdout, exitCode, err). exitCode == -1 表示命令未找到或启动失败。
var runReadOnly = func(ctx context.Context, name string, args ...string) (string, int, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	var stdout, stderr strings.Builder
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	if err == nil {
		return stdout.String(), 0, nil
	}
	if ee, ok := err.(*exec.ExitError); ok {
		msg := strings.TrimSpace(stderr.String())
		return stdout.String(), ee.ExitCode(), fmt.Errorf("%s: %s", ee.String(), msg)
	}
	return "", -1, err
}

func runCmd(name string, args ...string) (string, int, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return runReadOnly(ctx, name, args...)
}

// ----------------------- 数据结构 -----------------------

type ipRuleLine struct {
	Prio   int
	From   string // "all" 或 CIDR
	To     string
	IIf    string
	OIf    string
	FwMark string // "0x7892" 或 ""
	Action string // "lookup main" / "lookup 111" / "goto N"
	Raw    string
}

type ipRouteLine struct {
	Dest string // "default" 或 CIDR
	Dev  string
	Raw  string
}

type iptRule struct {
	Index  int    // 1-based 在该 chain 内的位置
	Proto  string // "tcp" / "udp" / "all"
	DPort  string // "53" 或 "22,80,443" 或 ""
	DAddr  string // "28.0.0.0/8" 或 ""
	OIf    string // "-o utun" 出接口
	Target string // "ACCEPT" / "RETURN" / "REJECT" / "sing-box" 等
	Spec   string // 原始 -A 行
}

type interfererTarget struct {
	Proto string
	DPort string
	DAddr string
}

// ----------------------- 解析器 -----------------------

func parseIPRules(out string) []ipRuleLine {
	var rules []ipRuleLine
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		colon := strings.Index(line, ":")
		if colon <= 0 {
			continue
		}
		prio, err := strconv.Atoi(strings.TrimSpace(line[:colon]))
		if err != nil {
			continue
		}
		rest := strings.TrimSpace(line[colon+1:])
		r := ipRuleLine{Prio: prio, Raw: line, From: "all"}
		toks := strings.Fields(rest)
		for i := 0; i < len(toks); i++ {
			t := toks[i]
			switch t {
			case "from":
				if i+1 < len(toks) {
					r.From = toks[i+1]
					i++
				}
			case "to":
				if i+1 < len(toks) {
					r.To = toks[i+1]
					i++
				}
			case "iif":
				if i+1 < len(toks) {
					r.IIf = toks[i+1]
					i++
				}
			case "oif":
				if i+1 < len(toks) {
					r.OIf = toks[i+1]
					i++
				}
			case "fwmark":
				if i+1 < len(toks) {
					fm := toks[i+1]
					if slash := strings.Index(fm, "/"); slash > 0 {
						fm = fm[:slash]
					}
					r.FwMark = fm
					i++
				}
			case "lookup", "goto":
				if i+1 < len(toks) {
					r.Action = t + " " + toks[i+1]
					i++
				}
			}
		}
		rules = append(rules, r)
	}
	return rules
}

func parseIPRoutes(out string) []ipRouteLine {
	var routes []ipRouteLine
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		toks := strings.Fields(line)
		if len(toks) == 0 {
			continue
		}
		r := ipRouteLine{Dest: toks[0], Raw: line}
		for i := 1; i < len(toks); i++ {
			if toks[i] == "dev" && i+1 < len(toks) {
				r.Dev = toks[i+1]
				break
			}
		}
		routes = append(routes, r)
	}
	return routes
}

// parseIptablesS 解析 `iptables -S CHAIN` 输出。
// 跳过 `-N`/`-P` 行；每条 `-A CHAIN ...` 生成一条记录，Index 自 1 起。
func parseIptablesS(out string) []iptRule {
	var rules []iptRule
	idx := 0
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "-A ") {
			continue
		}
		idx++
		toks := strings.Fields(line)
		// toks[0]="-A", toks[1]=<chain>，从 toks[2] 起是规则参数。
		r := iptRule{Index: idx, Spec: line, Proto: "all"}
		for i := 2; i < len(toks); i++ {
			t := toks[i]
			switch t {
			case "-p":
				if i+1 < len(toks) {
					r.Proto = toks[i+1]
					i++
				}
			case "-d":
				if i+1 < len(toks) {
					r.DAddr = toks[i+1]
					i++
				}
			case "--dport", "--dports":
				if i+1 < len(toks) {
					r.DPort = toks[i+1]
					i++
				}
			case "-o":
				if i+1 < len(toks) {
					r.OIf = toks[i+1]
					i++
				}
			case "-j":
				if i+1 < len(toks) {
					r.Target = toks[i+1]
					i++
				}
			}
		}
		rules = append(rules, r)
	}
	return rules
}

// ----------------------- 端口/地址相交判定 -----------------------

type portSpec struct{ lo, hi int }

func parsePortSpec(s string) []portSpec {
	if s == "" {
		return nil
	}
	var specs []portSpec
	for _, part := range strings.Split(s, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		if i := strings.Index(part, ":"); i >= 0 {
			lo, err1 := strconv.Atoi(strings.TrimSpace(part[:i]))
			hi, err2 := strconv.Atoi(strings.TrimSpace(part[i+1:]))
			if err1 == nil && err2 == nil {
				specs = append(specs, portSpec{lo, hi})
			}
		} else {
			p, err := strconv.Atoi(part)
			if err == nil {
				specs = append(specs, portSpec{p, p})
			}
		}
	}
	return specs
}

// dportOverlap 两侧端口集合相交则返回 true。任一侧空（未限定）→ 视作 catch-all，相交。
func dportOverlap(a, b string) bool {
	if a == "" || b == "" {
		return true
	}
	sa := parsePortSpec(a)
	sb := parsePortSpec(b)
	if len(sa) == 0 || len(sb) == 0 {
		return true
	}
	for _, x := range sa {
		for _, y := range sb {
			if x.lo <= y.hi && y.lo <= x.hi {
				return true
			}
		}
	}
	return false
}

// cidrOverlap 两个 CIDR/单 IP 是否相交。任一侧空 → catch-all。
func cidrOverlap(a, b string) bool {
	if a == "" || b == "" {
		return true
	}
	na, nb := toCIDR(a), toCIDR(b)
	if na == nil || nb == nil {
		return true
	}
	return na.Contains(nb.IP) || nb.Contains(na.IP)
}

func toCIDR(s string) *net.IPNet {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil
	}
	if !strings.Contains(s, "/") {
		ip := net.ParseIP(s)
		if ip == nil {
			return nil
		}
		if v4 := ip.To4(); v4 != nil {
			return &net.IPNet{IP: v4, Mask: net.CIDRMask(32, 32)}
		}
		return &net.IPNet{IP: ip, Mask: net.CIDRMask(128, 128)}
	}
	_, n, err := net.ParseCIDR(s)
	if err != nil {
		return nil
	}
	return n
}

func protoMatch(a, b string) bool {
	if a == "" || a == "all" || b == "" || b == "all" {
		return true
	}
	return a == b
}

// findInterferers 返回 prior 中可能拦截 ours 流量的规则。
func findInterferers(prior []iptRule, ours interfererTarget) []iptRule {
	blocking := map[string]bool{
		"ACCEPT": true, "RETURN": true, "DROP": true, "REJECT": true,
		"DNAT": true, "SNAT": true, "MASQUERADE": true, "REDIRECT": true,
	}
	var hits []iptRule
	for _, r := range prior {
		if !blocking[r.Target] {
			continue
		}
		if !protoMatch(r.Proto, ours.Proto) {
			continue
		}
		if !dportOverlap(r.DPort, ours.DPort) {
			continue
		}
		if !cidrOverlap(r.DAddr, ours.DAddr) {
			continue
		}
		hits = append(hits, r)
	}
	return hits
}

// ----------------------- 顶层入口 -----------------------

// tunExists 判断 TUN 接口当前是否存在；用作「sing-box 是否运行」的近似信号。
func tunExists(tun string) bool {
	_, code, _ := runCmd("ip", "link", "show", tun)
	return code == 0
}

// checkRouting 跑全套运行时网络规则检查；非 root 则跳过。
func checkRouting(r config.Routing) []doctorCheck {
	if os.Geteuid() != 0 {
		return []doctorCheck{{
			Name:   "routing checks",
			Status: "info",
			Detail: "skipped (need root for iptables -S)",
		}}
	}
	var out []doctorCheck
	out = append(out, checkTunDevice(r.Tun)...)
	out = append(out, checkIPRule(r)...)
	out = append(out, checkIPRoute(r)...)
	out = append(out, checkIptablesChains(r)...)
	out = append(out, checkIp6tablesDNS()...)
	return out
}

// ----------------------- 各项检查 -----------------------

func checkTunDevice(tun string) []doctorCheck {
	out, code, err := runCmd("ip", "link", "show", tun)
	if code == -1 {
		return []doctorCheck{{Name: "ip link " + tun, Status: "warn", Detail: "ip command unavailable: " + err.Error()}}
	}
	if code != 0 {
		return []doctorCheck{{Name: "ip link " + tun, Status: "fail", Detail: tun + " not present (sing-box not running?)"}}
	}
	if strings.Contains(out, "state DOWN") {
		return []doctorCheck{{Name: "ip link " + tun, Status: "warn", Detail: "interface is DOWN"}}
	}
	return []doctorCheck{{Name: "ip link " + tun, Status: "pass"}}
}

func checkIPRule(r config.Routing) []doctorCheck {
	out, code, err := runCmd("ip", "rule", "list")
	if code == -1 {
		return []doctorCheck{{Name: "ip rule", Status: "warn", Detail: "ip command unavailable: " + err.Error()}}
	}
	if code != 0 {
		return []doctorCheck{{Name: "ip rule", Status: "fail", Detail: fmt.Sprintf("ip rule list exit %d", code)}}
	}
	rules := parseIPRules(out)
	wantAction := fmt.Sprintf("lookup %d", r.RouteTable)
	var ours *ipRuleLine
	for i := range rules {
		if rules[i].FwMark == r.RouteMark && rules[i].Action == wantAction {
			ours = &rules[i]
			break
		}
	}
	if ours == nil {
		return []doctorCheck{{
			Name:   "ip rule fwmark " + r.RouteMark,
			Status: "fail",
			Detail: fmt.Sprintf("rule `fwmark %s lookup %d` not found", r.RouteMark, r.RouteTable),
		}}
	}
	var checks []doctorCheck
	checks = append(checks, doctorCheck{
		Name:   fmt.Sprintf("ip rule fwmark %s → table %d", r.RouteMark, r.RouteTable),
		Status: "pass",
		Detail: fmt.Sprintf("prio %d", ours.Prio),
	})
	if ours.Prio > 32766 {
		checks = append(checks, doctorCheck{
			Name:   "ip rule position",
			Status: "fail",
			Detail: fmt.Sprintf("prio %d after main(32766); rule will never be hit", ours.Prio),
		})
	}
	for _, p := range rules {
		if p.Prio >= ours.Prio {
			continue
		}
		if p.Action == "lookup local" {
			continue // 内核默认，跳过
		}
		if p.FwMark == r.RouteMark {
			checks = append(checks, doctorCheck{
				Name:   fmt.Sprintf("ip rule prio %d", p.Prio),
				Status: "fail",
				Detail: "duplicate fwmark " + r.RouteMark + " rule earlier; preempts our " + p.Action,
			})
			continue
		}
		if p.From == "all" && p.FwMark == "" && p.IIf == "" && p.OIf == "" && p.To == "" &&
			(strings.HasPrefix(p.Action, "lookup ") || strings.HasPrefix(p.Action, "goto ")) {
			checks = append(checks, doctorCheck{
				Name:   fmt.Sprintf("ip rule prio %d", p.Prio),
				Status: "warn",
				Detail: fmt.Sprintf("from all %s — may catch fwmark traffic before ours", p.Action),
			})
		}
	}
	return checks
}

func checkIPRoute(r config.Routing) []doctorCheck {
	out, code, err := runCmd("ip", "route", "show", "table", strconv.Itoa(r.RouteTable))
	if code == -1 {
		return []doctorCheck{{Name: "ip route", Status: "warn", Detail: "ip command unavailable: " + err.Error()}}
	}
	if code != 0 {
		return []doctorCheck{{Name: fmt.Sprintf("ip route table %d", r.RouteTable), Status: "fail", Detail: fmt.Sprintf("exit %d", code)}}
	}
	routes := parseIPRoutes(out)
	var checks []doctorCheck
	foundDefault := false
	for _, rt := range routes {
		if rt.Dest == "default" && rt.Dev == r.Tun {
			foundDefault = true
			break
		}
	}
	if foundDefault {
		checks = append(checks, doctorCheck{
			Name:   fmt.Sprintf("ip route table %d default", r.RouteTable),
			Status: "pass",
			Detail: "dev " + r.Tun,
		})
	} else {
		checks = append(checks, doctorCheck{
			Name:   fmt.Sprintf("ip route table %d default", r.RouteTable),
			Status: "fail",
			Detail: fmt.Sprintf("expected `default dev %s`; table empty or wrong dev", r.Tun),
		})
	}
	for _, rt := range routes {
		if rt.Dest == "default" {
			continue
		}
		if rt.Dev != "" && rt.Dev != r.Tun {
			checks = append(checks, doctorCheck{
				Name:   fmt.Sprintf("ip route table %d shadow", r.RouteTable),
				Status: "warn",
				Detail: fmt.Sprintf("%s dev %s — bypasses TUN for that destination", rt.Dest, rt.Dev),
			})
		}
	}
	return checks
}

type expectedJump struct {
	Table  string
	Chain  string
	Target string
	Proto  string
	DPort  string
	DAddr  string
	OIf    string
	Label  string
}

func expectedJumps(r config.Routing) []expectedJump {
	return []expectedJump{
		{
			Table: "nat", Chain: "PREROUTING", Target: chainSingBox,
			Proto: "tcp", DPort: r.ProxyPorts,
			Label: fmt.Sprintf("nat/PREROUTING -p tcp --dports %s -j %s", r.ProxyPorts, chainSingBox),
		},
		{
			Table: "nat", Chain: "PREROUTING", Target: chainSingBox,
			Proto: "tcp", DAddr: r.FakeIP,
			Label: fmt.Sprintf("nat/PREROUTING -p tcp -d %s -j %s", r.FakeIP, chainSingBox),
		},
		{
			Table: "nat", Chain: "PREROUTING", Target: chainSingBoxDNS,
			Proto: "tcp", DPort: "53",
			Label: fmt.Sprintf("nat/PREROUTING -p tcp --dport 53 -j %s", chainSingBoxDNS),
		},
		{
			Table: "nat", Chain: "PREROUTING", Target: chainSingBoxDNS,
			Proto: "udp", DPort: "53",
			Label: fmt.Sprintf("nat/PREROUTING -p udp --dport 53 -j %s", chainSingBoxDNS),
		},
		{
			Table: "mangle", Chain: "PREROUTING", Target: chainSingBoxMark,
			Proto: "udp", DPort: r.ProxyPorts,
			Label: fmt.Sprintf("mangle/PREROUTING -p udp --dports %s -j %s", r.ProxyPorts, chainSingBoxMark),
		},
		{
			Table: "mangle", Chain: "PREROUTING", Target: chainSingBoxMark,
			Proto: "udp", DAddr: r.FakeIP,
			Label: fmt.Sprintf("mangle/PREROUTING -p udp -d %s -j %s", r.FakeIP, chainSingBoxMark),
		},
		{
			Table: "filter", Chain: "FORWARD", Target: "ACCEPT",
			OIf:   r.Tun,
			Label: fmt.Sprintf("filter/FORWARD -o %s -j ACCEPT", r.Tun),
		},
	}
}

func matchJump(rule iptRule, want expectedJump) bool {
	if rule.Target != want.Target {
		return false
	}
	if want.Proto != "" && rule.Proto != want.Proto {
		return false
	}
	if want.DPort != "" && rule.DPort != want.DPort {
		return false
	}
	if want.DAddr != "" && rule.DAddr != want.DAddr {
		return false
	}
	if want.OIf != "" && rule.OIf != want.OIf {
		return false
	}
	return true
}

func checkIptablesChains(r config.Routing) []doctorCheck {
	var checks []doctorCheck
	// 三大父链按 table 缓存，避免重复调用 iptables -S。
	type key struct{ table, chain string }
	cache := map[key][]iptRule{}
	loadChain := func(table, chain string) ([]iptRule, int, error) {
		k := key{table, chain}
		if rules, ok := cache[k]; ok {
			return rules, 0, nil
		}
		out, code, err := runCmd("iptables", "-t", table, "-S", chain)
		if code != 0 {
			return nil, code, err
		}
		rules := parseIptablesS(out)
		cache[k] = rules
		return rules, 0, nil
	}

	for _, want := range expectedJumps(r) {
		rules, code, err := loadChain(want.Table, want.Chain)
		if code == -1 {
			checks = append(checks, doctorCheck{
				Name:   "iptables " + want.Label,
				Status: "warn",
				Detail: "iptables unavailable: " + err.Error(),
			})
			continue
		}
		if code != 0 {
			checks = append(checks, doctorCheck{
				Name:   "iptables " + want.Label,
				Status: "fail",
				Detail: fmt.Sprintf("iptables -t %s -S %s exit %d", want.Table, want.Chain, code),
			})
			continue
		}
		var found []iptRule
		for _, rl := range rules {
			if matchJump(rl, want) {
				found = append(found, rl)
			}
		}
		if len(found) == 0 {
			checks = append(checks, doctorCheck{
				Name:   "iptables " + want.Label,
				Status: "fail",
				Detail: "jump rule not found",
			})
			continue
		}
		first := found[0]
		checks = append(checks, doctorCheck{
			Name:   "iptables " + want.Label,
			Status: "pass",
			Detail: fmt.Sprintf("line %d", first.Index),
		})
		if len(found) > 1 {
			lines := make([]string, len(found))
			for i, f := range found {
				lines[i] = strconv.Itoa(f.Index)
			}
			checks = append(checks, doctorCheck{
				Name:   "iptables " + want.Label + " duplicate",
				Status: "warn",
				Detail: "appears at lines " + strings.Join(lines, ",") + " — startup may have run multiple times without cleanup",
			})
		}
		// 干扰扫描（仅 PREROUTING / FORWARD）
		var prior []iptRule
		for _, p := range rules {
			if p.Index < first.Index {
				prior = append(prior, p)
			}
		}
		target := interfererTarget{Proto: want.Proto, DPort: want.DPort, DAddr: want.DAddr}
		for _, h := range findInterferers(prior, target) {
			if h.Target == want.Target {
				continue // 同 target 已通过 duplicate 警告处理
			}
			checks = append(checks, doctorCheck{
				Name:   fmt.Sprintf("iptables %s/%s line %d", want.Table, want.Chain, h.Index),
				Status: "warn",
				Detail: fmt.Sprintf("%s %s before our %s (line %d) may swallow proxied traffic [%s]",
					h.Target, h.Proto, want.Target, first.Index, strings.TrimPrefix(h.Spec, "-A "+want.Chain+" ")),
			})
		}
	}

	// 子链存在性 + 规则数下界
	for _, sc := range []struct {
		Table, Chain string
		MinRules     int
	}{
		{"nat", chainSingBox, 5},
		{"nat", chainSingBoxDNS, 3},
		{"mangle", chainSingBoxMark, 5},
	} {
		out, code, _ := runCmd("iptables", "-t", sc.Table, "-S", sc.Chain)
		if code == -1 {
			checks = append(checks, doctorCheck{
				Name:   "iptables " + sc.Table + "/" + sc.Chain,
				Status: "warn",
				Detail: "iptables unavailable",
			})
			continue
		}
		if code != 0 {
			checks = append(checks, doctorCheck{
				Name:   "iptables " + sc.Table + "/" + sc.Chain,
				Status: "fail",
				Detail: "chain not created (startup.sh may have failed)",
			})
			continue
		}
		rules := parseIptablesS(out)
		if len(rules) < sc.MinRules {
			checks = append(checks, doctorCheck{
				Name:   "iptables " + sc.Table + "/" + sc.Chain,
				Status: "warn",
				Detail: fmt.Sprintf("%d rules; expected ≥%d (startup.sh may have stopped early)", len(rules), sc.MinRules),
			})
		} else {
			checks = append(checks, doctorCheck{
				Name:   "iptables " + sc.Table + "/" + sc.Chain,
				Status: "pass",
				Detail: fmt.Sprintf("%d rules", len(rules)),
			})
		}
	}
	return checks
}

func checkIp6tablesDNS() []doctorCheck {
	out, code, err := runCmd("ip6tables", "-t", "filter", "-S", "INPUT")
	if code == -1 {
		return []doctorCheck{{Name: "ip6tables", Status: "warn", Detail: "ip6tables unavailable: " + err.Error()}}
	}
	if code != 0 {
		return []doctorCheck{{Name: "ip6tables INPUT", Status: "fail", Detail: fmt.Sprintf("ip6tables -S INPUT exit %d", code)}}
	}
	rules := parseIptablesS(out)
	var checks []doctorCheck
	for _, proto := range []string{"tcp", "udp"} {
		var ours *iptRule
		for i := range rules {
			rl := rules[i]
			if rl.Target == "REJECT" && rl.Proto == proto && rl.DPort == "53" {
				ours = &rl
				break
			}
		}
		if ours == nil {
			checks = append(checks, doctorCheck{
				Name:   "ip6tables INPUT REJECT 53/" + proto,
				Status: "fail",
				Detail: "missing — IPv6 DNS could leak",
			})
			continue
		}
		checks = append(checks, doctorCheck{
			Name:   "ip6tables INPUT REJECT 53/" + proto,
			Status: "pass",
			Detail: fmt.Sprintf("line %d", ours.Index),
		})
		var prior []iptRule
		for _, p := range rules {
			if p.Index < ours.Index {
				prior = append(prior, p)
			}
		}
		target := interfererTarget{Proto: proto, DPort: "53"}
		for _, h := range findInterferers(prior, target) {
			if h.Target == "REJECT" {
				continue
			}
			checks = append(checks, doctorCheck{
				Name:   fmt.Sprintf("ip6tables INPUT line %d", h.Index),
				Status: "warn",
				Detail: fmt.Sprintf("%s %s before our REJECT 53/%s — may bypass IPv6 DNS fallback [%s]",
					h.Target, h.Proto, proto, strings.TrimPrefix(h.Spec, "-A INPUT ")),
			})
		}
	}
	return checks
}
