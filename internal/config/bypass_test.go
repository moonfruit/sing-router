package config

import (
	"strings"
	"testing"
)

func TestDefaultBypassIsDisabled(t *testing.T) {
	b := DefaultBypass()
	if b.Enabled {
		t.Fatal("bypass must default to disabled")
	}
	if b.DefaultTTLSec != 120 || b.MaxTTLSec != 600 {
		t.Fatalf("default ttl = %d, max ttl = %d", b.DefaultTTLSec, b.MaxTTLSec)
	}
}

func TestLoadBypassOverridesDefaults(t *testing.T) {
	ttl, max := 30, 90
	cfg := &DaemonConfig{Bypass: BypassConfig{
		Enabled:       true,
		DefaultTTLSec: &ttl,
		MaxTTLSec:     &max,
		StaticIPs:     []string{"192.168.50.7"},
		StaticMACs:    []string{"00:E0:4C:67:01:46"},
	}}
	b := LoadBypass(cfg)
	if !b.Enabled || b.DefaultTTLSec != 30 || b.MaxTTLSec != 90 {
		t.Fatalf("loaded: %+v", b)
	}
	if len(b.StaticIPs) != 1 || b.StaticIPs[0] != "192.168.50.7" {
		t.Fatalf("static ips: %v", b.StaticIPs)
	}
	if len(b.StaticMACs) != 1 || b.StaticMACs[0] != "00:E0:4C:67:01:46" {
		t.Fatalf("static macs: %v", b.StaticMACs)
	}
}

func TestLoadBypassNilConfigReturnsDefaults(t *testing.T) {
	if LoadBypass(nil).Enabled {
		t.Fatal("nil cfg must yield disabled bypass")
	}
}

// token 是本功能唯一的身份来源，不允许空跑。
func TestValidateRejectsEnabledWithoutToken(t *testing.T) {
	b := DefaultBypass()
	b.Enabled = true
	err := b.Validate("")
	if err == nil {
		t.Fatal("expected error when enabled with empty token")
	}
	if !strings.Contains(err.Error(), "[http].token") {
		t.Fatalf("error should name the missing key: %v", err)
	}
}

func TestValidateAcceptsEnabledWithToken(t *testing.T) {
	b := DefaultBypass()
	b.Enabled = true
	if err := b.Validate("deadbeef"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

// 关闭时不校验任何东西——用户没启用就不该被配置错误挡住启动。
func TestValidateSkipsWhenDisabled(t *testing.T) {
	b := DefaultBypass()
	b.DefaultTTLSec = 0
	if err := b.Validate(""); err != nil {
		t.Fatalf("disabled bypass must not be validated: %v", err)
	}
}

func TestValidateRejectsBadTTLRange(t *testing.T) {
	b := DefaultBypass()
	b.Enabled = true
	b.DefaultTTLSec = 900
	b.MaxTTLSec = 600
	if err := b.Validate("tok"); err == nil {
		t.Fatal("default_ttl_sec > max_ttl_sec must fail")
	}
}

func TestEnvVarsDisabledYieldsEmptyEnabledFlag(t *testing.T) {
	env := DefaultBypass().EnvVars()
	if env["CLIENT_BYPASS_ENABLED"] != "" {
		t.Fatalf("disabled must render empty flag, got %q", env["CLIENT_BYPASS_ENABLED"])
	}
	// 键必须存在——startup.sh 用 ${CLIENT_BYPASS_STATIC_IPS} 无默认值展开。
	for _, k := range []string{"CLIENT_BYPASS_TTL", "CLIENT_BYPASS_STATIC_IPS", "CLIENT_BYPASS_STATIC_MACS"} {
		if _, ok := env[k]; !ok {
			t.Fatalf("env must always define %s", k)
		}
	}
}

func TestEnvVarsEnabledJoinsListsWithSpace(t *testing.T) {
	b := DefaultBypass()
	b.Enabled = true
	b.StaticIPs = []string{"192.168.50.7", "192.168.50.8"}
	b.StaticMACs = []string{"AA:BB:CC:DD:EE:FF"}
	env := b.EnvVars()
	if env["CLIENT_BYPASS_ENABLED"] != "1" {
		t.Fatalf("enabled flag = %q", env["CLIENT_BYPASS_ENABLED"])
	}
	if env["CLIENT_BYPASS_TTL"] != "120" {
		t.Fatalf("ttl = %q", env["CLIENT_BYPASS_TTL"])
	}
	if env["CLIENT_BYPASS_STATIC_IPS"] != "192.168.50.7 192.168.50.8" {
		t.Fatalf("static ips = %q", env["CLIENT_BYPASS_STATIC_IPS"])
	}
	if env["CLIENT_BYPASS_STATIC_MACS"] != "AA:BB:CC:DD:EE:FF" {
		t.Fatalf("static macs = %q", env["CLIENT_BYPASS_STATIC_MACS"])
	}
}
