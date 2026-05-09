package install

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRenderURL(t *testing.T) {
	cases := []struct {
		prefix, tmpl, version, want string
	}{
		{"", "https://gh/v{version}.tgz", "1.0.0", "https://gh/v1.0.0.tgz"},
		{"https://mirror/", "https://gh/v{version}.tgz", "1.0.0", "https://mirror/https://gh/v1.0.0.tgz"},
		{"https://mirror", "https://gh/v{version}.tgz", "1.0.0", "https://mirror/https://gh/v1.0.0.tgz"},
	}
	for _, c := range cases {
		if got := RenderURL(c.prefix, c.tmpl, c.version); got != c.want {
			t.Fatalf("prefix=%q want %q got %q", c.prefix, c.want, got)
		}
	}
}

func TestDownloadFileAtomic(t *testing.T) {
	payload := strings.Repeat("hello-", 1000)
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, payload)
	}))
	defer ts.Close()
	dir := t.TempDir()
	target := filepath.Join(dir, "out", "file.bin")
	if err := DownloadFile(ts.URL, target, 1, 5); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(target)
	if string(data) != payload {
		t.Fatal("payload mismatch")
	}
	// 没有遗留 tmp
	if _, err := os.Stat(target + ".tmp"); !os.IsNotExist(err) {
		t.Fatal("tmp survived")
	}
}

func TestDownloadFileRetries(t *testing.T) {
	var attempts int
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		if attempts < 3 {
			http.Error(w, "boom", http.StatusInternalServerError)
			return
		}
		_, _ = io.WriteString(w, "ok")
	}))
	defer ts.Close()
	dir := t.TempDir()
	target := filepath.Join(dir, "f")
	if err := DownloadFile(ts.URL, target, 5, 3); err != nil {
		t.Fatal(err)
	}
	if attempts != 3 {
		t.Fatalf("attempts=%d want 3", attempts)
	}
}

func TestDownloadFileFailsAfterMaxRetries(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer ts.Close()
	dir := t.TempDir()
	err := DownloadFile(ts.URL, filepath.Join(dir, "x"), 1, 1)
	if err == nil {
		t.Fatal("expected error")
	}
}
