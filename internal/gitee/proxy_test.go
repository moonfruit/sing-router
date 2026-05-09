package gitee

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// newTestProxy 用一个 httptest 模拟 gitee 上游，返回 proxy handler 与上游 URL。
func newTestProxy(t *testing.T, upstream http.Handler, token string) (http.Handler, *httptest.Server) {
	t.Helper()
	srv := httptest.NewServer(upstream)
	t.Cleanup(srv.Close)
	c := &Client{
		APIBase: srv.URL,
		Owner:   "moonfruit",
		Repo:    "private",
		Token:   token,
	}
	return c.NewProxyHandler(), srv
}

func TestProxy_ForwardsAndAttachesToken(t *testing.T) {
	var seenRef, seenToken, seenPath string
	upstream := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seenRef = r.URL.Query().Get("ref")
		seenToken = r.URL.Query().Get("access_token")
		seenPath = r.URL.Path
		w.Header().Set("ETag", `"upstream"`)
		w.Header().Set("Content-Type", "application/octet-stream")
		_, _ = io.WriteString(w, "rule-set-bytes")
	})
	h, _ := newTestProxy(t, upstream, "tk-secret")

	req := httptest.NewRequest(http.MethodGet, ProxyPathPrefix+"main/private/rules/geoip-cn.srs", nil)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d", rr.Code)
	}
	if seenRef != "main" {
		t.Fatalf("ref = %q", seenRef)
	}
	if seenToken != "tk-secret" {
		t.Fatalf("access_token not forwarded: %q", seenToken)
	}
	if seenPath != "/repos/moonfruit/private/raw/private/rules/geoip-cn.srs" {
		t.Fatalf("upstream path = %q", seenPath)
	}
	if rr.Body.String() != "rule-set-bytes" {
		t.Fatalf("body mismatch: %q", rr.Body.String())
	}
	if rr.Header().Get("ETag") != `"upstream"` {
		t.Fatalf("ETag not passed through: %q", rr.Header().Get("ETag"))
	}
}

func TestProxy_HeadOmitsBody(t *testing.T) {
	upstream := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("ETag", `"x"`)
		_, _ = io.WriteString(w, "body-should-not-arrive")
	})
	h, _ := newTestProxy(t, upstream, "tk")
	req := httptest.NewRequest(http.MethodHead, ProxyPathPrefix+"main/x", nil)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d", rr.Code)
	}
	if rr.Body.Len() != 0 {
		t.Fatalf("HEAD should have empty body, got %q", rr.Body.String())
	}
	if rr.Header().Get("ETag") != `"x"` {
		t.Fatal("HEAD should still convey ETag")
	}
}

func TestProxy_PassesIfNoneMatch(t *testing.T) {
	var seenIfNoneMatch string
	upstream := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seenIfNoneMatch = r.Header.Get("If-None-Match")
		w.WriteHeader(http.StatusNotModified)
	})
	h, _ := newTestProxy(t, upstream, "tk")
	req := httptest.NewRequest(http.MethodGet, ProxyPathPrefix+"main/x", nil)
	req.Header.Set("If-None-Match", `"client-etag"`)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusNotModified {
		t.Fatalf("status = %d", rr.Code)
	}
	if seenIfNoneMatch != `"client-etag"` {
		t.Fatalf("If-None-Match not forwarded: %q", seenIfNoneMatch)
	}
}

func TestProxy_4xxBodyNotLeaked(t *testing.T) {
	const tokenLeak = "tk-leak-XYZ"
	upstream := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// gitee 真实场景中错误 body 可能含 token 回显，模拟之。
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = io.WriteString(w, `{"message": "invalid token: `+tokenLeak+`"}`)
	})
	h, _ := newTestProxy(t, upstream, tokenLeak)
	req := httptest.NewRequest(http.MethodGet, ProxyPathPrefix+"main/x", nil)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d", rr.Code)
	}
	if strings.Contains(rr.Body.String(), tokenLeak) {
		t.Fatalf("token leaked through 4xx body: %q", rr.Body.String())
	}
}

func TestProxy_RejectsBadMethod(t *testing.T) {
	h, _ := newTestProxy(t, http.NewServeMux(), "tk")
	for _, m := range []string{http.MethodPost, http.MethodPut, http.MethodDelete, http.MethodPatch} {
		req := httptest.NewRequest(m, ProxyPathPrefix+"main/x", nil)
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, req)
		if rr.Code != http.StatusMethodNotAllowed {
			t.Fatalf("%s: status = %d", m, rr.Code)
		}
	}
}

func TestProxy_RejectsBadPaths(t *testing.T) {
	h, _ := newTestProxy(t, http.NewServeMux(), "tk")
	cases := []string{
		ProxyPathPrefix,                 // 缺 ref/path
		ProxyPathPrefix + "main",        // 缺 path
		ProxyPathPrefix + "main/",       // 空 path
		ProxyPathPrefix + "../main/x",   // ref 注入
		ProxyPathPrefix + "main/../etc", // path 注入
		ProxyPathPrefix + "main/./x",    // path 含 .
		ProxyPathPrefix + "main//x",     // 空段
	}
	for _, p := range cases {
		req := httptest.NewRequest(http.MethodGet, p, nil)
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, req)
		if rr.Code != http.StatusBadRequest {
			t.Fatalf("path %q: status = %d (want 400)", p, rr.Code)
		}
	}
}

func TestProxy_UpstreamConnectFailReturns502(t *testing.T) {
	c := &Client{
		APIBase: "http://127.0.0.1:1", // 必失败
		Owner:   "o",
		Repo:    "r",
		Token:   "tk",
	}
	h := c.NewProxyHandler()
	req := httptest.NewRequest(http.MethodGet, ProxyPathPrefix+"main/x", nil)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusBadGateway {
		t.Fatalf("status = %d (want 502)", rr.Code)
	}
}

func TestProxy_RejectsBadPrefix(t *testing.T) {
	h, _ := newTestProxy(t, http.NewServeMux(), "tk")
	req := httptest.NewRequest(http.MethodGet, "/wrong/prefix/main/x", nil)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d", rr.Code)
	}
}
