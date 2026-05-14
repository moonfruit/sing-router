package cli

import (
	"testing"
	"time"

	"github.com/moonfruit/sing-router/internal/config"
)

func TestBuildStatusExtraIncludesFirmware(t *testing.T) {
	f := buildStatusExtra("/opt/home/sing-router", "config.d", "koolshare")
	snap := f()
	if snap["firmware"] != "koolshare" {
		t.Fatalf("firmware=%v want koolshare", snap["firmware"])
	}
	cfg, ok := snap["config"].(map[string]any)
	if !ok {
		t.Fatalf("config key missing or wrong type: %+v", snap["config"])
	}
	if cfg["config_dir"] != "/opt/home/sing-router/config.d" {
		t.Fatalf("config_dir=%v", cfg["config_dir"])
	}
}

func TestBuildStatusExtraEmptyFirmwareReportsUnknown(t *testing.T) {
	f := buildStatusExtra("/opt/home/sing-router", "config.d", "")
	snap := f()
	if snap["firmware"] != "unknown" {
		t.Fatalf("empty firmware should report 'unknown', got %v", snap["firmware"])
	}
}

// 回归保护：sing-box 冷启动（cache-file 加载 ~2s + rule-set 下载 ~2s + router
// 启动 ~2s）实测要 4-6 秒；任何把内置默认 TotalTimeout 改回小常量的回归会被
// 这条用例拍下。
//
// 同时锁住 ready dial 端口列表：默认只 dial mixed-in (7890)。绝不能再回到
// dial dns-in (1053) / redirect-in (7892) —— 那两个是 transparent 入站，本机
// 自连即触发 sing-box 自身回环（CPU 100%），见 readyCheckDialMixedPort 注释。
func TestBuildSupervisorWiring_Defaults(t *testing.T) {
	w := buildSupervisorWiring(config.SupervisorConfig{})

	if got, want := w.Ready.TotalTimeout, 60*time.Second; got != want {
		t.Fatalf("default TotalTimeout = %v, want %v", got, want)
	}
	if got, want := w.Ready.Interval, 200*time.Millisecond; got != want {
		t.Fatalf("default Interval = %v, want %v", got, want)
	}
	if w.Ready.ClashAPIURL != "http://127.0.0.1:9999/version" {
		t.Fatalf("default ClashAPIURL = %q", w.Ready.ClashAPIURL)
	}
	wantDials := []string{"127.0.0.1:7890"}
	if len(w.Ready.TCPDials) != 1 || w.Ready.TCPDials[0] != wantDials[0] {
		t.Fatalf("default TCPDials = %v, want %v", w.Ready.TCPDials, wantDials)
	}
	for _, d := range w.Ready.TCPDials {
		if d == "127.0.0.1:1053" || d == "127.0.0.1:7892" {
			t.Fatalf("TCPDials must NOT include transparent inbound %q (会触发 sing-box 自身回环)", d)
		}
	}
	if w.StopGrace != 0 {
		t.Fatalf("default StopGrace should be 0 (supervisor.New 内部填默认), got %v", w.StopGrace)
	}
	if w.RouteWatchInterval != 30*time.Second {
		t.Fatalf("default RouteWatchInterval = %v, want 30s", w.RouteWatchInterval)
	}
}

// daemon.toml [supervisor] 节里的覆盖必须真正生效——之前一段时间这些字段
// 在 wireup 里被忽略，导致用户改 toml 不动作；这条用例锁住每个旋钮的覆盖通路。
func TestBuildSupervisorWiring_TomlOverrides(t *testing.T) {
	tMs := 30000
	iMs := 500
	clash := false
	dial := false
	stopSec := 7
	keepBackoff := 4242
	routeWatchSec := 15
	backoffSeq := []int{100, 200, 400}
	w := buildSupervisorWiring(config.SupervisorConfig{
		ReadyCheckTimeoutMs:         &tMs,
		ReadyCheckIntervalMs:        &iMs,
		ReadyCheckClashAPI:          &clash,
		ReadyCheckDialInbounds:      &dial,
		StopGraceSeconds:            &stopSec,
		IptablesKeepWhenBackoffLtMs: &keepBackoff,
		RouteWatchIntervalSec:       &routeWatchSec,
		CrashPostReadyBackoffMs:     backoffSeq,
	})

	if w.Ready.TotalTimeout != 30*time.Second {
		t.Fatalf("TotalTimeout = %v", w.Ready.TotalTimeout)
	}
	if w.Ready.Interval != 500*time.Millisecond {
		t.Fatalf("Interval = %v", w.Ready.Interval)
	}
	if w.Ready.ClashAPIURL != "" {
		t.Fatalf("disabled ClashAPI should clear URL, got %q", w.Ready.ClashAPIURL)
	}
	if len(w.Ready.TCPDials) != 0 {
		t.Fatalf("disabled DialInbounds should empty dials, got %v", w.Ready.TCPDials)
	}
	if w.StopGrace != 7*time.Second {
		t.Fatalf("StopGrace = %v", w.StopGrace)
	}
	if w.IptablesKeepBackoffLtMs != 4242 {
		t.Fatalf("IptablesKeepBackoffLtMs = %d", w.IptablesKeepBackoffLtMs)
	}
	if len(w.BackoffMs) != 3 || w.BackoffMs[0] != 100 || w.BackoffMs[2] != 400 {
		t.Fatalf("BackoffMs = %v", w.BackoffMs)
	}
	if w.RouteWatchInterval != 15*time.Second {
		t.Fatalf("RouteWatchInterval = %v, want 15s", w.RouteWatchInterval)
	}
}

// ReadyCheckTimeoutMs = 0（显式 0）应被视作"未提供"，回退到 60s 默认；不能 0 = 立刻超时。
func TestBuildSupervisorWiring_TimeoutZeroFallsBackToDefault(t *testing.T) {
	zero := 0
	w := buildSupervisorWiring(config.SupervisorConfig{ReadyCheckTimeoutMs: &zero})
	if w.Ready.TotalTimeout != 60*time.Second {
		t.Fatalf("zero should fall back to 60s default, got %v", w.Ready.TotalTimeout)
	}
}
