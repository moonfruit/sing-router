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
	vars := TemplateVars{
		DownloadSingBox:   true,
		DownloadCNList:    false,
		DownloadZashboard: false,
		AutoStart:         true,
		Firmware:          "koolshare",
	}
	if err := SeedDefaults(dir, vars); err != nil {
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
		"config.d/zoo.json",
		"var/cn.txt",
	} {
		if _, err := os.Stat(filepath.Join(dir, p)); err != nil {
			t.Errorf("missing seed file %s: %v", p, err)
		}
	}
	cnData, err := os.ReadFile(filepath.Join(dir, "var/cn.txt"))
	if err != nil {
		t.Fatalf("read seeded cn.txt: %v", err)
	}
	firstLine := strings.SplitN(string(cnData), "\n", 2)[0]
	if !strings.Contains(firstLine, "/") {
		t.Errorf("seeded cn.txt first line is not a CIDR: %q", firstLine)
	}
}

func TestSeedRendersTemplateVars(t *testing.T) {
	dir := t.TempDir()
	if err := EnsureLayout(dir); err != nil {
		t.Fatal(err)
	}
	vars := TemplateVars{
		DownloadSingBox:   true,
		DownloadCNList:    false,
		DownloadZashboard: false,
		AutoStart:         true,
		Firmware:          "koolshare",
	}
	if err := SeedDefaults(dir, vars); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(dir, "daemon.toml"))
	if err != nil {
		t.Fatal(err)
	}
	got := string(data)
	for _, want := range []string{
		"download_sing_box   = true",
		"download_cn_list    = false",
		"auto_start          = true",
		`firmware            = "koolshare"`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("rendered daemon.toml missing %q\n--- got ---\n%s", want, got)
		}
	}
}

func TestSeedRendersGiteeToken(t *testing.T) {
	dir := t.TempDir()
	if err := EnsureLayout(dir); err != nil {
		t.Fatal(err)
	}
	vars := TemplateVars{Firmware: "koolshare", GiteeToken: "abc123"}
	if err := SeedDefaults(dir, vars); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(dir, "daemon.toml"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `token = "abc123"`) {
		t.Errorf("daemon.toml missing token = \"abc123\"\n--- got ---\n%s", data)
	}
}

func TestSeedRendersEmptyGiteeToken(t *testing.T) {
	dir := t.TempDir()
	if err := EnsureLayout(dir); err != nil {
		t.Fatal(err)
	}
	if err := SeedDefaults(dir, TemplateVars{Firmware: "koolshare"}); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(dir, "daemon.toml"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `token = ""`) {
		t.Errorf("daemon.toml should default token to empty string\n--- got ---\n%s", data)
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
	if err := SeedDefaults(dir, TemplateVars{Firmware: "koolshare"}); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(daemonToml)
	if !strings.Contains(string(data), "user edit") {
		t.Fatalf("seed should not overwrite existing daemon.toml; got: %s", data)
	}
}
