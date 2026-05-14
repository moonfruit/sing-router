package config

import (
	"testing"
)

func TestDefaultRouting(t *testing.T) {
	r := DefaultRouting()
	cases := map[string]any{
		"DnsPort":      1053,
		"RedirectPort": 7892,
		"RouteMark":    "0x7892",
		"BypassMark":   "0x7890",
		"Tun":          "utun",
		"FakeIP":       "28.0.0.0/8",
		"LAN":          "192.168.50.0/24",
		"RouteTable":   7890,
		"ProxyPorts":   "22,80,443,8080,8443",
	}
	if r.DnsPort != cases["DnsPort"].(int) {
		t.Fatalf("DnsPort %d", r.DnsPort)
	}
	if r.FakeIP != cases["FakeIP"].(string) {
		t.Fatalf("FakeIP %s", r.FakeIP)
	}
	if r.RouteMark != cases["RouteMark"].(string) {
		t.Fatalf("RouteMark %s", r.RouteMark)
	}
}

func TestLoadRoutingOverridesFromTOML(t *testing.T) {
	p := 9999
	fakeip := "30.0.0.0/8"
	cfg := &DaemonConfig{Router: RouterConfig{
		RedirectPort: &p,
		FakeIP:       &fakeip,
	}}
	r := LoadRouting(cfg)
	if r.RedirectPort != 9999 {
		t.Fatalf("override RedirectPort failed: %d", r.RedirectPort)
	}
	if r.FakeIP != "30.0.0.0/8" {
		t.Fatalf("override FakeIP failed: %s", r.FakeIP)
	}
	// 未覆盖的字段保持默认
	if r.DnsPort != 1053 {
		t.Fatalf("untouched DnsPort changed: %d", r.DnsPort)
	}
}

func TestRoutingEnvVars(t *testing.T) {
	r := DefaultRouting()
	env := r.EnvVars("/opt/home/sing-router/var/cn.txt")
	want := map[string]string{
		"DNS_PORT":      "1053",
		"REDIRECT_PORT": "7892",
		"ROUTE_MARK":    "0x7892",
		"BYPASS_MARK":   "0x7890",
		"TUN":           "utun",
		"FAKEIP":        "28.0.0.0/8",
		"LAN":           "192.168.50.0/24",
		"ROUTE_TABLE":   "7890",
		"PROXY_PORTS":   "22,80,443,8080,8443",
		"CN_IP_CIDR":    "/opt/home/sing-router/var/cn.txt",
	}
	for k, v := range want {
		if env[k] != v {
			t.Fatalf("env %s want %q got %q", k, v, env[k])
		}
	}
	if len(env) != len(want) {
		t.Fatalf("env length: want %d got %d", len(want), len(env))
	}
}
