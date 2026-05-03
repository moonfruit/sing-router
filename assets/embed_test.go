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
		"daemon.toml.default",
		"initd/S99sing-router",
		"jffs/nat-start.snippet",
		"jffs/services-start.snippet",
		"shell/startup.sh",
		"shell/teardown.sh",
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
	data, _ := ReadFile("jffs/nat-start.snippet")
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
