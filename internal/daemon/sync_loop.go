package daemon

import (
	"context"
	"time"

	"github.com/moonfruit/sing2seq/clef"

	syncpkg "github.com/moonfruit/sing-router/internal/sync"
)

// SyncLoopConfig 控制后台资源同步行为。intervalSec=0 时 StartSyncLoop 不启动 goroutine。
type SyncLoopConfig struct {
	IntervalSec     int
	OnStartDelaySec int
	AutoApply       bool // true:拉到新资源后自动 apply(zoo/sing-box → restart;cn.txt → ipset reload);false:仅 log
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
					"panic in {Name}: see stderr.log for stack",
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
		runSyncOnce(ctx, updater, em, applier, cfg.AutoApply)
		ticker := time.NewTicker(time.Duration(cfg.IntervalSec) * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				em.Info("sync", "sync.loop.stopped", "background sync stopped", nil)
				return
			case <-ticker.C:
				runSyncOnce(ctx, updater, em, applier, cfg.AutoApply)
			}
		}
	}()
}

func runSyncOnce(ctx context.Context, u *syncpkg.Updater, em *clef.Emitter, applier *Applier, autoApply bool) {
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

	// auto_apply 开启:zoo / sing-box 走完整 Applier 流程;cn.txt 走轻量 ipset reload。
	zooOK := r.Zoo.Err == nil && r.Zoo.Changed
	binOK := r.SingBox.Err == nil && r.SingBox.Changed
	if zooOK || binOK {
		stagingPath := ""
		if binOK {
			stagingPath = r.SingBox.StagingPath
		}
		if err := applier.ApplySingBoxOrZoo(ctx, zooOK, stagingPath); err != nil {
			em.Warn("apply", "apply.failed",
				"apply zoo/sing-box: {Err}", map[string]any{"Err": err.Error()})
		}
	}
	if r.CNList.Err == nil && r.CNList.Changed {
		if err := applier.ApplyCNList(ctx); err != nil {
			em.Warn("apply", "apply.cn_ipset.error",
				"apply cn.txt: {Err}", map[string]any{"Err": err.Error()})
		}
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
