package install

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func read(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestInjectCreatesFileIfMissing(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "nat-start")
	if err := InjectHook(target, "sing-router-test", "echo hi"); err != nil {
		t.Fatal(err)
	}
	out := read(t, target)
	if !strings.Contains(out, "# BEGIN sing-router-test") || !strings.Contains(out, "# END sing-router-test") {
		t.Fatalf("markers missing: %s", out)
	}
	if !strings.Contains(out, "echo hi") {
		t.Fatalf("payload missing: %s", out)
	}
	info, _ := os.Stat(target)
	if info.Mode().Perm()&0o100 == 0 {
		t.Fatal("created file should be executable")
	}
}

func TestInjectAppendsToExisting(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "nat-start")
	if err := os.WriteFile(target, []byte("#!/bin/sh\n# user content above\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := InjectHook(target, "sing-router-test", "echo new"); err != nil {
		t.Fatal(err)
	}
	out := read(t, target)
	if !strings.Contains(out, "user content above") {
		t.Fatal("preserved content missing")
	}
	if !strings.Contains(out, "echo new") {
		t.Fatal("payload missing")
	}
}

func TestInjectReplacesExistingBlock(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "nat-start")
	initial := `#!/bin/sh
# preface
# BEGIN sing-router-test (managed by ` + "`sing-router install`" + `; do not edit)
echo old
# END sing-router-test
# postface
`
	if err := os.WriteFile(target, []byte(initial), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := InjectHook(target, "sing-router-test", "echo new"); err != nil {
		t.Fatal(err)
	}
	out := read(t, target)
	if strings.Contains(out, "echo old") {
		t.Fatal("old payload should be replaced")
	}
	if !strings.Contains(out, "echo new") {
		t.Fatal("new payload missing")
	}
	if !strings.Contains(out, "# preface") || !strings.Contains(out, "# postface") {
		t.Fatal("non-block content disturbed")
	}
	if strings.Count(out, "# BEGIN sing-router-test") != 1 {
		t.Fatal("expected exactly one BEGIN marker")
	}
}

func TestInjectIdempotentMultipleTimes(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "nat-start")
	for i := 0; i < 5; i++ {
		if err := InjectHook(target, "sing-router-test", "echo x"); err != nil {
			t.Fatalf("attempt %d: %v", i, err)
		}
	}
	out := read(t, target)
	if strings.Count(out, "# BEGIN sing-router-test") != 1 {
		t.Fatalf("multiple BEGIN markers: %s", out)
	}
}

func TestInjectIgnoresOtherBlocks(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "nat-start")
	initial := `#!/bin/sh
# BEGIN other-tool
echo other
# END other-tool
`
	if err := os.WriteFile(target, []byte(initial), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := InjectHook(target, "sing-router-test", "echo us"); err != nil {
		t.Fatal(err)
	}
	out := read(t, target)
	if !strings.Contains(out, "BEGIN other-tool") || !strings.Contains(out, "echo other") {
		t.Fatal("other block disturbed")
	}
	if !strings.Contains(out, "BEGIN sing-router-test") {
		t.Fatal("our block missing")
	}
}

func TestRemoveExistingBlock(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "nat-start")
	initial := `#!/bin/sh
# preface

# BEGIN sing-router-test (managed by ` + "`sing-router install`" + `; do not edit)
echo us
# END sing-router-test

# trailing
`
	if err := os.WriteFile(target, []byte(initial), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := RemoveHook(target, "sing-router-test"); err != nil {
		t.Fatal(err)
	}
	out := read(t, target)
	if strings.Contains(out, "BEGIN sing-router-test") || strings.Contains(out, "echo us") {
		t.Fatalf("block not removed: %s", out)
	}
	if !strings.Contains(out, "# preface") || !strings.Contains(out, "# trailing") {
		t.Fatal("surrounding content disturbed")
	}
}

func TestRemoveOnMissingFileNoError(t *testing.T) {
	if err := RemoveHook("/nonexistent/path", "sing-router-test"); err != nil {
		t.Fatalf("expected nil err on missing file, got %v", err)
	}
}

