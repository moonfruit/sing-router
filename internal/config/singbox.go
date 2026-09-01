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

// CheckSingBoxConfig 调 `sing-box check -D <workDir> -C <configDir>` 校验配置。
// 二进制不存在或不可执行时返回非 *CheckError 的 error。
//
// workDir 必须与 supervisor 启 `sing-box run` 时用的 rundir 一致（见
// internal/daemon/supervisor.go 的 SingBoxDir/SingBoxArgs）：配置里 external_ui
// 之类的相对路径以 working directory 为基准解析，check 与 run 的基准不一致就会
// 校验出与实际运行时无关的结果。这里既设 cmd.Dir 也传 -D，两者缺一不可——前者
// 保证子进程根本不继承 daemon 的 cwd，后者保证 sing-box 内部的解析基准正确。
func CheckSingBoxConfig(ctx context.Context, binary, workDir, configDir string) error {
	if _, err := statFile(binary); err != nil {
		return fmt.Errorf("sing-box binary: %w", err)
	}
	cmd := execCommand(ctx, binary, "check", "-D", workDir, "-C", configDir)
	cmd.Dir = workDir
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return &CheckError{Stderr: stderr.String(), Err: err}
	}
	return nil
}
