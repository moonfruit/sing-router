# Firmware Koolshare Primary Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Reverse `sing-router` 的目标固件优先级——把 koolshare 官改 + Entware 提升为主支持，把梅林固件 + Entware 降为只编译/单元测试不验证的后备。把所有固件相关动作封装到新包 `internal/firmware/`，让其它包对固件无感知。

**Architecture:** 引入 `internal/firmware` 抽象层，定义 `Target` 接口（`InstallHooks` / `RemoveHooks` / `VerifyHooks`），两份实现（`koolshare`、`merlin`）。`Detect()` 仅在能强证 koolshare 时返回 koolshare，否则返回 `ErrUnknown` 让 install 命令显式索要 `--firmware` flag。决议结果写入 `daemon.toml [install].firmware` 持久化；`uninstall` / `doctor` 读它而不重新探测。`assets/jffs/` 被 `assets/firmware/{koolshare,merlin}/` 取代。daemon/supervisor/config/log/shell 包零修改。

**Tech Stack:** Go (existing); BurntSushi/toml (existing); cobra (existing); embed/io/fs（标准库）。

**Spec:** `docs/superpowers/specs/2026-05-05-firmware-koolshare-primary-design.md`（commit ad25a7a）

---

## File Structure

**Create:**
- `internal/firmware/firmware.go` — `Kind`, `HookCheck`, `Target` 接口, `ByName`, `New`, `Detect`, `ErrUnknown`
- `internal/firmware/firmware_test.go`
- `internal/firmware/koolshare.go` — koolshare `Target` 实现
- `internal/firmware/koolshare_test.go`
- `internal/firmware/merlin.go` — merlin `Target` 实现（委托给 `internal/install.InjectHook/RemoveHook`）
- `internal/firmware/merlin_test.go`
- `internal/firmware/nvram.go` — `nvramReader` 接口 + `shellNvram` 默认实现
- `internal/firmware/nvram_test.go`
- `assets/firmware/koolshare/N99sing-router.sh` — koolshare nat-start hook 脚本
- `assets/firmware/merlin/nat-start.snippet` — 从 `assets/jffs/` 迁移
- `assets/firmware/merlin/services-start.snippet` — 从 `assets/jffs/` 迁移

**Modify:**
- `assets/embed.go` — `//go:embed` directive 增加 `firmware/**`
- `assets/embed_test.go` — 替换路径测试
- `assets/daemon.toml.default` — `[install]` section 增加 `firmware` 注释
- `internal/config/daemon_toml.go` — `InstallConfig` 增加 `Firmware string` 字段；新增 `WriteInstallFirmware()` helper
- `internal/config/daemon_toml_test.go` — 测试新字段 + 新 helper
- `internal/cli/install.go` — 加 `--firmware`/`--yes` flags；替换 hook 注入为 `firmware.Target`；改名 `--skip-jffs` → `--skip-firmware-hooks`；写回 `[install].firmware`
- `internal/cli/uninstall.go` — 读 daemon.toml 决议 firmware；用 `Target.RemoveHooks`；改名 flag
- `internal/cli/doctor.go` — 替换 `checkJffsHook` 为遍历 `Target.VerifyHooks()`；显示 firmware target
- `internal/daemon/api.go` — `statusSnapshot` 加 `daemon.firmware` 字段
- `internal/cli/status.go` — pretty print 加 firmware 行
- `internal/cli/wireup_daemon.go` — 把 firmware kind 注入 daemon `StatusExtra`

**Delete:**
- `assets/jffs/nat-start.snippet`（迁移后删除）
- `assets/jffs/services-start.snippet`（迁移后删除）
- `assets/jffs/` 目录

**Untouched (per spec):** `internal/daemon/supervisor.go`, `internal/daemon/statemachine.go`, `internal/daemon/ready.go`, `internal/config/zoo.go`, `internal/log/*`, `internal/shell/*`, `internal/state/*`, `assets/shell/*`, `assets/initd/*`, `assets/config.d.default/*`, `internal/install/jffs_hooks.go`（保留，但仅 firmware/merlin.go 调用）。

---

## Task 1: Bootstrap firmware package with types and Target interface

**Files:**
- Create: `internal/firmware/firmware.go`
- Test: `internal/firmware/firmware_test.go`

- [ ] **Step 1: Write the failing test**

Create `internal/firmware/firmware_test.go`:

```go
package firmware

import (
	"errors"
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
	wrapped := errors.New("x: " + ErrUnknown.Error())
	_ = wrapped
	if !errors.Is(ErrUnknown, ErrUnknown) {
		t.Fatal("ErrUnknown should match itself via errors.Is")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/firmware/... -run TestKind -v`
Expected: FAIL — package `firmware` does not exist.

- [ ] **Step 3: Write minimal implementation**

Create `internal/firmware/firmware.go`:

```go
// Package firmware 封装 sing-router 在不同路由器固件下的 install / uninstall /
// doctor 三类操作差异。daemon/supervisor 等运行时不依赖此包。
package firmware

import "errors"

// Kind 是已知的固件目标。新增目标只在此处加。
type Kind string

const (
	KindKoolshare Kind = "koolshare"
	KindMerlin    Kind = "merlin"
)

// HookCheck 是 doctor 用的只读体检结果项。
type HookCheck struct {
	Kind     string // "file" | "nvram"
	Path     string // 文件路径或 nvram 键名
	Required bool
	Present  bool
	Note     string
}

// Target 封装一个固件目标的全部"安装侧"能力。
// 不涉及 daemon/supervisor 的运行时行为——那部分对所有目标统一。
type Target interface {
	Kind() Kind
	InstallHooks(rundir string) error
	RemoveHooks() error
	VerifyHooks() []HookCheck
}

// ErrUnknown 由 Detect 返回，表示当前环境无法被强证为任何已知固件。
var ErrUnknown = errors.New("firmware: cannot determine target")
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/firmware/... -v`
Expected: PASS — both tests green.

- [ ] **Step 5: Commit**

```bash
git add internal/firmware/firmware.go internal/firmware/firmware_test.go
git commit -m "feat(firmware): bootstrap package with Kind/HookCheck/Target"
```

---

## Task 2: nvramReader interface + shellNvram default

**Files:**
- Create: `internal/firmware/nvram.go`
- Test: `internal/firmware/nvram_test.go`

- [ ] **Step 1: Write the failing test**

Create `internal/firmware/nvram_test.go`:

```go
package firmware

import "testing"

func TestFakeNvramGet(t *testing.T) {
	f := fakeNvram{"jffs2_scripts": "1", "model": "RT-BE88U"}
	v, err := f.Get("jffs2_scripts")
	if err != nil || v != "1" {
		t.Fatalf("got (%q, %v) want (\"1\", nil)", v, err)
	}
	v, err = f.Get("missing")
	if err != nil {
		t.Fatalf("missing key should not error, got %v", err)
	}
	if v != "" {
		t.Fatalf("missing key should return empty string, got %q", v)
	}
}

func TestShellNvramGetTrimmed(t *testing.T) {
	old := nvramExec
	t.Cleanup(func() { nvramExec = old })
	nvramExec = func(args ...string) ([]byte, error) {
		if len(args) != 2 || args[0] != "get" || args[1] != "extendno" {
			t.Fatalf("unexpected args %v", args)
		}
		return []byte("37094_koolcenter\n"), nil
	}
	got, err := (shellNvram{}).Get("extendno")
	if err != nil || got != "37094_koolcenter" {
		t.Fatalf("got (%q, %v) want (37094_koolcenter, nil)", got, err)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/firmware/... -run Nvram -v`
Expected: FAIL — `nvramExec`, `shellNvram`, `fakeNvram` undefined.

- [ ] **Step 3: Write minimal implementation**

Create `internal/firmware/nvram.go`:

```go
package firmware

import (
	"os/exec"
	"strings"
)

// nvramReader 是 doctor 体检 Merlin 路径时读 nvram 的最小接口。
// 生产实现 shell out 到 `nvram get`；测试实现是内存 map。
type nvramReader interface {
	Get(key string) (string, error)
}

// nvramExec 是 shellNvram 真正调用的命令；测试可替换。
var nvramExec = func(args ...string) ([]byte, error) {
	return exec.Command("nvram", args...).Output()
}

type shellNvram struct{}

func (shellNvram) Get(key string) (string, error) {
	out, err := nvramExec("get", key)
	if err != nil {
		return "", err
	}
	return strings.TrimRight(string(out), "\r\n"), nil
}

// fakeNvram 仅用于测试。
type fakeNvram map[string]string

func (f fakeNvram) Get(key string) (string, error) { return f[key], nil }
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/firmware/... -run Nvram -v`
Expected: PASS — both tests green.

- [ ] **Step 5: Commit**

```bash
git add internal/firmware/nvram.go internal/firmware/nvram_test.go
git commit -m "feat(firmware): nvramReader interface + shellNvram"
```

---

## Task 3: koolshare Target implementation

