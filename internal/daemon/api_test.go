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
func TestServeHTTPListensOnIPv4Only(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	mux := NewMux(APIDeps{Supervisor: newTestSupervisor(t), Version: "t", Rundir: "/tmp"})
	port := freePort(t)
	addr := fmt.Sprintf("127.0.0.1:%d", port)
	done := make(chan error, 1)
	go func() { done <- ServeHTTP(ctx, mux, addr) }()

	// 等 listener 就绪
	var resp *http.Response
	var err error
	for i := 0; i < 50; i++ {
		resp, err = http.Get("http://" + addr + "/api/v1/status")
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
