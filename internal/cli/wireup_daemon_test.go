package cli

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/moonfruit/sing2seq/clef"

	"github.com/moonfruit/sing-router/internal/config"
	log "github.com/moonfruit/sing-router/internal/log"
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
	routeWatchSec := 15
	backoffSeq := []int{100, 200, 400}
	w := buildSupervisorWiring(config.SupervisorConfig{
		ReadyCheckTimeoutMs:     &tMs,
		ReadyCheckIntervalMs:    &iMs,
		ReadyCheckClashAPI:      &clash,
		ReadyCheckDialInbounds:  &dial,
		StopGraceSeconds:        &stopSec,
		RouteWatchIntervalSec:   &routeWatchSec,
		CrashPostReadyBackoffMs: backoffSeq,
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

func newTestStack(t *testing.T) *log.EmitterStack {
	t.Helper()
	dir := t.TempDir()
	w, err := log.NewWriter(log.WriterConfig{Path: filepath.Join(dir, "test.log")})
	if err != nil {
		t.Fatal(err)
	}
	return log.NewEmitterStack(log.StackConfig{
		Source: "daemon", MinLevel: log.LevelInfo, Writer: w,
	})
}

// newTestStackWithLevels 构造一个带 writer log path 的 stack；emitter floor
// 取 logLevel/seqLevel 更小那个，writer 按 logLevel 过滤。返回 (stack, logPath)。
func newTestStackWithLevels(t *testing.T, logLevel, seqLevel log.Level) (*log.EmitterStack, string) {
	t.Helper()
	dir := t.TempDir()
	logPath := filepath.Join(dir, "test.log")
	w, err := log.NewWriter(log.WriterConfig{Path: logPath})
	if err != nil {
		t.Fatal(err)
	}
	stack := log.NewEmitterStack(log.StackConfig{
		Source:         "daemon",
		MinLevel:       min(logLevel, seqLevel),
		WriterMinLevel: logLevel,
		Writer:         w,
	})
	return stack, logPath
}

// seqWillAttach 是 attach 的 gate；Enabled=false 或 URL=="" 都不应 attach。
func TestSeqWillAttachGate(t *testing.T) {
	cases := []struct {
		name string
		cfg  config.SeqConfig
		want bool
	}{
		{"disabled", config.SeqConfig{Enabled: false, URL: "http://x"}, false},
		{"empty url", config.SeqConfig{Enabled: true, URL: ""}, false},
		{"both set", config.SeqConfig{Enabled: true, URL: "http://x"}, true},
	}
	for _, c := range cases {
		if got := seqWillAttach(c.cfg); got != c.want {
			t.Errorf("%s: got=%v want=%v", c.name, got, c.want)
		}
	}
}

// resolveSeqLevel 行为合约：空字符串默认 info（无 warn）；非法值 fallback
// 到 info（带 warn）；合法值原样返回。
func TestResolveSeqLevel(t *testing.T) {
	cases := []struct {
		in       string
		wantLvl  clef.Level
		wantWarn bool
	}{
		{"", clef.LevelInfo, false},
		{"info", clef.LevelInfo, false},
		{"warn", clef.LevelWarn, false},
		{"error", clef.LevelError, false},
		{"bogus", clef.LevelInfo, true},
	}
	for _, c := range cases {
		lvl, warn := resolveSeqLevel(c.in)
		if lvl != c.wantLvl {
			t.Errorf("in=%q lvl=%v want=%v", c.in, lvl, c.wantLvl)
		}
		if (warn != "") != c.wantWarn {
			t.Errorf("in=%q warn=%q wantWarn=%v", c.in, warn, c.wantWarn)
		}
	}
}

// TestSeqAndWriterIndependentLevels 守护核心需求：[log].level 与 [seq].level
// 是两条独立阈值——本地 log 文件只收 writerLevel+，seq 只收 seqLevel+。
// 这里覆盖三个组合：seq 低于 log（典型：本地少噪、远程更全）、seq 高于 log
// （典型：本地全、远程只看告警）、相等（最常见，单 floor）。
func TestSeqAndWriterIndependentLevels(t *testing.T) {
	cases := []struct {
		name        string
		writerLevel log.Level
		seqLevel    clef.Level
		seqLevelStr string
		// emitter 各发一次 Info / Warn / Error。事件被记进 Source="daemon"。
		// 然后断言 writer 文件与 mock seq server 各自看到什么 EventID。
		wantInLog []string
		wantInSeq []string
	}{
		{
			name:        "writer=warn, seq=info — log 安静，远程详细",
			writerLevel: log.LevelWarn, seqLevel: clef.LevelInfo, seqLevelStr: "info",
			wantInLog: []string{"warn-1", "error-1"},
			wantInSeq: []string{"info-1", "warn-1", "error-1"},
		},
		{
			name:        "writer=info, seq=warn — 本地详细，远程只看告警",
			writerLevel: log.LevelInfo, seqLevel: clef.LevelWarn, seqLevelStr: "warn",
			wantInLog: []string{"info-1", "warn-1", "error-1"},
			wantInSeq: []string{"warn-1", "error-1"},
		},
		{
			name:        "writer=info, seq=info — 两边一致",
			writerLevel: log.LevelInfo, seqLevel: clef.LevelInfo, seqLevelStr: "info",
			wantInLog: []string{"info-1", "warn-1", "error-1"},
			wantInSeq: []string{"info-1", "warn-1", "error-1"},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var (
				mu       sync.Mutex
				seqLines []string
			)
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				buf := make([]byte, 4096)
				n, _ := r.Body.Read(buf)
				mu.Lock()
				seqLines = append(seqLines, string(buf[:n]))
				mu.Unlock()
				w.WriteHeader(http.StatusOK)
			}))
			defer srv.Close()

			stack, logPath := newTestStackWithLevels(t, c.writerLevel, log.Level(c.seqLevel))
			attachSeqSink(stack, config.SeqConfig{
				Enabled: true, URL: srv.URL, Level: c.seqLevelStr,
			}, c.seqLevel)

			stack.Emitter.Info("test", "info-1", "", nil)
			stack.Emitter.Warn("test", "warn-1", "", nil)
			stack.Emitter.Error("test", "error-1", "", nil)

			if err := stack.Close(context.Background()); err != nil {
				t.Fatalf("Close: %v", err)
			}

			// 检查 writer log 文件
			data, _ := os.ReadFile(logPath)
			body := string(data)
			for _, id := range c.wantInLog {
				if !strings.Contains(body, id) {
					t.Errorf("writer missing %q; log was: %s", id, body)
				}
			}
			// 没列入 wantInLog 的不应出现
			for _, id := range []string{"info-1", "warn-1", "error-1"} {
				if !slices.Contains(c.wantInLog, id) && strings.Contains(body, id) {
					t.Errorf("writer should NOT contain %q (filtered by WriterMinLevel); log was: %s", id, body)
				}
			}

			// 检查 seq server
			mu.Lock()
			defer mu.Unlock()
			joined := strings.Join(seqLines, "\n")
			for _, id := range c.wantInSeq {
				if !strings.Contains(joined, id) {
					t.Errorf("seq missing %q; received: %s", id, joined)
				}
			}
			for _, id := range []string{"info-1", "warn-1", "error-1"} {
				if !slices.Contains(c.wantInSeq, id) && strings.Contains(joined, id) {
					t.Errorf("seq should NOT contain %q (filtered by [seq].level); received: %s", id, joined)
				}
			}
		})
	}
}

