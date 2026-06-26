package daemon

import (
	"context"
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
	// ZashboardUIDir 非空时,每轮 sync 末尾本地生成 <UIDir>/zashboard.json(独立步骤,不进 Applier)。
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

	// auto_apply 开启:只把本轮真正成功且 Changed 的资源交给 Apply 走 4 阶段，
	// 合并到一次 Restart。避免无关资源（如旧的损坏 zoo.raw.json）阻塞本轮真变化
	// 的资源 apply——若总是 ApplyAll，zoo preprocess 失败会让 cn.txt / sing-box
	// 的 commit + restart 也被跳过。
	var kinds []Resource
	if r.SingBox.Err == nil && r.SingBox.Changed {
		kinds = append(kinds, ResourceSingBox)
	}
	if r.Zoo.Err == nil && r.Zoo.Changed {
		kinds = append(kinds, ResourceZoo)
	}
	if r.CNList.Err == nil && r.CNList.Changed {
		kinds = append(kinds, ResourceCN)
	}
	if len(kinds) == 0 {
		return
	}
	if err := applier.Apply(ctx, kinds); err != nil {
		em.Warn("apply", "apply.failed",
			"apply resources {Kinds}: {Err}",
			map[string]any{"Kinds": kinds, "Err": err.Error()})
	}
}

// generateZashboard 本地生成 ui/zashboard.json(source-ip-label-list)。
// 独立于资源 apply:不进 Applier、不触发 restart。ui_dir 不存在则静默跳过。
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
		em.Debug("zashboard", "zashboard.generate.skipped", "ui_dir absent; zashboard generation skipped", nil)
	case res.Changed:
		em.Info("zashboard", "zashboard.generate.updated", "zashboard.json updated ({Count} entries)",
			map[string]any{"Count": res.Count})
	default:
		em.Debug("zashboard", "zashboard.generate.unchanged", "zashboard.json unchanged", nil)
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
