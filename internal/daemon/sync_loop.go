package daemon

import (
	"context"
	"slices"
	"time"

	"github.com/moonfruit/sing2seq/clef"

	syncpkg "github.com/moonfruit/sing-router/internal/sync"
	"github.com/moonfruit/sing-router/internal/zashboard"
)

// SyncLoopConfig 控制后台资源同步行为。intervalSec=0 时 StartSyncLoop 不启动 goroutine。
type SyncLoopConfig struct {
	IntervalSec     int
	OnStartDelaySec int
	AutoApply       bool // true:拉到新资源后自动 apply(zoo/sing-box → restart;cn.txt → ipset reload);false:仅 log
	// ZashboardUIDir 非空时,每轮 sync 末尾本地生成 <UIDir>/zashboard-settings.json
	// (独立步骤,不进 Applier)。sing-box 更新 external UI 时会清空该目录,靠这里每轮重写自愈。
	ZashboardUIDir        string
	ZashboardStaticLabels map[string]string
}

// StartSyncLoop 在后台周期性调用 updater.UpdateAll(ctx)。
//
// 行为：
//   - intervalSec <= 0：不启动 goroutine，直接返回。
//   - 启动后等 onStartDelaySec 才跑首次同步（避免 daemon 启动洪峰与 sing-box 抢资源）。
//   - 每次同步结果通过 emitter 写入日志；同步失败仅记录，不影响 daemon 主流程。
//   - AutoApply=true 且 applier!=nil 时,有变化的资源会被自动 apply(见 runSyncOnce)。
//   - ctx 取消时 goroutine 优雅退出。
func StartSyncLoop(ctx context.Context, updater *syncpkg.Updater, cfg SyncLoopConfig, em *clef.Emitter, applier *Applier) {
	if cfg.IntervalSec <= 0 {
		em.Info("sync", "sync.loop.disabled", "background sync disabled (interval_seconds=0)", nil)
		return
	}
	go func() {
		defer func() {
			if r := recover(); r != nil {
				reportPanic("sync.loop", r)
				em.Fatal("recover", "panic.recovered",
					"panic in {Name}: see sing-router.err for stack",
					map[string]any{"Name": "sync.loop"})
			}
		}()
		em.Info("sync", "sync.loop.started",
			"background sync starting in {DelaySec}s; interval={IntervalSec}s; auto_apply={AutoApply}",
			map[string]any{"DelaySec": cfg.OnStartDelaySec, "IntervalSec": cfg.IntervalSec, "AutoApply": cfg.AutoApply})
		if cfg.OnStartDelaySec > 0 {
			select {
			case <-ctx.Done():
				return
			case <-time.After(time.Duration(cfg.OnStartDelaySec) * time.Second):
			}
		}
		runSyncOnce(ctx, updater, em, applier, cfg)
		ticker := time.NewTicker(time.Duration(cfg.IntervalSec) * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				em.Info("sync", "sync.loop.stopped", "background sync stopped", nil)
				return
			case <-ticker.C:
				runSyncOnce(ctx, updater, em, applier, cfg)
			}
		}
	}()
}

func runSyncOnce(ctx context.Context, u *syncpkg.Updater, em *clef.Emitter, applier *Applier, cfg SyncLoopConfig) {
	defer generateZashboard(ctx, em, cfg)
	autoApply := cfg.AutoApply
	r := u.UpdateAll(ctx)
	logItem(em, "sing-box", r.SingBox.Changed, r.SingBox.Version, r.SingBox.Err)
	logItem(em, "cn.txt", r.CNList.Changed, "", r.CNList.Err)
	logItem(em, "zoo.json", r.Zoo.Changed, "", r.Zoo.Err)

	if !autoApply || applier == nil {
		// auto_apply 关闭:把 sing-box staging 顺手 commit 到 bin/sing-box,
		// 行为与改造前的 UpdateAll 保持一致(仅缺重启 — 与历史行为相同)。
		if r.SingBox.Err == nil && r.SingBox.Changed {
			if err := u.CommitSingBoxStaging(); err != nil {
				em.Warn("sync", "sync.commit.failed",
					"sing-box staging commit failed: {Err}", map[string]any{"Err": err.Error()})
			}
		}
		return
	}

	// auto_apply 开启:本轮 Changed 的资源 + 此前未能生效的资源（见 planApply）
	// 合并成一次 Apply，最多一次 Restart。
	st, err := applier.loadApplyState()
	if err != nil {
		em.Warn("apply", "apply.state.unreadable", "read apply-state: {Err}", map[string]any{"Err": err.Error()})
	}
	p := planApply(r, st, applier.currentInputs(), time.Now())
	if p.skip() {
		em.Info("apply", "apply.retry.cooldown",
			"skip apply {Kinds}: same inputs failed at {FailedAt}; retry after {RetryAt}",
			map[string]any{"Kinds": p.Kinds, "FailedAt": st.LastFailure.At, "RetryAt": p.CoolUntil})
		return
	}
	kinds := p.Kinds
	if slices.Contains(p.Retry, ResourceSingBox) {
		// 上次失败时 staging 已被回滚吃掉，且 etag 不变时 updater 不会再解压：从落盘的压缩包重新暂存。
		staged, err := u.StageSingBoxFromTarball()
		switch {
		case err != nil:
			em.Warn("apply", "apply.retry.stage_failed", "re-stage sing-box from tarball: {Err}",
				map[string]any{"Err": err.Error()})
			kinds = slices.DeleteFunc(kinds, func(k Resource) bool { return k == ResourceSingBox })
		case !staged:
			// 压缩包里的二进制其实已经装上了（如 CLI update 直接安装），补记即可。
			tarball := applier.currentInputs().Tarball
			_ = applier.updateApplyState(func(s *applyState) { s.SingBoxTarball = tarball })
			kinds = slices.DeleteFunc(kinds, func(k Resource) bool { return k == ResourceSingBox })
		}
	}
	if len(kinds) == 0 {
		return
	}
	if len(p.Retry) > 0 {
		em.Info("apply", "apply.retry", "retrying resources not yet applied: {Kinds}",
			map[string]any{"Kinds": p.Retry})
	}
	if err := applier.Apply(ctx, kinds); err != nil {
		em.Warn("apply", "apply.failed",
			"apply resources {Kinds}: {Err}",
			map[string]any{"Kinds": kinds, "Err": err.Error()})
	}
}

