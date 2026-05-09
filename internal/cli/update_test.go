package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestUpdate_UnknownTargetFails(t *testing.T) {
	rundir := t.TempDir()
	if err := os.WriteFile(filepath.Join(rundir, "daemon.toml"), []byte(`
[gitee]
token = "tk"
`), 0o644); err != nil {
		t.Fatal(err)
	}
	cmd := NewRootCmd()
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetErr(&buf)
	cmd.SetArgs([]string{"update", "-D", rundir, "garbage"})
	err := cmd.Execute()
	if err == nil || !strings.Contains(err.Error(), "unknown target") {
		t.Fatalf("expected unknown-target error, got %v (out=%s)", err, buf.String())
	}
}

func TestUpdate_MissingTokenFails(t *testing.T) {
	rundir := t.TempDir()
	if err := os.WriteFile(filepath.Join(rundir, "daemon.toml"), []byte(""), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SING_ROUTER_GITEE_TOKEN", "")
	cmd := NewRootCmd()
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetErr(&buf)
	cmd.SetArgs([]string{"update", "-D", rundir, "sing-box"})
	err := cmd.Execute()
	if err == nil || !strings.Contains(err.Error(), "gitee.token is empty") {
		t.Fatalf("expected token error, got %v", err)
	}
}

func TestUpdate_CNDoesNotRequireToken(t *testing.T) {
	rundir := t.TempDir()
	// 没有 token 的 daemon.toml；cn 资源不依赖 token，不应在 token 校验处失败。
	// 但也别真去拉公网 cn.txt — 把 URL 指向一个不存在的本地端口让下载在网络层失败。
	if err := os.WriteFile(filepath.Join(rundir, "daemon.toml"), []byte(`
[download]
cn_list_url = "http://127.0.0.1:1/never"
http_retries = 0
`), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SING_ROUTER_GITEE_TOKEN", "")
	cmd := NewRootCmd()
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetErr(&buf)
	cmd.SetArgs([]string{"update", "-D", rundir, "cn"})
	err := cmd.Execute()
	// 应该是网络错误，而不是"gitee.token is empty"。
	if err == nil {
		t.Fatal("expected network error")
	}
	if strings.Contains(err.Error(), "gitee.token is empty") {
		t.Fatalf("cn target should not require token: %v", err)
	}
}
