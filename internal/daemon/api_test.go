package daemon

import (
	"context"
	"encoding/json"
	"fmt"
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

func TestAPIReapplyRulesRequiresRunning(t *testing.T) {
	// supervisor 默认在 booting 态 → reapply-rules 应当 409
	sup := New(SupervisorConfig{Emitter: newTestEmitter(t)})
	mux := NewMux(APIDeps{Supervisor: sup, ReapplyRules: func(_ context.Context) error { return nil }})
	ts := httptest.NewServer(mux)
	defer ts.Close()
	resp, _ := http.Post(ts.URL+"/api/v1/reapply-rules", "application/json", nil)
	if resp.StatusCode != 409 {
		t.Fatalf("status: %d", resp.StatusCode)
	}
}
