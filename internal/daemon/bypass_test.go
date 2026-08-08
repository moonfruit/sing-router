package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/moonfruit/sing2seq/clef"
)

// fakeIpset 记录收到的每一次 argv，供逐字断言。
type fakeIpset struct {
	mu    sync.Mutex
	calls [][]string
	err   error
}

func (f *fakeIpset) run(_ context.Context, args ...string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, args)
	return f.err
}

func (f *fakeIpset) got(i int) []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls[i]
}

func newBypassMux(fake *fakeIpset) http.Handler {
	return NewMux(APIDeps{Bypass: &BypassDeps{
		Enabled:       true,
		DefaultTTLSec: 120,
		MaxTTLSec:     600,
		IpsetRun:      fake.run,
	}})
}

func postBypass(t *testing.T, h http.Handler, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/bypass", strings.NewReader(body))
	req.RemoteAddr = "127.0.0.1:5555"
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestBypassRegisterInvokesIpsetWithExactArgv(t *testing.T) {
	fake := &fakeIpset{}
	rec := postBypass(t, newBypassMux(fake), `{"ips":["192.168.50.80"],"ttl_sec":120}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d body %s", rec.Code, rec.Body.String())
	}
	want := []string{"-exist", "add", "client_bypass", "192.168.50.80", "timeout", "120"}
	got := fake.got(0)
	if len(got) != len(want) {
		t.Fatalf("argv = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("argv = %v, want %v", got, want)
		}
	}
}

func TestBypassRegisterUsesDefaultTTLWhenOmitted(t *testing.T) {
	fake := &fakeIpset{}
	rec := postBypass(t, newBypassMux(fake), `{"ips":["192.168.50.80"]}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d", rec.Code)
	}
	got := fake.got(0)
	if got[5] != "120" {
		t.Fatalf("ttl arg = %q, want 120", got[5])
	}
}

func TestBypassRegisterAcceptsMultipleIPs(t *testing.T) {
	fake := &fakeIpset{}
	rec := postBypass(t, newBypassMux(fake), `{"ips":["192.168.50.80","192.168.50.81"]}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d", rec.Code)
	}
	if len(fake.calls) != 2 {
		t.Fatalf("expected 2 ipset calls, got %d", len(fake.calls))
	}
}

// 本次仅 IPv4：v6 显式拒绝而非静默丢弃，否则用户注册了却查不出为何不生效。
func TestBypassRejectsIPv6Explicitly(t *testing.T) {
	fake := &fakeIpset{}
	rec := postBypass(t, newBypassMux(fake), `{"ips":["2408:820c::1"]}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status %d, want 400", rec.Code)
	}
	if code := errCode(t, rec); code != "bypass.ipv6_unsupported" {
		t.Fatalf("error code = %q", code)
	}
	if len(fake.calls) != 0 {
		t.Fatal("must not touch ipset on validation failure")
	}
}

// 全有或全无：一个地址不合法则整个请求失败，避免客户端以为全部注册成功。
func TestBypassRejectsWholeRequestOnOneBadIP(t *testing.T) {
	fake := &fakeIpset{}
	rec := postBypass(t, newBypassMux(fake), `{"ips":["192.168.50.80","nonsense"]}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status %d, want 400", rec.Code)
	}
	if code := errCode(t, rec); code != "bypass.bad_ip" {
		t.Fatalf("error code = %q", code)
	}
	if len(fake.calls) != 0 {
		t.Fatal("must not partially apply")
	}
}

func TestBypassRejectsTTLAboveMax(t *testing.T) {
	fake := &fakeIpset{}
	rec := postBypass(t, newBypassMux(fake), `{"ips":["192.168.50.80"],"ttl_sec":99999}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status %d, want 400", rec.Code)
	}
	if code := errCode(t, rec); code != "bypass.bad_ttl" {
		t.Fatalf("error code = %q", code)
	}
}

func TestBypassRejectsEmptyAndOversizedIPList(t *testing.T) {
	fake := &fakeIpset{}
	if rec := postBypass(t, newBypassMux(fake), `{"ips":[]}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("empty list: status %d, want 400", rec.Code)
	}
	many := make([]string, 0, 17)
	for i := 0; i < 17; i++ {
		many = append(many, `"192.168.50.80"`)
	}
	body := `{"ips":[` + strings.Join(many, ",") + `]}`
	if rec := postBypass(t, newBypassMux(fake), body); rec.Code != http.StatusBadRequest {
		t.Fatalf("oversized list: status %d, want 400", rec.Code)
	}
}

func TestBypassRevokeInvokesIpsetDel(t *testing.T) {
	fake := &fakeIpset{}
	req := httptest.NewRequest(http.MethodDelete, "/api/v1/bypass",
		strings.NewReader(`{"ips":["192.168.50.80"]}`))
	req.RemoteAddr = "127.0.0.1:5555"
	rec := httptest.NewRecorder()
	newBypassMux(fake).ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d", rec.Code)
	}
	want := []string{"-exist", "del", "client_bypass", "192.168.50.80"}
	got := fake.got(0)
	if len(got) != len(want) {
		t.Fatalf("argv = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("argv = %v, want %v", got, want)
		}
	}
}

func TestBypassListParsesMembersWithTTL(t *testing.T) {
	out := "Name: client_bypass\nType: hash:ip\nHeader: family inet timeout 120\n" +
		"Members:\n192.168.50.80 timeout 118\n192.168.50.81 timeout 0\n"
	mux := NewMux(APIDeps{Bypass: &BypassDeps{
		Enabled: true, DefaultTTLSec: 120, MaxTTLSec: 600,
		IpsetList: func(_ context.Context, _ string) (string, error) { return out, nil },
	}})
	req := httptest.NewRequest(http.MethodGet, "/api/v1/bypass", nil)
	req.RemoteAddr = "127.0.0.1:5555"
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d", rec.Code)
	}
	var body struct {
		Entries []struct {
			IP         string `json:"ip"`
			TimeoutSec int    `json:"timeout_sec"`
		} `json:"entries"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if len(body.Entries) != 2 {
		t.Fatalf("entries = %+v", body.Entries)
	}
	if body.Entries[0].IP != "192.168.50.80" || body.Entries[0].TimeoutSec != 118 {
		t.Fatalf("entry 0 = %+v", body.Entries[0])
	}
}

// 未启用时端点根本不注册，命中 mux 的 404 而非 handler。
func TestBypassEndpointAbsentWhenDisabled(t *testing.T) {
	mux := NewMux(APIDeps{})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/bypass", strings.NewReader(`{"ips":["1.2.3.4"]}`))
	req.RemoteAddr = "127.0.0.1:5555"
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status %d, want 404", rec.Code)
	}
}

// ipset 调用失败时，register / revoke / list 三条路径都要能返回
// 500 + bypass.ipset_failed——这条错误分支此前一次都没被断言过。

func TestBypassRegisterIpsetFailureReturns500(t *testing.T) {
	fake := &fakeIpset{err: errors.New("ipset: kernel error")}
	rec := postBypass(t, newBypassMux(fake), `{"ips":["192.168.50.80"]}`)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status %d, want 500", rec.Code)
	}
	if code := errCode(t, rec); code != "bypass.ipset_failed" {
		t.Fatalf("error code = %q", code)
	}
}

