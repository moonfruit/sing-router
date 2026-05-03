package install

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSeedWritesDefaultsWhenMissing(t *testing.T) {
	dir := t.TempDir()
	if err := EnsureLayout(dir); err != nil {
		t.Fatal(err)
	}
	if err := SeedDefaults(dir); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{
		"daemon.toml",
		"config.d/clash.json",
		"config.d/dns.json",
		"config.d/inbounds.json",
		"config.d/log.json",
		"config.d/cache.json",
		"config.d/certificate.json",
		"config.d/http.json",
		"config.d/outbounds.json",
	} {
		if _, err := os.Stat(filepath.Join(dir, p)); err != nil {
			t.Errorf("missing seed file %s: %v", p, err)
		}
	}
}

func TestSeedPreservesExisting(t *testing.T) {
	dir := t.TempDir()
	if err := EnsureLayout(dir); err != nil {
		t.Fatal(err)
	}
	daemonToml := filepath.Join(dir, "daemon.toml")
	if err := os.WriteFile(daemonToml, []byte("# user edit\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := SeedDefaults(dir); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(daemonToml)
	if !strings.Contains(string(data), "user edit") {
		t.Fatalf("seed should not overwrite existing daemon.toml; got: %s", data)
	}
}
