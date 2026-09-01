package config

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestSingBoxCheckOK(t *testing.T) {
	// 替换 ExecCommand 为 fake：返回 exit 0 + stdout 空
	origExec := execCommand
	execCommand = func(ctx context.Context, name string, args ...string) *exec.Cmd {
		return exec.CommandContext(ctx, "true")
	}
	defer func() { execCommand = origExec }()

	// 替换 statFile 为 fake：让二进制检查通过
	origStat := statFile
	statFile = func(name string) (os.FileInfo, error) {
		return nil, nil
	}
	defer func() { statFile = origStat }()

	err := CheckSingBoxConfig(context.Background(), "/opt/sing-box", t.TempDir(), "/opt/conf.d")
	if err != nil {
		t.Fatalf("expected nil err, got %v", err)
	}
}

func TestSingBoxCheckFail(t *testing.T) {
	origExec := execCommand
	execCommand = func(ctx context.Context, name string, args ...string) *exec.Cmd {
		return exec.CommandContext(ctx, "false")
	}
	defer func() { execCommand = origExec }()

	// 替换 statFile 为 fake：让二进制检查通过
	origStat := statFile
	statFile = func(name string) (os.FileInfo, error) {
		return nil, nil
	}
	defer func() { statFile = origStat }()

	err := CheckSingBoxConfig(context.Background(), "/opt/sing-box", t.TempDir(), "/opt/conf.d")
	if err == nil {
		t.Fatal("expected error")
	}
	var ce *CheckError
	if !errors.As(err, &ce) {
		t.Fatalf("error type: %T", err)
	}
}

func TestSingBoxCheckMissingBinary(t *testing.T) {
	err := CheckSingBoxConfig(context.Background(), "/nonexistent/sing-box", "/opt/rundir", "/opt/conf.d")
	if err == nil {
		t.Fatal("expected error for missing binary")
	}
}

// TestSingBoxCheckRunsInWorkDir 守住这条约束：check 子进程必须跑在 workDir 里，
// 不能继承 daemon 自己的 cwd。真机事故：U 盘掉线重挂载后 daemon 的 cwd 落在已被
// lazy umount 的旧 fs 上，check 读相对路径的 external_ui("ui") 拿到 EIO，
// 于是每轮 sync 都误判「配置校验失败」并回滚。
//
// 断言方式是真实行为而非 mock 调用记录：fake 命令在自己的 cwd 下找 marker 文件，
// 找不到就 exit 1 —— 只有 cmd.Dir 真的被设成 workDir 才可能通过。
func TestSingBoxCheckRunsInWorkDir(t *testing.T) {
	workDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(workDir, "marker"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	origExec := execCommand
	execCommand = func(ctx context.Context, name string, args ...string) *exec.Cmd {
		return exec.CommandContext(ctx, "test", "-f", "marker")
	}
	defer func() { execCommand = origExec }()

	origStat := statFile
	statFile = func(name string) (os.FileInfo, error) { return nil, nil }
	defer func() { statFile = origStat }()

	if err := CheckSingBoxConfig(context.Background(), "/opt/sing-box", workDir, "/opt/conf.d"); err != nil {
		t.Fatalf("check 子进程没跑在 workDir 里: %v", err)
	}
}

// TestSingBoxCheckPassesWorkDirFlag 守住 check 与 run 的命令行对齐：supervisor 启
// run 时传 `-D <rundir>`（见 internal/daemon/supervisor.go），check 也必须传，
// 否则 sing-box 内部解析相对路径的基准与实际运行时不一致。
func TestSingBoxCheckPassesWorkDirFlag(t *testing.T) {
	var gotArgs []string

	origExec := execCommand
	execCommand = func(ctx context.Context, name string, args ...string) *exec.Cmd {
		gotArgs = args
		return exec.CommandContext(ctx, "true")
	}
	defer func() { execCommand = origExec }()

	origStat := statFile
	statFile = func(name string) (os.FileInfo, error) { return nil, nil }
	defer func() { statFile = origStat }()

	workDir := t.TempDir()
	if err := CheckSingBoxConfig(context.Background(), "/opt/sing-box", workDir, "/opt/conf.d"); err != nil {
		t.Fatalf("unexpected err: %v", err)
	}

	want := []string{"check", "-D", workDir, "-C", "/opt/conf.d"}
	if len(gotArgs) != len(want) {
		t.Fatalf("args = %v, want %v", gotArgs, want)
	}
	for i := range want {
		if gotArgs[i] != want[i] {
			t.Fatalf("args = %v, want %v", gotArgs, want)
		}
	}
}
