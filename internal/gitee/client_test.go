package gitee

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/moonfruit/sing-router/internal/config"
)

func newTestClient(token string) *Client {
	return NewClient(config.GiteeConfig{
		Token: token,
		Owner: "moonfruit",
		Repo:  "private",
	})
}

func TestRawURL_IncludesRefAndToken(t *testing.T) {
	c := newTestClient("tk-secret")
	u := c.RawURL("binary", "version.txt")

	parsed, err := url.Parse(u)
	if err != nil {
		t.Fatal(err)
	}
	if parsed.Path != "/api/v5/repos/moonfruit/private/raw/version.txt" {
		t.Fatalf("path = %q", parsed.Path)
	}
	if parsed.Query().Get("ref") != "binary" {
		t.Fatalf("ref = %q", parsed.Query().Get("ref"))
	}
	if parsed.Query().Get("access_token") != "tk-secret" {
		t.Fatalf("access_token = %q", parsed.Query().Get("access_token"))
	}
}

func TestRawURL_EmptyTokenOmitsQuery(t *testing.T) {
	c := newTestClient("")
	u := c.RawURL("main", "config.json")
	parsed, _ := url.Parse(u)
	if parsed.Query().Has("access_token") {
		t.Fatalf("access_token should not appear when empty: %s", u)
	}
	if parsed.Query().Get("ref") != "main" {
		t.Fatalf("ref = %q", parsed.Query().Get("ref"))
	}
}

func TestRawURL_TrimsLeadingSlashInPath(t *testing.T) {
	c := newTestClient("tk")
	u := c.RawURL("main", "/private/rules/geoip-cn.srs")
	parsed, _ := url.Parse(u)
	if parsed.Path != "/api/v5/repos/moonfruit/private/raw/private/rules/geoip-cn.srs" {
		t.Fatalf("path = %q", parsed.Path)
	}
}

func TestRawURL_DifferentRefs(t *testing.T) {
	c := newTestClient("tk")
	for _, ref := range []string{"binary", "main", "master", "release"} {
		u := c.RawURL(ref, "x")
		if !strings.Contains(u, "ref="+ref) {
			t.Fatalf("ref %q missing from url: %s", ref, u)
		}
	}
}

func TestDownloadRaw_PassesETagAndAttachesToken(t *testing.T) {
	const payload = "version-1.13.5\n"
	const etag = `W/"abc123"`

	var receivedToken string
	var receivedRef string
	var receivedIfNoneMatch atomic.Value
	receivedIfNoneMatch.Store("")

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedToken = r.URL.Query().Get("access_token")
		receivedRef = r.URL.Query().Get("ref")
		receivedIfNoneMatch.Store(r.Header.Get("If-None-Match"))
		if r.Header.Get("If-None-Match") == etag {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		w.Header().Set("ETag", etag)
		_, _ = io.WriteString(w, payload)
	}))
	defer srv.Close()

	c := &Client{
		APIBase: srv.URL,
		Owner:   "moonfruit",
		Repo:    "private",
		Token:   "tk-secret",
	}
	dir := t.TempDir()
	target := filepath.Join(dir, "out", "version.txt")

	changed, err := c.DownloadRaw(context.Background(), "binary", "version.txt", target)
	if err != nil {
		t.Fatal(err)
	}
	if !changed {
		t.Fatal("first download should be changed=true")
	}
	if receivedToken != "tk-secret" {
		t.Fatalf("token not forwarded; got %q", receivedToken)
	}
	if receivedRef != "binary" {
		t.Fatalf("ref not forwarded; got %q", receivedRef)
	}
	if data, _ := os.ReadFile(target); string(data) != payload {
		t.Fatalf("payload mismatch: %q", string(data))
	}

	// 二次下载：服务端见 If-None-Match → 304。
	changed, err = c.DownloadRaw(context.Background(), "binary", "version.txt", target)
	if err != nil {
		t.Fatal(err)
	}
	if changed {
		t.Fatal("304 should yield changed=false")
	}
	if receivedIfNoneMatch.Load().(string) != etag {
		t.Fatalf("If-None-Match not sent; got %q", receivedIfNoneMatch.Load())
	}
}

func TestVersion_TrimmedAndNoTokenInError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "  1.13.5 \n")
	}))
	defer srv.Close()
	c := &Client{APIBase: srv.URL, Owner: "moonfruit", Repo: "private", Token: "tk"}
	v, err := c.Version(context.Background(), "binary", "version.txt")
	if err != nil {
		t.Fatal(err)
	}
	if v != "1.13.5" {
		t.Fatalf("version = %q", v)
	}
}

func TestWrapErr_RedactsToken(t *testing.T) {
	c := &Client{Token: "tk-secret-XYZ"}
	in := errors.New("download failed: http 401: token=tk-secret-XYZ invalid")
	out := c.wrapErr(in)
	if strings.Contains(out.Error(), "tk-secret-XYZ") {
		t.Fatalf("token leaked: %v", out)
	}
	if !strings.Contains(out.Error(), tokenRedactPlaceholder) {
		t.Fatalf("placeholder missing: %v", out)
	}
}

func TestWrapErr_NilOrNoToken(t *testing.T) {
	c := &Client{Token: ""}
	if c.wrapErr(nil) != nil {
		t.Fatal("nil should pass through")
	}
	in := errors.New("plain")
	if c.wrapErr(in).Error() != "plain" {
		t.Fatal("no-token wrapping should be identity")
	}
}

func TestVersion_ErrorContainsNoToken(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "echo: "+r.URL.RawQuery, http.StatusUnauthorized)
	}))
	defer srv.Close()
	c := &Client{APIBase: srv.URL, Owner: "o", Repo: "r", Token: "tk-leak"}
	_, err := c.Version(context.Background(), "main", "v.txt")
	if err == nil {
		t.Fatal("expected error")
	}
	if strings.Contains(err.Error(), "tk-leak") {
		t.Fatalf("token leaked in error: %v", err)
	}
}

func TestRedactToken(t *testing.T) {
	c := &Client{Token: "tk"}
	if got := c.RedactToken("hello tk world"); got != "hello "+tokenRedactPlaceholder+" world" {
		t.Fatalf("got %q", got)
	}
}
