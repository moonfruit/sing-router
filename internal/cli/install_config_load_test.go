package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

// 首装场景：daemon.toml 尚不存在。LoadDaemonConfig 按约定返回默认 cfg、nil
// error，install 的配置加载这一步必须放行、继续走后续 SeedDefaults 流程，
// 不能因为本次修复而误伤首次安装这条必经路径。
func TestInstall_MissingDaemonTomlLoadsDefaultsAndProceeds(t *testing.T) {
	rundir := t.TempDir()
	cmd := newInstallCmd()
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetErr(&buf)
	cmd.SetArgs([]string{"-D", rundir, "--firmware=koolshare", "--dry-run", "--debug-only"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("install with missing daemon.toml should succeed, got err: %v\noutput: %s", err, buf.String())
	}
	mustContain(t, buf.String(), "Debug seed complete")
}

// 核心回归：daemon.toml 存在但内容是坏 TOML（比如手工编辑写坏语法）。修复前
// `cfg, _ := config.LoadDaemonConfig(...)` 吞掉了 err，cfg 为 nil，紧接着的
// cfg.Install.* 解引用会直接 panic——用户看到的是一段 Go 堆栈而不是"配置文件
// 哪里错了"。修复后必须：不 panic、Execute() 返回明确 error、错误信息里带上
// 出问题的文件路径，方便用户定位。
func TestInstall_CorruptDaemonTomlReturnsErrorNotPanic(t *testing.T) {
	rundir := t.TempDir()
	cfgPath := filepath.Join(rundir, "daemon.toml")
	if err := os.MkdirAll(rundir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cfgPath, []byte("this is not valid toml [[[\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	cmd := newInstallCmd()
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetErr(&buf)
	cmd.SetArgs([]string{"-D", rundir, "--firmware=koolshare", "--dry-run", "--debug-only"})

	// 关键断言本身就是"没有 panic"：如果修复前的 bug 还在，这里会在
	// cmd.Execute() 内部 panic 并让整个测试进程崩溃，而不是走到下面的
	// err 检查。
	err := cmd.Execute()
	if err == nil {
		t.Fatalf("expected error for corrupt daemon.toml, got success; output: %s", buf.String())
	}
	mustContain(t, err.Error(), cfgPath)
}
