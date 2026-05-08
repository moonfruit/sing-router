package cli

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/moonfruit/sing-router/internal/config"
)

// ----------------------- runReadOnly stubbing -----------------------

type cmdResult struct {
	out  string
	code int
	err  error
}

func stubRunReadOnly(t *testing.T, results map[string]cmdResult) {
	t.Helper()
	orig := runReadOnly
	t.Cleanup(func() { runReadOnly = orig })
	runReadOnly = func(ctx context.Context, name string, args ...string) (string, int, error) {
		key := name + " " + strings.Join(args, " ")
		if r, ok := results[key]; ok {
			return r.out, r.code, r.err
		}
		return "", -1, fmt.Errorf("unexpected command: %s", key)
	}
}

func findCheck(checks []doctorCheck, namePrefix string) *doctorCheck {
	for i := range checks {
		if strings.HasPrefix(checks[i].Name, namePrefix) {
			return &checks[i]
		}
	}
	return nil
}

func countStatus(checks []doctorCheck, status string) int {
	n := 0
	for _, c := range checks {
		if c.Status == status {
			n++
		}
	}
	return n
}

// ----------------------- 解析器 -----------------------

func TestParseIPRules(t *testing.T) {
	in := `0:	from all lookup local
32765:	from all fwmark 0x7892 lookup 111
32766:	from all lookup main
32767:	from all lookup default
`
	rules := parseIPRules(in)
	if len(rules) != 4 {
		t.Fatalf("len=%d want 4", len(rules))
	}
	if rules[0].Prio != 0 || rules[0].Action != "lookup local" {
		t.Errorf("rule0=%+v", rules[0])
	}
	if rules[1].FwMark != "0x7892" || rules[1].Action != "lookup 111" {
		t.Errorf("rule1=%+v", rules[1])
	}
}

func TestParseIPRules_FwMarkMask(t *testing.T) {
	in := "32700:	from all fwmark 0x100/0xff00 lookup 50\n"
	rules := parseIPRules(in)
	if len(rules) != 1 || rules[0].FwMark != "0x100" {
		t.Fatalf("got %+v", rules)
	}
}

func TestParseIPRoutes(t *testing.T) {
	in := `default dev utun scope link
1.1.1.1 via 192.168.1.1 dev eth0
`
	rt := parseIPRoutes(in)
	if len(rt) != 2 {
		t.Fatalf("len=%d want 2", len(rt))
	}
	if rt[0].Dest != "default" || rt[0].Dev != "utun" {
		t.Errorf("rt0=%+v", rt[0])
	}
	if rt[1].Dest != "1.1.1.1" || rt[1].Dev != "eth0" {
		t.Errorf("rt1=%+v", rt[1])
	}
}

func TestParseIptablesS(t *testing.T) {
	in := `-P PREROUTING ACCEPT
-N sing-box
-A PREROUTING -p tcp -m multiport --dports 22,80,443,8080,8443 -j sing-box
-A PREROUTING -d 28.0.0.0/8 -p tcp -j sing-box
-A PREROUTING -p tcp -m tcp --dport 53 -j sing-box-dns
-A PREROUTING -p udp -m udp --dport 53 -j sing-box-dns
`
	rules := parseIptablesS(in)
	if len(rules) != 4 {
		t.Fatalf("len=%d want 4", len(rules))
	}
	if rules[0].Index != 1 || rules[0].DPort != "22,80,443,8080,8443" || rules[0].Target != "sing-box" {
		t.Errorf("r0=%+v", rules[0])
	}
	if rules[1].DAddr != "28.0.0.0/8" || rules[1].Proto != "tcp" {
		t.Errorf("r1=%+v", rules[1])
	}
	if rules[2].DPort != "53" || rules[2].Target != "sing-box-dns" {
		t.Errorf("r2=%+v", rules[2])
	}
}

// ----------------------- 端口/CIDR 相交 -----------------------

func TestDportOverlap(t *testing.T) {
	cases := []struct {
		a, b string
		want bool
	}{
		{"22,80,443", "53", false},
		{"22,80,443", "80", true},
		{"22,80,443", "", true},
		{"", "22", true},
		{"53", "53", true},
		{"1024:65535", "30000", true},
		{"1024:65535", "80", false},
		{"22,80,443,8080,8443", "8080", true},
	}
	for _, c := range cases {
		if got := dportOverlap(c.a, c.b); got != c.want {
			t.Errorf("dportOverlap(%q,%q)=%v want %v", c.a, c.b, got, c.want)
		}
	}
}

