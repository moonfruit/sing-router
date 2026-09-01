package shell

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestRunnerExecutesScriptWithEnv(t *testing.T) {
	r := NewRunner(RunnerConfig{
		Bash: "/bin/bash",
		Env:  map[string]string{"FOO": "bar", "BAZ": "qux"},
	})
	var stderr strings.Builder
	err := r.Run(context.Background(), "echo $FOO-$BAZ 1>&2", &stderr)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !strings.Contains(stderr.String(), "bar-qux") {
		t.Fatalf("stderr missing expected line: %q", stderr.String())
	}
}

func TestRunnerRequiredEnvAbsentFails(t *testing.T) {
	r := NewRunner(RunnerConfig{Bash: "/bin/bash"})
	var stderr strings.Builder
	script := `set -eu; : "${MUST_EXIST:?MUST_EXIST not set}"; echo ok`
	err := r.Run(context.Background(), script, &stderr)
	if err == nil {
		t.Fatal("expected error from missing env")
	}
	var rerr *Error
	if !errors.As(err, &rerr) {
		t.Fatalf("err type %T", err)
	}
	if rerr.ExitCode == 0 {
		t.Fatal("exit code should be non-zero")
	}
}

func TestRunnerStreamsStderrLineByLine(t *testing.T) {
	r := NewRunner(RunnerConfig{Bash: "/bin/bash"})
	var mu sync.Mutex
	lines := []string{}
	r.OnStderr = func(line string) {
		mu.Lock()
		defer mu.Unlock()
		lines = append(lines, line)
	}
	var stderr strings.Builder
	script := "echo line1 1>&2; echo line2 1>&2; echo line3 1>&2"
	if err := r.Run(context.Background(), script, &stderr); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(lines) != 3 || lines[0] != "line1" {
		t.Fatalf("stderr lines: %v", lines)
	}
}

// TestRunnerRunsInConfiguredDir 守住 startup.sh / teardown.sh 的 cwd 确定性。
// 真机事故：daemon 长跑期间 U 盘掉线重挂载，进程 cwd 落在已被 lazy umount 的
// 旧 fs 上（dmesg: "comm bash: error -5 reading directory block"），脚本继承的
// 就是这个失效 cwd。Dir 显式指向 rundir 后，脚本不再受 daemon 自身 cwd 影响。
func TestRunnerRunsInConfiguredDir(t *testing.T) {
	dir := t.TempDir()
	r := NewRunner(RunnerConfig{Bash: "/bin/bash", Dir: dir})

	var stderr strings.Builder
	if err := r.Run(context.Background(), "pwd 1>&2", &stderr); err != nil {
		t.Fatalf("Run: %v", err)
	}
	// macOS 的 t.TempDir() 在 /var → /private/var 符号链接下，取 base 名比较即可。
	if !strings.Contains(stderr.String(), filepath.Base(dir)) {
		t.Fatalf("脚本 cwd = %q, 期望在 %q 下", strings.TrimSpace(stderr.String()), dir)
	}
}
