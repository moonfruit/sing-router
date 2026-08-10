package cli

import (
	"fmt"
	"strings"
	"testing"

	"github.com/moonfruit/sing-router/internal/config"
)

func enabledBypass() config.Bypass {
	b := config.DefaultBypass()
	b.Enabled = true
	return b
}

func TestCheckBypassSetsDisabledYieldsInfo(t *testing.T) {
	checks := checkBypassSets(config.DefaultBypass())
	if len(checks) != 1 {
		t.Fatalf("expected a single info check, got %d", len(checks))
	}
	if checks[0].Status != "info" {
		t.Fatalf("status = %q, want info", checks[0].Status)
	}
	if !strings.Contains(checks[0].Detail, "disabled") {
		t.Fatalf("detail = %q", checks[0].Detail)
	}
}

func TestParseIpsetListEntries(t *testing.T) {
	out := "Name: client_bypass\nType: hash:ip\n" +
		"Header: family inet hashsize 1024 maxelem 65536 timeout 120\n" +
		"Number of entries: 2\nMembers:\n" +
		"192.168.50.80 timeout 118\n192.168.50.81 timeout 0\n"
	entries := parseIpsetListEntries(out)
	if len(entries) != 2 {
		t.Fatalf("entries = %v", entries)
	}
	if entries[0] != "192.168.50.80 timeout 118" {
		t.Fatalf("entry 0 = %q", entries[0])
	}
}

func TestParseIpsetListEntriesEmptySet(t *testing.T) {
	out := "Name: client_bypass\nNumber of entries: 0\nMembers:\n"
	if entries := parseIpsetListEntries(out); len(entries) != 0 {
		t.Fatalf("expected no entries, got %v", entries)
	}
}

// ----------------------- checkBypassReturns -----------------------
//
// 核心断言："RETURN 必须排在终结规则之前"——这正是本节存在的理由：顺序装反了
// 从客户端表现上完全看不出来（只是"没被放行"），必须靠这段位置判定兜底。

func TestCheckBypassReturns_ReturnBeforeTerminalPasses(t *testing.T) {
	rules := parseIptablesS(strings.Join([]string{
		"-N sing-box",
		"-A sing-box -m set --match-set client_bypass src -j RETURN",
		"-A sing-box -p tcp -s 192.168.50.0/24 -j REDIRECT --to-ports 7892",
	}, "\n"))
	checks := checkBypassReturns("nat", chainSingBox, rules, enabledBypass())
	if len(checks) != 1 {
		t.Fatalf("expected 1 check (only the dynamic set configured), got %+v", checks)
	}
	if checks[0].Status != "pass" || checks[0].Detail != "line 1" {
		t.Fatalf("check = %+v, want pass at line 1", checks[0])
	}
}

// 输出格式必须与 checkIptablesChains 的其余条目同构：`iptables <table>/<chain> ...`
// 打头，detail 是 `line N`——否则报告里 bypass 那几行看着像另一个工具吐的。
func TestCheckBypassReturns_LabelMatchesIptablesStyle(t *testing.T) {
	rules := parseIptablesS(strings.Join([]string{
		"-N sing-box-mark",
		"-A sing-box-mark -m set --match-set client_bypass src -j RETURN",
		"-A sing-box-mark -p udp -s 192.168.50.0/24 -j MARK --set-xmark 0x7892",
	}, "\n"))
	checks := checkBypassReturns("mangle", chainSingBoxMark, rules, enabledBypass())
	want := "iptables mangle/sing-box-mark -m set --match-set client_bypass src -j RETURN"
	if checks[0].Name != want {
		t.Fatalf("name = %q, want %q", checks[0].Name, want)
	}
}

func TestCheckBypassReturns_ReturnAfterTerminalFails(t *testing.T) {
	// 这条是最重要的回归用例：装反了顺序必须报 fail，否则整节检查失去意义。
	rules := parseIptablesS(strings.Join([]string{
		"-N sing-box",
		"-A sing-box -p tcp -s 192.168.50.0/24 -j REDIRECT --to-ports 7892",
		"-A sing-box -m set --match-set client_bypass src -j RETURN",
	}, "\n"))
	checks := checkBypassReturns("nat", chainSingBox, rules, enabledBypass())
	if checks[0].Status != "fail" {
		t.Fatalf("check = %+v, want fail", checks[0])
	}
	if !strings.Contains(checks[0].Detail, "never be reached") {
		t.Fatalf("detail = %q, want mention of unreachable RETURN", checks[0].Detail)
	}
}

