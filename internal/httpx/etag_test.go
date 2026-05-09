package httpx

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

func TestDownload_FreshThenNotModified(t *testing.T) {
	const payload = "hello-world"
	const etag = `"v1"`

	var got304 atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("If-None-Match") == etag {
			got304.Store(true)
			w.WriteHeader(http.StatusNotModified)
			return
		}
		w.Header().Set("ETag", etag)
		_, _ = io.WriteString(w, payload)
	}))
	defer srv.Close()

	dir := t.TempDir()
	target := filepath.Join(dir, "out", "file.bin")

	// 第一次下载 → 200，写文件 + .etag。
	changed, err := Download(context.Background(), srv.URL, target, Options{})
	if err != nil {
		t.Fatalf("first download: %v", err)
	}
	if !changed {
		t.Fatal("first download should be changed=true")
	}
	if data, _ := os.ReadFile(target); string(data) != payload {
		t.Fatalf("payload mismatch: %q", string(data))
	}
	if et, _ := os.ReadFile(target + ".etag"); string(et) != etag {
		t.Fatalf(".etag mismatch: %q", string(et))
	}

	// 第二次下载 → 服务器看到 If-None-Match 返回 304；本地文件不动。
	changed, err = Download(context.Background(), srv.URL, target, Options{})
	if err != nil {
		t.Fatalf("second download: %v", err)
	}
	if changed {
		t.Fatal("second download should be changed=false (304)")
	}
	if !got304.Load() {
		t.Fatal("server didn't observe If-None-Match")
	}

	// 没有遗留 .tmp。
	if _, err := os.Stat(target + ".tmp"); !os.IsNotExist(err) {
		t.Fatal(".tmp survived after success")
	}
}

func TestDownload_ETagChange_OverwritesAtomically(t *testing.T) {
	var v atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cur := v.Add(1)
		w.Header().Set("ETag", `"`+strings.Repeat("x", int(cur))+`"`)
		_, _ = io.WriteString(w, "v"+strings.Repeat("x", int(cur)))
	}))
	defer srv.Close()

	dir := t.TempDir()
	target := filepath.Join(dir, "f")
	if _, err := Download(context.Background(), srv.URL, target, Options{}); err != nil {
		t.Fatal(err)
	}
	first, _ := os.ReadFile(target)
	firstETag, _ := os.ReadFile(target + ".etag")

	// 第二次：服务器 etag 变了，应该重写。
	changed, err := Download(context.Background(), srv.URL, target, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if !changed {
		t.Fatal("etag changed → expected changed=true")
	}
	second, _ := os.ReadFile(target)
	secondETag, _ := os.ReadFile(target + ".etag")
	if string(first) == string(second) {
		t.Fatal("file content didn't change")
	}
	if string(firstETag) == string(secondETag) {
		t.Fatal(".etag didn't update")
	}
}

func TestDownload_NoETag_RemovesStale(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "no-etag-payload")
	}))
	defer srv.Close()

	dir := t.TempDir()
	target := filepath.Join(dir, "f")
	// 预置一个旧 .etag —— 服务端不返 ETag，应被清理掉。
	if err := os.WriteFile(target+".etag", []byte(`"stale"`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Download(context.Background(), srv.URL, target, Options{}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(target + ".etag"); !os.IsNotExist(err) {
		t.Fatal("stale .etag should be removed when server omits ETag")
	}
}

func TestDownload_RetriesOn5xx(t *testing.T) {
	var attempts atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if attempts.Add(1) < 3 {
			http.Error(w, "boom", http.StatusInternalServerError)
			return
		}
		_, _ = io.WriteString(w, "ok")
	}))
	defer srv.Close()

	dir := t.TempDir()
	if _, err := Download(context.Background(), srv.URL, filepath.Join(dir, "f"), Options{Retries: 5}); err != nil {
		t.Fatal(err)
	}
	if attempts.Load() != 3 {
		t.Fatalf("attempts=%d want 3", attempts.Load())
	}
}

func TestDownload_NoRetryOn4xx(t *testing.T) {
	var attempts atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts.Add(1)
		http.Error(w, "nope", http.StatusUnauthorized)
	}))
	defer srv.Close()

	dir := t.TempDir()
	_, err := Download(context.Background(), srv.URL, filepath.Join(dir, "f"), Options{Retries: 5})
	if err == nil {
		t.Fatal("expected error")
	}
	if Status(err) != http.StatusUnauthorized {
		t.Fatalf("status=%d want 401", Status(err))
	}
	if attempts.Load() != 1 {
		t.Fatalf("attempts=%d want 1 (no retry on 4xx)", attempts.Load())
	}
}

func TestDownload_CustomHeadersForwarded(t *testing.T) {
	var seen string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = r.Header.Get("X-Test")
		_, _ = io.WriteString(w, "ok")
	}))
	defer srv.Close()

	dir := t.TempDir()
	headers := http.Header{}
	headers.Set("X-Test", "marker")
	if _, err := Download(context.Background(), srv.URL, filepath.Join(dir, "f"), Options{Headers: headers}); err != nil {
		t.Fatal(err)
	}
	if seen != "marker" {
		t.Fatalf("custom header lost: %q", seen)
	}
}

func TestDownload_ContextCancel(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer srv.Close()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	dir := t.TempDir()
	_, err := Download(ctx, srv.URL, filepath.Join(dir, "f"), Options{Retries: 3})
	if err == nil {
		t.Fatal("expected error from canceled ctx")
	}
}
