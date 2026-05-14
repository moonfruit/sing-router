package assets

import (
	"strings"
	"testing"
)

func TestDefaultConfigsPresent(t *testing.T) {
	for _, p := range []string{
		"config.d.default/clash.json",
		"config.d.default/dns.json",
		"config.d.default/inbounds.json",
		"config.d.default/log.json",
		"config.d.default/cache.json",
		"config.d.default/certificate.json",
		"config.d.default/http.json",
		"config.d.default/outbounds.json",
		"config.d.default/zoo.json",
		"daemon.toml.tmpl",
		"initd/S99sing-router",
		"firmware/koolshare/N99sing-router.sh",
		"firmware/merlin/nat-start.snippet",
		"firmware/merlin/services-start.snippet",
		"shell/startup.sh",
		"shell/teardown.sh",
		"shell/reapply-routes.sh",
		"shell/reload-cn-ipset.sh",
	} {
		if _, err := ReadFile(p); err != nil {
			t.Errorf("missing %s: %v", p, err)
		}
	}
}

func TestDNSFakeIPRangeFixed(t *testing.T) {
	data, err := ReadFile("config.d.default/dns.json")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"inet4_range": "28.0.0.0/8"`) {
		t.Fatal("default dns.json should have inet4_range fixed to 28.0.0.0/8")
	}
	if strings.Contains(string(data), `"22.0.0.0/8"`) {
		t.Fatal("default dns.json still references obsolete 22.0.0.0/8")
	}
}

func TestNatStartSnippetMarkers(t *testing.T) {
	data, _ := ReadFile("firmware/merlin/nat-start.snippet")
	if !strings.Contains(string(data), "# BEGIN sing-router") {
		t.Fatal("BEGIN marker missing")
	}
	if !strings.Contains(string(data), "# END sing-router") {
		t.Fatal("END marker missing")
	}
	if !strings.Contains(string(data), "sing-router reapply-rules") {
		t.Fatal("snippet should call reapply-rules")
	}
}

func TestKoolshareScriptShape(t *testing.T) {
	data, err := ReadFile("firmware/koolshare/N99sing-router.sh")
	if err != nil {
		t.Fatal(err)
	}
	s := string(data)
	if !strings.HasPrefix(s, "#!/bin/sh") {
		t.Error("missing shebang")
	}
	if !strings.Contains(s, "command -v sing-router") {
		t.Error("missing entware-mount guard")
	}
	if !strings.Contains(s, "sing-router reapply-rules") {
		t.Error("must call reapply-rules")
	}
	if !strings.Contains(s, "start_nat") {
		t.Error("must handle start_nat action")
	}
}

func TestStartupShellRequiresEnvVars(t *testing.T) {
	data, err := ReadFile("shell/startup.sh")
	if err != nil {
		t.Fatal(err)
	}
	s := string(data)
	for _, name := range []string{"DNS_PORT", "REDIRECT_PORT", "ROUTE_MARK", "BYPASS_MARK",
		"TUN", "ROUTE_TABLE", "PROXY_PORTS", "FAKEIP", "LAN"} {
		// 期望脚本通过 : "${NAME:?...}" 强制要求该变量
		needle := `: "${` + name + `:?`
		if !strings.Contains(s, needle) {
			t.Errorf("startup.sh should hard-require %s via : \"${%s:?...}\"", name, name)
		}
	}
}

func TestReapplyRoutesShellShape(t *testing.T) {
	data, err := ReadFile("shell/reapply-routes.sh")
	if err != nil {
		t.Fatal(err)
	}
	s := string(data)
	// 必须 hard-require 三个 env
	for _, name := range []string{"TUN", "ROUTE_TABLE", "ROUTE_MARK"} {
		needle := `: "${` + name + `:?`
		if !strings.Contains(s, needle) {
			t.Errorf("reapply-routes.sh should hard-require %s via : \"${%s:?...}\"", name, name)
		}
	}
	// 必须装两条与 TUN 设备相关的规则
	if !strings.Contains(s, `ip route replace default dev "$TUN" table "$ROUTE_TABLE"`) {
		t.Error("reapply-routes.sh must install default route via `ip route replace default dev $TUN table $ROUTE_TABLE`")
	}
	if !strings.Contains(s, `ip rule add fwmark "$ROUTE_MARK" table "$ROUTE_TABLE"`) {
		t.Error("reapply-routes.sh must add fwmark rule")
	}
	// 不许执行 iptables / ipset 命令（职责清晰：只管路由）。匹配命令调用形式，
	// 注释里出现这些词不算违规。
	for _, badCmd := range []string{"\niptables ", "\nip6tables ", "\nipset ",
		"\tiptables ", "\tip6tables ", "\tipset "} {
		if strings.Contains(s, badCmd) {
			t.Errorf("reapply-routes.sh must NOT invoke %q", strings.TrimSpace(badCmd))
		}
	}
}
