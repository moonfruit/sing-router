package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

// runInstallReal 跑一次真正执行（非 --dry-run）的 install，用 --debug-only
// 避开 /opt/etc/init.d 与固件钩子写入，只留下 rundir 内的 SeedDefaults 效果
// 可观察——这正是 I1 需要验证的部分（daemon.toml 是否真的落地了 bypass 配置）。
func runInstallReal(t *testing.T, rundir string, extra ...string) string {
	t.Helper()
	cmd := newInstallCmd()
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetErr(&buf)
	args := append([]string{"-D", rundir, "--firmware=koolshare", "--debug-only"}, extra...)
	cmd.SetArgs(args)
	if err := cmd.Execute(); err != nil {
		t.Fatalf("install execute: %v\noutput: %s", err, buf.String())
	}
	return buf.String()
}

// 首次安装（daemon.toml 不存在）：SeedDefaults 直接写 daemon.toml，[bypass]
// 与 [http].token 应该真的落地，横幅应该照常打印"enabled"。
func TestInstall_EnableBypassFreshInstallActivates(t *testing.T) {
	rundir := t.TempDir()
	out := runInstallReal(t, rundir, "--enable-bypass")

	mustContain(t, out, "LAN client bypass enabled.")
	mustContain(t, out, "listen: 0.0.0.0:9998")
	mustNotContain(t, out, "NOT active yet")

	data, err := os.ReadFile(filepath.Join(rundir, "daemon.toml"))
	if err != nil {
		t.Fatalf("read daemon.toml: %v", err)
	}
	mustContain(t, string(data), "enabled = true")
}

// I1 的核心回归：daemon.toml 已经存在（早就装过 sing-router）时，
// writeDefaultAndSeed 保留用户文件、把新内容写到 daemon.toml.default，
// [bypass] 完全没有变化。此时绝不能打印"enabled"横幅——之前的 bug 正是
// 无条件打印，导致用户以为配置生效了，实际 LAN 客户端心跳会拿到
// connection refused。
func TestInstall_EnableBypassOnExistingDaemonTomlDoesNotActivate(t *testing.T) {
	rundir := t.TempDir()
	cfgPath := filepath.Join(rundir, "daemon.toml")
	if err := os.MkdirAll(rundir, 0o755); err != nil {
		t.Fatal(err)
	}
	// 一份与新渲染内容明显不同、但合法可解析的旧 daemon.toml：bypass 关闭。
	oldContent := "[bypass]\nenabled = false\n"
	if err := os.WriteFile(cfgPath, []byte(oldContent), 0o644); err != nil {
		t.Fatal(err)
	}

	out := runInstallReal(t, rundir, "--enable-bypass")

	mustContain(t, out, "LAN client bypass is NOT active yet.")
	mustContain(t, out, "[bypass].enabled = true")
	mustContain(t, out, "[http].token =")
	mustNotContain(t, out, "LAN client bypass enabled.")

	// daemon.toml 本体必须原样不动。
	data, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatalf("read daemon.toml: %v", err)
	}
	if string(data) != oldContent {
		t.Fatalf("daemon.toml must be left untouched, got:\n%s", data)
	}

	// 新内容应该落到 daemon.toml.default。
	defaultData, err := os.ReadFile(cfgPath + ".default")
	if err != nil {
		t.Fatalf("read daemon.toml.default: %v", err)
	}
	mustContain(t, string(defaultData), "enabled = true")
}

// 回读失败（daemon.toml 解析错）时 bypassConfigCommitted 必须把错误往上传，
// 不能静默返回"已生效"——这一路径由 install.go 里 verifyErr != nil 的分支
// 处理（打印"could not verify"并按未生效处理）。
//
// 注：这里直接单测 bypassConfigCommitted 而不走完整 install 命令，是因为
// newInstallCmd 在读取 --gitee-token 覆盖之前也会先 LoadDaemonConfig 一次
// 且忽略了错误（internal/cli/install.go 里 `cfg, _ := config.LoadDaemonConfig(...)`，
// 与本次 I1 修复无关的既有行为），一份解析不了的 daemon.toml 会在那一步就
// 让 cfg 变成 nil、随后访问 cfg.Install.* 触发 panic——这是另一个预先存在、
// 超出本轮 5 项修复范围的问题，不在这里顺带修。
func TestBypassConfigCommitted_ReadbackFailurePropagatesError(t *testing.T) {
	rundir := t.TempDir()
	cfgPath := filepath.Join(rundir, "daemon.toml")
	if err := os.WriteFile(cfgPath, []byte("this is not valid toml [[[\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	committed, err := bypassConfigCommitted(rundir, "tok")
	if err == nil {
		t.Fatal("expected error for unparsable daemon.toml")
	}
	if committed {
		t.Fatal("must not report committed on readback failure")
	}
}

// 文件不存在（理论上不该发生：SeedDefaults 刚写完/保留过一份）时
// LoadDaemonConfig 按其自身约定返回全默认 config、不报错；bypassConfigCommitted
// 应据此判定为"未生效"，不应该报错崩掉整条 install 流程。
func TestBypassConfigCommitted_MissingFileIsNotCommittedWithoutError(t *testing.T) {
	rundir := t.TempDir()
	committed, err := bypassConfigCommitted(rundir, "tok")
	if err != nil {
		t.Fatalf("missing daemon.toml should not error: %v", err)
	}
	if committed {
		t.Fatal("missing daemon.toml must not report committed")
	}
}

// --dry-run 从不真正写盘，验证逻辑必须被跳过，只给一条"预览"措辞，不能
// 误报"NOT active yet"（那会让首次安装的 dry-run 预览显得像出了错）。
func TestInstall_EnableBypassDryRunPreviewOnly(t *testing.T) {
	out := runInstallDryRun(t, "--enable-bypass")

	mustContain(t, out, "[dry-run] would enable LAN client bypass.")
	mustNotContain(t, out, "NOT active yet")
	mustNotContain(t, out, "LAN client bypass enabled.")
}