func TestBypassRevokeIpsetFailureReturns500(t *testing.T) {
	fake := &fakeIpset{err: errors.New("ipset: kernel error")}
	req := httptest.NewRequest(http.MethodDelete, "/api/v1/bypass",
		strings.NewReader(`{"ips":["192.168.50.80"]}`))
	req.RemoteAddr = "127.0.0.1:5555"
	rec := httptest.NewRecorder()
	newBypassMux(fake).ServeHTTP(rec, req)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status %d, want 500", rec.Code)
	}
	if code := errCode(t, rec); code != "bypass.ipset_failed" {
		t.Fatalf("error code = %q", code)
	}
}

func TestBypassListIpsetFailureReturns500(t *testing.T) {
	mux := NewMux(APIDeps{Bypass: &BypassDeps{
		Enabled: true, DefaultTTLSec: 120, MaxTTLSec: 600,
		IpsetList: func(_ context.Context, _ string) (string, error) {
			return "", errors.New("ipset: no such set")
		},
	}})
	req := httptest.NewRequest(http.MethodGet, "/api/v1/bypass", nil)
	req.RemoteAddr = "127.0.0.1:5555"
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status %d, want 500", rec.Code)
	}
	if code := errCode(t, rec); code != "bypass.ipset_failed" {
		t.Fatalf("error code = %q", code)
	}
}

