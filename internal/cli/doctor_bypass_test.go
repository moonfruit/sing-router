package cli

import (
	"fmt"
	"strings"
	"testing"

	"github.com/moonfruit/sing-router/internal/config"
)

func TestCheckClientBypassDisabledYieldsInfo(t *testing.T) {
	checks := checkClientBypass(config.DefaultBypass())
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

// ----------------------- checkBypassChainRules -----------------------
//
// 核心断言："RETURN 必须排在终结规则之前"——这正是本节存在的理由：顺序装反了
// 从客户端表现上完全看不出来（只是"没被放行"），必须靠这段位置判定兜底。

func TestCheckBypassChainRules_ReturnBeforeTerminalPasses(t *testing.T) {
	stubRunReadOnly(t, map[string]cmdResult{
		"iptables -t nat -S sing-box": {out: strings.Join([]string{
			"-N sing-box",
			"-A sing-box -m set --match-set client_bypass src -j RETURN",
			"-A sing-box -p tcp -s 192.168.50.0/24 -j REDIRECT --to-ports 7892",
		}, "\n")},
		"iptables -t mangle -S sing-box-mark": {out: strings.Join([]string{
			"-N sing-box-mark",
			"-A sing-box-mark -m set --match-set client_bypass src -j RETURN",
			"-A sing-box-mark -p udp -s 192.168.50.0/24 -j MARK --set-xmark 0x7892",
		}, "\n")},
		"iptables -t nat -S sing-box-dns": {out: strings.Join([]string{
			"-N sing-box-dns",
			"-A sing-box-dns -m set --match-set client_bypass src -j RETURN",
			"-A sing-box-dns -p tcp -s 192.168.50.0/24 -j REDIRECT --to-ports 1053",
		}, "\n")},
	})
	checks := checkBypassChainRules()
	if len(checks) != 3 {
		t.Fatalf("expected 3 checks, got %d", len(checks))
	}
	for _, c := range checks {
		if c.Status != "pass" {
			t.Fatalf("%s: status = %q, detail = %q, want pass", c.Name, c.Status, c.Detail)
		}
	}
}

func TestCheckBypassChainRules_ReturnAfterTerminalFails(t *testing.T) {
	// 这条是最重要的回归用例：装反了顺序必须报 fail，否则整节检查失去意义。
	stubRunReadOnly(t, map[string]cmdResult{
		"iptables -t nat -S sing-box": {out: strings.Join([]string{
			"-N sing-box",
			"-A sing-box -p tcp -s 192.168.50.0/24 -j REDIRECT --to-ports 7892",
			"-A sing-box -m set --match-set client_bypass src -j RETURN",
		}, "\n")},
		"iptables -t mangle -S sing-box-mark": {out: "-N sing-box-mark"},
		"iptables -t nat -S sing-box-dns":     {out: "-N sing-box-dns"},
	})
	checks := checkBypassChainRules()
	c := findCheck(checks, "nat/sing-box bypass RETURN")
	if c == nil {
		t.Fatalf("missing check for nat/sing-box")
	}
	if c.Status != "fail" {
		t.Fatalf("status = %q, detail = %q, want fail", c.Status, c.Detail)
	}
	if !strings.Contains(c.Detail, "never be reached") {
		t.Fatalf("detail = %q, want mention of unreachable RETURN", c.Detail)
	}
}

func TestCheckBypassChainRules_NoReturnFails(t *testing.T) {
	stubRunReadOnly(t, map[string]cmdResult{
		"iptables -t nat -S sing-box": {out: strings.Join([]string{
			"-N sing-box",
			"-A sing-box -p tcp -s 192.168.50.0/24 -j REDIRECT --to-ports 7892",
		}, "\n")},
		"iptables -t mangle -S sing-box-mark": {out: "-N sing-box-mark"},
		"iptables -t nat -S sing-box-dns":     {out: "-N sing-box-dns"},
	})
	checks := checkBypassChainRules()
	c := findCheck(checks, "nat/sing-box bypass RETURN")
	if c == nil || c.Status != "fail" {
		t.Fatalf("check = %+v, want fail", c)
	}
	if !strings.Contains(c.Detail, "no RETURN") {
		t.Fatalf("detail = %q", c.Detail)
	}
}

func TestCheckBypassChainRules_NoTerminalRuleWarns(t *testing.T) {
	// 链里只有 RETURN、没有终结规则：不能断言"在其前面"，也不该判 fail——
	// 链是否完整由 checkIptablesChains 负责，这里只如实报告无法判断顺序。
	stubRunReadOnly(t, map[string]cmdResult{
		"iptables -t nat -S sing-box": {out: strings.Join([]string{
			"-N sing-box",
			"-A sing-box -m set --match-set client_bypass src -j RETURN",
		}, "\n")},
		"iptables -t mangle -S sing-box-mark": {out: "-N sing-box-mark"},
		"iptables -t nat -S sing-box-dns":     {out: "-N sing-box-dns"},
	})
	checks := checkBypassChainRules()
	c := findCheck(checks, "nat/sing-box bypass RETURN")
	if c == nil {
		t.Fatalf("missing check for nat/sing-box")
	}
	if c.Status != "warn" {
		t.Fatalf("status = %q, detail = %q, want warn", c.Status, c.Detail)
	}
	if !strings.Contains(c.Detail, "no terminal rule") {
		t.Fatalf("detail = %q, want mention of missing terminal rule", c.Detail)
	}
}

func TestCheckBypassChainRules_ChainMissingFails(t *testing.T) {
	// iptables 命令本身能跑，但链不存在（sing-box 规则没装）：code != 0。
	stubRunReadOnly(t, map[string]cmdResult{
		"iptables -t nat -S sing-box": {code: 1, err: fmt.Errorf("iptables: No chain/target/match by that name")},
		"iptables -t mangle -S sing-box-mark": {out: strings.Join([]string{
			"-N sing-box-mark",
			"-A sing-box-mark -m set --match-set client_bypass src -j RETURN",
			"-A sing-box-mark -p udp -s 192.168.50.0/24 -j MARK --set-xmark 0x7892",
		}, "\n")},
		"iptables -t nat -S sing-box-dns": {out: strings.Join([]string{
			"-N sing-box-dns",
			"-A sing-box-dns -m set --match-set client_bypass src -j RETURN",
			"-A sing-box-dns -p tcp -s 192.168.50.0/24 -j REDIRECT --to-ports 1053",
		}, "\n")},
	})
	checks := checkBypassChainRules()
	c := findCheck(checks, "nat/sing-box bypass RETURN")
	if c == nil || c.Status != "fail" {
		t.Fatalf("check = %+v, want fail", c)
	}
	if !strings.Contains(c.Detail, "chain not found") {
		t.Fatalf("detail = %q", c.Detail)
	}
}

func TestCheckBypassChainRules_ToolUnavailableWarns(t *testing.T) {
	// code == -1：iptables 命令本身跑不起来（未安装），与"链不存在"是两回事，
	// 必须是 warn 而不是带有误导性根因的 fail。
	stubRunReadOnly(t, map[string]cmdResult{
		"iptables -t nat -S sing-box":         {code: -1, err: fmt.Errorf("exec: \"iptables\": executable file not found in $PATH")},
		"iptables -t mangle -S sing-box-mark": {code: -1, err: fmt.Errorf("exec: \"iptables\": executable file not found in $PATH")},
		"iptables -t nat -S sing-box-dns":     {code: -1, err: fmt.Errorf("exec: \"iptables\": executable file not found in $PATH")},
	})
	checks := checkBypassChainRules()
	for _, c := range checks {
		if c.Status != "warn" {
			t.Fatalf("%s: status = %q, want warn", c.Name, c.Status)
		}
		if !strings.Contains(c.Detail, "unavailable") {
			t.Fatalf("%s: detail = %q, want mention of unavailable tool", c.Name, c.Detail)
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
