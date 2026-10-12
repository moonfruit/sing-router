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
	"slices"
	"time"

	"github.com/moonfruit/sing2seq/clef"

	"github.com/moonfruit/sing-router/internal/version"
)

// Resource 是 Applier 能识别的资源种类。
type Resource int

const (
	ResourceSingBox Resource = iota
	ResourceZoo
	ResourceCN
)

// AllResources 是 Applier 默认处理的全部资源（按确定顺序）。
var AllResources = []Resource{ResourceSingBox, ResourceZoo, ResourceCN}

// String 返回对外暴露给 CLI/HTTP 的资源名（与 `update [kind]` 子命令对齐）。
func (r Resource) String() string {
	switch r {
	case ResourceSingBox:
		return "sing-box"
	case ResourceZoo:
		return "zoo"
	case ResourceCN:
		return "cn"
	default:
		return "unknown"
	}
}

// ParseResource 把 CLI/HTTP 传入的资源名解析成 Resource。空串 / "all" → AllResources。
func ParseResource(s string) ([]Resource, error) {
	switch s {
	case "", "all":
		return AllResources, nil
	case "sing-box":
		return []Resource{ResourceSingBox}, nil
	case "zoo":
		return []Resource{ResourceZoo}, nil
	case "cn":
		return []Resource{ResourceCN}, nil
	default:
		return nil, fmt.Errorf("unknown resource %q (want sing-box | zoo | cn | all)", s)
	}
}

// Applier 把 sync.Updater 拉到的资源真正应用到运行中的 sing-box。
//
// 4 阶段统一流程（详见 Apply）：
//
//   - stage：识别每个资源是否有新内容（sing-box.new 存在 / zoo.raw.json 触发预处理 /
//     cn.txt sha256 与 apply-state.json 不同）。
//   - validate：对 sing-box+zoo 跑 CheckConfig（cn.txt 不需要 check，startup.sh 直接读）。
//   - commit：rename staging → 目标路径；备份保留在 var/apply-backup/。
//   - restart：任何资源真变化 → sup.Restart；失败 → revert 全部 + sup.RestartForce。
//
// sha256 闸门保证「上游 etag 假信号」不会触发 Restart——内容真变化才动手。
type Applier struct {
	Rundir    string
	ConfigDir string // 相对 rundir，例如 "config"

	Emitter *clef.Emitter

	// Restart 把 sing-box 重新拉起来。**必须装绕节流的回调**（wireup 装 sup.RestartForce）：
	// Applier 自己有 sha256 闸门保证只在真有变化时进入第 4 阶段，如果再被外层 2s 节流挡掉
	// 返回 ErrRestartThrottled，已 commit 的新资源会跟运行中的旧 sing-box 失步，且
	// apply-state 会写入新 hash → 下次 sync 永远不会重试（彻底失效）。
	Restart func(ctx context.Context) error

	// CheckConfig 用新二进制 + 新 config 跑 `sing-box check`。
	CheckConfig func(ctx context.Context) error

	// PreprocessZoo 重新跑 PreprocessZooFile + EnsureRequiredRuleSets，把
	// var/zoo.raw.json 翻译成 config/zoo.json 与 config/rule-set.json。
	// 通常在 wireup 时绑定 rawURL/ref/required 列表（避免 daemon 包反向依赖 config 包）。
	PreprocessZoo func() error
}

// applyState 持久化到 var/apply-state.json，记录上次成功 apply 的各资源 sha256；
// daemon 重启后读回，允许在下次 sync 时识别"上游 etag 变化但内容未变"的假信号。
//
// SingBoxTarball / ZooRaw 记录"已生效"的输入（下载落盘的原件）sha256。sync_loop
// 拿它与当前落盘的输入比对：不一致即"待生效"，哪怕本轮 etag 没变也要重新 apply——
// 否则一次失败回滚后，etag 不再变化，该资源就永远停在旧版本（cn.txt 的 CNTxt
// 本就是输入 sha，同样参与比对）。
type applyState struct {
	SingBox        string        `json:"sing_box,omitempty"`
	ZooJSON        string        `json:"zoo_json,omitempty"`
	RuleSetJSON    string        `json:"rule_set_json,omitempty"`
	CNTxt          string        `json:"cn_txt,omitempty"`
	SingBoxTarball string        `json:"sing_box_tarball,omitempty"`
	ZooRaw         string        `json:"zoo_raw,omitempty"`
	LastFailure    *applyFailure `json:"last_failure,omitempty"`
}