func TestRemoveOnFileWithoutBlockKeepsContent(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "nat-start")
	if err := os.WriteFile(target, []byte("#!/bin/sh\necho only-user\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := RemoveHook(target, "sing-router-test"); err != nil {
		t.Fatal(err)
	}
	out := read(t, target)
	if !strings.Contains(out, "echo only-user") {
		t.Fatalf("user content should remain: %s", out)
	}
}

// 文件末尾没有换行时，InjectHook 的 append 分支应自动补上。
func TestInjectAppendsNewlineWhenMissing(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "nat-start")
	if err := os.WriteFile(target, []byte("#!/bin/sh\nlast-line-no-newline"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := InjectHook(target, "sing-router-test", "echo hi"); err != nil {
		t.Fatal(err)
	}
	out := read(t, target)
	if !strings.Contains(out, "last-line-no-newline\n# BEGIN sing-router-test") {
		t.Fatalf("missing inserted newline: %q", out)
	}
}

// END 行后无换行时，RemoveHook 应保留尾部不破坏。
func TestRemoveBlockAtEOFNoTrailingNewline(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "nat-start")
	initial := "#!/bin/sh\n# preface\n# BEGIN sing-router-test (managed by `sing-router install`; do not edit)\necho us\n# END sing-router-test"
	if err := os.WriteFile(target, []byte(initial), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := RemoveHook(target, "sing-router-test"); err != nil {
		t.Fatal(err)
	}
	out := read(t, target)
	if strings.Contains(out, "echo us") || strings.Contains(out, "BEGIN sing-router-test") {
		t.Fatalf("block not removed: %q", out)
	}
	if !strings.Contains(out, "# preface") {
		t.Fatalf("preface lost: %q", out)
	}
}

// BEGIN 出现但 END 缺失 → blockIndices 应认为无效，InjectHook 走 append 分支。
func TestInjectWhenBeginPresentButEndMissingAppends(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "nat-start")
	initial := "#!/bin/sh\n# BEGIN sing-router-test (managed by `sing-router install`; do not edit)\necho dangling\n"
	if err := os.WriteFile(target, []byte(initial), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := InjectHook(target, "sing-router-test", "echo new"); err != nil {
		t.Fatal(err)
	}
	out := read(t, target)
	// 原来的孤儿 BEGIN 行保留；新块附加在末尾
	if strings.Count(out, "# BEGIN sing-router-test") != 2 {
		t.Fatalf("expected two BEGIN markers (orphan + new): %q", out)
	}
	if !strings.Contains(out, "echo new") {
		t.Fatal("new payload missing")
	}
}

// BEGIN 是文件的第一行（无前置内容） → indexOfLine 走 HasPrefix=true 分支。
func TestInjectReplacesBlockAtFileStart(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "nat-start")
	initial := "# BEGIN sing-router-test (managed by `sing-router install`; do not edit)\necho old\n# END sing-router-test\n# tail\n"
	if err := os.WriteFile(target, []byte(initial), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := InjectHook(target, "sing-router-test", "echo new"); err != nil {
		t.Fatal(err)
	}
	out := read(t, target)
	if strings.Contains(out, "echo old") {
		t.Fatal("old payload should be replaced")
	}
	if !strings.Contains(out, "echo new") || !strings.Contains(out, "# tail") {
		t.Fatalf("replace failed: %q", out)
	}
}

// ReadFile 失败但非 NotExist（例如目标是目录）→ 错误被透传。
func TestInjectAndRemoveBubbleNonNotExistReadError(t *testing.T) {
	dir := t.TempDir()
	if err := InjectHook(dir, "sing-router-test", "echo x"); err == nil {
		t.Fatal("InjectHook on a directory should error")
	}
	if err := RemoveHook(dir, "sing-router-test"); err == nil {
		t.Fatal("RemoveHook on a directory should error")
	}
}

// writeExec 失败路径：父目录不存在 → WriteFile 报 ENOENT。
func TestWriteExecFailsWhenParentMissing(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "missing-subdir", "file")
	if err := writeExec(target, []byte("x")); err == nil {
		t.Fatal("expected error on missing parent")
	}
}
