package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"
)

// 用一个最小 supervisor 模拟器：构造好 state 后供 handler 读取。
func newTestSupervisor(t *testing.T) *Supervisor {
	t.Helper()
	binary := fakeSingBox(t)
	p, clash := freePort(t), freePort(t)
	sup := New(SupervisorConfig{
		Emitter:       newTestEmitter(t),
		SingBoxBinary: binary,
		SingBoxArgs:   []string{"--listen", strconv.Itoa(p), "--clash-port", strconv.Itoa(clash)},
		ReadyConfig: ReadyConfig{
			TCPDials:     []string{fmt.Sprintf("127.0.0.1:%d", p)},
			ClashAPIURL:  fmt.Sprintf("http://127.0.0.1:%d/version", clash),
			TotalTimeout: 2 * time.Second,
			Interval:     50 * time.Millisecond,
		},
		StartupHook:  func(_ context.Context) error { return nil },
		TeardownHook: func(_ context.Context) error { return nil },
		StopGrace:    1 * time.Second,
	})
	return sup
}

func TestAPIStatusReturnsJSON(t *testing.T) {
	sup := newTestSupervisor(t)
	mux := NewMux(APIDeps{Supervisor: sup, Version: "test-1.0", Rundir: "/tmp/rundir"})
	ts := httptest.NewServer(mux)
	defer ts.Close()
	resp, err := http.Get(ts.URL + "/api/v1/status")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != 200 {
		t.Fatalf("status %d", resp.StatusCode)
	}
	var body map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	daemon := body["daemon"].(map[string]any)
	if daemon["version"] != "test-1.0" {
		t.Fatalf("version: %v", daemon["version"])
	}
	if pid, ok := daemon["pid"].(float64); !ok || int(pid) != os.Getpid() {
		t.Fatalf("pid: %v (want %d)", daemon["pid"], os.Getpid())
	}
}

func TestAPIScript(t *testing.T) {
	sup := newTestSupervisor(t)
	mux := NewMux(APIDeps{
		Supervisor: sup,
		ScriptByName: func(name string) ([]byte, error) {
			if name != "startup" {
				return nil, fmt.Errorf("unknown")
			}
			return []byte("#!/usr/bin/env bash\necho hi"), nil
		},
	})
	ts := httptest.NewServer(mux)
	defer ts.Close()
	resp, _ := http.Get(ts.URL + "/api/v1/script/startup")
	body := readBody(t, resp)
	if !strings.Contains(body, "echo hi") {
		t.Fatalf("body: %s", body)
	}
	resp2, _ := http.Get(ts.URL + "/api/v1/script/missing")
	if resp2.StatusCode != 404 {
		t.Fatalf("status: %d", resp2.StatusCode)
	}
}

func readBody(t *testing.T, r *http.Response) string {
	t.Helper()
	defer func() { _ = r.Body.Close() }()
	var b strings.Builder
	buf := make([]byte, 1024)
	for {
		n, err := r.Body.Read(buf)
		if n > 0 {
			b.Write(buf[:n])
		}
		if err != nil {
			break
		}
	}
	return b.String()
}

func TestAPIApplyResourceQuery(t *testing.T) {
	// /api/v1/apply?resource=cn 应把 ResourceCN 传给 Apply hook。
	sup := New(SupervisorConfig{Emitter: newTestEmitter(t)})
	var gotKinds []Resource
	mux := NewMux(APIDeps{Supervisor: sup, Apply: func(_ context.Context, kinds []Resource) error {
		gotKinds = kinds
		return nil
	}})
	ts := httptest.NewServer(mux)
	defer ts.Close()
	resp, _ := http.Post(ts.URL+"/api/v1/apply?resource=cn", "application/json", nil)
	if resp.StatusCode != 200 {
		t.Fatalf("status: %d", resp.StatusCode)
	}
	if len(gotKinds) != 1 || gotKinds[0] != ResourceCN {
		t.Fatalf("kinds: %v want [ResourceCN]", gotKinds)
	}
	// 不传 query 时默认 all
	gotKinds = nil
	resp2, _ := http.Post(ts.URL+"/api/v1/apply", "application/json", nil)
	if resp2.StatusCode != 200 {
		t.Fatalf("status: %d", resp2.StatusCode)
	}
	if len(gotKinds) != len(AllResources) {
		t.Fatalf("kinds: %v want AllResources", gotKinds)
	}
	// 非法 resource 返 400
	resp3, _ := http.Post(ts.URL+"/api/v1/apply?resource=bogus", "application/json", nil)
	if resp3.StatusCode != 400 {
		t.Fatalf("status: %d want 400", resp3.StatusCode)
	}
}

