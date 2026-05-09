package firmware

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func TestKindString(t *testing.T) {
	cases := []struct {
		k    Kind
		want string
	}{
		{KindKoolshare, "koolshare"},
		{KindMerlin, "merlin"},
	}
	for _, c := range cases {
		if string(c.k) != c.want {
			t.Errorf("Kind=%q want %q", c.k, c.want)
		}
	}
}

func TestErrUnknownIsSentinel(t *testing.T) {
	wrapped := fmt.Errorf("detect failed: %w", ErrUnknown)
	if !errors.Is(wrapped, ErrUnknown) {
		t.Fatal("errors.Is(wrapped, ErrUnknown) should match through %w wrapping")
	}
}

func TestByName(t *testing.T) {
	cases := []struct {
		name    string
		wantKind Kind
		wantErr bool
	}{
		{"koolshare", KindKoolshare, false},
		{"merlin", KindMerlin, false},
		{"Koolshare", "", true},
		{"openwrt", "", true},
		{"", "", true},
	}
	for _, c := range cases {
		got, err := ByName(c.name)
		if c.wantErr {
			if err == nil {
				t.Errorf("ByName(%q) want err, got nil", c.name)
			}
			continue
		}
		if err != nil {
			t.Errorf("ByName(%q) unexpected err: %v", c.name, err)
			continue
		}
		if got.Kind() != c.wantKind {
			t.Errorf("ByName(%q) Kind=%q want %q", c.name, got.Kind(), c.wantKind)
		}
	}
}

func TestNewReturnsCorrectKind(t *testing.T) {
	if New(KindKoolshare).Kind() != KindKoolshare {
		t.Error("New(KindKoolshare) wrong Kind")
	}
	if New(KindMerlin).Kind() != KindMerlin {
		t.Error("New(KindMerlin) wrong Kind")
	}
}

func TestDetectKoolshareViaSymlink(t *testing.T) {
	old := detectBase
	t.Cleanup(func() { detectBase = old })
	dir := t.TempDir()
	detectBase = dir

	// Create /jffs/.asusrouter -> /koolshare/bin/kscore.sh
	if err := os.MkdirAll(filepath.Join(dir, "jffs"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "koolshare/bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "jffs/.asusrouter")
	target := filepath.Join(dir, "koolshare/bin/kscore.sh")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}

	got, err := Detect()
	if err != nil {
		t.Fatalf("expected nil err, got %v", err)
	}
	if got != KindKoolshare {
		t.Fatalf("Detect=%q want %q", got, KindKoolshare)
	}
}

func TestDetectKoolshareViaKscoreFile(t *testing.T) {
	old := detectBase
	t.Cleanup(func() { detectBase = old })
	dir := t.TempDir()
	detectBase = dir

	kscore := filepath.Join(dir, "koolshare/bin/kscore.sh")
	if err := os.MkdirAll(filepath.Dir(kscore), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(kscore, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	got, err := Detect()
	if err != nil {
		t.Fatalf("expected nil err, got %v", err)
	}
	if got != KindKoolshare {
		t.Fatalf("Detect=%q want %q", got, KindKoolshare)
	}
}

func TestDetectUnknown(t *testing.T) {
	old := detectBase
	t.Cleanup(func() { detectBase = old })
	detectBase = t.TempDir() // empty

	_, err := Detect()
	if !errors.Is(err, ErrUnknown) {
		t.Fatalf("want ErrUnknown, got %v", err)
	}
}

func TestDetectKscoreNotExecRejects(t *testing.T) {
	old := detectBase
	t.Cleanup(func() { detectBase = old })
	dir := t.TempDir()
	detectBase = dir

	kscore := filepath.Join(dir, "koolshare/bin/kscore.sh")
	_ = os.MkdirAll(filepath.Dir(kscore), 0o755)
	_ = os.WriteFile(kscore, []byte("not exec"), 0o644)

	_, err := Detect()
	if !errors.Is(err, ErrUnknown) {
		t.Fatalf("non-exec kscore.sh should not match; got %v", err)
	}
}