func TestCidrOverlap(t *testing.T) {
	cases := []struct {
		a, b string
		want bool
	}{
		{"28.0.0.0/8", "28.1.2.3/32", true},
		{"28.0.0.0/8", "10.0.0.0/8", false},
		{"0.0.0.0/0", "192.168.1.1", true},
		{"", "1.2.3.4", true},
		{"192.168.1.0/24", "192.168.1.5", true},
		{"192.168.1.0/24", "192.168.2.0/24", false},
	}
	for _, c := range cases {
		if got := cidrOverlap(c.a, c.b); got != c.want {
			t.Errorf("cidrOverlap(%q,%q)=%v want %v", c.a, c.b, got, c.want)
		}
	}
}

// ----------------------- checkIPRule -----------------------

func TestCheckIPRule_Pass(t *testing.T) {
	stubRunReadOnly(t, map[string]cmdResult{
		"ip rule list": {out: "0:\tfrom all lookup local\n32765:\tfrom all fwmark 0x7892 lookup 111\n32766:\tfrom all lookup main\n", code: 0},
	})
	r := config.DefaultRouting()
	checks := checkIPRule(r)
	if countStatus(checks, "fail") > 0 || countStatus(checks, "warn") > 0 {
		t.Fatalf("unexpected non-pass: %+v", checks)
	}
	if countStatus(checks, "pass") < 1 {
		t.Fatalf("expected at least 1 pass, got %+v", checks)
	}
}

func TestCheckIPRule_MissingFails(t *testing.T) {
	stubRunReadOnly(t, map[string]cmdResult{
		"ip rule list": {out: "0:\tfrom all lookup local\n32766:\tfrom all lookup main\n", code: 0},
	})
	r := config.DefaultRouting()
	checks := checkIPRule(r)
	if countStatus(checks, "fail") == 0 {
		t.Fatalf("expected fail, got %+v", checks)
	}
}

func TestCheckIPRule_CatchAllBeforeOursWarns(t *testing.T) {
	stubRunReadOnly(t, map[string]cmdResult{
		"ip rule list": {
			out: "0:\tfrom all lookup local\n" +
				"100:\tfrom all lookup main\n" +
				"32765:\tfrom all fwmark 0x7892 lookup 111\n" +
				"32766:\tfrom all lookup main\n",
			code: 0,
		},
	})
	r := config.DefaultRouting()
	checks := checkIPRule(r)
	hit := findCheck(checks, "ip rule prio 100")
	if hit == nil || hit.Status != "warn" {
		t.Fatalf("expected warn at prio 100, got %+v", checks)
	}
}

func TestCheckIPRule_DuplicateFwmarkFails(t *testing.T) {
	stubRunReadOnly(t, map[string]cmdResult{
		"ip rule list": {
			out: "0:\tfrom all lookup local\n" +
				"100:\tfrom all fwmark 0x7892 lookup 99\n" +
				"32765:\tfrom all fwmark 0x7892 lookup 111\n",
			code: 0,
		},
	})
	r := config.DefaultRouting()
	checks := checkIPRule(r)
	hit := findCheck(checks, "ip rule prio 100")
	if hit == nil || hit.Status != "fail" {
		t.Fatalf("expected fail at prio 100, got %+v", checks)
	}
}

// ----------------------- checkIPRoute -----------------------

func TestCheckIPRoute_Pass(t *testing.T) {
	stubRunReadOnly(t, map[string]cmdResult{
		"ip route show table 111": {out: "default dev utun scope link\n", code: 0},
	})
	r := config.DefaultRouting()
	checks := checkIPRoute(r)
	if countStatus(checks, "fail") > 0 || countStatus(checks, "warn") > 0 {
		t.Fatalf("unexpected non-pass: %+v", checks)
	}
}

func TestCheckIPRoute_DefaultMissingFails(t *testing.T) {
	stubRunReadOnly(t, map[string]cmdResult{
		"ip route show table 111": {out: "", code: 0},
	})
	r := config.DefaultRouting()
	checks := checkIPRoute(r)
	if countStatus(checks, "fail") == 0 {
		t.Fatalf("expected fail, got %+v", checks)
	}
}

