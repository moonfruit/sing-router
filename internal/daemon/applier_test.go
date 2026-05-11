package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// applierFixture 在 tmpdir 里搭出 Applier 期望的目录结构,并返回一些常用路径。
type applierFixture struct {
	rundir    string
	configDir string // 相对 rundir
	zooPath   string
	rulePath  string
	binPath   string
	stagePath string
}

func newApplierFixture(t *testing.T) applierFixture {
	t.Helper()
	rundir := t.TempDir()
	configDir := "config.d"
	cfg := applierFixture{
		rundir:    rundir,
		configDir: configDir,
		zooPath:   filepath.Join(rundir, configDir, "zoo.json"),
		rulePath:  filepath.Join(rundir, configDir, "rule-set.json"),
		binPath:   filepath.Join(rundir, "bin", "sing-box"),
		stagePath: filepath.Join(rundir, "bin", "sing-box.new"),
	}
	if err := os.MkdirAll(filepath.Join(rundir, configDir), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(rundir, "bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(rundir, "var"), 0o755); err != nil {
		t.Fatal(err)
	}
	return cfg
}

// writeFile 是 t.TempDir() 下的简便写入。
func writeFile(t *testing.T, p, content string) {
	t.Helper()
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestApplier_HappyPath: zoo + bin 都真变化 → Preprocess 写新 zoo/rule-set,
// CheckConfig 通过 → Restart 调用一次 → backup 被清理 → apply-state 更新。
func TestApplier_HappyPath(t *testing.T) {
	fx := newApplierFixture(t)
	// 旧 zoo / rule-set / bin
	writeFile(t, fx.zooPath, "OLD_ZOO")
	writeFile(t, fx.rulePath, "OLD_RULES")
	writeFile(t, fx.binPath, "OLD_BIN")
	// staging:新 bin
	writeFile(t, fx.stagePath, "NEW_BIN")

	var restartCalls, recoverCalls, checkCalls int
	a := &Applier{
		Rundir:    fx.rundir,
		ConfigDir: fx.configDir,
		Emitter:   newTestEmitter(t),
		Restart: func(ctx context.Context) error {
			restartCalls++
			return nil
		},
		Recover:     func(ctx context.Context) error { recoverCalls++; return nil },
		CheckConfig: func(ctx context.Context) error { checkCalls++; return nil },
		PreprocessZoo: func() error {
			// 模拟新的 zoo / rule-set 内容
			writeFile(t, fx.zooPath, "NEW_ZOO")
			writeFile(t, fx.rulePath, "NEW_RULES")
			return nil
		},
	}

	if err := a.ApplySingBoxOrZoo(context.Background(), true, fx.stagePath); err != nil {
		t.Fatalf("ApplySingBoxOrZoo: %v", err)
	}
	if restartCalls != 1 {
		t.Errorf("restart calls = %d, want 1", restartCalls)
	}
	if checkCalls != 1 {
		t.Errorf("check calls = %d, want 1", checkCalls)
	}
	if recoverCalls != 0 {
		t.Errorf("recover calls = %d, want 0", recoverCalls)
	}
	// staging 已被 rename → bin 当前是 NEW_BIN
	if data, _ := os.ReadFile(fx.binPath); string(data) != "NEW_BIN" {
		t.Errorf("bin contents = %q, want NEW_BIN", string(data))
	}
	if _, err := os.Stat(fx.stagePath); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("staging not removed")
	}
	// backup 已清理
	backupBin := filepath.Join(fx.rundir, "var", "apply-backup", "sing-box")
	if _, err := os.Stat(backupBin); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("backup bin should be cleaned")
	}
	// apply-state 已写
	state := readState(t, fx.rundir)
	if state.SingBox == "" || state.ZooJSON == "" || state.RuleSetJSON == "" {
		t.Errorf("apply-state hashes empty: %+v", state)
	}
}

