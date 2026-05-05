package cli

import (
	"testing"
)

func TestBuildStatusExtraIncludesFirmware(t *testing.T) {
	f := buildStatusExtra("/opt/home/sing-router", "config.d", "koolshare")
	snap := f()
	if snap["firmware"] != "koolshare" {
		t.Fatalf("firmware=%v want koolshare", snap["firmware"])
	}
	cfg, ok := snap["config"].(map[string]any)
	if !ok {
		t.Fatalf("config key missing or wrong type: %+v", snap["config"])
	}
	if cfg["config_dir"] != "/opt/home/sing-router/config.d" {
		t.Fatalf("config_dir=%v", cfg["config_dir"])
	}
}

func TestBuildStatusExtraEmptyFirmwareReportsUnknown(t *testing.T) {
	f := buildStatusExtra("/opt/home/sing-router", "config.d", "")
	snap := f()
	if snap["firmware"] != "unknown" {
		t.Fatalf("empty firmware should report 'unknown', got %v", snap["firmware"])
	}
}