func TestCheckBypassReturns_NoReturnFails(t *testing.T) {
	rules := parseIptablesS(strings.Join([]string{
		"-N sing-box",
		"-A sing-box -p tcp -s 192.168.50.0/24 -j REDIRECT --to-ports 7892",
	}, "\n"))
	checks := checkBypassReturns("nat", chainSingBox, rules, enabledBypass())
	if checks[0].Status != "fail" {
		t.Fatalf("check = %+v, want fail", checks[0])
	}
	if !strings.Contains(checks[0].Detail, "not found") {
		t.Fatalf("detail = %q", checks[0].Detail)
	}
}

func TestCheckBypassReturns_NoTerminalRuleWarns(t *testing.T) {
	// 链里只有 RETURN、没有终结规则：不能断言"在其前面"，也不该判 fail——
	// 链是否完整由子链规则数下界负责，这里只如实报告无法判断顺序。
	rules := parseIptablesS(strings.Join([]string{
		"-N sing-box",
		"-A sing-box -m set --match-set client_bypass src -j RETURN",
	}, "\n"))
	checks := checkBypassReturns("nat", chainSingBox, rules, enabledBypass())
	if checks[0].Status != "warn" {
		t.Fatalf("check = %+v, want warn", checks[0])
	}
	if !strings.Contains(checks[0].Detail, "no terminal rule") {
		t.Fatalf("detail = %q, want mention of missing terminal rule", checks[0].Detail)
	}
}

// 静态 set 只在配置非空时才被 startup.sh 装 RETURN，doctor 的期望必须同步：
// 配了 static_ips / static_macs 就必须有对应 RETURN，没配则一行都不该出。
func TestCheckBypassReturns_StaticSetsFollowConfig(t *testing.T) {
	rules := parseIptablesS(strings.Join([]string{
		"-N sing-box",
		"-A sing-box -m set --match-set client_bypass src -j RETURN",
		"-A sing-box -m set --match-set client_bypass_static src -j RETURN",
		"-A sing-box -p tcp -s 192.168.50.0/24 -j REDIRECT --to-ports 7892",
	}, "\n"))

	b := enabledBypass()
	if checks := checkBypassReturns("nat", chainSingBox, rules, b); len(checks) != 1 {
		t.Fatalf("no static config → only the dynamic set should be checked, got %+v", checks)
	}

	b.StaticIPs = []string{"192.168.50.9"}
	checks := checkBypassReturns("nat", chainSingBox, rules, b)
	if len(checks) != 2 {
		t.Fatalf("expected dynamic + static checks, got %+v", checks)
	}
	for _, c := range checks {
		if c.Status != "pass" {
			t.Fatalf("%s: status = %q, want pass", c.Name, c.Status)
		}
	}

	// 配了 MAC 但链里没装对应 RETURN → fail。
	b.StaticMACs = []string{"aa:bb:cc:dd:ee:ff"}
	checks = checkBypassReturns("nat", chainSingBox, rules, b)
	mac := findCheck(checks, "iptables nat/sing-box -m set --match-set client_bypass_mac src")
	if mac == nil || mac.Status != "fail" {
		t.Fatalf("check = %+v, want fail for missing MAC RETURN", mac)
	}
}

// `--match-set client_bypass src` 不能误匹配到 client_bypass_static 那条规则——
// 少了尾部的 " src" 就会，届时动态 set 缺失也会被报成 pass。
func TestCheckBypassReturns_DynamicSetNotMatchedByStaticRule(t *testing.T) {
	rules := parseIptablesS(strings.Join([]string{
		"-N sing-box",
		"-A sing-box -m set --match-set client_bypass_static src -j RETURN",
		"-A sing-box -p tcp -s 192.168.50.0/24 -j REDIRECT --to-ports 7892",
	}, "\n"))
	checks := checkBypassReturns("nat", chainSingBox, rules, enabledBypass())
	if checks[0].Status != "fail" {
		t.Fatalf("check = %+v, want fail: client_bypass_static must not satisfy client_bypass", checks[0])
	}
}

// ----------------------- 与 checkIptablesChains 的编排 -----------------------

