package install

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestValidateBinaryPath_AcceptsAbsolute(t *testing.T) {
	for _, p := range []string{"/opt/sbin/sing-router", "/usr/local/bin/sing-router"} {
		if err := ValidateBinaryPath(p); err != nil {
			t.Errorf("ValidateBinaryPath(%q) unexpected error: %v", p, err)
		}
	}
}

func TestValidateBinaryPath_RejectsRelativeAndEmpty(t *testing.T) {
	for _, p := range []string{"", "sing-router", "./sing-router", "bin/sing-router"} {
		if err := ValidateBinaryPath(p); err == nil {
			t.Errorf("ValidateBinaryPath(%q) should have errored", p)
		}
	}
}

func TestResolveSelfBinary_ReturnsAbsolute(t *testing.T) {
	bin, err := ResolveSelfBinary()
	if err != nil {
		t.Fatalf("ResolveSelfBinary: %v", err)
	}
	if !filepath.IsAbs(bin) {
		t.Errorf("ResolveSelfBinary returned non-absolute path: %q", bin)
	}
	// In go test the running binary is the test binary; just ensure non-empty
	// and that the path looks plausible (no template leakage etc.).
	if strings.TrimSpace(bin) == "" {
		t.Errorf("ResolveSelfBinary returned empty path")
	}
}