**Files:**
- Create: `internal/firmware/koolshare.go`
- Test: `internal/firmware/koolshare_test.go`
- Note: 测试用 fixture 临时脚本内容；Task 9 才把真实脚本放入 `assets/firmware/koolshare/N99sing-router.sh`。

- [ ] **Step 1: Write the failing test**

Create `internal/firmware/koolshare_test.go`:

```go
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
	if got[0].Kind != "file" {
		t.Fatalf("expected Kind=file, got %q", got[0].Kind)
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

// 仅用于编译期断言：koolshare 实现了 Target 与 fs.FS 的预期形态
var _ Target = (*koolshare)(nil)
var _ fs.FS = fstest.MapFS{}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/firmware/... -run Koolshare -v`
Expected: FAIL — `koolshare` struct undefined.

- [ ] **Step 3: Write minimal implementation**

Create `internal/firmware/koolshare.go`:

```go
package firmware

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
)

const koolshareHookRel = "koolshare/init.d/N99sing-router.sh"
const koolshareAssetPath = "firmware/koolshare/N99sing-router.sh"

type koolshare struct {
	base   string // 默认 "/"
	assets fs.FS
}

func (k *koolshare) Kind() Kind { return KindKoolshare }

func (k *koolshare) InstallHooks(_ string) error {
	script, err := fs.ReadFile(k.assets, koolshareAssetPath)
	if err != nil {
		return err
	}
	target := filepath.Join(k.base, koolshareHookRel)
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return err
	}
	return atomicWriteExec(target, script, 0o755)
}

func (k *koolshare) RemoveHooks() error {
	target := filepath.Join(k.base, koolshareHookRel)
	if err := os.Remove(target); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

func (k *koolshare) VerifyHooks() []HookCheck {
	target := filepath.Join(k.base, koolshareHookRel)
	info, err := os.Stat(target)
	present := err == nil && !info.IsDir() && info.Mode()&0o111 != 0
	return []HookCheck{{
		Kind:     "file",
		Path:     target,
		Required: true,
		Present:  present,
		Note:     "koolshare nat-start hook (replays iptables on WAN/firewall restart)",
	}}
}

// atomicWriteExec writes data to path via tmp+rename and chmods to mode.
func atomicWriteExec(path string, data []byte, mode os.FileMode) error {
	tmp := path + ".new"
	if err := os.WriteFile(tmp, data, mode); err != nil {
		return err
	}
	if err := os.Chmod(tmp, mode); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, path)
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/firmware/... -run Koolshare -v`
Expected: PASS — all 5 koolshare tests green.

- [ ] **Step 5: Commit**

```bash
git add internal/firmware/koolshare.go internal/firmware/koolshare_test.go
git commit -m "feat(firmware): koolshare Target (Install/Remove/VerifyHooks)"
```

---

## Task 4: merlin Target implementation

**Files:**
- Create: `internal/firmware/merlin.go`
- Test: `internal/firmware/merlin_test.go`

- [ ] **Step 1: Write the failing test**

Create `internal/firmware/merlin_test.go`:

```go
package firmware

import (
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
)

func newTestMerlin(t *testing.T, nv nvramReader) *merlin {
	t.Helper()
	a := fstest.MapFS{
		"firmware/merlin/nat-start.snippet": &fstest.MapFile{
			Data: []byte("# BEGIN sing-router (managed by `sing-router install`; do not edit)\nsing-router reapply-rules >/dev/null 2>&1 &\n# END sing-router\n"),
		},
		"firmware/merlin/services-start.snippet": &fstest.MapFile{
			Data: []byte("# BEGIN sing-router (managed by `sing-router install`; do not edit)\n/opt/etc/init.d/S99sing-router start &\n# END sing-router\n"),
		},
	}
	return &merlin{base: t.TempDir(), assets: a, nvram: nv}
}

func TestMerlinKind(t *testing.T) {
	m := newTestMerlin(t, fakeNvram{})
	if m.Kind() != KindMerlin {
		t.Fatalf("Kind=%q want %q", m.Kind(), KindMerlin)
	}
}

func TestMerlinInstallHooksInjectsBlocks(t *testing.T) {
	m := newTestMerlin(t, fakeNvram{})
	if err := m.InstallHooks(""); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"nat-start", "services-start"} {
		path := filepath.Join(m.base, "jffs/scripts", name)
		data := readFileT(t, path)
		if !strings.Contains(data, "# BEGIN sing-router") {
			t.Errorf("%s missing BEGIN marker:\n%s", name, data)
		}
		if !strings.Contains(data, "# END sing-router") {
			t.Errorf("%s missing END marker:\n%s", name, data)
		}
	}
}

func TestMerlinInstallHooksIdempotent(t *testing.T) {
	m := newTestMerlin(t, fakeNvram{})
	if err := m.InstallHooks(""); err != nil {
		t.Fatal(err)
	}
	if err := m.InstallHooks(""); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"nat-start", "services-start"} {
		path := filepath.Join(m.base, "jffs/scripts", name)
		data := readFileT(t, path)
		if strings.Count(data, "# BEGIN sing-router") != 1 {
			t.Errorf("%s should have exactly one BEGIN block, got:\n%s", name, data)
		}
	}
}

func TestMerlinRemoveHooks(t *testing.T) {
	m := newTestMerlin(t, fakeNvram{})
	_ = m.InstallHooks("")
	if err := m.RemoveHooks(); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"nat-start", "services-start"} {
		path := filepath.Join(m.base, "jffs/scripts", name)
		data := readFileT(t, path)
		if strings.Contains(data, "BEGIN sing-router") {
			t.Errorf("%s still has block after Remove:\n%s", name, data)
		}
	}
}

func TestMerlinVerifyHooks(t *testing.T) {
	m := newTestMerlin(t, fakeNvram{"jffs2_scripts": "1"})
	checks := m.VerifyHooks()
	if len(checks) != 3 {
		t.Fatalf("want 3 checks, got %d", len(checks))
	}
	if checks[0].Kind != "nvram" || checks[0].Path != "jffs2_scripts" {
		t.Errorf("first check should be nvram[jffs2_scripts], got %+v", checks[0])
	}
	if !checks[0].Present {
		t.Errorf("jffs2_scripts=1 should report Present=true, got %+v", checks[0])
	}

	// hooks not installed -> file checks Present=false
	if checks[1].Present || checks[2].Present {
		t.Errorf("uninstalled hooks should be Present=false")
	}

	_ = m.InstallHooks("")
	checks = m.VerifyHooks()
	if !checks[1].Present || !checks[2].Present {
		t.Errorf("installed hooks should be Present=true")
	}
}

func TestMerlinVerifyHooksDisabledScripts(t *testing.T) {
	m := newTestMerlin(t, fakeNvram{"jffs2_scripts": "0"})
	checks := m.VerifyHooks()
	if checks[0].Present {
		t.Errorf("jffs2_scripts=0 should report Present=false, got %+v", checks[0])
	}
}

// helpers
func readFileT(t *testing.T, path string) string {
	t.Helper()
	b, err := readFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(b)
}

var _ Target = (*merlin)(nil)
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/firmware/... -run Merlin -v`
Expected: FAIL — `merlin` struct, `readFile` helper undefined.

- [ ] **Step 3: Write minimal implementation**

Create `internal/firmware/merlin.go`:

```go
package firmware

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/moonfruit/sing-router/internal/install"
)

type merlin struct {
	base   string
	assets fs.FS
	nvram  nvramReader
}

func (m *merlin) Kind() Kind { return KindMerlin }

func (m *merlin) InstallHooks(_ string) error {
	natPayload, err := readSnippetPayload(m.assets, "firmware/merlin/nat-start.snippet")
	if err != nil {
		return err
	}
	svcPayload, err := readSnippetPayload(m.assets, "firmware/merlin/services-start.snippet")
	if err != nil {
		return err
	}
	natPath := filepath.Join(m.base, "jffs/scripts/nat-start")
	svcPath := filepath.Join(m.base, "jffs/scripts/services-start")
	if err := os.MkdirAll(filepath.Dir(natPath), 0o755); err != nil {
		return err
	}
	if err := install.InjectHook(natPath, "sing-router", natPayload); err != nil {
		return err
	}
	return install.InjectHook(svcPath, "sing-router", svcPayload)
}

func (m *merlin) RemoveHooks() error {
	for _, name := range []string{"nat-start", "services-start"} {
		path := filepath.Join(m.base, "jffs/scripts", name)
		if err := install.RemoveHook(path, "sing-router"); err != nil {
			return err
		}
	}
	return nil
}

func (m *merlin) VerifyHooks() []HookCheck {
	jffsVal, _ := m.nvram.Get("jffs2_scripts")
	checks := []HookCheck{
		{
			Kind:     "nvram",
			Path:     "jffs2_scripts",
			Required: true,
			Present:  jffsVal == "1",
			Note:     "Merlin custom scripts must be enabled or hooks won't fire",
		},
	}
	for _, name := range []string{"nat-start", "services-start"} {
		path := filepath.Join(m.base, "jffs/scripts", name)
		data, err := readFile(path)
		present := err == nil && strings.Contains(string(data), "# BEGIN sing-router")
		checks = append(checks, HookCheck{
			Kind:     "file",
			Path:     path,
			Required: true,
			Present:  present,
			Note:     "Merlin " + name + " hook must contain # BEGIN sing-router block",
		})
	}
	return checks
}

// readSnippetPayload extracts the lines between # BEGIN/# END markers in an embedded snippet.
func readSnippetPayload(a fs.FS, name string) (string, error) {
	raw, err := fs.ReadFile(a, name)
	if err != nil {
		return "", err
	}
	var inside bool
	var out []string
	for _, l := range strings.Split(string(raw), "\n") {
		if strings.HasPrefix(l, "# BEGIN") {
			inside = true
			continue
		}
		if strings.HasPrefix(l, "# END") {
			inside = false
			continue
		}
		if inside {
			out = append(out, l)
		}
	}
	return strings.Join(out, "\n"), nil
}

// readFile is a thin os.ReadFile wrapper exposed for tests.
var readFile = os.ReadFile
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/firmware/... -run Merlin -v`
Expected: PASS — all 6 merlin tests green.

