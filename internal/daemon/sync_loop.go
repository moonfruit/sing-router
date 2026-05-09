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
}

// StartSyncLoop 在后台周期性调用 updater.UpdateAll(ctx)。
//
// 行为：
//   - intervalSec <= 0：不启动 goroutine，直接返回。
//   - 启动后等 onStartDelaySec 才跑首次同步（避免 daemon 启动洪峰与 sing-box 抢资源）。
//   - 每次同步结果通过 emitter 写入日志；同步失败仅记录，不影响 daemon 主流程。
//   - ctx 取消时 goroutine 优雅退出。
func StartSyncLoop(ctx context.Context, updater *syncpkg.Updater, cfg SyncLoopConfig, em *clef.Emitter) {
	if cfg.IntervalSec <= 0 {
		em.Info("sync", "sync.loop.disabled", "background sync disabled (interval_seconds=0)", nil)
		return
	}
	go func() {
		em.Info("sync", "sync.loop.started", "background sync starting in {DelaySec}s; interval={IntervalSec}s",
			map[string]any{"DelaySec": cfg.OnStartDelaySec, "IntervalSec": cfg.IntervalSec})
		if cfg.OnStartDelaySec > 0 {
			select {
			case <-ctx.Done():
				return
			case <-time.After(time.Duration(cfg.OnStartDelaySec) * time.Second):
			}
		}
		runSyncOnce(ctx, updater, em)
		ticker := time.NewTicker(time.Duration(cfg.IntervalSec) * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				em.Info("sync", "sync.loop.stopped", "background sync stopped", nil)
				return
			case <-ticker.C:
				runSyncOnce(ctx, updater, em)
			}
		}
	}()
}

func runSyncOnce(ctx context.Context, u *syncpkg.Updater, em *clef.Emitter) {
	r := u.UpdateAll(ctx)
	logItem(em, "sing-box", r.SingBox.Changed, r.SingBox.Version, r.SingBox.Err)
	logItem(em, "cn.txt", r.CNList.Changed, "", r.CNList.Err)
	logItem(em, "zoo.json", r.Zoo.Changed, "", r.Zoo.Err)
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
