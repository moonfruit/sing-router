package install

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
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

func TestSeedDefaults_CopiesEmbeddedRuleSets(t *testing.T) {
	dir := t.TempDir()
	if err := EnsureLayout(dir); err != nil {
		t.Fatal(err)
	}
	if err := SeedDefaults(dir, TemplateVars{Firmware: "koolshare"}); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{
		"var/rules/geoip-cn.srs",
		"var/rules/geoip-cn.srs.etag",
		"var/rules/geosites-cn.srs",
		"var/rules/lan.srs",
		"var/rules/fakeip-bypass.srs",
	} {
		info, err := os.Stat(filepath.Join(dir, p))
		if err != nil {
			t.Errorf("missing %s: %v", p, err)
			continue
		}
		if info.Size() == 0 {
			t.Errorf("%s is empty (embed not populated?)", p)
		}
	}
}

func TestWriteIfNewer_SkipsWhenTargetNewer(t *testing.T) {
	rundir := t.TempDir()
	dst := "var/cn.txt"
	full := filepath.Join(rundir, dst)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte("user-content"), 0o644); err != nil {
		t.Fatal(err)
	}
	// 假装 binary 比文件早 1 小时 → 不该覆盖
	cmpMtime := time.Now().Add(-1 * time.Hour)
	called := false
	err := writeIfNewer(rundir, dst, cmpMtime, func() ([]byte, error) {
		called = true
		return []byte("embedded"), nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if called {
		t.Error("produce should not be called when target newer than cmpMtime")
	}
	got, _ := os.ReadFile(full)
	if string(got) != "user-content" {
		t.Errorf("file overwritten: %q", got)
	}
}

func TestWriteIfNewer_OverwritesWhenBinaryNewer(t *testing.T) {
	rundir := t.TempDir()
	dst := "var/cn.txt"
	full := filepath.Join(rundir, dst)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	// 把文件 mtime 倒退 1 小时；binary cmpMtime = now → 比文件新 → 该覆盖
	old := time.Now().Add(-1 * time.Hour)
	if err := os.Chtimes(full, old, old); err != nil {
		t.Fatal(err)
	}
	cmpMtime := time.Now()
	if err := writeIfNewer(rundir, dst, cmpMtime, func() ([]byte, error) {
		return []byte("fresh"), nil
	}); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(full)
	if string(got) != "fresh" {
		t.Errorf("expected overwrite to 'fresh', got %q", got)
	}
}

func TestWriteIfNewer_ZeroCmpMtimeIsLikeWriteIfMissing(t *testing.T) {
	rundir := t.TempDir()
	dst := "var/cn.txt"
	full := filepath.Join(rundir, dst)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte("user"), 0o644); err != nil {
		t.Fatal(err)
	}
	// cmpMtime 零值 → 已存在则不写
	if err := writeIfNewer(rundir, dst, time.Time{}, func() ([]byte, error) {
		return []byte("embedded"), nil
	}); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(full)
	if string(got) != "user" {
		t.Errorf("zero cmpMtime should preserve existing file; got %q", got)
	}
}

func TestSeedDefaults_DaemonTomlNotOverwrittenByNewerBinary(t *testing.T) {
	dir := t.TempDir()
	if err := EnsureLayout(dir); err != nil {
		t.Fatal(err)
	}
	tomlPath := filepath.Join(dir, "daemon.toml")
	if err := os.WriteFile(tomlPath, []byte("# user edited\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// daemon.toml 是 writeIfMissing 路径，不受 binary mtime 影响
	if err := SeedDefaults(dir, TemplateVars{Firmware: "koolshare"}); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(tomlPath)
	if !strings.Contains(string(got), "user edited") {
		t.Errorf("daemon.toml must be preserved by writeIfMissing semantics; got: %s", got)
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