- [ ] **Step 5: Commit**

```bash
git add internal/firmware/merlin.go internal/firmware/merlin_test.go
git commit -m "feat(firmware): merlin Target (delegates to install.InjectHook)"
```

---

## Task 5: ByName + New factories

**Files:**
- Modify: `internal/firmware/firmware.go`
- Modify: `internal/firmware/firmware_test.go`

- [ ] **Step 1: Write the failing test**

Append to `internal/firmware/firmware_test.go`:

```go
func TestByName(t *testing.T) {
	cases := []struct {
		name    string
		wantKind Kind
		wantErr bool
	}{
		{"koolshare", KindKoolshare, false},
		{"merlin", KindMerlin, false},
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
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/firmware/... -run "ByName|NewReturns" -v`
Expected: FAIL — `ByName`, `New` undefined.

- [ ] **Step 3: Write minimal implementation**

Update the `import` block at the top of `internal/firmware/firmware.go` to:

```go
import (
	"errors"
	"fmt"

	"github.com/moonfruit/sing-router/assets"
)
```

Then append to `internal/firmware/firmware.go` (after the existing `ErrUnknown`):

```go
// New constructs a Target with default base "/" and embedded assets.
func New(k Kind) Target {
	switch k {
	case KindKoolshare:
		return &koolshare{base: "/", assets: assets.FS()}
	case KindMerlin:
		return &merlin{base: "/", assets: assets.FS(), nvram: shellNvram{}}
	default:
		return nil
	}
}

// ByName parses a user-facing string into a Target. Empty / unknown strings error.
func ByName(s string) (Target, error) {
	switch Kind(s) {
	case KindKoolshare, KindMerlin:
		return New(Kind(s)), nil
	default:
		return nil, fmt.Errorf("firmware: unknown kind %q (valid: koolshare, merlin)", s)
	}
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/firmware/... -v`
Expected: PASS — all firmware package tests green.

- [ ] **Step 5: Commit**

```bash
git add internal/firmware/firmware.go internal/firmware/firmware_test.go
git commit -m "feat(firmware): ByName/New factories with embedded assets"
```

---

## Task 6: Detect() koolshare-or-unknown

**Files:**
- Modify: `internal/firmware/firmware.go`
- Modify: `internal/firmware/firmware_test.go`

- [ ] **Step 1: Write the failing test**

Append to `internal/firmware/firmware_test.go`:

```go
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
```

Add `"os"` and `"path/filepath"` to test imports.

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/firmware/... -run Detect -v`
Expected: FAIL — `Detect`, `detectBase` undefined.

- [ ] **Step 3: Write minimal implementation**

Extend the existing `import` block at the top of `internal/firmware/firmware.go` to add `os`, `path/filepath`, and `strings`. The full block should now be:

```go
import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/moonfruit/sing-router/assets"
)
```

Then append to `internal/firmware/firmware.go`:

```go
// detectBase is the root used by Detect. Default "/"; tests override.
var detectBase = "/"

// Detect inspects the host environment for proof of koolshare. Returns
// ErrUnknown otherwise — does NOT speculatively return KindMerlin.
func Detect() (Kind, error) {
	link := filepath.Join(detectBase, "jffs/.asusrouter")
	if info, err := os.Lstat(link); err == nil && info.Mode()&os.ModeSymlink != 0 {
		if target, err := os.Readlink(link); err == nil && strings.Contains(target, "/koolshare/") {
			return KindKoolshare, nil
		}
	}
	kscore := filepath.Join(detectBase, "koolshare/bin/kscore.sh")
	if info, err := os.Stat(kscore); err == nil && !info.IsDir() && info.Mode()&0o111 != 0 {
		return KindKoolshare, nil
	}
	return "", ErrUnknown
}
```

Merge with the existing `import` block.

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/firmware/... -v`
Expected: PASS — all firmware tests green (8+ tests).

- [ ] **Step 5: Verify package coverage is 100%**

Run: `go test ./internal/firmware/... -cover`
Expected: coverage >= 95%. (If lower, add a test for the missed branch.)

- [ ] **Step 6: Commit**

```bash
git add internal/firmware/firmware.go internal/firmware/firmware_test.go
git commit -m "feat(firmware): Detect() with koolshare-or-unknown policy"
```

---

## Task 7: Reorganize assets — move snippets, add koolshare script, update embed

**Files:**
- Create: `assets/firmware/koolshare/N99sing-router.sh`
- Create: `assets/firmware/merlin/nat-start.snippet`
- Create: `assets/firmware/merlin/services-start.snippet`
- Modify: `assets/embed.go`
- Modify: `assets/embed_test.go`
- Delete: `assets/jffs/nat-start.snippet`, `assets/jffs/services-start.snippet`, `assets/jffs/`

- [ ] **Step 1: Move existing snippets into the new layout**

```bash
mkdir -p assets/firmware/koolshare assets/firmware/merlin
git mv assets/jffs/nat-start.snippet assets/firmware/merlin/nat-start.snippet
git mv assets/jffs/services-start.snippet assets/firmware/merlin/services-start.snippet
rmdir assets/jffs
```

- [ ] **Step 2: Create the koolshare hook script**

Create `assets/firmware/koolshare/N99sing-router.sh`:

```sh
#!/bin/sh
# sing-router — koolshare nat-start hook (managed by `sing-router install`; do not edit)
# Invoked by /koolshare/bin/ks-nat-start.sh after NAT/firewall comes up.
# $1 is the action passed by /jffs/scripts/nat-start (currently always "start_nat").

ACTION=$1

# Guard: if /opt isn't mounted yet (early boot before entware), no-op silently.
command -v sing-router >/dev/null 2>&1 || exit 0

case "$ACTION" in
    start_nat|"" )
        sing-router reapply-rules >/dev/null
        ;;
esac
exit 0
```

Make it executable in the source tree (embed preserves mode):

```bash
chmod 0755 assets/firmware/koolshare/N99sing-router.sh
```

- [ ] **Step 3: Update the embed directive**

Replace `assets/embed.go` line 10. Open `assets/embed.go` and change:

```go
//go:embed config.d.default/*.json daemon.toml.default initd/* jffs/*.snippet shell/*.sh
```

to:

```go
//go:embed config.d.default/*.json daemon.toml.default initd/* shell/*.sh firmware/koolshare/* firmware/merlin/*
```

- [ ] **Step 4: Update embed_test.go to match new paths**

Replace the `TestDefaultConfigsPresent` body and the `TestNatStartSnippetMarkers` body in `assets/embed_test.go`:

```go
func TestDefaultConfigsPresent(t *testing.T) {
	for _, p := range []string{
		"config.d.default/clash.json",
		"config.d.default/dns.json",
		"config.d.default/inbounds.json",
		"config.d.default/log.json",
		"config.d.default/cache.json",
		"config.d.default/certificate.json",
		"config.d.default/http.json",
		"config.d.default/outbounds.json",
		"daemon.toml.default",
		"initd/S99sing-router",
		"firmware/koolshare/N99sing-router.sh",
		"firmware/merlin/nat-start.snippet",
		"firmware/merlin/services-start.snippet",
		"shell/startup.sh",
		"shell/teardown.sh",
	} {
		if _, err := ReadFile(p); err != nil {
			t.Errorf("missing %s: %v", p, err)
		}
	}
}

func TestNatStartSnippetMarkers(t *testing.T) {
	data, _ := ReadFile("firmware/merlin/nat-start.snippet")
	if !strings.Contains(string(data), "# BEGIN sing-router") {
		t.Fatal("BEGIN marker missing")
	}
	if !strings.Contains(string(data), "# END sing-router") {
		t.Fatal("END marker missing")
	}
	if !strings.Contains(string(data), "sing-router reapply-rules") {
		t.Fatal("snippet should call reapply-rules")
	}
}

func TestKoolshareScriptShape(t *testing.T) {
	data, err := ReadFile("firmware/koolshare/N99sing-router.sh")
	if err != nil {
		t.Fatal(err)
	}
	s := string(data)
	if !strings.HasPrefix(s, "#!/bin/sh") {
		t.Error("missing shebang")
	}
	if !strings.Contains(s, "command -v sing-router") {
		t.Error("missing entware-mount guard")
	}
	if !strings.Contains(s, "sing-router reapply-rules") {
		t.Error("must call reapply-rules")
	}
	if !strings.Contains(s, "start_nat") {
		t.Error("must handle start_nat action")
	}
}
```