// applyRetryCooldown 是同一组输入 apply 失败后再次重试前的冷却时长。
const applyRetryCooldown = 24 * time.Hour

// applyPlan 是一轮 sync 后的 apply 决策。
type applyPlan struct {
	Kinds     []Resource // 本轮要交给 Applier 的资源（含 Retry）
	Retry     []Resource // 其中 etag 未变、但此前未能生效而重试的资源
	CoolUntil time.Time  // 非零 = 同样的输入刚失败过，本轮跳过
}

func (p applyPlan) skip() bool { return !p.CoolUntil.IsZero() }

// planApply 决定本轮 apply 哪些资源。只看 Changed（etag 变化）是不够的：一次 apply
// 失败回滚后，上游 etag 不再变，该资源就永远不会被重试（2026-10 路由器 sing-box
// 因此停在旧版两个月）。所以以"落盘输入 ≠ 已生效输入"为准，下载出错的资源除外。
//
// 为免坏版本每轮都重启-失败-回滚一遍：若当前输入指纹与上次失败相同，且失败不足
// applyRetryCooldown，本轮跳过；输入或 sing-router 版本一变，指纹即不同，立即重试。
func planApply(r syncpkg.AllResult, st applyState, in applyInputs, now time.Time) applyPlan {
	var p applyPlan
	consider := func(k Resource, item syncpkg.ItemResult, current, applied string) {
		switch {
		case item.Err != nil:
		case item.Changed:
			p.Kinds = append(p.Kinds, k)
		case current != "" && current != applied:
			p.Kinds = append(p.Kinds, k)
			p.Retry = append(p.Retry, k)
		}
	}
	consider(ResourceSingBox, r.SingBox, in.Tarball, st.SingBoxTarball)
	consider(ResourceZoo, r.Zoo, in.ZooRaw, st.ZooRaw)
	consider(ResourceCN, r.CNList, in.CN, st.CNTxt)
	if len(p.Kinds) == 0 {
		return p
	}
	if f := st.LastFailure; f != nil && f.Fingerprint == in.fingerprint() {
		if until := f.At.Add(applyRetryCooldown); now.Before(until) {
			return applyPlan{Kinds: p.Kinds, CoolUntil: until}
		}
	}
	return p
}

// generateZashboard 本地生成 ui/zashboard-settings.json(source-ip-label-list)。
// 独立于资源 apply:不进 Applier、不触发 restart。
// ui_dir 不存在、或 external UI 尚未装好时静默跳过(理由见 zashboard.Generate)。
func generateZashboard(ctx context.Context, em *clef.Emitter, cfg SyncLoopConfig) {
	if cfg.ZashboardUIDir == "" {
		return
	}
	res, err := zashboard.Generate(ctx, cfg.ZashboardUIDir, cfg.ZashboardStaticLabels)
	for _, w := range res.Warnings {
		em.Debug("zashboard", "zashboard.collect.degraded", "{Warn}", map[string]any{"Warn": w})
	}
	switch {
	case err != nil:
		em.Warn("zashboard", "zashboard.generate.failed", "zashboard generate failed: {Err}",
			map[string]any{"Err": err.Error()})
	case res.Skipped:
		em.Debug("zashboard", "zashboard.generate.skipped",
			"zashboard generation skipped: {Reason}", map[string]any{"Reason": string(res.SkipReason)})
	case res.Changed:
		em.Info("zashboard", "zashboard.generate.updated", "{File} updated ({Count} entries)",
			map[string]any{"File": zashboard.SettingsFileName, "Count": res.Count})
	default:
		em.Debug("zashboard", "zashboard.generate.unchanged", "{File} unchanged",
			map[string]any{"File": zashboard.SettingsFileName})
	}
}

func logItem(em *clef.Emitter, name string, changed bool, version string, err error) {
	if err != nil {
		em.Warn("sync", "sync.item.failed", "{Name} update failed: {Err}",
			map[string]any{"Name": name, "Err": err.Error()})
		return
	}
	if !changed {
		em.Debug("sync", "sync.item.unchanged", "{Name} unchanged", map[string]any{"Name": name})
		return
	}
	if version != "" {
		em.Info("sync", "sync.item.updated", "{Name} updated to {Version}",
			map[string]any{"Name": name, "Version": version})
	} else {
		em.Info("sync", "sync.item.updated", "{Name} updated", map[string]any{"Name": name})
	}
}
