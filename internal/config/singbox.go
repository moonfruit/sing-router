package config

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
)

// execCommand 暴露给测试以便注入 fake exec.Cmd 工厂。
var execCommand = func(ctx context.Context, name string, args ...string) *exec.Cmd {
	return exec.CommandContext(ctx, name, args...)
}

// statFile 暴露给测试以便注入 fake stat。
var statFile = os.Stat

// CheckError 携带 sing-box check 的 stderr 输出，便于上层报告。
type CheckError struct {
	Stderr string
	Err    error
}

func (e *CheckError) Error() string {
	return fmt.Sprintf("sing-box check failed: %v\n%s", e.Err, e.Stderr)
}

func (e *CheckError) Unwrap() error { return e.Err }

// CheckSingBoxConfig 调 `sing-box check -C <dir>` 校验配置。
// 二进制不存在或不可执行时返回非 *CheckError 的 error。
func CheckSingBoxConfig(ctx context.Context, binary, configDir string) error {
	if _, err := statFile(binary); err != nil {
		return fmt.Errorf("sing-box binary: %w", err)
	}
	cmd := execCommand(ctx, binary, "check", "-C", configDir)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return &CheckError{Stderr: stderr.String(), Err: err}
	}
	return nil
}