func TestCheckIPRoute_ShadowRouteWarns(t *testing.T) {
	stubRunReadOnly(t, map[string]cmdResult{
		"ip route show table 111": {out: "default dev utun\n1.1.1.1 via 192.168.1.1 dev eth0\n", code: 0},
	})
	r := config.DefaultRouting()
	checks := checkIPRoute(r)
	if countStatus(checks, "warn") == 0 {
		t.Fatalf("expected warn for shadow route, got %+v", checks)
	}
}

// ----------------------- checkIptablesChains -----------------------

const fixturePreroutingNAT = `-P PREROUTING ACCEPT
-A PREROUTING -p tcp -m multiport --dports 22,80,443,8080,8443 -j sing-box
-A PREROUTING -d 28.0.0.0/8 -p tcp -j sing-box
-A PREROUTING -p tcp -m tcp --dport 53 -j sing-box-dns
-A PREROUTING -p udp -m udp --dport 53 -j sing-box-dns
`

const fixturePreroutingMangle = `-P PREROUTING ACCEPT
-A PREROUTING -p udp -m multiport --dports 22,80,443,8080,8443 -j sing-box-mark
-A PREROUTING -d 28.0.0.0/8 -p udp -j sing-box-mark
`

const fixtureForward = `-P FORWARD ACCEPT
-A FORWARD -o utun -j ACCEPT
`

const fixtureSubChainNAT = `-N sing-box
-A sing-box -p tcp -m tcp --dport 53 -j RETURN
-A sing-box -p udp -m udp --dport 53 -j RETURN
-A sing-box -m mark --mark 0x7890 -j RETURN
-A sing-box -d 10.0.0.0/8 -j RETURN
-A sing-box -d 192.168.0.0/16 -j RETURN
-A sing-box -p tcp -s 192.168.50.0/24 -j REDIRECT --to-ports 7892
`

const fixtureSubChainDNS = `-N sing-box-dns
-A sing-box-dns -m mark --mark 0x7890 -j RETURN
-A sing-box-dns -p tcp -s 192.168.50.0/24 -j REDIRECT --to-ports 1053
-A sing-box-dns -p udp -s 192.168.50.0/24 -j REDIRECT --to-ports 1053
`

const fixtureSubChainMark = `-N sing-box-mark
-A sing-box-mark -p tcp -m tcp --dport 53 -j RETURN
-A sing-box-mark -p udp -m udp --dport 53 -j RETURN
-A sing-box-mark -m mark --mark 0x7890 -j RETURN
-A sing-box-mark -d 10.0.0.0/8 -j RETURN
-A sing-box-mark -p udp -s 192.168.50.0/24 -j MARK --set-xmark 0x7892
`

func chainCmds(extra map[string]cmdResult) map[string]cmdResult {
	base := map[string]cmdResult{
		"iptables -t nat -S PREROUTING":          {out: fixturePreroutingNAT, code: 0},
		"iptables -t mangle -S PREROUTING":       {out: fixturePreroutingMangle, code: 0},
		"iptables -t filter -S FORWARD":          {out: fixtureForward, code: 0},
		"iptables -t nat -S sing-box":            {out: fixtureSubChainNAT, code: 0},
		"iptables -t nat -S sing-box-dns":        {out: fixtureSubChainDNS, code: 0},
		"iptables -t mangle -S sing-box-mark":    {out: fixtureSubChainMark, code: 0},
	}
	for k, v := range extra {
		base[k] = v
	}
	return base
}

func TestCheckIptablesChains_AllPass(t *testing.T) {
	stubRunReadOnly(t, chainCmds(nil))
	checks := checkIptablesChains(config.DefaultRouting())
	if n := countStatus(checks, "fail"); n > 0 {
		t.Fatalf("unexpected %d fails: %+v", n, checks)
	}
	if n := countStatus(checks, "warn"); n > 0 {
		t.Fatalf("unexpected %d warns: %+v", n, checks)
	}
}

