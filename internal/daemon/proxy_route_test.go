package daemon

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestNewMux_GiteeProxyDisabled — 当 GiteeProxy 为 nil 时返回 503，避免静默 404 误导。
func TestNewMux_GiteeProxyDisabled(t *testing.T) {
	mux := NewMux(APIDeps{
		Supervisor: New(SupervisorConfig{Emitter: newTestEmitter(t)}),
		Ctx:        context.Background(),
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/api/v1/proxy/gitee/main/x")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("status = %d (want 503)", resp.StatusCode)
	}
}

// TestNewMux_GiteeProxyDelegates — 配了 GiteeProxy 时，请求会透传到该 handler。
func TestNewMux_GiteeProxyDelegates(t *testing.T) {
	called := false
	stubProxy := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		_, _ = io.WriteString(w, "stub-payload-"+r.URL.Path)
	})
	mux := NewMux(APIDeps{
		Supervisor: New(SupervisorConfig{Emitter: newTestEmitter(t)}),
		Ctx:        context.Background(),
		GiteeProxy: stubProxy,
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/api/v1/proxy/gitee/main/private/rules/x.srs")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if string(body) != "stub-payload-/api/v1/proxy/gitee/main/private/rules/x.srs" {
		t.Fatalf("body mismatch: %q", string(body))
	}
	if !called {
		t.Fatal("stubProxy never invoked")
	}
}