// 带 bypass RETURN 的三条子链 fixture：RETURN 在链首，终结规则在链尾。
const (
	natSubChainWithBypass = `-N sing-box
-A sing-box -m set --match-set client_bypass src -j RETURN
-A sing-box -p tcp -m tcp --dport 53 -j RETURN
-A sing-box -p udp -m udp --dport 53 -j RETURN
-A sing-box -m mark --mark 0x7890 -j RETURN
-A sing-box -d 10.0.0.0/8 -j RETURN
-A sing-box -p tcp -s 192.168.50.0/24 -j REDIRECT --to-ports 7892
`
	dnsSubChainWithBypass = `-N sing-box-dns
-A sing-box-dns -m set --match-set client_bypass src -j RETURN
-A sing-box-dns -m mark --mark 0x7890 -j RETURN
-A sing-box-dns -p tcp -s 192.168.50.0/24 -j REDIRECT --to-ports 1053
-A sing-box-dns -p udp -s 192.168.50.0/24 -j REDIRECT --to-ports 1053
`
	markSubChainWithBypass = `-N sing-box-mark
-A sing-box-mark -m set --match-set client_bypass src -j RETURN
-A sing-box-mark -p tcp -m tcp --dport 53 -j RETURN
-A sing-box-mark -p udp -m udp --dport 53 -j RETURN
-A sing-box-mark -m mark --mark 0x7890 -j RETURN
-A sing-box-mark -d 10.0.0.0/8 -j RETURN
-A sing-box-mark -p udp -s 192.168.50.0/24 -j MARK --set-xmark 0x7892
`
)

func bypassChainCmds(extra map[string]cmdResult) map[string]cmdResult {
	return chainCmds(mergeCmds(map[string]cmdResult{
		"iptables -t nat -S sing-box":         {out: natSubChainWithBypass, code: 0},
		"iptables -t nat -S sing-box-dns":     {out: dnsSubChainWithBypass, code: 0},
		"iptables -t mangle -S sing-box-mark": {out: markSubChainWithBypass, code: 0},
	}, extra))
}

// bypass RETURN 全对时折进所属子链那一行，不单独占行。
func TestCheckIptablesChains_BypassReturnsCollapseIntoChainRow(t *testing.T) {
	stubRunReadOnly(t, bypassChainCmds(nil))
	checks := checkIptablesChains(config.DefaultRouting(), enabledBypass())
	if n := countStatus(checks, "fail") + countStatus(checks, "warn"); n > 0 {
		t.Fatalf("unexpected %d non-pass rows: %+v", n, checks)
	}
	for _, c := range checks {
		if strings.Contains(c.Name, "--match-set client_bypass") {
			t.Fatalf("bypass RETURN must not get its own row when it passes: %+v", c)
		}
	}
	row := findCheckExact(checks, "iptables nat/sing-box")
	if row == nil {
		t.Fatalf("missing chain row: %+v", checks)
	}
	if row.Detail != "6 rules incl. 1 bypass RETURN" {
		t.Fatalf("detail = %q, want the bypass count folded into the chain row", row.Detail)
	}
}

// 多个 set 时计数走复数形式，且仍然只有一行。
func TestCheckIptablesChains_BypassReturnsCountPluralized(t *testing.T) {
	const natTwoSets = `-N sing-box
-A sing-box -m set --match-set client_bypass src -j RETURN
-A sing-box -m set --match-set client_bypass_static src -j RETURN
-A sing-box -p tcp -m tcp --dport 53 -j RETURN
-A sing-box -p udp -m udp --dport 53 -j RETURN
-A sing-box -m mark --mark 0x7890 -j RETURN
-A sing-box -p tcp -s 192.168.50.0/24 -j REDIRECT --to-ports 7892
`
	b := enabledBypass()
	b.StaticIPs = []string{"192.168.50.9"}
	// 另外两条链也得跟着装 static RETURN，否则会被判 fail 而展开。
	stubRunReadOnly(t, bypassChainCmds(map[string]cmdResult{
		"iptables -t nat -S sing-box": {out: natTwoSets, code: 0},
		"iptables -t nat -S sing-box-dns": {out: strings.Replace(dnsSubChainWithBypass,
			"-A sing-box-dns -m mark",
			"-A sing-box-dns -m set --match-set client_bypass_static src -j RETURN\n-A sing-box-dns -m mark", 1), code: 0},
		"iptables -t mangle -S sing-box-mark": {out: strings.Replace(markSubChainWithBypass,
			"-A sing-box-mark -p tcp",
			"-A sing-box-mark -m set --match-set client_bypass_static src -j RETURN\n-A sing-box-mark -p tcp", 1), code: 0},
	}))
	checks := checkIptablesChains(config.DefaultRouting(), b)
	row := findCheckExact(checks, "iptables nat/sing-box")
	if row == nil || row.Detail != "6 rules incl. 2 bypass RETURNs" {
		t.Fatalf("row = %+v, want both RETURNs folded in", row)
	}
}

