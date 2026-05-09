package install

import (
	"os"
	"path/filepath"
	"testing"
)

func TestEnsureLayoutCreatesAllDirs(t *testing.T) {
	dir := t.TempDir()
	rundir := filepath.Join(dir, "sing-router")
	if err := EnsureLayout(rundir); err != nil {
		t.Fatal(err)
	}
	for _, sub := range []string{"config.d", "bin", "var", "run", "log"} {
		if info, err := os.Stat(filepath.Join(rundir, sub)); err != nil || !info.IsDir() {
			t.Errorf("missing dir %s: %v", sub, err)
		}
	}
	// ui 不创建
	if _, err := os.Stat(filepath.Join(rundir, "ui")); !os.IsNotExist(err) {
		t.Errorf("ui dir should NOT be created by install: %v", err)
	}
}

func TestEnsureLayoutIdempotent(t *testing.T) {
	dir := t.TempDir()
	rundir := filepath.Join(dir, "sing-router")
	for i := 0; i < 3; i++ {
		if err := EnsureLayout(rundir); err != nil {
			t.Fatalf("attempt %d: %v", i, err)
		}
	}
}