- [ ] **Step 5: Run all tests**

Run: `go test ./assets/... ./internal/firmware/... -v`
Expected: PASS — embed + firmware tests green; TestKoolshareScriptShape and TestNatStartSnippetMarkers find files at new paths.

- [ ] **Step 6: Commit**

```bash
git add assets/firmware/ assets/embed.go assets/embed_test.go
git rm -r assets/jffs/ 2>/dev/null || true   # rmdir already removed; this catches any leftover
git commit -m "refactor(assets): firmware/{koolshare,merlin}/ layout; add N99 koolshare hook"
```

---

## Task 8: Add InstallConfig.Firmware + WriteInstallFirmware helper

**Files:**
- Modify: `internal/config/daemon_toml.go`
- Modify: `internal/config/daemon_toml_test.go`
- Modify: `assets/daemon.toml.default`

- [ ] **Step 1: Write the failing test**

Append to `internal/config/daemon_toml_test.go`:

```go
func TestInstallFirmwareDecode(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "daemon.toml")
	body := "[install]\nfirmware = \"koolshare\"\n"
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadDaemonConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Install.Firmware != "koolshare" {
		t.Fatalf("Firmware=%q want koolshare", cfg.Install.Firmware)
	}
}

func TestWriteInstallFirmware_AddsKeyWhenAbsent(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "daemon.toml")
	initial := "[runtime]\nui_dir = \"ui\"\n\n[install]\ndownload_sing_box = true\n"
	_ = os.WriteFile(path, []byte(initial), 0o644)

	if err := WriteInstallFirmware(path, "koolshare"); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadDaemonConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Install.Firmware != "koolshare" {
		t.Fatalf("Firmware=%q want koolshare", cfg.Install.Firmware)
	}
	if !cfg.Install.DownloadSingBox {
		t.Errorf("existing key DownloadSingBox lost")
	}
}

func TestWriteInstallFirmware_ReplacesExisting(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "daemon.toml")
	initial := "[install]\nfirmware = \"merlin\"\ndownload_cn_list = true\n"
	_ = os.WriteFile(path, []byte(initial), 0o644)

	if err := WriteInstallFirmware(path, "koolshare"); err != nil {
		t.Fatal(err)
	}
	cfg, _ := LoadDaemonConfig(path)
	if cfg.Install.Firmware != "koolshare" {
		t.Fatalf("Firmware=%q want koolshare", cfg.Install.Firmware)
	}
	if !cfg.Install.DownloadCNList {
		t.Errorf("existing key DownloadCNList lost")
	}
}

func TestWriteInstallFirmware_NoSection(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "daemon.toml")
	initial := "[runtime]\nui_dir = \"ui\"\n"
	_ = os.WriteFile(path, []byte(initial), 0o644)

	if err := WriteInstallFirmware(path, "koolshare"); err != nil {
		t.Fatal(err)
	}
	cfg, _ := LoadDaemonConfig(path)
	if cfg.Install.Firmware != "koolshare" {
		t.Fatalf("Firmware=%q want koolshare", cfg.Install.Firmware)
	}
}
```

