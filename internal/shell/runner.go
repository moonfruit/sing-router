// Package shell 用 bash 跑嵌入脚本；env 由调用方注入。
package shell

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"syscall"
)

// RunnerConfig 控制 Runner 的行为。
type RunnerConfig struct {
	Bash string            // bash 可执行路径，默认 "/bin/bash"
	Env  map[string]string // 注入子进程的环境变量
}

// Runner 通过 stdin 把脚本喂给 bash 跑，避免落盘。
// 并发使用安全（每次 Run 独立的 cmd）。
type Runner struct {
	cfg RunnerConfig
	// OnStderr 可选：每行 stderr 触发一次回调（已 trim 行尾换行）。
	OnStderr func(line string)
}

// Error 携带退出码与最后一段 stderr，便于上层报告。
type Error struct {
	ExitCode int
	Stderr   string
	Cause    error
}

func (e *Error) Error() string {
	return fmt.Sprintf("script failed (exit=%d): %v\n%s", e.ExitCode, e.Cause, e.Stderr)
}

func (e *Error) Unwrap() error { return e.Cause }

// NewRunner 构造 Runner。
func NewRunner(cfg RunnerConfig) *Runner {
	if cfg.Bash == "" {
		cfg.Bash = "/bin/bash"
	}
	return &Runner{cfg: cfg}
}

// Run 在 ctx 控制下用 bash 执行 script；stderr 同时写入 capture（用于 Error.Stderr）
// 与 Runner.OnStderr 回调（用于实时事件流转）。
func (r *Runner) Run(ctx context.Context, script string, capture io.Writer) error {
	cmd := exec.CommandContext(ctx, r.cfg.Bash, "-s")
	cmd.Stdin = strings.NewReader(script)
	cmd.Env = r.envSlice()
	// 与 sing-box 同样的理由：把 shell 进程隔离到独立 pgid，shell 退出时的
	// SIGHUP 不会半途打断 startup.sh / teardown.sh / reapply-routes.sh。
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	pr, pw := io.Pipe()
	cmd.Stderr = pw
	cmd.Stdout = io.Discard

	var stderrCopy strings.Builder
	done := make(chan struct{})
	go func() {
		defer close(done)
		sc := bufio.NewScanner(pr)
		sc.Buffer(make([]byte, 64*1024), 4*1024*1024)
		for sc.Scan() {
			line := sc.Text()
			if r.OnStderr != nil {
				r.OnStderr(line)
			}
			if capture != nil {
				_, _ = capture.Write([]byte(line + "\n"))
			}
			stderrCopy.WriteString(line)
			stderrCopy.WriteByte('\n')
		}
	}()

	if err := cmd.Start(); err != nil {
		_ = pw.Close()
		return &Error{ExitCode: -1, Cause: err}
	}
	waitErr := cmd.Wait()
	_ = pw.Close()
	<-done

	if waitErr != nil {
		if ee, ok := waitErr.(*exec.ExitError); ok {
			return &Error{ExitCode: ee.ExitCode(), Stderr: stderrCopy.String(), Cause: waitErr}
		}
		return &Error{ExitCode: -1, Stderr: stderrCopy.String(), Cause: waitErr}
	}
	return nil
}

func (r *Runner) envSlice() []string {
	out := make([]string, 0, len(r.cfg.Env))
	for k, v := range r.cfg.Env {
		out = append(out, k+"="+v)
	}
	return out
}
