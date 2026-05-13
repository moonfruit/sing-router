package cli

import (
	"bytes"
	"os"
	"testing"

	"github.com/moonfruit/sing-router/internal/log"
)

func TestAutoLogProfile(t *testing.T) {
	cases := []struct {
		name      string
		env       map[string]string
		want      log.Profile
		clearKeys []string
	}{
		{name: "no_color", env: map[string]string{"NO_COLOR": "1"}, want: log.ProfileNone},
		{name: "truecolor", env: map[string]string{"COLORTERM": "truecolor"}, want: log.ProfileTrueColor},
		{name: "24bit", env: map[string]string{"COLORTERM": "24bit"}, want: log.ProfileTrueColor},
		{name: "256color term", env: map[string]string{"TERM": "xterm-256color"}, want: log.Profile256},
		{name: "basic 8", env: map[string]string{"TERM": "xterm"}, want: log.Profile8},
		{name: "dumb", env: map[string]string{"TERM": "dumb"}, want: log.ProfileNone},
		{name: "empty", env: map[string]string{}, want: log.ProfileNone},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Setenv("NO_COLOR", "")
			t.Setenv("COLORTERM", "")
			t.Setenv("TERM", "")
			for k, v := range c.env {
				t.Setenv(k, v)
			}
			// 在 t.Setenv("X","") 后 os.Getenv("X")=="" 但 LookupEnv 仍 ok；
			// autoLogProfile 用的是 Getenv，已正确：空字符串等同未设。
			if got := autoLogProfile(); got != c.want {
				t.Errorf("autoLogProfile env=%v: got %v want %v", c.env, got, c.want)
			}
		})
	}
}

func TestResolveLogColorNever(t *testing.T) {
	t.Setenv("NO_COLOR", "")
	t.Setenv("TERM", "xterm-256color")
	p, err := ResolveLogColor("never", "", "", os.Stdout)
	if err != nil || p != log.ProfileNone {
		t.Fatalf("never: got (%v,%v)", p, err)
	}
}

func TestResolveLogColorAutoNonTTY(t *testing.T) {
	t.Setenv("NO_COLOR", "")
	t.Setenv("TERM", "xterm-256color")
	var buf bytes.Buffer
	p, err := ResolveLogColor("auto", "truecolor", "auto", &buf)
	if err != nil || p != log.ProfileNone {
		t.Fatalf("auto non-TTY should be none; got (%v,%v)", p, err)
	}
}

func TestResolveLogColorAlwaysFlagOverridesCfg(t *testing.T) {
	t.Setenv("NO_COLOR", "")
	t.Setenv("TERM", "")
	var buf bytes.Buffer
	p, err := ResolveLogColor("always", "truecolor", "8", &buf)
	if err != nil || p != log.ProfileTrueColor {
		t.Fatalf("always + flag should win; got (%v,%v)", p, err)
	}
}

func TestResolveLogColorAlwaysCfgWhenNoFlag(t *testing.T) {
	t.Setenv("NO_COLOR", "")
	t.Setenv("TERM", "")
	var buf bytes.Buffer
	p, err := ResolveLogColor("always", "", "256", &buf)
	if err != nil || p != log.Profile256 {
		t.Fatalf("always + cfg=256: got (%v,%v)", p, err)
	}
}

func TestResolveLogColorAlwaysFallback8(t *testing.T) {
	t.Setenv("NO_COLOR", "")
	t.Setenv("COLORTERM", "")
	t.Setenv("TERM", "")
	var buf bytes.Buffer
	p, err := ResolveLogColor("always", "", "auto", &buf)
	if err != nil || p != log.Profile8 {
		t.Fatalf("always with no info should fallback to 8; got (%v,%v)", p, err)
	}
}

func TestResolveLogColorBadMode(t *testing.T) {
	if _, err := ResolveLogColor("rainbow", "", "", os.Stdout); err == nil {
		t.Fatal("expected error for invalid color mode")
	}
}
