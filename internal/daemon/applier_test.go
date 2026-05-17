package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// applierFixture 在 tmpdir 里搭出 Applier 期望的目录结构，并返回一些常用路径。
type applierFixture struct {
	rundir    string
	configDir string // 相对 rundir
	zooPath   string
	rulePath  string
	binPath   string
	stagePath string
	cnPath    string
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
		cnPath:    filepath.Join(rundir, "var", "cn.txt"),
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

func writeFile(t *testing.T, p, content string) {
	t.Helper()
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestApply_HappyPath_BinZoo: bin + zoo 都真变化 → CheckConfig 通过 → Restart 调一次。
func TestApply_HappyPath_BinZoo(t *testing.T) {
	fx := newApplierFixture(t)
	writeFile(t, fx.zooPath, "OLD_ZOO")
	writeFile(t, fx.rulePath, "OLD_RULES")
	writeFile(t, fx.binPath, "OLD_BIN")
	writeFile(t, fx.stagePath, "NEW_BIN")

	var restartCalls, checkCalls int
	a := &Applier{
		Rundir:      fx.rundir,
		ConfigDir:   fx.configDir,
		Emitter:     newTestEmitter(t),
		Restart:     func(ctx context.Context) error { restartCalls++; return nil },
		CheckConfig: func(ctx context.Context) error { checkCalls++; return nil },
		PreprocessZoo: func() error {
			writeFile(t, fx.zooPath, "NEW_ZOO")
			writeFile(t, fx.rulePath, "NEW_RULES")
			return nil
		},
	}

	if err := a.ApplyAll(context.Background()); err != nil {
		t.Fatalf("ApplyAll: %v", err)
	}
	if restartCalls != 1 {
		t.Errorf("restart calls = %d, want 1", restartCalls)
	}
	if checkCalls != 1 {
		t.Errorf("check calls = %d, want 1", checkCalls)
	}
	if data, _ := os.ReadFile(fx.binPath); string(data) != "NEW_BIN" {
		t.Errorf("bin contents = %q, want NEW_BIN", string(data))
	}
	if _, err := os.Stat(fx.stagePath); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("staging not removed")
	}
	backupBin := filepath.Join(fx.rundir, "var", "apply-backup", "sing-box")
	if _, err := os.Stat(backupBin); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("backup bin should be cleaned")
	}
	state := readState(t, fx.rundir)
	if state.SingBox == "" || state.ZooJSON == "" || state.RuleSetJSON == "" {
		t.Errorf("apply-state hashes empty: %+v", state)
	}
}

// TestApply_NoOpGate_ZooByteIdentical: PreprocessZoo 写出与旧版完全相同的内容 →
// sha256 闸门挡住，不触发 Restart。
func TestApply_NoOpGate_ZooByteIdentical(t *testing.T) {
	fx := newApplierFixture(t)
	writeFile(t, fx.zooPath, "SAME_ZOO")
	writeFile(t, fx.rulePath, "SAME_RULES")

	var restartCalls int
	a := &Applier{
		Rundir:    fx.rundir,
		ConfigDir: fx.configDir,
		Emitter:   newTestEmitter(t),
		Restart:   func(ctx context.Context) error { restartCalls++; return nil },
		CheckConfig: func(ctx context.Context) error {
			t.Fatal("CheckConfig should not be called when nothing actually changes")
			return nil
		},
		PreprocessZoo: func() error {
			writeFile(t, fx.zooPath, "SAME_ZOO")
			writeFile(t, fx.rulePath, "SAME_RULES")
			return nil
		},
	}

	if err := a.Apply(context.Background(), []Resource{ResourceZoo}); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if restartCalls != 0 {
		t.Errorf("restart should NOT be called for byte-identical output, got %d", restartCalls)
	}
}

