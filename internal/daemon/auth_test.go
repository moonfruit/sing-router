package daemon

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// okHandler 是被中间件包住的下游；命中即写 200。
func okHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
}

// doReq 直接调 ServeHTTP，用 httptest.NewRequest 后覆写 RemoteAddr，
// 从而不依赖真实网络就能模拟 loopback / LAN 两种来源。
func doReq(t *testing.T, h http.Handler, method, path, remoteAddr, token string) int {
	t.Helper()
	req := httptest.NewRequest(method, path, nil)
	req.RemoteAddr = remoteAddr
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec.Code
}

func TestLoopbackBypassesTokenOnEveryEndpoint(t *testing.T) {
	h := AuthMiddleware(okHandler(), AuthConfig{Token: "secret", BypassEnabled: true})
	for _, path := range []string{"/api/v1/status", "/api/v1/shutdown", "/api/v1/bypass"} {
		if code := doReq(t, h, http.MethodPost, path, "127.0.0.1:5555", ""); code != http.StatusOK {
			t.Errorf("loopback %s: got %d, want 200", path, code)
		}
	}
}

func TestLoopbackIPv6AlsoBypassesToken(t *testing.T) {
	h := AuthMiddleware(okHandler(), AuthConfig{Token: "secret", BypassEnabled: true})
	if code := doReq(t, h, http.MethodGet, "/api/v1/status", "[::1]:5555", ""); code != http.StatusOK {
		t.Fatalf("got %d, want 200", code)
	}
}

func TestLANWithValidTokenReachesBypassEndpoint(t *testing.T) {
	h := AuthMiddleware(okHandler(), AuthConfig{Token: "secret", BypassEnabled: true})
	if code := doReq(t, h, http.MethodPost, "/api/v1/bypass", "192.168.50.80:5555", "secret"); code != http.StatusOK {
		t.Fatalf("got %d, want 200", code)
	}
}

// 白名单之外的端点即使 token 正确也不给 LAN——这是本中间件存在的理由。
func TestLANCannotReachManagementEndpoints(t *testing.T) {
	h := AuthMiddleware(okHandler(), AuthConfig{Token: "secret", BypassEnabled: true})
	for _, path := range []string{"/api/v1/shutdown", "/api/v1/restart", "/api/v1/apply", "/api/v1/status"} {
		if code := doReq(t, h, http.MethodPost, path, "192.168.50.80:5555", "secret"); code != http.StatusForbidden {
			t.Errorf("LAN %s: got %d, want 403", path, code)
		}
	}
}

// 读操作只给 loopback：持 token 的客户端能写不能枚举当前放行了谁。
func TestLANCannotReadBypassSet(t *testing.T) {
	h := AuthMiddleware(okHandler(), AuthConfig{Token: "secret", BypassEnabled: true})
	if code := doReq(t, h, http.MethodGet, "/api/v1/bypass", "192.168.50.80:5555", "secret"); code != http.StatusForbidden {
		t.Fatalf("got %d, want 403", code)
	}
}

func TestLANWithBadTokenIsUnauthorized(t *testing.T) {
	h := AuthMiddleware(okHandler(), AuthConfig{Token: "secret", BypassEnabled: true})
	if code := doReq(t, h, http.MethodPost, "/api/v1/bypass", "192.168.50.80:5555", "wrong"); code != http.StatusUnauthorized {
		t.Fatalf("got %d, want 401", code)
	}
}

func TestLANWithoutTokenIsUnauthorized(t *testing.T) {
	h := AuthMiddleware(okHandler(), AuthConfig{Token: "secret", BypassEnabled: true})
	if code := doReq(t, h, http.MethodPost, "/api/v1/bypass", "192.168.50.80:5555", ""); code != http.StatusUnauthorized {
		t.Fatalf("got %d, want 401", code)
	}
}

// bypass 关闭时白名单整体失效，LAN 什么都碰不到。
func TestBypassDisabledClosesLANEntirely(t *testing.T) {
	h := AuthMiddleware(okHandler(), AuthConfig{Token: "secret", BypassEnabled: false})
	if code := doReq(t, h, http.MethodPost, "/api/v1/bypass", "192.168.50.80:5555", "secret"); code != http.StatusForbidden {
		t.Fatalf("got %d, want 403", code)
	}
}

// 转发头由客户端控制，采信即等于没有鉴权。
func TestForwardedHeadersAreIgnored(t *testing.T) {
	h := AuthMiddleware(okHandler(), AuthConfig{Token: "secret", BypassEnabled: true})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/shutdown", nil)
	req.RemoteAddr = "192.168.50.80:5555"
	req.Header.Set("X-Forwarded-For", "127.0.0.1")
	req.Header.Set("X-Real-IP", "127.0.0.1")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("got %d, want 403 (forwarded headers must not grant loopback)", rec.Code)
	}
}

// 解析不出来的 RemoteAddr 一律按非 loopback 处理（fail closed）。
func TestUnparsableRemoteAddrIsNotLoopback(t *testing.T) {
	for _, addr := range []string{"", "garbage", "not-an-ip:1"} {
		if isLoopbackAddr(addr) {
			t.Errorf("%q must not be treated as loopback", addr)
		}
	}
}

func TestIsLoopbackAddrRecognisesLocalRanges(t *testing.T) {
	for _, addr := range []string{"127.0.0.1:1", "127.9.9.9:1", "[::1]:1"} {
		if !isLoopbackAddr(addr) {
			t.Errorf("%q must be loopback", addr)
		}
	}
	for _, addr := range []string{"192.168.50.80:1", "10.0.0.1:1", "[fe80::1]:1"} {
		if isLoopbackAddr(addr) {
			t.Errorf("%q must not be loopback", addr)
		}
	}
}
