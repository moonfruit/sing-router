package cli

import (
	"context"
	"fmt"
	"maps"
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
		"ip rule list": {out: "0:\tfrom all lookup local\n32765:\tfrom all fwmark 0x7892 lookup 7892\n32766:\tfrom all lookup main\n", code: 0},
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
				"32765:\tfrom all fwmark 0x7892 lookup 7892\n" +
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
				"32765:\tfrom all fwmark 0x7892 lookup 7892\n",
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
		"ip route show table 7892": {out: "default dev utun scope link\n", code: 0},
	})
	r := config.DefaultRouting()
	checks := checkIPRoute(r)
	if countStatus(checks, "fail") > 0 || countStatus(checks, "warn") > 0 {
		t.Fatalf("unexpected non-pass: %+v", checks)
	}
}

func TestCheckIPRoute_DefaultMissingFails(t *testing.T) {
	stubRunReadOnly(t, map[string]cmdResult{
		"ip route show table 7892": {out: "", code: 0},
	})
	r := config.DefaultRouting()
	checks := checkIPRoute(r)
	if countStatus(checks, "fail") == 0 {
		t.Fatalf("expected fail, got %+v", checks)
	}
}

func TestCheckIPRoute_ShadowRouteWarns(t *testing.T) {
	stubRunReadOnly(t, map[string]cmdResult{
		"ip route show table 7892": {out: "default dev utun\n1.1.1.1 via 192.168.1.1 dev eth0\n", code: 0},
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
	maps.Copy(base, extra)
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

// ----------------------- checkRejectFallbacks -----------------------

// 完整 fixture：v6 × INPUT × 53（INPUT 不需 -i） + v6 × FORWARD × 53
// + v4&v6 × FORWARD × 853，FORWARD 链统一带 -i br0 收窄到 LAN 入向。
// 对应 startup.sh 2.4 / 2.5 节实际安装的规则集合。
// startup.sh 2.4/2.5 节实际写入的 spec：TCP 显式 --reject-with tcp-reset
// (偏离默认)；UDP 走默认 REJECT (默认即 icmp[6]-port-unreachable，无需
// 显式)。doctor 本身不解析 --reject-with，fixture 这样写只为如实反映
// 脚本意图。
const fixtureIp6INPUT53 = `-P INPUT ACCEPT
-A INPUT -p tcp -m tcp --dport 53 -j REJECT --reject-with tcp-reset
-A INPUT -p udp -m udp --dport 53 -j REJECT
`

const fixtureIp6FORWARDAll = `-P FORWARD ACCEPT
-A FORWARD -i br0 -p tcp -m tcp --dport 53 -j REJECT --reject-with tcp-reset
-A FORWARD -i br0 -p udp -m udp --dport 53 -j REJECT
-A FORWARD -i br0 -p tcp -m tcp --dport 853 -j REJECT --reject-with tcp-reset
-A FORWARD -i br0 -p udp -m udp --dport 853 -j REJECT
`

const fixtureIp4FORWARD853 = `-P FORWARD ACCEPT
-A FORWARD -i br0 -p tcp -m tcp --dport 853 -j REJECT --reject-with tcp-reset
-A FORWARD -i br0 -p udp -m udp --dport 853 -j REJECT
`

func rejectCmds(extra map[string]cmdResult) map[string]cmdResult {
	base := map[string]cmdResult{
		"ip6tables -t filter -S INPUT":   {out: fixtureIp6INPUT53, code: 0},
		"ip6tables -t filter -S FORWARD": {out: fixtureIp6FORWARDAll, code: 0},
		"iptables -t filter -S FORWARD":  {out: fixtureIp4FORWARD853, code: 0},
	}
	maps.Copy(base, extra)
	return base
}

func TestCheckRejectFallbacks_Pass(t *testing.T) {
	stubRunReadOnly(t, rejectCmds(nil))
	checks := checkRejectFallbacks(config.DefaultRouting())
	if n := countStatus(checks, "fail"); n > 0 {
		t.Fatalf("unexpected %d fails: %+v", n, checks)
	}
	if n := countStatus(checks, "warn"); n > 0 {
		t.Fatalf("unexpected %d warns: %+v", n, checks)
	}
	// 4 个 (family,chain,port) × 2 proto = 8 个 pass 行
	if n := countStatus(checks, "pass"); n != 8 {
		t.Fatalf("expected 8 pass rows, got %d: %+v", n, checks)
	}
}

func TestCheckRejectFallbacks_AcceptBeforeRejectWarns(t *testing.T) {
	const interfered = `-P INPUT ACCEPT
-A INPUT -p udp -m udp --dport 53 -j ACCEPT
-A INPUT -p tcp -m tcp --dport 53 -j REJECT
-A INPUT -p udp -m udp --dport 53 -j REJECT
`
	stubRunReadOnly(t, rejectCmds(map[string]cmdResult{
		"ip6tables -t filter -S INPUT": {out: interfered, code: 0},
	}))
	checks := checkRejectFallbacks(config.DefaultRouting())
	if findCheck(checks, "ip6tables INPUT line 1") == nil {
		t.Fatalf("expected warn at ip6tables INPUT line 1, got %+v", checks)
	}
}

func TestCheckRejectFallbacks_V6Port53ForwardMissingFails(t *testing.T) {
	// 部分规则：v6/FORWARD 53 只装了 tcp 一条，且缺 `-i br0` 收窄 → 应当判 fail
	// （IIf 不匹配等同于规则缺失，提示用户重新跑 startup.sh）
	const partial = `-P FORWARD ACCEPT
-A FORWARD -p tcp -m tcp --dport 53 -j REJECT
-A FORWARD -i br0 -p tcp -m tcp --dport 853 -j REJECT
-A FORWARD -i br0 -p udp -m udp --dport 853 -j REJECT
`
	stubRunReadOnly(t, rejectCmds(map[string]cmdResult{
		"ip6tables -t filter -S FORWARD": {out: partial, code: 0},
	}))
	checks := checkRejectFallbacks(config.DefaultRouting())
	tcp := findCheck(checks, "ip6tables FORWARD -i br0 REJECT 53/tcp")
	udp := findCheck(checks, "ip6tables FORWARD -i br0 REJECT 53/udp")
	if tcp == nil || tcp.Status != "fail" {
		t.Fatalf("expected fail for missing -i br0 on v6 FORWARD tcp 53, got %+v", checks)
	}
	if udp == nil || udp.Status != "fail" {
		t.Fatalf("expected fail for missing v6 FORWARD udp 53, got %+v", checks)
	}
}

func TestCheckRejectFallbacks_V4Port853ForwardMissingFails(t *testing.T) {
	const noIp4DoT = `-P FORWARD ACCEPT
`
	stubRunReadOnly(t, rejectCmds(map[string]cmdResult{
		"iptables -t filter -S FORWARD": {out: noIp4DoT, code: 0},
	}))
	checks := checkRejectFallbacks(config.DefaultRouting())
	tcp := findCheck(checks, "iptables FORWARD -i br0 REJECT 853/tcp")
	udp := findCheck(checks, "iptables FORWARD -i br0 REJECT 853/udp")
	if tcp == nil || tcp.Status != "fail" || udp == nil || udp.Status != "fail" {
		t.Fatalf("expected fails for both v4 FORWARD 853, got %+v", checks)
	}
}

// 前置规则限定了不同接口（如 -i wg0），不会命中 LAN 入向流量，因此
// 不应被报为干扰；而同接口或 catch-all 的前置 ACCEPT 仍应报警。
func TestCheckRejectFallbacks_OtherIfaceAcceptNotInterferer(t *testing.T) {
	const ip4FwdMixed = `-P FORWARD ACCEPT
-A FORWARD -i wg0 -p tcp -m tcp --dport 853 -j ACCEPT
-A FORWARD -i br0 -p tcp -m tcp --dport 853 -j REJECT
-A FORWARD -i br0 -p udp -m udp --dport 853 -j REJECT
`
	stubRunReadOnly(t, rejectCmds(map[string]cmdResult{
		"iptables -t filter -S FORWARD": {out: ip4FwdMixed, code: 0},
	}))
	checks := checkRejectFallbacks(config.DefaultRouting())
	for _, c := range checks {
		if strings.HasPrefix(c.Name, "iptables FORWARD line 1") {
			t.Fatalf("wg0 ACCEPT should not be flagged as interferer for br0 rule: %+v", c)
		}
	}
}

func TestCheckRejectFallbacks_SameIfaceAcceptIsInterferer(t *testing.T) {
	const ip4FwdSameIface = `-P FORWARD ACCEPT
-A FORWARD -i br0 -p tcp -m tcp --dport 853 -j ACCEPT
-A FORWARD -i br0 -p tcp -m tcp --dport 853 -j REJECT
-A FORWARD -i br0 -p udp -m udp --dport 853 -j REJECT
`
	stubRunReadOnly(t, rejectCmds(map[string]cmdResult{
		"iptables -t filter -S FORWARD": {out: ip4FwdSameIface, code: 0},
	}))
	checks := checkRejectFallbacks(config.DefaultRouting())
	if findCheck(checks, "iptables FORWARD line 1") == nil {
		t.Fatalf("same-iface ACCEPT before REJECT should warn, got %+v", checks)
	}
}

// 自定义 lan_iface=br1 时，doctor 应当按新接口匹配。
func TestCheckRejectFallbacks_CustomLanIface(t *testing.T) {
	const ip6FORWARDBr1 = `-P FORWARD ACCEPT
-A FORWARD -i br1 -p tcp -m tcp --dport 53 -j REJECT
-A FORWARD -i br1 -p udp -m udp --dport 53 -j REJECT
-A FORWARD -i br1 -p tcp -m tcp --dport 853 -j REJECT
-A FORWARD -i br1 -p udp -m udp --dport 853 -j REJECT
`
	const ip4FORWARDBr1 = `-P FORWARD ACCEPT
-A FORWARD -i br1 -p tcp -m tcp --dport 853 -j REJECT
-A FORWARD -i br1 -p udp -m udp --dport 853 -j REJECT
`
	stubRunReadOnly(t, map[string]cmdResult{
		"ip6tables -t filter -S INPUT":   {out: fixtureIp6INPUT53, code: 0},
		"ip6tables -t filter -S FORWARD": {out: ip6FORWARDBr1, code: 0},
		"iptables -t filter -S FORWARD":  {out: ip4FORWARDBr1, code: 0},
	})
	r := config.DefaultRouting()
	r.LanIface = "br1"
	checks := checkRejectFallbacks(r)
	if n := countStatus(checks, "fail"); n > 0 {
		t.Fatalf("unexpected fails with lan_iface=br1: %+v", checks)
	}
	if findCheck(checks, "ip6tables FORWARD -i br1 REJECT 53/tcp") == nil {
		t.Fatalf("expected label to use br1, got %+v", checks)
	}
}

func TestCheckRejectFallbacks_Ip6Unavailable(t *testing.T) {
	stubRunReadOnly(t, map[string]cmdResult{
		"ip6tables -t filter -S INPUT":   {out: "", code: -1, err: fmt.Errorf("exec: not found")},
		"ip6tables -t filter -S FORWARD": {out: "", code: -1, err: fmt.Errorf("exec: not found")},
		"iptables -t filter -S FORWARD":  {out: fixtureIp4FORWARD853, code: 0},
	})
	checks := checkRejectFallbacks(config.DefaultRouting())
	// ip6tables 不可用应只产生一条 family-level warn（去重）
	warns := 0
	for _, c := range checks {
		if c.Name == "ip6tables" && c.Status == "warn" {
			warns++
		}
	}
	if warns != 1 {
		t.Fatalf("expected exactly 1 family-level ip6tables warn, got %d: %+v", warns, checks)
	}
	// v4 部分仍应该通过
	if n := countStatus(checks, "fail"); n > 0 {
		t.Fatalf("unexpected fails: %+v", checks)
	}
}
