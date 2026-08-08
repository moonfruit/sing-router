package cli

import (
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