// 出问题就展开到条目级——和 rundir / config 折叠一个语义：折叠行消失，
// 只留坏掉的那条，且它自带完整的 `iptables <table>/<chain> ...` 上下文。
func TestCheckIptablesChains_BrokenBypassReturnExpands(t *testing.T) {
	// RETURN 装到了 REDIRECT 后面：永远走不到，必须报出来。
	const natReturnAfterTerminal = `-N sing-box
-A sing-box -p tcp -m tcp --dport 53 -j RETURN
-A sing-box -p udp -m udp --dport 53 -j RETURN
-A sing-box -m mark --mark 0x7890 -j RETURN
-A sing-box -d 10.0.0.0/8 -j RETURN
-A sing-box -p tcp -s 192.168.50.0/24 -j REDIRECT --to-ports 7892
-A sing-box -m set --match-set client_bypass src -j RETURN
`
	stubRunReadOnly(t, bypassChainCmds(map[string]cmdResult{
		"iptables -t nat -S sing-box": {out: natReturnAfterTerminal, code: 0},
	}))
	checks := checkIptablesChains(config.DefaultRouting(), enabledBypass())

	bad := findCheckExact(checks, "iptables nat/sing-box -m set --match-set client_bypass src -j RETURN")
	if bad == nil || bad.Status != "fail" {
		t.Fatalf("check = %+v, want an expanded fail row", bad)
	}
	if findCheckExact(checks, "iptables nat/sing-box") != nil {
		t.Errorf("collapsed chain row must be dropped once something in the group fails: %+v", checks)
	}
	// 同组的其余两条链没问题 → 仍然是折叠的一行。
	if c := findCheckExact(checks, "iptables nat/sing-box-dns"); c == nil || c.Status != "pass" {
		t.Errorf("healthy chains must stay collapsed: %+v", c)
	}
}

// 链本身取不到时不再刷 RETURN missing——"链不存在"已经是根因。
func TestCheckIptablesChains_MissingChainSkipsBypassReturns(t *testing.T) {
	stubRunReadOnly(t, bypassChainCmds(map[string]cmdResult{
		"iptables -t nat -S sing-box": {out: "", code: 1, err: fmt.Errorf("No chain/target/match by that name.")},
	}))
	checks := checkIptablesChains(config.DefaultRouting(), enabledBypass())
	if c := findCheck(checks, "iptables nat/sing-box -m set"); c != nil {
		t.Fatalf("should not report bypass RETURN when the chain is missing: %+v", c)
	}
}

// bypass 关闭时 iptables 段完全不提 RETURN，子链 detail 也不带计数后缀。
func TestCheckIptablesChains_BypassDisabledEmitsNoReturnRows(t *testing.T) {
	stubRunReadOnly(t, chainCmds(nil))
	checks := checkIptablesChains(config.DefaultRouting(), config.DefaultBypass())
	for _, c := range checks {
		if strings.Contains(c.Name, "--match-set client_bypass") || strings.Contains(c.Detail, "bypass RETURN") {
			t.Fatalf("bypass disabled but got %+v", c)
		}
	}
}

// ----------------------- checkBypassSet: 工具缺失 vs 对象不存在 -----------------------

func TestCheckBypassSet_ToolUnavailableWarnsEvenWhenNotWanted(t *testing.T) {
	// client_bypass_static 在 StaticIPs 为空时"预期不存在"，但 ipset 命令本身
	// 跑不起来时不能被误判成"符合预期的 pass"——这正是 Important 1 要堵的假阴性。
	stubRunReadOnly(t, map[string]cmdResult{
		"ipset list client_bypass_static": {code: -1, err: fmt.Errorf("exec: \"ipset\": executable file not found in $PATH")},
	})
	spec := bypassSets[1] // client_bypass_static
	c := checkBypassSet(spec, config.DefaultBypass())
	if c.Status != "warn" {
		t.Fatalf("status = %q, detail = %q, want warn", c.Status, c.Detail)
	}
	if !strings.Contains(c.Detail, "unavailable") {
		t.Fatalf("detail = %q, want mention of unavailable tool", c.Detail)
	}
}

func TestCheckBypassSet_ToolUnavailableWarnsWhenWanted(t *testing.T) {
	stubRunReadOnly(t, map[string]cmdResult{
		"ipset list client_bypass": {code: -1, err: fmt.Errorf("exec: \"ipset\": executable file not found in $PATH")},
	})
	spec := bypassSets[0] // client_bypass，恒 want=true
	c := checkBypassSet(spec, config.DefaultBypass())
	if c.Status != "warn" {
		t.Fatalf("status = %q, detail = %q, want warn", c.Status, c.Detail)
	}
	if !strings.Contains(c.Detail, "unavailable") {
		t.Fatalf("detail = %q, want mention of unavailable tool", c.Detail)
	}
}