// 完整闭环：起一个 mock Seq server，启用 sink，发一条事件，验证 server 收到。
// 之后 stack.Close 必须不卡住 — drain 同步完成。
func TestAttachSeqSinkEndToEnd(t *testing.T) {
	var (
		got        atomic.Int32
		gotContent atomic.Value // string
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/ingest/clef" {
			t.Errorf("unexpected path %q", r.URL.Path)
		}
		if r.Header.Get("Content-Type") != "application/vnd.serilog.clef" {
			t.Errorf("unexpected content-type %q", r.Header.Get("Content-Type"))
		}
		if r.Header.Get("X-Seq-ApiKey") != "k-test" {
			t.Errorf("missing/wrong api key header: %q", r.Header.Get("X-Seq-ApiKey"))
		}
		buf := make([]byte, 4096)
		n, _ := r.Body.Read(buf)
		gotContent.Store(string(buf[:n]))
		got.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	stack := newTestStack(t)
	attachSeqSink(stack, config.SeqConfig{
		Enabled: true, URL: srv.URL, APIKey: "k-test", Level: "info",
	}, clef.LevelInfo)

	stack.Emitter.Info("test", "boot", "hello {Where}", map[string]any{"Where": "world"})

	// Close 触发 drain；同步收完最后一批。
	if err := stack.Close(context.Background()); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if got.Load() < 1 {
		t.Fatalf("seq server received 0 batches; expected >= 1")
	}
	body, _ := gotContent.Load().(string)
	if body == "" {
		t.Fatalf("seq server got empty body")
	}
}