// ServeHTTP 必须只监听 IPv4。路由器的 v6 地址是公网直接可达的（没有 NAT
// 兜底），一旦监听 v6，管理 API 就挂在公网上了。
//
// 【必须用 wildcard 地址，不能用 127.0.0.1】Go 的 favoriteAddrFamily 对
// "127.0.0.1" 这种显式 v4 地址在 net.Listen("tcp", ...) 下也会选 AF_INET——
// 把 ServeHTTP 里的 net.Listen("tcp4", ...) 整个 revert 成 net.Listen("tcp", ...)，
// 用 127.0.0.1 绑的这条测试照样是绿的，完全测不出双栈回归。0.0.0.0 才会真正
// 触发 net.Listen("tcp", ...) 的双栈监听行为，这条测试的价值全靠它。
func TestServeHTTPListensOnIPv4Only(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	mux := NewMux(APIDeps{Supervisor: newTestSupervisor(t), Version: "t", Rundir: "/tmp"})
	port := freePort(t)
	listenAddr := fmt.Sprintf("0.0.0.0:%d", port)
	dialAddr := fmt.Sprintf("127.0.0.1:%d", port)
	done := make(chan error, 1)
	go func() { done <- ServeHTTP(ctx, mux, listenAddr) }()

	// 等 listener 就绪
	var resp *http.Response
	var err error
	for i := 0; i < 50; i++ {
		resp, err = http.Get("http://" + dialAddr + "/api/v1/status")
		if err == nil {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if err != nil {
		t.Fatalf("v4 request failed: %v", err)
	}
	_ = resp.Body.Close()

	// 同端口的 v6 环回必须连不上——证明没有走双栈监听。
	c, derr := net.DialTimeout("tcp6", fmt.Sprintf("[::1]:%d", port), 300*time.Millisecond)
	if derr == nil {
		_ = c.Close()
		t.Fatal("v6 loopback is reachable; ServeHTTP must bind tcp4 only")
	}
	cancel()
	<-done
}

// 显式配 v6 监听地址应当报错而不是静默降级。
func TestServeHTTPRejectsIPv6ListenAddress(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	mux := NewMux(APIDeps{})
	err := ServeHTTP(ctx, mux, fmt.Sprintf("[::1]:%d", freePort(t)))
	if err == nil {
		t.Fatal("expected error for IPv6 listen address")
	}
	if !strings.Contains(err.Error(), "tcp4") {
		t.Fatalf("error should mention tcp4: %v", err)
	}
}

// fakeBypassDeps 返回一个不真正调用 ipset 的 BypassDeps，供组装链路测试用。
func fakeBypassDeps(enabled bool) *BypassDeps {
	return &BypassDeps{
		Enabled:       enabled,
		DefaultTTLSec: 60,
		MaxTTLSec:     600,
		IpsetRun:      func(context.Context, ...string) error { return nil },
		IpsetList:     func(context.Context, string) (string, error) { return "", nil },
	}
}

// buildHTTPHandler 是「AuthMiddleware 必须包住整个 mux、且 deps.Bypass 必须在
// NewMux 之前赋值」这条安全属性的唯一测试点。这条属性以前内联在 daemon.Run
// 里，只能靠人工 review 保证；提出来之后才能被测试锁死。
func TestBuildHTTPHandlerWrapsAuthAroundWholeMux(t *testing.T) {
	deps := APIDeps{Supervisor: newTestSupervisor(t), Bypass: fakeBypassDeps(true)}
	h := buildHTTPHandler(deps, "secret")

	// LAN + 正确 token 读 bypass 集合：读操作只给 loopback，中间件必须挡在
	// mux 之前——否则 bypass handler 自己完全不做来源校验，会直接放行。
	if code := doReq(t, h, http.MethodGet, "/api/v1/bypass", "192.168.50.80:5555", "secret"); code != http.StatusForbidden {
		t.Errorf("LAN GET bypass: got %d, want 403", code)
	}

	// LAN + 正确 token 写 bypass：白名单端点，应该放行。
	req := httptest.NewRequest(http.MethodPost, "/api/v1/bypass", strings.NewReader(`{"ips":["192.168.50.80"]}`))
	req.RemoteAddr = "192.168.50.80:5555"
	req.Header.Set("Authorization", "Bearer secret")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Errorf("LAN POST bypass: got %d, want 200 (body=%s)", rec.Code, rec.Body.String())
	}

	// LAN + 正确 token 打管理端点：不在白名单里，必须 403——证明中间件包住
	// 的是整个 mux 而不是只包了 bypass 这一个端点。
	if code := doReq(t, h, http.MethodPost, "/api/v1/shutdown", "192.168.50.80:5555", "secret"); code != http.StatusForbidden {
		t.Errorf("LAN POST shutdown: got %d, want 403", code)
	}

	// loopback 读 bypass：免 token 全权。
	if code := doReq(t, h, http.MethodGet, "/api/v1/bypass", "127.0.0.1:5555", ""); code != http.StatusOK {
		t.Errorf("loopback GET bypass: got %d, want 200", code)
	}
}

// Bypass 为 nil 或 Enabled=false 时，/api/v1/bypass 端点在 mux 里根本不存在
// （NewMux 的注册条件），LAN 请求必须 403，且即便走 loopback 绕过中间件也该
// 命中 404——证明"未启用"不只是鉴权层面的拒绝，路由本身也没有注册。
func TestBuildHTTPHandlerBypassNilOrDisabledDoesNotRegisterEndpoint(t *testing.T) {
	sup := newTestSupervisor(t)
	cases := []struct {
		name   string
		bypass *BypassDeps
	}{
		{"nil", nil},
		{"disabled", fakeBypassDeps(false)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			deps := APIDeps{Supervisor: sup, Bypass: tc.bypass}
			h := buildHTTPHandler(deps, "secret")

			if code := doReq(t, h, http.MethodPost, "/api/v1/bypass", "192.168.50.80:5555", "secret"); code != http.StatusForbidden {
				t.Errorf("LAN: got %d, want 403", code)
			}

			req := httptest.NewRequest(http.MethodPost, "/api/v1/bypass", strings.NewReader(`{"ips":["192.168.50.80"]}`))
			req.RemoteAddr = "127.0.0.1:5555"
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)
			if rec.Code != http.StatusNotFound {
				t.Errorf("loopback: got %d, want 404 (route must not be registered)", rec.Code)
			}
		})
	}
}