// 多地址循环中途失败时不回滚，但必须把已成功的地址通过 detail 回传，
// 否则客户端无法判断哪些还需要在下一轮重试。
func TestBypassRegisterPartialFailureReportsSucceeded(t *testing.T) {
	fake := &fakeIpset{}
	calls := 0
	runFn := func(ctx context.Context, args ...string) error {
		calls++
		fake.mu.Lock()
		fake.calls = append(fake.calls, args)
		fake.mu.Unlock()
		if calls == 2 {
			return errors.New("ipset: boom on second call")
		}
		return nil
	}
	mux := NewMux(APIDeps{Bypass: &BypassDeps{
		Enabled: true, DefaultTTLSec: 120, MaxTTLSec: 600,
		IpsetRun: runFn,
	}})
	rec := postBypass(t, mux, `{"ips":["192.168.50.80","192.168.50.81"]}`)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status %d, want 500", rec.Code)
	}
	var body struct {
		Error struct {
			Detail struct {
				Succeeded []string `json:"succeeded"`
			} `json:"detail"`
		} `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if len(body.Error.Detail.Succeeded) != 1 || body.Error.Detail.Succeeded[0] != "192.168.50.80" {
		t.Fatalf("detail.succeeded = %+v", body.Error.Detail.Succeeded)
	}
}

// json body 超过上限必须在参数校验之前被挡住，且不能碰 ipset。
func TestBypassRegisterRejectsOversizedBody(t *testing.T) {
	fake := &fakeIpset{}
	huge := strings.Repeat("a", maxBypassBodyBytes+1)
	body := `{"ips":["` + huge + `"]}`
	rec := postBypass(t, newBypassMux(fake), body)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status %d, want 400", rec.Code)
	}
	if code := errCode(t, rec); code != "bypass.bad_request" {
		t.Fatalf("error code = %q", code)
	}
	if len(fake.calls) != 0 {
		t.Fatal("must not touch ipset on oversized body")
	}
}

// recordingBus 订阅所有事件并塞进 channel，供测试同步等待 Emitter.Warn 落地
// （Bus.Publish 是异步的：投递给订阅方是走 goroutine + channel）。
func recordingBus(t *testing.T) (*clef.Bus, chan *clef.Event) {
	t.Helper()
	bus := clef.NewBus(8)
	events := make(chan *clef.Event, 8)
	bus.Subscribe(clef.SubscriberFunc{
		MatchFn:   func(*clef.Event) bool { return true },
		DeliverFn: func(e *clef.Event) { events <- e },
	})
	t.Cleanup(bus.Close)
	return bus, events
}

func waitEvent(t *testing.T, events chan *clef.Event) *clef.Event {
	t.Helper()
	select {
	case e := <-events:
		return e
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for event")
		return nil
	}
}

// EnsureSet 必须在 daemon 启动早期把动态租约 set 建好，否则 client_bypass
// 只由 startup.sh 创建，而 startup.sh 要等 ready check（默认上限 60s）走完
// 才跑，冷启动窗口内客户端心跳会全部拿到 500。
func TestEnsureSetCreatesSetWithExactArgv(t *testing.T) {
	fake := &fakeIpset{}
	deps := BypassDeps{Enabled: true, DefaultTTLSec: 120, MaxTTLSec: 600, IpsetRun: fake.run}
	deps.EnsureSet(context.Background())
	want := []string{"-exist", "create", "client_bypass", "hash:ip", "timeout", "120"}
	got := fake.got(0)
	if len(got) != len(want) {
		t.Fatalf("argv = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("argv = %v, want %v", got, want)
		}
	}
}

// bypass 未启用时 EnsureSet 不该碰 ipset——关闭时这个功能应当彻底不存在。
func TestEnsureSetNoopWhenDisabled(t *testing.T) {
	fake := &fakeIpset{}
	deps := BypassDeps{Enabled: false, DefaultTTLSec: 120, IpsetRun: fake.run}
	deps.EnsureSet(context.Background())
	if len(fake.calls) != 0 {
		t.Fatalf("expected no ipset calls, got %v", fake.calls)
	}
}

// EnsureSet 失败只记 warn，不 panic、不返回 error——真正兜底还是 startup.sh。
func TestEnsureSetWarnsOnFailureButDoesNotPanic(t *testing.T) {
	bus, events := recordingBus(t)
	em := clef.NewEmitter(clef.EmitterConfig{Source: "daemon", MinLevel: clef.LevelInfo, Bus: bus})

	deps := BypassDeps{
		Enabled:       true,
		DefaultTTLSec: 120,
		Emitter:       em,
		IpsetRun:      func(context.Context, ...string) error { return errors.New("ipset: boom") },
	}
	deps.EnsureSet(context.Background())

	e := waitEvent(t, events)
	if id, _ := e.Get("EventID"); id != "bypass.ipset_failed" {
		t.Fatalf("EventID = %v, want bypass.ipset_failed", id)
	}
}

// nil Emitter（测试 / 未来其它调用方不注入）必须静默跳过，不能 panic。
func TestEnsureSetNilEmitterDoesNotPanic(t *testing.T) {
	deps := BypassDeps{
		Enabled:       true,
		DefaultTTLSec: 120,
		IpsetRun:      func(context.Context, ...string) error { return errors.New("boom") },
	}
	deps.EnsureSet(context.Background()) // 不能 panic
}

// register/revoke/list 三条失败路径都要能把事件发给 Emitter，这样 daemon 本地
// log / seq 才留得下痕迹——此前失败只出现在 HTTP 响应体里，doctor 只能报
// 「set missing」，看不出原因。
func TestBypassHandlersReportIpsetFailureToEmitter(t *testing.T) {
	cases := []struct {
		name string
		do   func(h http.Handler)
	}{
		{"register", func(h http.Handler) {
			postBypassHandler(t, h, http.MethodPost, `{"ips":["192.168.50.80"]}`)
		}},
		{"revoke", func(h http.Handler) {
			postBypassHandler(t, h, http.MethodDelete, `{"ips":["192.168.50.80"]}`)
		}},
		{"list", func(h http.Handler) {
			postBypassHandler(t, h, http.MethodGet, "")
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			bus, events := recordingBus(t)
			em := clef.NewEmitter(clef.EmitterConfig{Source: "daemon", MinLevel: clef.LevelInfo, Bus: bus})
			mux := NewMux(APIDeps{Bypass: &BypassDeps{
				Enabled: true, DefaultTTLSec: 120, MaxTTLSec: 600,
				Emitter:   em,
				IpsetRun:  func(context.Context, ...string) error { return errors.New("ipset: boom") },
				IpsetList: func(context.Context, string) (string, error) { return "", errors.New("ipset: boom") },
			}})
			tc.do(mux)
			e := waitEvent(t, events)
			if id, _ := e.Get("EventID"); id != "bypass.ipset_failed" {
				t.Fatalf("EventID = %v, want bypass.ipset_failed", id)
			}
		})
	}
}

func postBypassHandler(t *testing.T, h http.Handler, method, body string) *httptest.ResponseRecorder {
	t.Helper()
	var req *http.Request
	if body == "" {
		req = httptest.NewRequest(method, "/api/v1/bypass", nil)
	} else {
		req = httptest.NewRequest(method, "/api/v1/bypass", strings.NewReader(body))
	}
	req.RemoteAddr = "127.0.0.1:5555"
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

// installFakeIpset 把一个可执行 shell 脚本伪装成 `ipset`，塞进 PATH 最前面，
// 让 realIpsetRun（写死调用 "ipset"）在没有真实 ipset/root 权限的开发机 /
// CI 上也能被测试到。
func installFakeIpset(t *testing.T, script string) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "ipset")
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+script+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

// M1：realIpsetRun 之前用 .Run() 丢弃 stderr，失败时错误只剩 "exit status 1"，
// 排障要靠猜。捕获 stderr 之后错误信息必须带上具体原因。
func TestRealIpsetRunCapturesStderrOnFailure(t *testing.T) {
	installFakeIpset(t, "echo 'ipset v7.6: Syntax error' >&2\nexit 1")
	err := realIpsetRun(context.Background(), "add", "client_bypass", "192.168.50.80")
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "Syntax error") {
		t.Fatalf("error should include captured stderr: %v", err)
	}
}

// 【只看退出码】目标设备 ipset userspace(v7.6) 比内核(protocol 6) 新，每次
// 调用都可能往 stderr 打协议版本 warning；捕获 stderr 仅用于错误信息，绝不能
// 参与成败判定——退出码 0 时哪怕 stderr 非空也必须视为成功。
func TestRealIpsetRunIgnoresStderrOnSuccess(t *testing.T) {
	installFakeIpset(t, "echo 'Warning: Kernel support protocol versions 6-6 while userspace supports protocol versions 6-7' >&2\nexit 0")
	if err := realIpsetRun(context.Background(), "add", "client_bypass", "192.168.50.80"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

// 超长 stderr 必须被截断，不能把错误信息 / HTTP 响应体撑爆。
func TestRealIpsetRunTruncatesLongStderr(t *testing.T) {
	installFakeIpset(t, "yes x | head -c 4096 >&2\nexit 1")
	err := realIpsetRun(context.Background(), "add", "client_bypass", "192.168.50.80")
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "truncated") {
		t.Fatalf("expected truncation marker: %v", err)
	}
}

func errCode(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	var body struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode error body %q: %v", rec.Body.String(), err)
	}
	return body.Error.Code
}
