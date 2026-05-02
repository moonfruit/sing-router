package cli

import (
	"bytes"
	"strings"
	"testing"

	"github.com/moonfruit/sing-router/internal/version"
)

func TestVersionSubcommand(t *testing.T) {
	version.Version = "1.2.3"
	cmd := NewRootCmd()
	cmd.SetArgs([]string{"version"})

	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetErr(&buf)

	if err := cmd.Execute(); err != nil {
		t.Fatalf("execute: %v", err)
	}
	if got := strings.TrimSpace(buf.String()); got != "1.2.3" {
		t.Fatalf("want 1.2.3, got %q", got)
	}
}