// TestApply_NoOpGate_BinByteIdentical: staging 二进制与现行 bin 内容相同 →
// 不触发 Restart。
func TestApply_NoOpGate_BinByteIdentical(t *testing.T) {
	fx := newApplierFixture(t)
	writeFile(t, fx.binPath, "SAME_BIN")
	writeFile(t, fx.stagePath, "SAME_BIN")

	var restartCalls int
	a := &Applier{
		Rundir:    fx.rundir,
		ConfigDir: fx.configDir,
		Emitter:   newTestEmitter(t),
		Restart:   func(ctx context.Context) error { restartCalls++; return nil },
		CheckConfig: func(ctx context.Context) error {
			t.Fatal("CheckConfig should not be called when bin is identical")
			return nil
		},
	}

	if err := a.Apply(context.Background(), []Resource{ResourceSingBox}); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if restartCalls != 0 {
		t.Errorf("restart should NOT be called for byte-identical bin, got %d", restartCalls)
	}
	if data, _ := os.ReadFile(fx.binPath); string(data) != "SAME_BIN" {
		t.Errorf("bin contents = %q, want SAME_BIN", string(data))
	}
}

// TestApply_CheckFailReverts: CheckConfig 失败 → 全部 revert，Restart 不被调用。
func TestApply_CheckFailReverts(t *testing.T) {
	fx := newApplierFixture(t)
	writeFile(t, fx.zooPath, "OLD_ZOO")
	writeFile(t, fx.rulePath, "OLD_RULES")
	writeFile(t, fx.binPath, "OLD_BIN")
	writeFile(t, fx.stagePath, "NEW_BIN")

	var restartCalls int
	a := &Applier{
		Rundir:      fx.rundir,
		ConfigDir:   fx.configDir,
		Emitter:     newTestEmitter(t),
		Restart:     func(ctx context.Context) error { restartCalls++; return nil },
		CheckConfig: func(ctx context.Context) error { return errors.New("synthetic check failure") },
		PreprocessZoo: func() error {
			writeFile(t, fx.zooPath, "NEW_ZOO")
			writeFile(t, fx.rulePath, "NEW_RULES")
			return nil
		},
	}

	if err := a.ApplyAll(context.Background()); err != nil {
		t.Fatalf("ApplyAll unexpected err: %v", err)
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

// TestApply_RestartFailRevertsAndRecoverRestarts: CheckConfig 通过，第一次 Restart 失败 →
// revert → 再调一次 Restart（用旧配置拉回）。两次 Restart 都走绕节流的回调（必生效）。
func TestApply_RestartFailRevertsAndRecoverRestarts(t *testing.T) {
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
		Restart: func(ctx context.Context) error {
			restartCalls++
			if restartCalls == 1 {
				return errors.New("restart fail")
			}
			return nil // recover-restart 成功
		},
		CheckConfig: func(ctx context.Context) error { return nil },
		PreprocessZoo: func() error {
			writeFile(t, fx.zooPath, "NEW_ZOO")
			writeFile(t, fx.rulePath, "NEW_RULES")
			return nil
		},
	}

	err := a.ApplyAll(context.Background())
	if err == nil {
		t.Fatal("expected first restart error to surface")
	}
	if restartCalls != 2 {
		t.Errorf("Restart should be called twice (failed + recover), got %d", restartCalls)
	}
	if data, _ := os.ReadFile(fx.zooPath); string(data) != "OLD_ZOO" {
		t.Errorf("zoo not reverted: %q", string(data))
	}
	if data, _ := os.ReadFile(fx.binPath); string(data) != "OLD_BIN" {
		t.Errorf("bin not reverted: %q", string(data))
	}
}

// TestApply_CNTriggersRestart: cn.txt sha256 与 apply-state.cn_txt 不同 →
// 触发一次 Restart（不再有独立 ReloadCNIpset 轻量路径）；再次调用 hash 未变 → no-op。
func TestApply_CNTriggersRestart(t *testing.T) {
	fx := newApplierFixture(t)
	writeFile(t, fx.cnPath, "1.0.0.0/8\n")

	var restartCalls int
	a := &Applier{
		Rundir:    fx.rundir,
		ConfigDir: fx.configDir,
		Emitter:   newTestEmitter(t),
		Restart:   func(ctx context.Context) error { restartCalls++; return nil },
	}

	if err := a.Apply(context.Background(), []Resource{ResourceCN}); err != nil {
		t.Fatalf("Apply cn first: %v", err)
	}
	if restartCalls != 1 {
		t.Fatalf("first cn change: restart should be triggered once, got %d", restartCalls)
	}
	if err := a.Apply(context.Background(), []Resource{ResourceCN}); err != nil {
		t.Fatalf("Apply cn second: %v", err)
	}
	if restartCalls != 1 {
		t.Fatalf("second call: cn unchanged → no restart, got %d total", restartCalls)
	}
	writeFile(t, fx.cnPath, "2.0.0.0/8\n")
	if err := a.Apply(context.Background(), []Resource{ResourceCN}); err != nil {
		t.Fatalf("Apply cn third: %v", err)
	}
	if restartCalls != 2 {
		t.Fatalf("third call: cn changed → restart again, got %d total", restartCalls)
	}
}

// TestApply_ThreeResourcesSingleRestart: sing-box + zoo + cn.txt 同时变化时
// Apply 应只调一次 Restart（核心需求：避免被 throttle 丢动作）。
func TestApply_ThreeResourcesSingleRestart(t *testing.T) {
	fx := newApplierFixture(t)
	writeFile(t, fx.zooPath, "OLD_ZOO")
	writeFile(t, fx.rulePath, "OLD_RULES")
	writeFile(t, fx.binPath, "OLD_BIN")
	writeFile(t, fx.stagePath, "NEW_BIN")
	writeFile(t, fx.cnPath, "1.0.0.0/8\n")

	var restartCalls int
	a := &Applier{
		Rundir:      fx.rundir,
		ConfigDir:   fx.configDir,
		Emitter:     newTestEmitter(t),
		Restart:     func(ctx context.Context) error { restartCalls++; return nil },
		CheckConfig: func(ctx context.Context) error { return nil },
		PreprocessZoo: func() error {
			writeFile(t, fx.zooPath, "NEW_ZOO")
			writeFile(t, fx.rulePath, "NEW_RULES")
			return nil
		},
	}

	if err := a.ApplyAll(context.Background()); err != nil {
		t.Fatalf("ApplyAll: %v", err)
	}
	if restartCalls != 1 {
		t.Fatalf("three resources changed simultaneously → restart should be ONE, got %d", restartCalls)
	}
	state := readState(t, fx.rundir)
	if state.SingBox == "" || state.ZooJSON == "" || state.CNTxt == "" {
		t.Errorf("apply-state should record all three hashes: %+v", state)
	}
}

// TestApply_SingleResourceOnlyTouchesKind: 只传 [ResourceCN]，
// 即使 sing-box.new 存在也不应被 commit（保留给用户后续 update sing-box --apply）。
func TestApply_SingleResourceOnlyTouchesKind(t *testing.T) {
	fx := newApplierFixture(t)
	writeFile(t, fx.binPath, "OLD_BIN")
	writeFile(t, fx.stagePath, "NEW_BIN")
	writeFile(t, fx.cnPath, "1.0.0.0/8\n")

	var restartCalls int
	a := &Applier{
		Rundir:  fx.rundir,
		ConfigDir: fx.configDir,
		Emitter: newTestEmitter(t),
		Restart: func(ctx context.Context) error { restartCalls++; return nil },
	}

	if err := a.Apply(context.Background(), []Resource{ResourceCN}); err != nil {
		t.Fatalf("Apply cn-only: %v", err)
	}
	if restartCalls != 1 {
		t.Fatalf("cn change → restart once, got %d", restartCalls)
	}
	if data, _ := os.ReadFile(fx.binPath); string(data) != "OLD_BIN" {
		t.Errorf("bin should NOT be touched by cn-only apply: %q", string(data))
	}
	if _, err := os.Stat(fx.stagePath); err != nil {
		t.Errorf("staging should be preserved by cn-only apply: %v", err)
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
