package daemon

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/moonfruit/sing-router/internal/config"
	syncpkg "github.com/moonfruit/sing-router/internal/sync"
)

// TestStartSyncLoop_DisabledWhenIntervalZero — IntervalSec=0 时立刻返回，不启动 goroutine。
func TestStartSyncLoop_DisabledWhenIntervalZero(t *testing.T) {
	rundir := t.TempDir()
	cfg := &config.DaemonConfig{
		Download: config.DownloadConfig{CNListURL: "http://127.0.0.1:1/never"},
	}
	u := syncpkg.NewUpdater(cfg, rundir)
	em := newTestEmitter(t)

	StartSyncLoop(context.Background(), u, SyncLoopConfig{IntervalSec: 0}, em)
	// 给一段宽限时间——若不慎启动了 goroutine 它会去打 127.0.0.1:1，cn.txt 仍不会出现。
	time.Sleep(100 * time.Millisecond)
	if _, err := os.Stat(filepath.Join(rundir, "var", "cn.txt")); !os.IsNotExist(err) {
		t.Fatal("cn.txt should not be written when interval=0")
	}
}

// TestStartSyncLoop_RunsAndStopsOnCtxCancel — 启动后跑首次同步，ctx 取消后停止。
func TestStartSyncLoop_RunsAndStopsOnCtxCancel(t *testing.T) {
	const cnPayload = "1.0.0.0/8"
	var cnHits atomic.Int32
	cnSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cnHits.Add(1)
		_, _ = io.WriteString(w, cnPayload)
	}))
	defer cnSrv.Close()

	// gitee 这一路（sing-box / zoo）我们让它持续 401，验证"任一失败不影响其他"的设计。
	giteeSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "no", http.StatusUnauthorized)
	}))
	defer giteeSrv.Close()

	rundir := t.TempDir()
	cfg := &config.DaemonConfig{
		Gitee: config.GiteeConfig{
			Token: "tk",
			Owner: "o",
			Repo:  "r",
			SingBox: config.GiteeSingBoxConfig{
				Ref: "binary", VersionPath: "version.txt",
				TarballPathTemplate: "sb-{version}.tar.gz",
			},
			Zoo: config.GiteeZooConfig{Ref: "main", Path: "config.json"},
		},
		Download: config.DownloadConfig{CNListURL: cnSrv.URL, HTTPRetries: 0},
	}
	u := syncpkg.NewUpdater(cfg, rundir)

	em := newTestEmitter(t)
	ctx, cancel := context.WithCancel(context.Background())
	StartSyncLoop(ctx, u, SyncLoopConfig{IntervalSec: 1, OnStartDelaySec: 0}, em)

	// 等到 cn.txt 出现或超时——证明 loop 至少跑过一次 UpdateAll。
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if data, err := os.ReadFile(filepath.Join(rundir, "var", "cn.txt")); err == nil {
			if string(data) != cnPayload {
				t.Fatalf("cn.txt = %q want %q", string(data), cnPayload)
			}
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if cnHits.Load() == 0 {
		t.Fatal("cn server never hit; loop didn't run")
	}
	cancel()
	// 给 goroutine 一点时间退出，避免后续 t.Cleanup 报泄漏。
	time.Sleep(50 * time.Millisecond)
}