// applyFailure 记录最近一次 apply 失败时的输入指纹。sync_loop 据此冷却：同样的
// 输入（含 sing-router 版本）刚失败过就不立刻重试，免得每轮都重启-失败-回滚一遍。
type applyFailure struct {
	Fingerprint string    `json:"fingerprint"`
	At          time.Time `json:"at"`
}

// applyInputs 是一轮 apply 的输入原件 sha256；文件不存在为空串。
type applyInputs struct {
	Tarball string // var/sing-box.tar.gz
	ZooRaw  string // var/zoo.raw.json
	CN      string // var/cn.txt
}

func (in applyInputs) fingerprint() string { return in.fingerprintWith(version.String()) }

// fingerprintWith 把 sing-router 版本也算进指纹：修 bug 的新版本上线后，同样的
// 输入可能就能成功了，应当立即重试而不是等冷却。
func (in applyInputs) fingerprintWith(ver string) string {
	return sha256Bytes([]byte(in.Tarball + "\n" + in.ZooRaw + "\n" + in.CN + "\n" + ver))
}

// currentInputs 读取当前落盘的输入原件 sha256。读失败按不存在处理（空串）。
func (a *Applier) currentInputs() applyInputs {
	h := func(rel string) string {
		s, _ := fileSHA256(filepath.Join(a.Rundir, rel))
		return s
	}
	return applyInputs{Tarball: h(tarballRelPath), ZooRaw: h(zooRawRelPath), CN: h(cnRelPath)}
}

const (
	applyStateFile    = "var/apply-state.json"
	applyBackupSubdir = "var/apply-backup"
	zooFileName       = "zoo.json"
	ruleSetFileName   = "rule-set.json"
	singBoxRelPath    = "bin/sing-box"
	tarballRelPath    = "var/sing-box.tar.gz"
	zooRawRelPath     = "var/zoo.raw.json"
	cnRelPath         = "var/cn.txt"
)

// ApplyAll 是 sync_loop 用的默认入口：处理 sing-box / zoo / cn.txt 三种资源。
func (a *Applier) ApplyAll(ctx context.Context) error {
	return a.Apply(ctx, AllResources)
}

