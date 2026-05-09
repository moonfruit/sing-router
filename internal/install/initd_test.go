package install

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWriteInitdSetsExecutable(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "S99sing-router")
	if err := WriteInitd(target, "/opt/home/sing-router"); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(target)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm()&0o100 == 0 {
		t.Fatalf("not executable: %v", info.Mode())
	}
	data, _ := os.ReadFile(target)
	if !strings.Contains(string(data), `daemon -D /opt/home/sing-router`) {
		t.Fatalf("ARGS not substituted: %s", data)
	}
}

func TestWriteInitdOverwrites(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "S99sing-router")
	if err := os.WriteFile(target, []byte("garbage"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := WriteInitd(target, "/opt/x"); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(target)
	if strings.Contains(string(data), "garbage") {
		t.Fatal("init.d should be overwritten")
	}
}
