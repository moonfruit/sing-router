package cli

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"

	"github.com/moonfruit/sing-router/internal/firmware"
)

func TestResolveFirmware_FlagWins(t *testing.T) {
	got, err := resolveFirmware("merlin", "koolshare")
	if err != nil || got != firmware.KindMerlin {
		t.Fatalf("got (%q,%v) want (merlin,nil)", got, err)
	}
}

func TestResolveFirmware_TomlOverDetect(t *testing.T) {
	// detectBase already points to "/" — Detect may or may not match;
	// daemon.toml "merlin" should win regardless.
	got, err := resolveFirmware("auto", "merlin")
	if err != nil || got != firmware.KindMerlin {
		t.Fatalf("got (%q,%v) want (merlin,nil)", got, err)
	}
}

func TestResolveFirmware_InvalidFlag(t *testing.T) {
	_, err := resolveFirmware("openwrt", "")
	if err == nil {
		t.Fatal("expected error for invalid firmware name")
	}
}

func TestConfirmMerlin_YesAccepted(t *testing.T) {
	for _, ans := range []string{"y\n", "Y\n", "yes\n", "YES\n", " y \n"} {
		var out bytes.Buffer
		if !confirmMerlin(&out, strings.NewReader(ans)) {
			t.Errorf("answer %q should accept", ans)
		}
	}
}

func TestConfirmMerlin_NoOrEmptyRejected(t *testing.T) {
	for _, ans := range []string{"\n", "n\n", "no\n", "anything\n"} {
		var out bytes.Buffer
		if confirmMerlin(&out, strings.NewReader(ans)) {
			t.Errorf("answer %q should reject", ans)
		}
	}
}

func TestValidateGiteeToken_AcceptsSafeAndEmpty(t *testing.T) {
	for _, tok := range []string{"", "abc123", "ghp_AbCdEfG-_HiJkLmN1234567"} {
		if err := validateGiteeToken(tok); err != nil {
			t.Errorf("token %q should be accepted, got %v", tok, err)
		}
	}
}

func TestValidateGiteeToken_RejectsUnsafeChars(t *testing.T) {
	cases := []string{
		`abc"123`,
		`abc\123`,
		"abc\n123",
		"abc\r123",
	}
	for _, tok := range cases {
		if err := validateGiteeToken(tok); err == nil {
			t.Errorf("token %q should be rejected", tok)
		}
	}
}

// 显式 --binary 必须是绝对路径——否则会装一个 nat-start 找不到 sing-router
// 的失效 hook。Codex P3 (2026-05-17 review) 守护。
func TestResolveInstallBinary_RejectsRelativeFlag(t *testing.T) {
	for _, p := range []string{"sing-router", "./sing-router", "bin/sing-router"} {
		if _, err := resolveInstallBinary(p); err == nil {
			t.Errorf("resolveInstallBinary(%q) should have errored", p)
		}
	}
}

func TestResolveInstallBinary_AcceptsAbsoluteFlag(t *testing.T) {
	got, err := resolveInstallBinary("/opt/sbin/sing-router")
	if err != nil {
		t.Fatalf("resolveInstallBinary: %v", err)
	}
	if got != "/opt/sbin/sing-router" {
		t.Errorf("absolute flag should pass through unchanged, got %q", got)
	}
}

// 空 flag → 走 self-resolve；结果必须绝对路径。
func TestResolveInstallBinary_EmptyFlagFallsBackToSelf(t *testing.T) {
	got, err := resolveInstallBinary("")
	if err != nil {
		t.Fatalf("resolveInstallBinary: %v", err)
	}
	if !filepath.IsAbs(got) {
		t.Errorf("self-resolved path should be absolute, got %q", got)
	}
}