// Apply 按 kinds 限定的范围跑 4 阶段流程，一轮最多调 1 次 sup.Restart——
// 避免「sing-box+zoo+cn 同时变化时各自调一次 Restart，第二次被 throttle 丢动作」。
//
// kinds 为空时 = 全部资源（等价 AllResources）。单一资源（如 ["sing-box"]）
// 时退化为「只处理该资源」，CheckConfig / Restart / Revert 全部仅作用于它。
func (a *Applier) Apply(ctx context.Context, kinds []Resource) error {
	if len(kinds) == 0 {
		kinds = AllResources
	}
	wantBin := slices.Contains(kinds, ResourceSingBox)
	wantZoo := slices.Contains(kinds, ResourceZoo)
	wantCN := slices.Contains(kinds, ResourceCN)

	zooPath := filepath.Join(a.Rundir, a.ConfigDir, zooFileName)
	rulePath := filepath.Join(a.Rundir, a.ConfigDir, ruleSetFileName)
	binPath := filepath.Join(a.Rundir, singBoxRelPath)
	binStaging := filepath.Join(a.Rundir, singBoxRelPath+".new")
	backupBinPath := filepath.Join(a.Rundir, applyBackupSubdir, "sing-box")
	cnPath := filepath.Join(a.Rundir, cnRelPath)

	// 任何未走到成功出口的返回都记一次失败指纹，供 sync_loop 冷却重试。
	inputs := a.currentInputs()
	succeeded := false
	defer func() {
		if !succeeded {
			_ = a.updateApplyState(func(s *applyState) {
				s.LastFailure = &applyFailure{Fingerprint: inputs.fingerprint(), At: time.Now()}
			})
		}
	}()

	// === 1) sing-box：staging 存在即认为待安装 ===
	var (
		binChanged    bool
		binBackedUp   bool
		binHashBefore string
		binHashAfter  string
	)
	if wantBin {
		if _, err := os.Stat(binStaging); err == nil {
			binChanged = true
			if err := os.MkdirAll(filepath.Dir(backupBinPath), 0o755); err != nil {
				return fmt.Errorf("mkdir apply-backup: %w", err)
			}
			h, err := fileSHA256(binPath)
			if err != nil && !errors.Is(err, os.ErrNotExist) {
				return fmt.Errorf("hash current sing-box: %w", err)
			}
			binHashBefore = h
			if _, statErr := os.Stat(binPath); statErr == nil {
				if err := os.Rename(binPath, backupBinPath); err != nil {
					return fmt.Errorf("backup sing-box: %w", err)
				}
				binBackedUp = true
			}
		}
	}

	// === 2) zoo：in-memory backup → 调 PreprocessZoo 直接重写 zoo.json/rule-set.json ===
	var (
		zooBackup, ruleBackup         []byte
		zooExisted, ruleExisted       bool
		zooHashBefore, ruleHashBefore string
		zooHashAfter, ruleHashAfter   string
	)
	if wantZoo {
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

	// commit sing-box（rename staging → 正式路径）
	if binChanged {
		if err := os.Rename(binStaging, binPath); err != nil {
			if binBackedUp {
				_ = os.Rename(backupBinPath, binPath)
			}
			return fmt.Errorf("install sing-box: %w", err)
		}
	}
	// commit zoo（PreprocessZoo 直接重写）
	if wantZoo && a.PreprocessZoo != nil {
		if err := a.PreprocessZoo(); err != nil {
			a.revert(wantZoo, binChanged, zooPath, rulePath, binPath, backupBinPath,
				zooBackup, zooExisted, ruleBackup, ruleExisted, binBackedUp)
			a.emitWarn("apply.preprocess.failed", "zoo preprocess: {Err}",
				map[string]any{"Err": err.Error()})
			return nil
		}
	}

	// === 3) 真变化闸门（sing-box / zoo） ===
	zooDelta := false
	ruleDelta := false
	binDelta := false
	if wantZoo || binChanged {
		var err error
		zooHashAfter, ruleHashAfter, binHashAfter, err = hashCurrent(zooPath, rulePath, binPath)
		if err != nil {
			a.revert(wantZoo, binChanged, zooPath, rulePath, binPath, backupBinPath,
				zooBackup, zooExisted, ruleBackup, ruleExisted, binBackedUp)
			return fmt.Errorf("hash artifacts: %w", err)
		}
		zooDelta = wantZoo && zooHashAfter != zooHashBefore
		ruleDelta = wantZoo && ruleHashAfter != ruleHashBefore
		binDelta = binChanged && binHashAfter != binHashBefore
	}

	// === 4) cn.txt：仅判断 sha256 是否变化（不需要 staging；startup.sh 重启时直接读） ===
	var (
		cnDelta      bool
		cnHashAfter  string
	)
	if wantCN {
		h, err := fileSHA256(cnPath)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			a.revert(wantZoo, binChanged, zooPath, rulePath, binPath, backupBinPath,
				zooBackup, zooExisted, ruleBackup, ruleExisted, binBackedUp)
			return fmt.Errorf("hash cn.txt: %w", err)
		}
		if h != "" {
			st, _ := a.loadApplyState()
			if h != st.CNTxt {
				cnDelta = true
				cnHashAfter = h
			}
		}
	}

	// 成功出口共用：记下本轮已生效的输入原件，清掉失败记录。
	markApplied := func(s *applyState) {
		if binChanged {
			s.SingBoxTarball = inputs.Tarball
		}
		if wantZoo {
			s.ZooRaw = inputs.ZooRaw
		}
		s.LastFailure = nil
	}

	// === 5) 任一资源真变化 → CheckConfig + Restart；否则 no-op return ===
	if !zooDelta && !ruleDelta && !binDelta && !cnDelta {
		a.cleanupBackup(backupBinPath, binBackedUp)
		succeeded = true
		// hash 没变也要更新 apply-state（避免下次 sync 因为快照空白反复尝试 commit）
		_ = a.updateApplyState(func(s *applyState) {
			markApplied(s)
			if wantZoo {
				s.ZooJSON = zooHashAfter
				s.RuleSetJSON = ruleHashAfter
			}
			if binChanged {
				s.SingBox = binHashAfter
			}
		})
		a.emitInfo("apply.noop",
			"resources sha256 unchanged; no restart",
			map[string]any{"WantBin": wantBin, "WantZoo": wantZoo, "WantCN": wantCN})
		return nil
	}

	// CheckConfig 仅当 sing-box / zoo 有变化时需要
	if (zooDelta || ruleDelta || binDelta) && a.CheckConfig != nil {
		if err := a.CheckConfig(ctx); err != nil {
			a.revert(wantZoo, binChanged, zooPath, rulePath, binPath, backupBinPath,
				zooBackup, zooExisted, ruleBackup, ruleExisted, binBackedUp)
			a.emitWarn("apply.check.failed",
				"sing-box check failed; reverted: {Err}",
				map[string]any{"Err": err.Error()})
			return nil
		}
	}

	// Restart（Restart 字段必须装绕节流的回调，见字段注释；下面同理）
	if err := a.Restart(ctx); err != nil {
		a.revert(wantZoo, binChanged, zooPath, rulePath, binPath, backupBinPath,
			zooBackup, zooExisted, ruleBackup, ruleExisted, binBackedUp)
		// revert 后再调一次 Restart 把 sing-box 按旧配置拉回来。
		if rerr := a.Restart(ctx); rerr != nil {
			a.emitError("apply.recover.failed",
				"restart failed AND recover-restart failed: restartErr={RestartErr} recoverErr={RecoverErr}",
				map[string]any{"RestartErr": err.Error(), "RecoverErr": rerr.Error()})
		} else {
			a.emitError("apply.restart.failed",
				"restart failed; reverted and recovered with previous config: {Err}",
				map[string]any{"Err": err.Error()})
		}
		return err
	}

	// success
	a.cleanupBackup(backupBinPath, binBackedUp)
	succeeded = true
	_ = a.updateApplyState(func(s *applyState) {
		markApplied(s)
		if binChanged {
			s.SingBox = binHashAfter
		}
		if wantZoo {
			s.ZooJSON = zooHashAfter
			s.RuleSetJSON = ruleHashAfter
		}
		if cnDelta {
			s.CNTxt = cnHashAfter
		}
	})
	a.emitInfo("apply.ok",
		"sing-box restarted with new resources: zoo={Zoo} rule={Rule} bin={Bin} cn={CN}",
		map[string]any{"Zoo": zooDelta, "Rule": ruleDelta, "Bin": binDelta, "CN": cnDelta})
	return nil
}

// revert 把所有备份回滚到原位。zoo/rule-set 从 in-memory 字节回写；sing-box 从
// apply-backup/ rename 回去。如果某项 originally 不存在（zooExisted=false 等），
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
		_ = os.Remove(binPath) // 可能是失败前已 commit 的新版，删掉
		_ = os.Rename(backupBinPath, binPath)
	}
}

func (a *Applier) cleanupBackup(backupBinPath string, binBackedUp bool) {
	if binBackedUp {
		_ = os.Remove(backupBinPath)
	}
}

// loadApplyState 从 var/apply-state.json 读；不存在返回零值。
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

func (a *Applier) emitInfo(code, msg string, fields map[string]any) {
	if a.Emitter == nil {
		return
	}
	a.Emitter.Info("apply", code, msg, fields)
}

func (a *Applier) emitWarn(code, msg string, fields map[string]any) {
	if a.Emitter == nil {
		return
	}
	a.Emitter.Warn("apply", code, msg, fields)
}

func (a *Applier) emitError(code, msg string, fields map[string]any) {
	if a.Emitter == nil {
		return
	}
	a.Emitter.Error("apply", code, msg, fields)
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

// hashCurrent 同时算三个产物的 sha256；不存在的视为空串。
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
