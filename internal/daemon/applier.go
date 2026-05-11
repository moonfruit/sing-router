package daemon

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/moonfruit/sing2seq/clef"
)

// Applier 把 sync.Updater 拉到的资源真正应用到运行中的 sing-box:
//
//   - sing-box 二进制(rundir/bin/sing-box.new)与 zoo.raw.json 触发完整的
//     "备份 → 落盘 → sha256 闸门 → CheckConfig → Restart → 失败 revert" 流程。
//   - cn.txt 走轻量路径:仅 reload ipset,不重启 sing-box。
//
// 真有变化才动手(硬约束):每条路径都以最终产物的 sha256 与上次成功 apply 的快照
// 比对,完全相同则 no-op,把上游 etag 假信号挡掉。
type Applier struct {
	Rundir    string
	ConfigDir string // 相对 rundir,例如 "config.d"

	Emitter *clef.Emitter

	// Restart 触发 Supervisor.Restart;失败时 Applier 会调 Recover 恢复旧配置。
	Restart func(ctx context.Context) error
	// Recover 等价于 Supervisor.RecoverFromFailedApply,允许从 Fatal/Reloading
	// 回到 Reloading→Running,把 sing-box 用 revert 后的旧 config 拉回来。
	Recover func(ctx context.Context) error

	// CheckConfig 用新二进制 + 新 config.d 跑 `sing-box check`。
	CheckConfig func(ctx context.Context) error

	// PreprocessZoo 重新跑 PreprocessZooFile + EnsureRequiredRuleSets,把
	// var/zoo.raw.json 翻译成 config.d/zoo.json 与 config.d/rule-set.json。
	// 通常在 wireup 时绑定 rawURL/ref/required 列表(避免 daemon 包反向依赖 config 包)。
	PreprocessZoo func() error

	// ReloadCNIpset 跑 reload-cn-ipset.sh,仅重建 cn ipset 不动 iptables 规则。
	ReloadCNIpset func(ctx context.Context) error
}

// applyState 持久化到 var/apply-state.json,记录上次成功 apply 的各资源 sha256;
// daemon 重启后读回,允许在下次 sync 时识别"上游 etag 变化但内容未变"的假信号。
type applyState struct {
	SingBox     string `json:"sing_box,omitempty"`
	ZooJSON     string `json:"zoo_json,omitempty"`
	RuleSetJSON string `json:"rule_set_json,omitempty"`
	CNTxt       string `json:"cn_txt,omitempty"`
}

const (
	applyStateFile    = "var/apply-state.json"
	applyBackupSubdir = "var/apply-backup"
	zooFileName       = "zoo.json"
	ruleSetFileName   = "rule-set.json"
	singBoxRelPath    = "bin/sing-box"
)

