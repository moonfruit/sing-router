package install

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/moonfruit/sing-router/assets"
)

func TestSeedWritesDefaultsWhenMissing(t *testing.T) {
	dir := t.TempDir()
	if err := EnsureLayout(dir); err != nil {
		t.Fatal(err)
	}
	vars := TemplateVars{
		DownloadSingBox: true,
		DownloadCNList:  false,
		AutoStart:       true,
		Firmware:        "koolshare",
	}
	if err := SeedDefaults(dir, vars); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{
		"daemon.toml",
		"config.d/clash.json",
		"config.d/dns.json",
		"config.d/hosts",
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
		DownloadSingBox: true,
		DownloadCNList:  false,
		AutoStart:       true,
		Firmware:        "koolshare",
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
	// daemon.toml 走 writeDefaultAndSeed，已存在则用户编辑保留不动
	if err := SeedDefaults(dir, TemplateVars{Firmware: "koolshare"}); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(tomlPath)
	if !strings.Contains(string(got), "user edited") {
		t.Errorf("daemon.toml must be preserved when it already exists; got: %s", got)
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

// 首次 install：xxx 落盘，不应该产生 xxx.default（缺失对比基准 → 无意义）。
func TestSeedDefaults_FirstInstallWritesSeedOnly(t *testing.T) {
	dir := t.TempDir()
	if err := EnsureLayout(dir); err != nil {
		t.Fatal(err)
	}
	if err := SeedDefaults(dir, TemplateVars{Firmware: "koolshare"}); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{
		"daemon.toml",
		"config.d/clash.json",
		"config.d/dns.json",
		"config.d/hosts",
		"config.d/inbounds.json",
		"config.d/log.json",
		"config.d/cache.json",
		"config.d/certificate.json",
		"config.d/http.json",
		"config.d/outbounds.json",
		"config.d/zoo.json",
	} {
		seed := filepath.Join(dir, p)
		if _, err := os.Stat(seed); err != nil {
			t.Errorf("missing seed %s: %v", p, err)
		}
		if _, err := os.Stat(seed + ".default"); !os.IsNotExist(err) {
			t.Errorf("first install should not create %s.default (err=%v)", p, err)
		}
	}
}

// xxx 已存在且与嵌入内容一致：不写 xxx.default，避免产生冗余文件。
func TestWriteDefaultAndSeed_NoDefaultWhenSeedAlreadyMatches(t *testing.T) {
	rundir := t.TempDir()
	dst := "config.d/foo.json"
	full := filepath.Join(rundir, dst)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte("same"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := writeDefaultAndSeed(rundir, dst, func() ([]byte, error) {
		return []byte("same"), nil
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(full + ".default"); !os.IsNotExist(err) {
		t.Errorf("expected no .default when seed already matches; stat err=%v", err)
	}
	got, _ := os.ReadFile(full)
	if string(got) != "same" {
		t.Errorf("seed should be untouched; got %q", got)
	}
}

// xxx 已存在但内容与嵌入不同：保留 xxx（用户编辑），把新内容覆盖到 xxx.default。
func TestWriteDefaultAndSeed_WritesDefaultOnDivergence(t *testing.T) {
	rundir := t.TempDir()
	dst := "config.d/foo.json"
	full := filepath.Join(rundir, dst)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte("user-edited"), 0o644); err != nil {
		t.Fatal(err)
	}
	// 旧的 .default 也应被新内容覆盖
	if err := os.WriteFile(full+".default", []byte("stale-default"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := writeDefaultAndSeed(rundir, dst, func() ([]byte, error) {
		return []byte("new-default"), nil
	}); err != nil {
		t.Fatal(err)
	}
	gotSeed, _ := os.ReadFile(full)
	if string(gotSeed) != "user-edited" {
		t.Errorf("seed should be preserved; got %q", gotSeed)
	}
	gotDef, _ := os.ReadFile(full + ".default")
	if string(gotDef) != "new-default" {
		t.Errorf(".default should hold the latest embedded content; got %q", gotDef)
	}
}

// daemon.toml 比较必须是【现有文件 vs 渲染后文本】，不能 vs 原始模板。
// 现有文件就是上次同 vars 渲染的产物 → 二次 install 同 vars → 应不产生 .default。
func TestSeedDefaults_DaemonTomlRenderedCompare_NoDefaultWhenVarsUnchanged(t *testing.T) {
	dir := t.TempDir()
	if err := EnsureLayout(dir); err != nil {
		t.Fatal(err)
	}
	vars := TemplateVars{Firmware: "koolshare", GiteeToken: "abc123", AutoStart: true}
	if err := SeedDefaults(dir, vars); err != nil {
		t.Fatal(err)
	}
	defPath := filepath.Join(dir, "daemon.toml.default")
	// 首装：不应有 .default
	if _, err := os.Stat(defPath); !os.IsNotExist(err) {
		t.Fatalf("first install must not create daemon.toml.default; stat err=%v", err)
	}
	// 同 vars 再装一次：现有 daemon.toml 与重新渲染结果一致 → 仍不应有 .default。
	// 如果比较错误地走模板原文（带 {{.GiteeToken}}），那两次都会判定不一致并写 .default。
	if err := SeedDefaults(dir, vars); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(defPath); !os.IsNotExist(err) {
		t.Fatalf("second install with identical vars must not create daemon.toml.default; stat err=%v", err)
	}
}

// daemon.toml 已存在且与新 vars 渲染结果不同：写 daemon.toml.default，
// 其内容必须等于"新 vars 渲染后的文本"——不是模板原文，也不是旧 vars 渲染结果。
func TestSeedDefaults_DaemonTomlRenderedCompare_DefaultEqualsNewRender(t *testing.T) {
	dir := t.TempDir()
	if err := EnsureLayout(dir); err != nil {
		t.Fatal(err)
	}
	if err := SeedDefaults(dir, TemplateVars{Firmware: "koolshare", GiteeToken: "old"}); err != nil {
		t.Fatal(err)
	}
	newVars := TemplateVars{Firmware: "koolshare", GiteeToken: "new"}
	if err := SeedDefaults(dir, newVars); err != nil {
		t.Fatal(err)
	}
	gotDef, err := os.ReadFile(filepath.Join(dir, "daemon.toml.default"))
	if err != nil {
		t.Fatalf("daemon.toml.default missing: %v", err)
	}
	wantRendered, err := renderDaemonToml(newVars)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(gotDef, wantRendered) {
		t.Errorf("daemon.toml.default must equal renderDaemonToml(newVars)")
	}
	// 反向断言：.default 不应等于未渲染的原始模板。
	rawTmpl, err := assets.ReadFile("daemon.toml.tmpl")
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(gotDef, rawTmpl) {
		t.Error("daemon.toml.default must not equal the raw (unrendered) template")
	}
}

// xxx 不存在：写 xxx，不写 xxx.default。
func TestWriteDefaultAndSeed_FirstInstallSkipsDefault(t *testing.T) {
	rundir := t.TempDir()
	dst := "config.d/foo.json"
	if err := os.MkdirAll(filepath.Join(rundir, "config.d"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := writeDefaultAndSeed(rundir, dst, func() ([]byte, error) {
		return []byte("v1"), nil
	}); err != nil {
		t.Fatal(err)
	}
	full := filepath.Join(rundir, dst)
	got, err := os.ReadFile(full)
	if err != nil {
		t.Fatalf("seed should be written: %v", err)
	}
	if string(got) != "v1" {
		t.Errorf("seed content mismatch: %q", got)
	}
	if _, err := os.Stat(full + ".default"); !os.IsNotExist(err) {
		t.Errorf("first install should not create .default; stat err=%v", err)
	}
}
