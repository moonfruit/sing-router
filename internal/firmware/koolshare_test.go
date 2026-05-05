package firmware

import (
	"io/fs"
	"os"
	"path/filepath"
	"testing"
	"testing/fstest"
)

func newTestKoolshare(t *testing.T) *koolshare {
	t.Helper()
	a := fstest.MapFS{
		"firmware/koolshare/N99sing-router.sh": &fstest.MapFile{
			Data: []byte("#!/bin/sh\nexit 0\n"),
			Mode: 0o644,
		},
	}
	return &koolshare{base: t.TempDir(), assets: a}
}

func TestKoolshareKind(t *testing.T) {
	k := newTestKoolshare(t)
	if k.Kind() != KindKoolshare {
		t.Fatalf("Kind=%q want %q", k.Kind(), KindKoolshare)
	}
}

func TestKoolshareInstallHooksWritesScript(t *testing.T) {
	k := newTestKoolshare(t)
	if err := k.InstallHooks("/opt/home/sing-router"); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(k.base, "koolshare/init.d/N99sing-router.sh")
	info, err := os.Stat(target)
	if err != nil {
		t.Fatalf("script not created: %v", err)
	}
	if info.Mode().Perm()&0o100 == 0 {
		t.Fatalf("script not executable: mode=%v", info.Mode())
	}
	data, _ := os.ReadFile(target)
	if string(data) != "#!/bin/sh\nexit 0\n" {
		t.Fatalf("script content mismatch: %q", data)
	}
}

func TestKoolshareInstallHooksIdempotent(t *testing.T) {
	k := newTestKoolshare(t)
	if err := k.InstallHooks(""); err != nil {
		t.Fatal(err)
	}
	if err := k.InstallHooks(""); err != nil {
		t.Fatalf("second install should be no-op success, got %v", err)
	}
}

func TestKoolshareRemoveHooksWhenAbsent(t *testing.T) {
	k := newTestKoolshare(t)
	if err := k.RemoveHooks(); err != nil {
		t.Fatalf("Remove on absent should be nil, got %v", err)
	}
}

func TestKoolshareRemoveHooksWhenPresent(t *testing.T) {
	k := newTestKoolshare(t)
	_ = k.InstallHooks("")
	if err := k.RemoveHooks(); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(k.base, "koolshare/init.d/N99sing-router.sh")
	if _, err := os.Stat(target); !os.IsNotExist(err) {
		t.Fatalf("script should be gone, stat err=%v", err)
	}
}

func TestKoolshareVerifyHooks(t *testing.T) {
	k := newTestKoolshare(t)

	// absent
	got := k.VerifyHooks()
	if len(got) != 1 || got[0].Present {
		t.Fatalf("expected 1 absent check, got %+v", got)
	}
	if got[0].Type != "file" {
		t.Fatalf("expected Type=file, got %q", got[0].Type)
	}

	// install then verify present
	_ = k.InstallHooks("")
	got = k.VerifyHooks()
	if !got[0].Present {
		t.Fatalf("expected Present=true after install, got %+v", got[0])
	}

	// strip exec bit -> Present=false (file exists but non-exec)
	_ = os.Chmod(filepath.Join(k.base, "koolshare/init.d/N99sing-router.sh"), 0o644)
	got = k.VerifyHooks()
	if got[0].Present {
		t.Fatalf("non-exec file should be Present=false, got %+v", got[0])
	}
}

// Compile-time assertions
var _ Target = (*koolshare)(nil)
var _ fs.FS = fstest.MapFS{}
