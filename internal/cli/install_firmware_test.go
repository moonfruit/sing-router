package cli

import (
	"bytes"
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