func TestCheckIptablesChains_AcceptBeforeJumpWarns(t *testing.T) {
	const interfered = `-P PREROUTING ACCEPT
-A PREROUTING -p tcp -m tcp --dport 80 -j ACCEPT
-A PREROUTING -p tcp -m multiport --dports 22,80,443,8080,8443 -j sing-box
-A PREROUTING -d 28.0.0.0/8 -p tcp -j sing-box
-A PREROUTING -p tcp -m tcp --dport 53 -j sing-box-dns
-A PREROUTING -p udp -m udp --dport 53 -j sing-box-dns
`
	stubRunReadOnly(t, chainCmds(map[string]cmdResult{
		"iptables -t nat -S PREROUTING": {out: interfered, code: 0},
	}))
	checks := checkIptablesChains(config.DefaultRouting())
	hit := findCheck(checks, "iptables nat/PREROUTING line 1")
	if hit == nil || hit.Status != "warn" {
		t.Fatalf("expected warn for line 1 ACCEPT, got %+v", checks)
	}
}

func TestCheckIptablesChains_MissingJumpFails(t *testing.T) {
	const noFakeIP = `-P PREROUTING ACCEPT
-A PREROUTING -p tcp -m multiport --dports 22,80,443,8080,8443 -j sing-box
-A PREROUTING -p tcp -m tcp --dport 53 -j sing-box-dns
-A PREROUTING -p udp -m udp --dport 53 -j sing-box-dns
`
	stubRunReadOnly(t, chainCmds(map[string]cmdResult{
		"iptables -t nat -S PREROUTING": {out: noFakeIP, code: 0},
	}))
	checks := checkIptablesChains(config.DefaultRouting())
	if countStatus(checks, "fail") == 0 {
		t.Fatalf("expected fail for missing fakeip jump, got %+v", checks)
	}
}

func TestCheckIptablesChains_SubchainMissingFails(t *testing.T) {
	stubRunReadOnly(t, chainCmds(map[string]cmdResult{
		"iptables -t nat -S sing-box": {out: "", code: 1, err: fmt.Errorf("No chain/target/match by that name.")},
	}))
	checks := checkIptablesChains(config.DefaultRouting())
	if findCheck(checks, "iptables nat/sing-box") == nil ||
		findCheck(checks, "iptables nat/sing-box").Status != "fail" {
		t.Fatalf("expected fail for missing sing-box chain, got %+v", checks)
	}
}

// ----------------------- checkIp6tablesDNS -----------------------

func TestCheckIp6tablesDNS_Pass(t *testing.T) {
	const ok = `-P INPUT ACCEPT
-A INPUT -p tcp -m tcp --dport 53 -j REJECT --reject-with icmp6-port-unreachable
-A INPUT -p udp -m udp --dport 53 -j REJECT --reject-with icmp6-port-unreachable
`
	stubRunReadOnly(t, map[string]cmdResult{
		"ip6tables -t filter -S INPUT": {out: ok, code: 0},
	})
	checks := checkIp6tablesDNS()
	if countStatus(checks, "fail") > 0 || countStatus(checks, "warn") > 0 {
		t.Fatalf("unexpected non-pass: %+v", checks)
	}
}

func TestCheckIp6tablesDNS_AcceptBeforeRejectWarns(t *testing.T) {
	const interfered = `-P INPUT ACCEPT
-A INPUT -p udp -m udp --dport 53 -j ACCEPT
-A INPUT -p tcp -m tcp --dport 53 -j REJECT
-A INPUT -p udp -m udp --dport 53 -j REJECT
`
	stubRunReadOnly(t, map[string]cmdResult{
		"ip6tables -t filter -S INPUT": {out: interfered, code: 0},
	})
	checks := checkIp6tablesDNS()
	if findCheck(checks, "ip6tables INPUT line 1") == nil {
		t.Fatalf("expected warn at line 1, got %+v", checks)
	}
}

func TestCheckIp6tablesDNS_MissingFails(t *testing.T) {
	const partial = `-P INPUT ACCEPT
-A INPUT -p tcp -m tcp --dport 53 -j REJECT
`
	stubRunReadOnly(t, map[string]cmdResult{
		"ip6tables -t filter -S INPUT": {out: partial, code: 0},
	})
	checks := checkIp6tablesDNS()
	if countStatus(checks, "fail") == 0 {
		t.Fatalf("expected fail for missing udp REJECT, got %+v", checks)
	}
}

func TestCheckIp6tablesDNS_Unavailable(t *testing.T) {
	stubRunReadOnly(t, map[string]cmdResult{
		"ip6tables -t filter -S INPUT": {out: "", code: -1, err: fmt.Errorf("exec: not found")},
	})
	checks := checkIp6tablesDNS()
	if checks[0].Status != "warn" {
		t.Fatalf("expected warn when ip6tables unavailable, got %+v", checks)
	}
}