// TestApplier_NoOpGate_ZooByteIdentical: zoo.raw.json 真变,但白名单过滤后
// 产物 zoo.json 与 rule-set.json 字节相同 → 不触发 Restart,backup 全清。
func TestApplier_NoOpGate_ZooByteIdentical(t *testing.T) {
	fx := newApplierFixture(t)
	writeFile(t, fx.zooPath, "SAME_ZOO")
	writeFile(t, fx.rulePath, "SAME_RULES")
	// 无 bin 变化

	var restartCalls int
	a := &Applier{
		Rundir:    fx.rundir,
		ConfigDir: fx.configDir,
		Emitter:   newTestEmitter(t),
		Restart:   func(ctx context.Context) error { restartCalls++; return nil },
		Recover:   func(ctx context.Context) error { return nil },
		CheckConfig: func(ctx context.Context) error {
			t.Fatal("CheckConfig should not be called when nothing actually changes")
			return nil
		},
		PreprocessZoo: func() error {
			// 写入完全相同的字节
			writeFile(t, fx.zooPath, "SAME_ZOO")
			writeFile(t, fx.rulePath, "SAME_RULES")
			return nil
		},
	}

	if err := a.ApplySingBoxOrZoo(context.Background(), true, ""); err != nil {
		t.Fatalf("ApplySingBoxOrZoo: %v", err)
	}
	if restartCalls != 0 {
		t.Errorf("restart should NOT be called for byte-identical output, got %d calls", restartCalls)
	}
}

// TestApplier_NoOpGate_BinByteIdentical: staging 二进制虽然存在但与现行
// bin 内容相同 → 仍旧不触发 Restart。
//
// 实际场景下 sync.go 在解压时就会 sha256-gate 掉这种情况(不返回 staging),
// 但 Applier 层也应有兜底,验证两层闸门都到位。
func TestApplier_NoOpGate_BinByteIdentical(t *testing.T) {
	fx := newApplierFixture(t)
	writeFile(t, fx.binPath, "SAME_BIN")
	writeFile(t, fx.stagePath, "SAME_BIN")

	var restartCalls int
	a := &Applier{
		Rundir:    fx.rundir,
		ConfigDir: fx.configDir,
		Emitter:   newTestEmitter(t),
		Restart:   func(ctx context.Context) error { restartCalls++; return nil },
		Recover:   func(ctx context.Context) error { return nil },
		CheckConfig: func(ctx context.Context) error {
			t.Fatal("CheckConfig should not be called when bin is identical")
			return nil
		},
	}

	if err := a.ApplySingBoxOrZoo(context.Background(), false, fx.stagePath); err != nil {
		t.Fatalf("ApplySingBoxOrZoo: %v", err)
	}
	if restartCalls != 0 {
		t.Errorf("restart should NOT be called for byte-identical bin, got %d calls", restartCalls)
	}
	// bin 仍是 SAME_BIN
	if data, _ := os.ReadFile(fx.binPath); string(data) != "SAME_BIN" {
		t.Errorf("bin contents = %q, want SAME_BIN", string(data))
	}
}

// TestApplier_CheckFailReverts: CheckConfig 失败 → 旧 zoo/rule-set/bin 完整 revert,
// Restart 不被调用。
func TestApplier_CheckFailReverts(t *testing.T) {
	fx := newApplierFixture(t)
	writeFile(t, fx.zooPath, "OLD_ZOO")
	writeFile(t, fx.rulePath, "OLD_RULES")
	writeFile(t, fx.binPath, "OLD_BIN")
	writeFile(t, fx.stagePath, "NEW_BIN")

	var restartCalls int
	a := &Applier{
		Rundir:    fx.rundir,
		ConfigDir: fx.configDir,
		Emitter:   newTestEmitter(t),
		Restart:   func(ctx context.Context) error { restartCalls++; return nil },
		Recover:   func(ctx context.Context) error { return nil },
		CheckConfig: func(ctx context.Context) error {
			return errors.New("synthetic check failure")
		},
		PreprocessZoo: func() error {
			writeFile(t, fx.zooPath, "NEW_ZOO")
			writeFile(t, fx.rulePath, "NEW_RULES")
			return nil
		},
	}

	if err := a.ApplySingBoxOrZoo(context.Background(), true, fx.stagePath); err != nil {
		t.Fatalf("ApplySingBoxOrZoo unexpected err: %v", err)
	}
	if restartCalls != 0 {
		t.Errorf("restart should NOT be called when CheckConfig fails")
	}
	if data, _ := os.ReadFile(fx.zooPath); string(data) != "OLD_ZOO" {
		t.Errorf("zoo not reverted: %q", string(data))
	}
	if data, _ := os.ReadFile(fx.rulePath); string(data) != "OLD_RULES" {
		t.Errorf("rule-set not reverted: %q", string(data))
	}
	if data, _ := os.ReadFile(fx.binPath); string(data) != "OLD_BIN" {
		t.Errorf("bin not reverted: %q", string(data))
	}
}