// ApplySingBoxOrZoo 在 zoo.raw.json 或 sing-box 二进制有变化时执行:
//
//  1. 备份现行 zoo.json / rule-set.json(in-memory)+ sing-box bin(rename 到 backup);
//  2. 提交 staging:rename bin/sing-box.new → bin/sing-box;调 PreprocessZoo 重写
//     config.d/zoo.json + rule-set.json;
//  3. 真变化闸门:对 zoo.json / rule-set.json / sing-box 算 sha256,全部与备份一致
//     → 视为 etag 假信号,**不重启**,更新 apply-state 后返回 nil;
//  4. CheckConfig:用新二进制验整套 config.d;失败 → revert 全部备份 + warn + 返 nil;
//  5. Restart:失败 → revert 全部备份 + RecoverFromFailedApply + 返 error。
//
// binStagingPath != "" 表示 sing-box.new 存在需要被安装;为空表示仅 zooChanged。
func (a *Applier) ApplySingBoxOrZoo(ctx context.Context, zooChanged bool, binStagingPath string) error {
	binChanged := binStagingPath != ""
	if !zooChanged && !binChanged {
		return nil
	}

	zooPath := filepath.Join(a.Rundir, a.ConfigDir, zooFileName)
	rulePath := filepath.Join(a.Rundir, a.ConfigDir, ruleSetFileName)
	binPath := filepath.Join(a.Rundir, singBoxRelPath)
	backupBinPath := filepath.Join(a.Rundir, applyBackupSubdir, "sing-box")

	// --- Step 1: backup (zoo/rule-set in memory; bin via rename to apply-backup/) ---
	var (
		zooBackup, ruleBackup           []byte
		zooExisted, ruleExisted         bool
		binBackedUp                     bool
		zooHashBefore, ruleHashBefore   string
		binHashBefore                   string
	)

	if zooChanged {
		var err error
		zooBackup, zooExisted, err = readIfExists(zooPath)
		if err != nil {
			return fmt.Errorf("backup zoo.json: %w", err)
		}
		ruleBackup, ruleExisted, err = readIfExists(rulePath)
		if err != nil {
			return fmt.Errorf("backup rule-set.json: %w", err)
		}
		zooHashBefore = sha256Bytes(zooBackup)
		ruleHashBefore = sha256Bytes(ruleBackup)
	}
	if binChanged {
		if err := os.MkdirAll(filepath.Dir(backupBinPath), 0o755); err != nil {
			return fmt.Errorf("mkdir apply-backup: %w", err)
		}
		h, err := fileSHA256(binPath)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("hash current sing-box: %w", err)
		}
		binHashBefore = h
		// rename existing bin → backup so that staging rename can succeed atomically.
		if _, statErr := os.Stat(binPath); statErr == nil {
			if err := os.Rename(binPath, backupBinPath); err != nil {
				return fmt.Errorf("backup sing-box: %w", err)
			}
			binBackedUp = true
		}
	}

	// --- Step 2: commit staging artifacts (binary first, then zoo/rule-set) ---
	if binChanged {
		if err := os.Rename(binStagingPath, binPath); err != nil {
			// Try to restore the backup so the daemon remains usable.
			if binBackedUp {
				_ = os.Rename(backupBinPath, binPath)
			}
			return fmt.Errorf("install sing-box: %w", err)
		}
	}
	if zooChanged && a.PreprocessZoo != nil {
		if err := a.PreprocessZoo(); err != nil {
			// Preprocess failed: revert any partial state.
			a.revert(zooChanged, binChanged, zooPath, rulePath, binPath, backupBinPath,
				zooBackup, zooExisted, ruleBackup, ruleExisted, binBackedUp)
			a.Emitter.Warn("apply", "apply.preprocess.failed", "zoo preprocess: {Err}",
				map[string]any{"Err": err.Error()})
			return nil
		}
	}

	// --- Step 3: real-change gate ---
	zooHashAfter, ruleHashAfter, binHashAfter, err := hashCurrent(zooPath, rulePath, binPath)
	if err != nil {
		a.revert(zooChanged, binChanged, zooPath, rulePath, binPath, backupBinPath,
			zooBackup, zooExisted, ruleBackup, ruleExisted, binBackedUp)
		return fmt.Errorf("hash artifacts: %w", err)
	}
	zooDelta := zooChanged && zooHashAfter != zooHashBefore
	ruleDelta := zooChanged && ruleHashAfter != ruleHashBefore
	binDelta := binChanged && binHashAfter != binHashBefore
	if !zooDelta && !ruleDelta && !binDelta {
		// 没有任何产物实际变化 → 直接清掉 backup,记录哈希,不重启。
		a.cleanupBackup(backupBinPath, binBackedUp)
		_ = a.updateApplyState(func(s *applyState) {
			s.SingBox = binHashAfter
			s.ZooJSON = zooHashAfter
			s.RuleSetJSON = ruleHashAfter
		})
		a.Emitter.Info("apply", "apply.noop",
			"resources downloaded but content unchanged; no restart",
			map[string]any{"ZooChanged": zooChanged, "BinChanged": binChanged})
		return nil
	}

	// --- Step 4: CheckConfig ---
	if a.CheckConfig != nil {
		if err := a.CheckConfig(ctx); err != nil {
			a.revert(zooChanged, binChanged, zooPath, rulePath, binPath, backupBinPath,
				zooBackup, zooExisted, ruleBackup, ruleExisted, binBackedUp)
			a.Emitter.Warn("apply", "apply.check.failed",
				"sing-box check failed; reverted: {Err}",
				map[string]any{"Err": err.Error()})
			return nil
		}
	}

	// --- Step 5: Restart sing-box ---
	if err := a.Restart(ctx); err != nil {
		a.revert(zooChanged, binChanged, zooPath, rulePath, binPath, backupBinPath,
			zooBackup, zooExisted, ruleBackup, ruleExisted, binBackedUp)
		// 走 RecoverFromFailedApply 把 sing-box 按 revert 后的旧配置拉回来。
		if rerr := a.Recover(ctx); rerr != nil {
			a.Emitter.Error("apply", "apply.recover.failed",
				"restart failed AND recover failed: restartErr={RestartErr} recoverErr={RecoverErr}",
				map[string]any{"RestartErr": err.Error(), "RecoverErr": rerr.Error()})
		} else {
			a.Emitter.Error("apply", "apply.restart.failed",
				"restart failed; reverted and recovered with previous config: {Err}",
				map[string]any{"Err": err.Error()})
		}
		return err
	}

	// --- Step 6: success cleanup ---
	a.cleanupBackup(backupBinPath, binBackedUp)
	_ = a.updateApplyState(func(s *applyState) {
		s.SingBox = binHashAfter
		s.ZooJSON = zooHashAfter
		s.RuleSetJSON = ruleHashAfter
	})
	a.Emitter.Info("apply", "apply.ok",
		"sing-box restarted with new resources (zoo={ZooChanged} bin={BinChanged})",
		map[string]any{"ZooChanged": zooDelta || ruleDelta, "BinChanged": binDelta})
	return nil
}

