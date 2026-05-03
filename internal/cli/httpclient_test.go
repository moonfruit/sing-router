package cli

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestHTTPClientGet(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/status" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"daemon":{"state":"running"}}`))
	}))
	defer ts.Close()

	c := NewHTTPClient(ts.URL)
	var body map[string]any
	if err := c.GetJSON("/api/v1/status", &body); err != nil {
		t.Fatal(err)
	}
	daemon := body["daemon"].(map[string]any)
	if daemon["state"] != "running" {
		t.Fatal("decode failed")
	}
}

func TestHTTPClientPostError(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusConflict)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"error": map[string]any{
				"code":    "daemon.state_conflict",
				"message": "not running",
			},
		})
	}))
	defer ts.Close()

	c := NewHTTPClient(ts.URL)
	err := c.PostJSON("/api/v1/restart", nil, nil)
	if err == nil {
		t.Fatal("expected error")
	}
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("err type %T", err)
	}
	if apiErr.Code != "daemon.state_conflict" {
		t.Fatalf("code: %s", apiErr.Code)
	}
}

func TestHTTPClientNotRunning(t *testing.T) {
	c := NewHTTPClient("http://127.0.0.1:1") // 拒绝
	err := c.GetJSON("/api/v1/status", nil)
	if err == nil {
		t.Fatal("expected dial error")
	}
	if !IsDaemonNotRunning(err) {
		t.Fatalf("expected IsDaemonNotRunning true, got %v", err)
	}
}