// TestApplier_RestartFailRecovers: CheckConfig 通过,Restart 失败 → revert + Recover 被调。
func TestApplier_RestartFailRecovers(t *testing.T) {
	fx := newApplierFixture(t)
	writeFile(t, fx.zooPath, "OLD_ZOO")
	writeFile(t, fx.rulePath, "OLD_RULES")
	writeFile(t, fx.binPath, "OLD_BIN")
	writeFile(t, fx.stagePath, "NEW_BIN")

	var recoverCalls int
	a := &Applier{
		Rundir:    fx.rundir,
		ConfigDir: fx.configDir,
		Emitter:   newTestEmitter(t),
		Restart:   func(ctx context.Context) error { return errors.New("restart fail") },
		Recover:   func(ctx context.Context) error { recoverCalls++; return nil },
		CheckConfig: func(ctx context.Context) error { return nil },
		PreprocessZoo: func() error {
			writeFile(t, fx.zooPath, "NEW_ZOO")
			writeFile(t, fx.rulePath, "NEW_RULES")
			return nil
		},
	}

	err := a.ApplySingBoxOrZoo(context.Background(), true, fx.stagePath)
	if err == nil {
		t.Fatal("expected restart error to surface")
	}
	if recoverCalls != 1 {
		t.Errorf("Recover should be called once, got %d", recoverCalls)
	}
	// 内容已 revert 到旧版
	if data, _ := os.ReadFile(fx.zooPath); string(data) != "OLD_ZOO" {
		t.Errorf("zoo not reverted: %q", string(data))
	}
	if data, _ := os.ReadFile(fx.binPath); string(data) != "OLD_BIN" {
		t.Errorf("bin not reverted: %q", string(data))
	}
}

// TestApplier_CNListGate: cn.txt sha256 与 apply-state.cn_txt 一致 → 不触发 ReloadCNIpset。
func TestApplier_CNListGate(t *testing.T) {
	fx := newApplierFixture(t)
	cnPath := filepath.Join(fx.rundir, "var", "cn.txt")
	writeFile(t, cnPath, "1.0.0.0/8\n")

	var calls int
	a := &Applier{
		Rundir:    fx.rundir,
		ConfigDir: fx.configDir,
		Emitter:   newTestEmitter(t),
		ReloadCNIpset: func(ctx context.Context) error {
			calls++
			return nil
		},
	}

	// 首次:state 没有 cn 哈希 → 触发 reload
	if err := a.ApplyCNList(context.Background()); err != nil {
		t.Fatalf("ApplyCNList first: %v", err)
	}
	if calls != 1 {
		t.Fatalf("first call: reload should be triggered once, got %d", calls)
	}
	// 二次:cn.txt 不变 → 闸门挡住
	if err := a.ApplyCNList(context.Background()); err != nil {
		t.Fatalf("ApplyCNList second: %v", err)
	}
	if calls != 1 {
		t.Fatalf("second call: reload should be gated, got %d total calls", calls)
	}
	// 第三次:cn.txt 真变 → 再次触发
	writeFile(t, cnPath, "2.0.0.0/8\n")
	if err := a.ApplyCNList(context.Background()); err != nil {
		t.Fatalf("ApplyCNList third: %v", err)
	}
	if calls != 2 {
		t.Fatalf("third call: reload should fire again, got %d total calls", calls)
	}
}

func readState(t *testing.T, rundir string) applyState {
	t.Helper()
	var s applyState
	data, err := os.ReadFile(filepath.Join(rundir, applyStateFile))
	if err != nil {
		t.Fatalf("read state: %v", err)
	}
	if err := json.Unmarshal(data, &s); err != nil {
		t.Fatalf("unmarshal state: %v", err)
	}
	return s
}