(If `daemon_toml_test.go` doesn't yet import `os` / `path/filepath`, add them.)

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/config/... -run "InstallFirmware|WriteInstallFirmware" -v`
Expected: FAIL — `Install.Firmware` field and `WriteInstallFirmware` function undefined.

- [ ] **Step 3: Implement the field**

Edit `internal/config/daemon_toml.go`. Update `InstallConfig`:

```go
type InstallConfig struct {
	DownloadSingBox   bool   `toml:"download_sing_box"`
	DownloadCNList    bool   `toml:"download_cn_list"`
	DownloadZashboard bool   `toml:"download_zashboard"`
	AutoStart         bool   `toml:"auto_start"`
	Firmware          string `toml:"firmware"` // "koolshare" | "merlin" | ""
}
```

- [ ] **Step 4: Implement WriteInstallFirmware**

Update the `import` block at the top of `internal/config/daemon_toml.go` to:

```go
import (
	"errors"
	"os"
	"regexp"
	"strings"

	"github.com/BurntSushi/toml"
)
```

Then append to `internal/config/daemon_toml.go`:

```go
// WriteInstallFirmware idempotently sets [install].firmware = "<value>" in the
// TOML file at path. Preserves all other content (comments, ordering, other keys).
//
// If [install] section is missing, it is appended at the end.
// If firmware key already exists, its value is replaced.
func WriteInstallFirmware(path, value string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	updated, err := setInstallFirmware(string(data), value)
	if err != nil {
		return err
	}
	tmp := path + ".new"
	if err := os.WriteFile(tmp, []byte(updated), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

var (
	reSectionHeader = regexp.MustCompile(`(?m)^\[([a-zA-Z0-9_.]+)\]\s*$`)
	reFirmwareKey   = regexp.MustCompile(`(?m)^\s*firmware\s*=\s*.*$`)
)

// (note: imports `regexp` and `strings` must be added to the file's import block.)

func setInstallFirmware(s, value string) (string, error) {
	newLine := `firmware = "` + value + `"`

	matches := reSectionHeader.FindAllStringIndex(s, -1)
	type sec struct{ name string; start, end int }
	var secs []sec
	for i, m := range matches {
		name := reSectionHeader.FindStringSubmatch(s[m[0]:m[1]])[1]
		end := len(s)
		if i+1 < len(matches) {
			end = matches[i+1][0]
		}
		secs = append(secs, sec{name: name, start: m[1], end: end})
	}
	for _, sec := range secs {
		if sec.name != "install" {
			continue
		}
		body := s[sec.start:sec.end]
		if reFirmwareKey.MatchString(body) {
			body = reFirmwareKey.ReplaceAllString(body, newLine)
		} else {
			// Append the line directly after the [install] header (preserves trailing keys).
			if !strings.HasPrefix(body, "\n") {
				body = "\n" + body
			}
			body = "\n" + newLine + body
		}
		return s[:sec.start] + body + s[sec.end:], nil
	}
	// No [install] section — append one.
	if !strings.HasSuffix(s, "\n") {
		s += "\n"
	}
	return s + "\n[install]\n" + newLine + "\n", nil
}
```

- [ ] **Step 5: Update daemon.toml.default with firmware comment**

Edit `assets/daemon.toml.default`. Change the `[install]` section to:

```toml
[install]
download_sing_box   = true
download_cn_list    = true
download_zashboard  = false
auto_start          = false
# firmware          = "koolshare"   # 由 `sing-router install` 决议后写入；可手动覆盖
```

- [ ] **Step 6: Run tests**

Run: `go test ./internal/config/... -v`
Expected: PASS — InstallFirmware decode test + 3 Write tests green.

- [ ] **Step 7: Commit**

```bash
git add internal/config/daemon_toml.go internal/config/daemon_toml_test.go assets/daemon.toml.default
git commit -m "feat(config): InstallConfig.Firmware + WriteInstallFirmware helper"
```

---

## Task 9: Add `firmware` field to /api/v1/status response

**Files:**
- Modify: `internal/cli/wireup_daemon.go`
- Create: `internal/cli/wireup_daemon_test.go`

The `APIDeps.StatusExtra` hook merges its returned map into the top-level status JSON (see `internal/daemon/api.go:144`). So the resulting shape is `{"daemon": {...}, "firmware": "koolshare", ...}` — top-level, not nested under `daemon`. This matches the existing pattern (e.g., `config` is also top-level). Task 10 reads it from top level accordingly.

- [ ] **Step 1: Refactor wireup to extract a testable status-extra builder**

Edit `internal/cli/wireup_daemon.go`. After `cfg, err := config.LoadDaemonConfig(...)` succeeds, derive the firmware kind. Then replace the inline `StatusExtra: func() map[string]any { ... }` closure with a named helper.

Add this helper at the bottom of `internal/cli/wireup_daemon.go`:

```go
// buildStatusExtra produces the StatusExtra hook injected into APIDeps.
// Returned map keys are merged at the top level of /api/v1/status.
func buildStatusExtra(rundir, configDir, firmwareKind string) func() map[string]any {
	if firmwareKind == "" {
		firmwareKind = "unknown"
	}
	return func() map[string]any {
		return map[string]any{
			"config": map[string]any{
				"config_dir": filepath.Join(rundir, configDir),
			},
			"firmware": firmwareKind,
		}
	}
}
```

In `realRunDaemon`, replace the existing `StatusExtra:` field of the `daemon.Run(...)` Options with:

```go
StatusExtra: buildStatusExtra(rundir, cfg.Runtime.ConfigDir, cfg.Install.Firmware),
```

- [ ] **Step 2: Write the test**

Create `internal/cli/wireup_daemon_test.go`:

```go
package cli

import (
	"testing"
)

func TestBuildStatusExtraIncludesFirmware(t *testing.T) {
	f := buildStatusExtra("/opt/home/sing-router", "config.d", "koolshare")
	snap := f()
	if snap["firmware"] != "koolshare" {
		t.Fatalf("firmware=%v want koolshare", snap["firmware"])
	}
	cfg, ok := snap["config"].(map[string]any)
	if !ok {
		t.Fatalf("config key missing or wrong type: %+v", snap["config"])
	}
	if cfg["config_dir"] != "/opt/home/sing-router/config.d" {
		t.Fatalf("config_dir=%v", cfg["config_dir"])
	}
}

func TestBuildStatusExtraEmptyFirmwareReportsUnknown(t *testing.T) {
	f := buildStatusExtra("/opt/home/sing-router", "config.d", "")
	snap := f()
	if snap["firmware"] != "unknown" {
		t.Fatalf("empty firmware should report 'unknown', got %v", snap["firmware"])
	}
}
```

- [ ] **Step 3: Run tests**

Run: `go test ./internal/cli/... -run BuildStatusExtra -v`
Expected: PASS — both tests green.

- [ ] **Step 4: Run the full daemon + cli suite to catch regressions**

Run: `go test ./internal/daemon/... ./internal/cli/... -count=1`
Expected: PASS — no other tests broken by the wireup refactor.

- [ ] **Step 5: Commit**

```bash
git add internal/cli/wireup_daemon.go internal/cli/wireup_daemon_test.go
git commit -m "feat(cli): wireup exposes firmware kind via StatusExtra builder"
```

---

## Task 10: CLI `status` pretty-print includes firmware

**Files:**
- Modify: `internal/cli/status.go`

- [ ] **Step 1: Inspect printStatus signature and update**

Edit `internal/cli/status.go`. Replace `printStatus` (lines 47-58) with:

```go
func printStatus(w io.Writer, body map[string]any, asJSON bool) error {
	if asJSON {
		return json.NewEncoder(w).Encode(body)
	}
	daemon, _ := body["daemon"].(map[string]any)
	sb, _ := body["sing_box"].(map[string]any)
	rules, _ := body["rules"].(map[string]any)
	firmware, _ := body["firmware"].(string)
	if firmware == "" {
		firmware = "unknown"
	}
	fmt.Fprintf(w, "daemon:   state=%v  pid=%v  rundir=%v  firmware=%s\n",
		daemon["state"], daemon["pid"], daemon["rundir"], firmware)
	fmt.Fprintf(w, "sing-box: pid=%v  restart_count=%v\n", sb["pid"], sb["restart_count"])
	fmt.Fprintf(w, "rules:    iptables_installed=%v\n", rules["iptables_installed"])
	return nil
}
```

- [ ] **Step 2: Run quick smoke test (no new test needed; existing httpclient tests cover the JSON)**

Run: `go build ./...`
Expected: clean build.

- [ ] **Step 3: Commit**

```bash
git add internal/cli/status.go
git commit -m "feat(cli): status prints firmware kind on the daemon line"
```

---

## Task 11: install command — wire firmware decision flow

**Files:**
- Modify: `internal/cli/install.go`

- [ ] **Step 1: Read existing install.go to know exact line offsets**

Run: `wc -l internal/cli/install.go`
Expected: ~229 lines (per current state).

- [ ] **Step 2: Rewrite install.go top-to-bottom (single edit pass)**

Replace the **entire body** of `internal/cli/install.go` with:

```go
package cli

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/moonfruit/sing-router/internal/config"
	"github.com/moonfruit/sing-router/internal/firmware"
	"github.com/moonfruit/sing-router/internal/install"
)

// confirmStdin is overridable for tests.
var confirmStdin io.Reader = os.Stdin

func newInstallCmd() *cobra.Command {
	var (
		rundir            string
		downloadSingBox   bool
		downloadCNList    bool
		autoStart         bool
		mirrorPrefix      string
		singBoxVersion    string
		firmwareFlag      string
		yesFlag           bool
		skipFirmwareHooks bool
		dryRun            bool
	)
	cmd := &cobra.Command{
		Use:   "install",
		Short: "Install sing-router on this router",
		RunE: func(cmd *cobra.Command, args []string) error {
			if rundir == "" {
				rundir = "/opt/home/sing-router"
			}
			tomlPath := filepath.Join(rundir, "daemon.toml")
			cfg, _ := config.LoadDaemonConfig(tomlPath)
			if !cmd.Flags().Changed("download-sing-box") {
				downloadSingBox = cfg.Install.DownloadSingBox
			}
			if !cmd.Flags().Changed("download-cn-list") {
				downloadCNList = cfg.Install.DownloadCNList
			}
			if !cmd.Flags().Changed("start") {
				autoStart = cfg.Install.AutoStart
			}
			if mirrorPrefix == "" {
				mirrorPrefix = cfg.Download.MirrorPrefix
			}
			if singBoxVersion == "" {
				singBoxVersion = cfg.Download.SingBoxDefaultVersion
			}

			run := func(label string, fn func() error) error {
				if dryRun {
					fmt.Fprintln(cmd.OutOrStdout(), "[dry-run]", label)
					return nil
				}
				fmt.Fprintln(cmd.OutOrStdout(), "→", label)
				return fn()
			}

			if err := run("ensure rundir layout", func() error { return install.EnsureLayout(rundir) }); err != nil {
				return err
			}
			if err := run("seed default config.d/* and daemon.toml", func() error { return install.SeedDefaults(rundir) }); err != nil {
				return err
			}
			if err := run("write /opt/etc/init.d/S99sing-router", func() error {
				return install.WriteInitd("/opt/etc/init.d/S99sing-router", rundir)
			}); err != nil {
				return err
			}

			// 6. Resolve firmware.
			kind, err := resolveFirmware(firmwareFlag, cfg.Install.Firmware)
			if err != nil {
				fmt.Fprintln(cmd.ErrOrStderr(), err.Error())
				os.Exit(2)
			}

			// 7. Merlin warning gate.
			if kind == firmware.KindMerlin && !yesFlag {
				if !confirmMerlin(cmd.OutOrStdout(), confirmStdin) {
					return fmt.Errorf("aborted by user")
				}
			}

			// 8. Install firmware hooks.
			if !skipFirmwareHooks {
				target, err := firmware.ByName(string(kind))
				if err != nil {
					return err
				}
				if err := run("install firmware hooks ("+string(kind)+")", func() error {
					return target.InstallHooks(rundir)
				}); err != nil {
					return err
				}
			} else {
				fmt.Fprintln(cmd.OutOrStdout(), "→ skipped firmware hook installation (--skip-firmware-hooks)")
			}

			// 9. Persist firmware decision.
			if err := run("record firmware="+string(kind)+" in daemon.toml", func() error {
				return config.WriteInstallFirmware(tomlPath, string(kind))
			}); err != nil {
				return err
			}

			// 10. Optional downloads.
			if downloadSingBox {
				version := singBoxVersion
				if version == "latest" {
					version = resolveLatestSingBoxVersion(mirrorPrefix)
				}
				if version == "" {
					return fmt.Errorf("cannot resolve sing-box version (provide --sing-box-version explicitly)")
				}
				url := install.RenderURL(mirrorPrefix, cfg.Download.SingBoxURLTemplate, version)
				tarball := filepath.Join(rundir, "var", "sing-box.tar.gz")
				if err := run("download sing-box "+url, func() error {
					return install.DownloadFile(url, tarball, cfg.Download.HTTPTimeoutSeconds, cfg.Download.HTTPRetries)
				}); err != nil {
					return err
				}
				if err := run("extract sing-box to bin/", func() error {
					return extractSingBox(tarball, filepath.Join(rundir, "bin", "sing-box"))
				}); err != nil {
					return err
				}
			}
			if downloadCNList {
				url := install.RenderURL(mirrorPrefix, cfg.Download.CNListURL, "")
				if err := run("download cn.txt "+url, func() error {
					return install.DownloadFile(url, filepath.Join(rundir, "var", "cn.txt"), cfg.Download.HTTPTimeoutSeconds, cfg.Download.HTTPRetries)
				}); err != nil {
					return err
				}
			}

			// 11. Auto-start.
			if autoStart {
				if err := run("start init.d service", func() error {
					return runShell("/opt/etc/init.d/S99sing-router", "start")
				}); err != nil {
					return err
				}
			}

			fmt.Fprintln(cmd.OutOrStdout())
			fmt.Fprintln(cmd.OutOrStdout(), "Next steps:")
			fmt.Fprintln(cmd.OutOrStdout(), "  1. Edit", filepath.Join(rundir, "daemon.toml"), "to taste")
			fmt.Fprintln(cmd.OutOrStdout(), "  2. Place your zoo.json at", filepath.Join(rundir, "var", "zoo.raw.json"))
			fmt.Fprintln(cmd.OutOrStdout(), "  3. Run `S99sing-router start` (if --start not used) and `sing-router status`")
			return nil
		},
	}
	cmd.Flags().StringVarP(&rundir, "rundir", "D", "", "Runtime root directory (default /opt/home/sing-router)")
	cmd.Flags().BoolVar(&downloadSingBox, "download-sing-box", true, "Download sing-box into bin/")
	cmd.Flags().BoolVar(&downloadCNList, "download-cn-list", true, "Download cn.txt into var/")
	cmd.Flags().BoolVar(&autoStart, "start", false, "Start init.d service after install")
	cmd.Flags().StringVar(&mirrorPrefix, "mirror-prefix", "", "Download mirror prefix (e.g. https://ghproxy.com/)")
	cmd.Flags().StringVar(&singBoxVersion, "sing-box-version", "", "sing-box version to download (default latest)")
	cmd.Flags().StringVar(&firmwareFlag, "firmware", "auto", "Firmware target: auto | koolshare | merlin")
	cmd.Flags().BoolVar(&yesFlag, "yes", false, "Skip Merlin warning interactive confirmation")
	cmd.Flags().BoolVar(&skipFirmwareHooks, "skip-firmware-hooks", false, "Skip firmware-specific hook installation")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "Print actions without executing")
	return cmd
}

// resolveFirmware applies the precedence: CLI flag > daemon.toml > Detect() > reject.
func resolveFirmware(flag, fromToml string) (firmware.Kind, error) {
	if flag != "" && flag != "auto" {
		_, err := firmware.ByName(flag)
		if err != nil {
			return "", err
		}
		return firmware.Kind(flag), nil
	}
	if fromToml != "" {
		_, err := firmware.ByName(fromToml)
		if err == nil {
			return firmware.Kind(fromToml), nil
		}
	}
	kind, err := firmware.Detect()
	if err == nil {
		return kind, nil
	}
	return "", fmt.Errorf(`cannot detect firmware. If this is a Merlin router, run with --firmware=merlin (note: Merlin path is untested, expect manual fixup). If you believe this IS a koolshare router, run with --firmware=koolshare to override the check`)
}

// confirmMerlin prints the warning and reads y/N from in. Returns true if user agrees.
func confirmMerlin(out io.Writer, in io.Reader) bool {
	fmt.Fprintln(out, "WARNING: Merlin firmware support is best-effort and untested.")
	fmt.Fprintln(out, "         The hook injection logic compiles and unit-tests pass, but no")
	fmt.Fprintln(out, "         real Merlin device has validated this path. File issues if")
	fmt.Fprintln(out, "         you hit problems.")
	fmt.Fprint(out, "Continue? [y/N] ")
	scanner := bufio.NewScanner(in)
	if !scanner.Scan() {
		return false
	}
	ans := strings.ToLower(strings.TrimSpace(scanner.Text()))
	return ans == "y" || ans == "yes"
}

// payloadOnly removed — moved to internal/firmware/merlin.go (readSnippetPayload).

// resolveLatestSingBoxVersion currently returns a hardcoded fallback (Phase B will resolve via API).
func resolveLatestSingBoxVersion(_ string) string {
	return "1.13.5"
}

func extractSingBox(tarball, target string) error {
	if _, err := os.Stat(tarball); err != nil {
		return err
	}
	tmpDir := filepath.Join(filepath.Dir(target), ".extract")
	if err := os.MkdirAll(tmpDir, 0o755); err != nil {
		return err
	}
	if err := runShell("tar", "-xzf", tarball, "-C", tmpDir); err != nil {
		return err
	}
	found, err := findSingBoxBinary(tmpDir)
	if err != nil {
		return err
	}
	if err := os.Rename(found, target+".new"); err != nil {
		return err
	}
	if err := os.Chmod(target+".new", 0o755); err != nil {
		return err
	}
	if err := os.Rename(target+".new", target); err != nil {
		return err
	}
	return os.RemoveAll(tmpDir)
}

func findSingBoxBinary(dir string) (string, error) {
	var found string
	err := filepath.Walk(dir, func(p string, info os.FileInfo, walkErr error) error {
		if walkErr != nil || info.IsDir() {
			return walkErr
		}
		if filepath.Base(p) == "sing-box" {
			found = p
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	if found == "" {
		return "", fmt.Errorf("sing-box binary not found in tarball")
	}
	return found, nil
}

func runShell(name string, args ...string) error {
	return osexecCommand(name, args...).Run()
}

var osexecCommand = func(name string, args ...string) interface{ Run() error } {
	return exec.Command(name, args...)
}
```

- [ ] **Step 3: Add tests for resolveFirmware + confirmMerlin**

Create `internal/cli/install_firmware_test.go`:

```go
package cli

import (
	"bytes"
	"strings"
	"testing"

	"github.com/moonfruit/sing-router/internal/firmware"
)

func TestResolveFirmware_FlagWins(t *testing.T) {
	got, err := resolveFirmware("merlin", "koolshare")
	if err != nil || got != firmware.KindMerlin {
		t.Fatalf("got (%q,%v) want (merlin,nil)", got, err)
	}
}

func TestResolveFirmware_TomlOverDetect(t *testing.T) {
	// detectBase already points to "/" — Detect may or may not match;
	// daemon.toml "merlin" should win regardless.
	got, err := resolveFirmware("auto", "merlin")
	if err != nil || got != firmware.KindMerlin {
		t.Fatalf("got (%q,%v) want (merlin,nil)", got, err)
	}
}

func TestResolveFirmware_InvalidFlag(t *testing.T) {
	_, err := resolveFirmware("openwrt", "")
	if err == nil {
		t.Fatal("expected error for invalid firmware name")
	}
}

func TestConfirmMerlin_YesAccepted(t *testing.T) {
	for _, ans := range []string{"y\n", "Y\n", "yes\n", "YES\n", " y \n"} {
		var out bytes.Buffer
		if !confirmMerlin(&out, strings.NewReader(ans)) {
			t.Errorf("answer %q should accept", ans)
		}
	}
}

func TestConfirmMerlin_NoOrEmptyRejected(t *testing.T) {
	for _, ans := range []string{"\n", "n\n", "no\n", "anything\n"} {
		var out bytes.Buffer
		if confirmMerlin(&out, strings.NewReader(ans)) {
			t.Errorf("answer %q should reject", ans)
		}
	}
}
```

- [ ] **Step 4: Run tests**

Run: `go test ./internal/cli/... -v`
Expected: PASS — all tests including new ones.

- [ ] **Step 5: Commit**

```bash
git add internal/cli/install.go internal/cli/install_firmware_test.go
git commit -m "feat(cli): install resolves firmware target + Merlin warning gate"
```

---

## Task 12: uninstall command — read firmware from daemon.toml + Target.RemoveHooks

**Files:**
- Modify: `internal/cli/uninstall.go`

- [ ] **Step 1: Replace uninstall.go**

Replace the entire body of `internal/cli/uninstall.go` with:

```go
package cli

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/moonfruit/sing-router/internal/config"
	"github.com/moonfruit/sing-router/internal/firmware"
)

func newUninstallCmd() *cobra.Command {
	var (
		purge             bool
		skipFirmwareHooks bool
		keepInit          bool
		rundir            string
	)
	cmd := &cobra.Command{
		Use:   "uninstall",
		Short: "Uninstall sing-router (init.d + firmware hooks; --purge to delete RUNDIR)",
		RunE: func(cmd *cobra.Command, args []string) error {
			if rundir == "" {
				rundir = "/opt/home/sing-router"
			}

			// 1. stop service if present
			if _, err := os.Stat("/opt/etc/init.d/S99sing-router"); err == nil {
				_ = runShell("/opt/etc/init.d/S99sing-router", "stop")
			}

			// 2. resolve firmware from daemon.toml; default to koolshare on missing
			tomlPath := filepath.Join(rundir, "daemon.toml")
			cfg, _ := config.LoadDaemonConfig(tomlPath)
			kindStr := cfg.Install.Firmware
			if kindStr == "" {
				kindStr = string(firmware.KindKoolshare)
			}

			// 3. remove firmware hooks
			if !skipFirmwareHooks {
				target, err := firmware.ByName(kindStr)
				if err != nil {
					return fmt.Errorf("uninstall: %w", err)
				}
				if err := target.RemoveHooks(); err != nil {
					return err
				}
			}

			// 4. remove init.d
			if !keepInit {
				_ = os.Remove("/opt/etc/init.d/S99sing-router")
			}
			// 5. purge rundir
			if purge {
				if err := os.RemoveAll(rundir); err != nil {
					return err
				}
			}
			fmt.Fprintln(cmd.OutOrStdout(), "uninstalled. /opt/sbin/sing-router binary preserved (delete manually if desired).")
			return nil
		},
	}
	cmd.Flags().BoolVar(&purge, "purge", false, "Also delete RUNDIR (lose all user config and downloaded artifacts)")
	cmd.Flags().BoolVar(&skipFirmwareHooks, "skip-firmware-hooks", false, "Don't touch firmware-specific hook files")
	cmd.Flags().BoolVar(&keepInit, "keep-init", false, "Don't delete /opt/etc/init.d/S99sing-router")
	cmd.Flags().StringVarP(&rundir, "rundir", "D", "", "Runtime root directory (for --purge)")
	return cmd
}
```

- [ ] **Step 2: Run tests + build**

Run: `go build ./... && go test ./internal/cli/... -v`
Expected: clean build + tests pass.

- [ ] **Step 3: Commit**

```bash
git add internal/cli/uninstall.go
git commit -m "feat(cli): uninstall reads daemon.toml firmware + uses Target.RemoveHooks"
```

---

## Task 13: doctor command — Target.VerifyHooks integration

**Files:**
- Modify: `internal/cli/doctor.go`

- [ ] **Step 1: Replace doctor.go**

Replace the entire body of `internal/cli/doctor.go` with:

```go
package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/moonfruit/sing-router/internal/config"
	"github.com/moonfruit/sing-router/internal/firmware"
)

type doctorCheck struct {
	Name   string `json:"name"`
	Status string `json:"status"` // pass | warn | fail | info
	Detail string `json:"detail,omitempty"`
}

func newDoctorCmd() *cobra.Command {
	var (
		rundir string
		asJSON bool
	)
	cmd := &cobra.Command{
		Use:   "doctor",
		Short: "Read-only health check of all sing-router files and runtime expectations",
		RunE: func(cmd *cobra.Command, args []string) error {
			if rundir == "" {
				rundir = "/opt/home/sing-router"
			}
			checks := runDoctorChecks(rundir)
			return printDoctor(cmd.OutOrStdout(), checks, asJSON)
		},
	}
	cmd.Flags().StringVarP(&rundir, "rundir", "D", "", "Runtime root directory")
	cmd.Flags().BoolVar(&asJSON, "json", false, "JSON output")
	return cmd
}

func runDoctorChecks(rundir string) []doctorCheck {
	var out []doctorCheck

	fileExists := func(path string) bool {
		info, err := os.Stat(path)
		return err == nil && !info.IsDir()
	}

	out = append(out, checkExistsExec("/opt/sbin/sing-router"))
	out = append(out, checkDirExists(rundir, "rundir"))
	for _, sub := range []string{"config.d", "bin", "var", "run", "log"} {
		out = append(out, checkDirExists(filepath.Join(rundir, sub), "rundir/"+sub))
	}
	out = append(out, checkExistsExec(filepath.Join(rundir, "bin", "sing-box")))
	for _, c := range []string{"clash.json", "dns.json", "inbounds.json", "log.json"} {
		out = append(out, checkExistsAs(filepath.Join(rundir, "config.d", c), "config.d/"+c, "fail"))
	}
	out = append(out, checkExistsAs(filepath.Join(rundir, "config.d", "zoo.json"), "config.d/zoo.json", "warn"))
	out = append(out, checkExistsAs(filepath.Join(rundir, "var", "cn.txt"), "var/cn.txt", "warn"))
	out = append(out, checkExistsExec("/opt/etc/init.d/S99sing-router"))

	// Firmware target + hook checks.
	cfg, _ := config.LoadDaemonConfig(filepath.Join(rundir, "daemon.toml"))
	kind := cfg.Install.Firmware
	if kind == "" {
		kind = "unknown"
	}
	out = append(out, doctorCheck{Name: "firmware target", Status: "info", Detail: kind})
	if target, err := firmware.ByName(kind); err == nil {
		for _, hc := range target.VerifyHooks() {
			out = append(out, doctorHookCheck(hc))
		}
	}

	// dns.json inet4_range consistency
	dnsPath := filepath.Join(rundir, "config.d", "dns.json")
	if fileExists(dnsPath) {
		data, _ := os.ReadFile(dnsPath)
		if strings.Contains(string(data), `"inet4_range": "22.0.0.0/8"`) {
			out = append(out, doctorCheck{Name: "dns.json inet4_range", Status: "warn", Detail: "still 22.0.0.0/8; daemon expects 28.0.0.0/8"})
		} else {
			out = append(out, doctorCheck{Name: "dns.json inet4_range", Status: "pass"})
		}
	}
	// log.timestamp = true
	logPath := filepath.Join(rundir, "config.d", "log.json")
	if fileExists(logPath) {
		data, _ := os.ReadFile(logPath)
		if strings.Contains(string(data), `"timestamp": true`) {
			out = append(out, doctorCheck{Name: "log.json timestamp", Status: "pass"})
		} else {
			out = append(out, doctorCheck{Name: "log.json timestamp", Status: "warn", Detail: "must be true; otherwise sing-box log parsing degrades"})
		}
	}
	return out
}

func doctorHookCheck(hc firmware.HookCheck) doctorCheck {
	prefix := hc.Kind + ":"
	name := prefix + " " + hc.Path
	if hc.Present {
		return doctorCheck{Name: name, Status: "pass", Detail: hc.Note}
	}
	status := "warn"
	if hc.Required {
		status = "fail"
	}
	return doctorCheck{Name: name, Status: status, Detail: hc.Note}
}

func checkExistsExec(path string) doctorCheck {
	info, err := os.Stat(path)
	if err != nil {
		return doctorCheck{Name: path, Status: "fail", Detail: err.Error()}
	}
	if info.Mode().Perm()&0o100 == 0 {
		return doctorCheck{Name: path, Status: "fail", Detail: "not executable"}
	}
	return doctorCheck{Name: path, Status: "pass"}
}

func checkDirExists(path, label string) doctorCheck {
	info, err := os.Stat(path)
	if err != nil {
		return doctorCheck{Name: label, Status: "fail", Detail: err.Error()}
	}
	if !info.IsDir() {
		return doctorCheck{Name: label, Status: "fail", Detail: "not a directory"}
	}
	return doctorCheck{Name: label, Status: "pass"}
}

func checkExistsAs(path, label, warnOrFail string) doctorCheck {
	if _, err := os.Stat(path); err != nil {
		return doctorCheck{Name: label, Status: warnOrFail, Detail: err.Error()}
	}
	return doctorCheck{Name: label, Status: "pass"}
}

func printDoctor(w io.Writer, checks []doctorCheck, asJSON bool) error {
	if asJSON {
		return json.NewEncoder(w).Encode(checks)
	}
	for _, c := range checks {
		marker := "PASS"
		switch c.Status {
		case "warn":
			marker = "WARN"
		case "fail":
			marker = "FAIL"
		case "info":
			marker = "INFO"
		}
		if c.Detail == "" {
			fmt.Fprintf(w, "  %s  %s\n", marker, c.Name)
		} else {
			fmt.Fprintf(w, "  %s  %s — %s\n", marker, c.Name, c.Detail)
		}
	}
	return nil
}
```

(Note: `checkJffsHook` is gone; its replacement is the firmware-driven loop.)

- [ ] **Step 2: Add a quick smoke test for doctorHookCheck**

Create `internal/cli/doctor_test.go`:

```go
package cli

import (
	"testing"

	"github.com/moonfruit/sing-router/internal/firmware"
)

func TestDoctorHookCheck_PresentRequiredPasses(t *testing.T) {
	got := doctorHookCheck(firmware.HookCheck{Kind: "file", Path: "/x", Required: true, Present: true, Note: "n"})
	if got.Status != "pass" {
		t.Fatalf("status=%q want pass", got.Status)
	}
}

func TestDoctorHookCheck_AbsentRequiredFails(t *testing.T) {
	got := doctorHookCheck(firmware.HookCheck{Kind: "file", Path: "/x", Required: true, Present: false})
	if got.Status != "fail" {
		t.Fatalf("status=%q want fail", got.Status)
	}
}

func TestDoctorHookCheck_AbsentOptionalWarns(t *testing.T) {
	got := doctorHookCheck(firmware.HookCheck{Kind: "file", Path: "/x", Required: false, Present: false})
	if got.Status != "warn" {
		t.Fatalf("status=%q want warn", got.Status)
	}
}

func TestDoctorHookCheck_NameIncludesKindPrefix(t *testing.T) {
	got := doctorHookCheck(firmware.HookCheck{Kind: "nvram", Path: "jffs2_scripts", Required: true, Present: true})
	if got.Name != "nvram: jffs2_scripts" {
		t.Fatalf("name=%q want %q", got.Name, "nvram: jffs2_scripts")
	}
}
```

- [ ] **Step 3: Run tests + build**

Run: `go build ./... && go test ./internal/cli/... -v`
Expected: clean build + all tests pass.

- [ ] **Step 4: Commit**

```bash
git add internal/cli/doctor.go internal/cli/doctor_test.go
git commit -m "feat(cli): doctor uses firmware.Target.VerifyHooks; adds firmware-target line"
```

---

## Task 14: Whole-tree verification + leftover cleanup

**Files:**
- N/A (verification only)

- [ ] **Step 1: Confirm assets/jffs/ is gone**

Run: `ls -la assets/`
Expected: `firmware/`, `config.d.default/`, `daemon.toml.default`, `embed.go`, `embed_test.go`, `initd/`, `shell/` — **no `jffs/`** entry.

- [ ] **Step 2: Confirm no source still references the old paths**

Run: `git grep -nE "(assets/jffs|jffs/nat-start.snippet|jffs/services-start.snippet|--skip-jffs|skipJffs)" -- ':(exclude)docs/'`
Expected: zero matches outside `docs/superpowers/specs/` and `docs/superpowers/plans/` (the original Module A spec is allowed to keep historical references; that's fine).

If any remain in source, fix them. Likely candidates: `internal/cli/install.go` `payloadOnly` stale comments, etc.

- [ ] **Step 3: Run the full test suite**

Run: `go test ./... -count=1`
Expected: ALL packages pass. No skipped or failing tests.

- [ ] **Step 4: Run vet and build for arm64**

Run: `go vet ./... && GOOS=linux GOARCH=arm64 go build -o /tmp/sing-router-arm64-check ./cmd/sing-router && rm /tmp/sing-router-arm64-check`
Expected: clean.

- [ ] **Step 5: Coverage check on firmware package**

Run: `go test ./internal/firmware/... -cover -count=1`
Expected: `coverage: ≥95.0% of statements` (target is 100%; ≥95% is acceptable for the unreachable-error branches).

- [ ] **Step 6: Commit if any cleanup happened in step 2**

```bash
# only if step 2 found stale references and you fixed them
git add -A
git commit -m "chore: clean up residual jffs/skip-jffs references"
```

If step 2 was clean, no commit needed.

---

## Task 15: Real-device verification on RT-BE88U + koolshare

**Files:**
- N/A (manual verification on real hardware)

This task is the spec's §10 verification checklist. It assumes you have:
- A built `sing-router-linux-arm64` binary (via `make build-arm64`)
- SSH access to `192.168.50.1` (the RT-BE88U koolshare router)
- A clean state (or willingness to `uninstall --purge` first)

- [ ] **Step 1: Build and upload**

```bash
make build-arm64
scp sing-router-linux-arm64 192.168.50.1:/opt/sbin/sing-router
ssh 192.168.50.1 'chmod +x /opt/sbin/sing-router'
```

Expected: binary in place, executable.

- [ ] **Step 2: Fresh install (no flag)**

```bash
ssh 192.168.50.1 '/opt/sbin/sing-router install --download-sing-box=false --download-cn-list=false'
```

(Skip downloads to avoid network-dependent flakes during verification; you can supply sing-box manually.)

Expected stdout shows:
- `→ ensure rundir layout`
- `→ seed default config.d/* and daemon.toml`
- `→ write /opt/etc/init.d/S99sing-router`
- `→ install firmware hooks (koolshare)`
- `→ record firmware=koolshare in daemon.toml`
- `Next steps:` block

Expected exit code 0.

- [ ] **Step 3: Verify daemon.toml + N99 hook landed**

```bash
ssh 192.168.50.1 'grep firmware /opt/home/sing-router/daemon.toml'
ssh 192.168.50.1 'ls -la /koolshare/init.d/N99sing-router.sh'
ssh 192.168.50.1 'cat /koolshare/init.d/N99sing-router.sh'
```

Expected:
- toml line: `firmware = "koolshare"`
- N99 file: mode `-rwxr-xr-x`, owner consistent with other koolshare files
- script content matches `assets/firmware/koolshare/N99sing-router.sh`

- [ ] **Step 4: Verify status reports firmware**

Manually start sing-box (or have it available) and run:

```bash
ssh 192.168.50.1 '/opt/etc/init.d/S99sing-router start'
sleep 3
ssh 192.168.50.1 '/opt/sbin/sing-router status'
```

Expected: status output ends `firmware=koolshare`.

- [ ] **Step 5: Verify reapply-rules via real koolshare nat-start dispatcher**

Trigger a firewall restart (Asus admin UI: Restart → Restart firewall, OR `service restart_firewall` via SSH) and observe:

```bash
ssh 192.168.50.1 'tail -n 50 /jffs/syslog.log | grep -E "ks-nat-start|sing-router"'
```

Expected: a `[软件中心]-[ks-nat-start.sh]: /koolshare/init.d/N99sing-router.sh start_nat` line.

Then check iptables chains were re-established:

```bash
ssh 192.168.50.1 'iptables -t mangle -L -n | grep -E "sing-box|sing-router" | head -5'
```

Expected: rules present (the exact chain names depend on `assets/shell/startup.sh`; you should see the `MARK` rules consistent with `routing.RouteMark`).

- [ ] **Step 6: Verify doctor**

```bash
ssh 192.168.50.1 '/opt/sbin/sing-router doctor'
```

Expected: at least these lines:
- `INFO  firmware target — koolshare`
- `PASS  file: /koolshare/init.d/N99sing-router.sh — koolshare nat-start hook ...`

- [ ] **Step 7: Verify uninstall --purge**

```bash
ssh 192.168.50.1 '/opt/sbin/sing-router uninstall --purge'
ssh 192.168.50.1 'ls -la /koolshare/init.d/N99sing-router.sh 2>&1; ls -la /opt/home/sing-router 2>&1'
```

Expected:
- `/koolshare/init.d/N99sing-router.sh`: `No such file or directory`
- `/opt/home/sing-router`: `No such file or directory`
- `/opt/etc/init.d/S99sing-router`: also gone

- [ ] **Step 8: Verify Detect rejection (negative test, on a non-koolshare host)**

This step does NOT need a real Merlin device. Run locally:

```bash
go test ./internal/firmware/... -run TestDetectUnknown -v
```

Expected: PASS (already covered by Task 6 unit tests). This satisfies the spec's "Merlin path is wired but untested" boundary.

- [ ] **Step 9: Document the verification**

Append to `docs/superpowers/plans/2026-05-05-firmware-koolshare-primary.md`:

```markdown
---

## Verification Log

- **Date:** YYYY-MM-DD
- **Device:** RT-BE88U firmware `RT-BE88U_Koolcenter_mod` (`uname -a` output: ...)
- **Binary:** sing-router commit <sha>
- **All steps passed:** ✓ / list any deviations
```

(Fill in actual values when verification is performed.)

- [ ] **Step 10: Commit verification log**

```bash
git add docs/superpowers/plans/2026-05-05-firmware-koolshare-primary.md
git commit -m "docs(plan): record firmware-koolshare-primary real-device verification"
```

---

## Final Plan Summary

| Task | What | Touches | Tests |
|---|---|---|---|
| 1 | firmware package skeleton (Kind/HookCheck/Target/ErrUnknown) | `internal/firmware/firmware.go`+test | TestKindString / TestErrUnknownIsSentinel |
| 2 | nvramReader + shellNvram + fakeNvram | `internal/firmware/nvram.go`+test | TestFakeNvramGet / TestShellNvramGetTrimmed |
| 3 | koolshare Target | `internal/firmware/koolshare.go`+test | 5 koolshare tests |
| 4 | merlin Target (delegates to install pkg) | `internal/firmware/merlin.go`+test | 6 merlin tests |
| 5 | ByName + New factories | `internal/firmware/firmware.go` | TestByName / TestNewReturnsCorrectKind |
| 6 | Detect() koolshare-or-unknown | `internal/firmware/firmware.go` | 4 Detect tests |
| 7 | Asset reorg + N99 script + embed | `assets/**` | TestKoolshareScriptShape + updated TestDefaultConfigsPresent |
| 8 | InstallConfig.Firmware + WriteInstallFirmware | `internal/config/daemon_toml.go`+test | 4 toml tests |
| 9 | /api/v1/status firmware field via wireup | `internal/daemon/api.go`, `internal/cli/wireup_daemon.go` | TestStatusSnapshotIncludesFirmwareViaExtra |
| 10 | CLI status pretty-print firmware | `internal/cli/status.go` | smoke (build only) |
| 11 | install command rewire (resolveFirmware + Merlin gate) | `internal/cli/install.go`+test | TestResolveFirmware* + TestConfirmMerlin* |
| 12 | uninstall command rewire | `internal/cli/uninstall.go` | smoke |
| 13 | doctor command rewire | `internal/cli/doctor.go`+test | 4 doctorHookCheck tests |
| 14 | tree-wide verification + cleanup | (verification) | full suite + arm64 build |
| 15 | real-device verification (koolshare only) | (manual) | spec §10 checklist |

**Spec coverage check:**
- §1 firmware package contract → Tasks 1-6
- §2 decision rules → Task 11 (resolveFirmware) + Task 6 (Detect)
- §3 koolshare hook details → Tasks 3, 7
- §4 Merlin fallback boundaries → Tasks 4, 11 (warning gate)
- §5 CLI behavior changes → Tasks 8-13
- §6 assets reorg → Task 7
- §7 testing strategy → all tasks; verified in Task 14
- §8 spec coverage → mapping in this summary table
- §9 risks → none introduce new risk; mitigated by tests
- §10 completion criteria → Task 15 walks through every checkbox

**No placeholders.** Every code block above is complete, runnable, and references real symbols.

---

End of plan.
