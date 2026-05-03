package config

import (
	"context"
	"errors"
	"os"
	"os/exec"
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

	err := CheckSingBoxConfig(context.Background(), "/opt/sing-box", "/opt/conf.d")
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

	err := CheckSingBoxConfig(context.Background(), "/opt/sing-box", "/opt/conf.d")
	if err == nil {
		t.Fatal("expected error")
	}
	var ce *CheckError
	if !errors.As(err, &ce) {
		t.Fatalf("error type: %T", err)
	}
}

func TestSingBoxCheckMissingBinary(t *testing.T) {
	err := CheckSingBoxConfig(context.Background(), "/nonexistent/sing-box", "/opt/conf.d")
	if err == nil {
		t.Fatal("expected error for missing binary")
	}
}