// ApplyPending 检查当前磁盘状态(sing-box.new 是否存在 + zoo/cn 是否变化)并执行
// 对应 apply。供 HTTP `/api/v1/apply` 端点使用:CLI `update --apply` 走这条路径,
// 让 daemon 端按 sync_loop 同样的语义把已落盘的资源真正生效。
func (a *Applier) ApplyPending(ctx context.Context) error {
	stagingPath := filepath.Join(a.Rundir, singBoxRelPath+".new")
	binStaging := ""
	if _, err := os.Stat(stagingPath); err == nil {
		binStaging = stagingPath
	}
	// zooChanged=true 让 Applier 跑 Preprocess+Ensure,内部 sha256 闸门会挡住无变化。
	if err := a.ApplySingBoxOrZoo(ctx, true, binStaging); err != nil {
		return err
	}
	return a.ApplyCNList(ctx)
}

// ApplyCNList 在 cn.txt 实际变化时调 ReloadCNIpset 重建 ipset。
// "实际变化" 用 sha256 与 apply-state.cn_txt 比对决定;一致则 no-op。
func (a *Applier) ApplyCNList(ctx context.Context) error {
	cnPath := filepath.Join(a.Rundir, "var", "cn.txt")
	hash, err := fileSHA256(cnPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("hash cn.txt: %w", err)
	}
	st, _ := a.loadApplyState()
	if st.CNTxt == hash {
		a.Emitter.Info("apply", "apply.cn_ipset.noop",
			"cn.txt downloaded but sha256 unchanged; ipset reload skipped", nil)
		return nil
	}
	if a.ReloadCNIpset == nil {
		return errors.New("ReloadCNIpset hook not wired")
	}
	if err := a.ReloadCNIpset(ctx); err != nil {
		a.Emitter.Warn("apply", "apply.cn_ipset.failed",
			"reload cn ipset: {Err}", map[string]any{"Err": err.Error()})
		return err
	}
	_ = a.updateApplyState(func(s *applyState) { s.CNTxt = hash })
	a.Emitter.Info("apply", "apply.cn_ipset.ok", "cn ipset reloaded", nil)
	return nil
}

// revert 把所有备份回滚到原位。zoo/rule-set 从 in-memory 字节回写;sing-box 从
// apply-backup/ rename 回去。如果某项 originally 不存在(zooExisted=false 等),
// revert 时删除当前文件即可。
func (a *Applier) revert(
	zooChanged, binChanged bool,
	zooPath, rulePath, binPath, backupBinPath string,
	zooBackup []byte, zooExisted bool,
	ruleBackup []byte, ruleExisted bool,
	binBackedUp bool,
) {
	if zooChanged {
		if zooExisted {
			_ = atomicWriteFile(zooPath, zooBackup, 0o644)
		} else {
			_ = os.Remove(zooPath)
		}
		if ruleExisted {
			_ = atomicWriteFile(rulePath, ruleBackup, 0o644)
		} else {
			_ = os.Remove(rulePath)
		}
	}
	if binChanged && binBackedUp {
		_ = os.Remove(binPath) // 可能是失败前已 commit 的新版,删掉
		_ = os.Rename(backupBinPath, binPath)
	}
}

func (a *Applier) cleanupBackup(backupBinPath string, binBackedUp bool) {
	if binBackedUp {
		_ = os.Remove(backupBinPath)
	}
}

// loadApplyState 从 var/apply-state.json 读;不存在返回零值。
func (a *Applier) loadApplyState() (applyState, error) {
	var s applyState
	data, err := os.ReadFile(filepath.Join(a.Rundir, applyStateFile))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return s, nil
		}
		return s, err
	}
	if err := json.Unmarshal(data, &s); err != nil {
		return applyState{}, err
	}
	return s, nil
}

// updateApplyState 读 → mutate → 原子写回。mutate 收到的是当前快照的副本。
func (a *Applier) updateApplyState(mutate func(*applyState)) error {
	s, _ := a.loadApplyState()
	mutate(&s)
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	path := filepath.Join(a.Rundir, applyStateFile)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return atomicWriteFile(path, data, 0o644)
}

// --- helpers ---

func readIfExists(path string) (data []byte, existed bool, err error) {
	data, err = os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, false, nil
		}
		return nil, false, err
	}
	return data, true, nil
}

func atomicWriteFile(path string, data []byte, mode os.FileMode) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, mode); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

func sha256Bytes(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

func fileSHA256(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer func() { _ = f.Close() }()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// hashCurrent 同时算三个产物的 sha256;不存在的视为空串。
func hashCurrent(zooPath, rulePath, binPath string) (zoo, rule, bin string, err error) {
	hf := func(p string) (string, error) {
		h, err := fileSHA256(p)
		if errors.Is(err, os.ErrNotExist) {
			return "", nil
		}
		return h, err
	}
	if zoo, err = hf(zooPath); err != nil {
		return
	}
	if rule, err = hf(rulePath); err != nil {
		return
	}
	if bin, err = hf(binPath); err != nil {
		return
	}
	return
}
