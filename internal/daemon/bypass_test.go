package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// fakeIpset 记录收到的每一次 argv，供逐字断言。
type fakeIpset struct {
	mu    sync.Mutex
	calls [][]string
	err   error
}

func (f *fakeIpset) run(_ context.Context, args ...string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, args)
	return f.err
}

func (f *fakeIpset) got(i int) []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls[i]
}

func newBypassMux(fake *fakeIpset) http.Handler {
	return NewMux(APIDeps{Bypass: &BypassDeps{
		Enabled:       true,
		DefaultTTLSec: 120,
		MaxTTLSec:     600,
		IpsetRun:      fake.run,
	}})
}

func postBypass(t *testing.T, h http.Handler, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/bypass", strings.NewReader(body))
	req.RemoteAddr = "127.0.0.1:5555"
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestBypassRegisterInvokesIpsetWithExactArgv(t *testing.T) {
	fake := &fakeIpset{}
	rec := postBypass(t, newBypassMux(fake), `{"ips":["192.168.50.80"],"ttl_sec":120}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d body %s", rec.Code, rec.Body.String())
	}
	want := []string{"-exist", "add", "client_bypass", "192.168.50.80", "timeout", "120"}
	got := fake.got(0)
	if len(got) != len(want) {
		t.Fatalf("argv = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("argv = %v, want %v", got, want)
		}
	}
}

func TestBypassRegisterUsesDefaultTTLWhenOmitted(t *testing.T) {
	fake := &fakeIpset{}
	rec := postBypass(t, newBypassMux(fake), `{"ips":["192.168.50.80"]}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d", rec.Code)
	}
	got := fake.got(0)
	if got[5] != "120" {
		t.Fatalf("ttl arg = %q, want 120", got[5])
	}
}

func TestBypassRegisterAcceptsMultipleIPs(t *testing.T) {
	fake := &fakeIpset{}
	rec := postBypass(t, newBypassMux(fake), `{"ips":["192.168.50.80","192.168.50.81"]}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d", rec.Code)
	}
	if len(fake.calls) != 2 {
		t.Fatalf("expected 2 ipset calls, got %d", len(fake.calls))
	}
}

// 本次仅 IPv4：v6 显式拒绝而非静默丢弃，否则用户注册了却查不出为何不生效。
func TestBypassRejectsIPv6Explicitly(t *testing.T) {
	fake := &fakeIpset{}
	rec := postBypass(t, newBypassMux(fake), `{"ips":["2408:820c::1"]}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status %d, want 400", rec.Code)
	}
	if code := errCode(t, rec); code != "bypass.ipv6_unsupported" {
		t.Fatalf("error code = %q", code)
	}
	if len(fake.calls) != 0 {
		t.Fatal("must not touch ipset on validation failure")
	}
}

// 全有或全无：一个地址不合法则整个请求失败，避免客户端以为全部注册成功。
func TestBypassRejectsWholeRequestOnOneBadIP(t *testing.T) {
	fake := &fakeIpset{}
	rec := postBypass(t, newBypassMux(fake), `{"ips":["192.168.50.80","nonsense"]}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status %d, want 400", rec.Code)
	}
	if code := errCode(t, rec); code != "bypass.bad_ip" {
		t.Fatalf("error code = %q", code)
	}
	if len(fake.calls) != 0 {
		t.Fatal("must not partially apply")
	}
}

func TestBypassRejectsTTLAboveMax(t *testing.T) {
	fake := &fakeIpset{}
	rec := postBypass(t, newBypassMux(fake), `{"ips":["192.168.50.80"],"ttl_sec":99999}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status %d, want 400", rec.Code)
	}
	if code := errCode(t, rec); code != "bypass.bad_ttl" {
		t.Fatalf("error code = %q", code)
	}
}

func TestBypassRejectsEmptyAndOversizedIPList(t *testing.T) {
	fake := &fakeIpset{}
	if rec := postBypass(t, newBypassMux(fake), `{"ips":[]}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("empty list: status %d, want 400", rec.Code)
	}
	many := make([]string, 0, 17)
	for i := 0; i < 17; i++ {
		many = append(many, `"192.168.50.80"`)
	}
	body := `{"ips":[` + strings.Join(many, ",") + `]}`
	if rec := postBypass(t, newBypassMux(fake), body); rec.Code != http.StatusBadRequest {
		t.Fatalf("oversized list: status %d, want 400", rec.Code)
	}
}

func TestBypassRevokeInvokesIpsetDel(t *testing.T) {
	fake := &fakeIpset{}
	req := httptest.NewRequest(http.MethodDelete, "/api/v1/bypass",
		strings.NewReader(`{"ips":["192.168.50.80"]}`))
	req.RemoteAddr = "127.0.0.1:5555"
	rec := httptest.NewRecorder()
	newBypassMux(fake).ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d", rec.Code)
	}
	want := []string{"-exist", "del", "client_bypass", "192.168.50.80"}
	got := fake.got(0)
	if len(got) != len(want) {
		t.Fatalf("argv = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("argv = %v, want %v", got, want)
		}
	}
}

func TestBypassListParsesMembersWithTTL(t *testing.T) {
	out := "Name: client_bypass\nType: hash:ip\nHeader: family inet timeout 120\n" +
		"Members:\n192.168.50.80 timeout 118\n192.168.50.81 timeout 0\n"
	mux := NewMux(APIDeps{Bypass: &BypassDeps{
		Enabled: true, DefaultTTLSec: 120, MaxTTLSec: 600,
		IpsetList: func(_ context.Context, _ string) (string, error) { return out, nil },
	}})
	req := httptest.NewRequest(http.MethodGet, "/api/v1/bypass", nil)
	req.RemoteAddr = "127.0.0.1:5555"
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d", rec.Code)
	}
	var body struct {
		Entries []struct {
			IP         string `json:"ip"`
			TimeoutSec int    `json:"timeout_sec"`
		} `json:"entries"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if len(body.Entries) != 2 {
		t.Fatalf("entries = %+v", body.Entries)
	}
	if body.Entries[0].IP != "192.168.50.80" || body.Entries[0].TimeoutSec != 118 {
		t.Fatalf("entry 0 = %+v", body.Entries[0])
	}
}

// 未启用时端点根本不注册，命中 mux 的 404 而非 handler。
func TestBypassEndpointAbsentWhenDisabled(t *testing.T) {
	mux := NewMux(APIDeps{})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/bypass", strings.NewReader(`{"ips":["1.2.3.4"]}`))
	req.RemoteAddr = "127.0.0.1:5555"
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status %d, want 404", rec.Code)
	}
}

// ipset 调用失败时，register / revoke / list 三条路径都要能返回
// 500 + bypass.ipset_failed——这条错误分支此前一次都没被断言过。

func TestBypassRegisterIpsetFailureReturns500(t *testing.T) {
	fake := &fakeIpset{err: errors.New("ipset: kernel error")}
	rec := postBypass(t, newBypassMux(fake), `{"ips":["192.168.50.80"]}`)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status %d, want 500", rec.Code)
	}
	if code := errCode(t, rec); code != "bypass.ipset_failed" {
		t.Fatalf("error code = %q", code)
	}
}

func TestBypassRevokeIpsetFailureReturns500(t *testing.T) {
	fake := &fakeIpset{err: errors.New("ipset: kernel error")}
	req := httptest.NewRequest(http.MethodDelete, "/api/v1/bypass",
		strings.NewReader(`{"ips":["192.168.50.80"]}`))
	req.RemoteAddr = "127.0.0.1:5555"
	rec := httptest.NewRecorder()
	newBypassMux(fake).ServeHTTP(rec, req)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status %d, want 500", rec.Code)
	}
	if code := errCode(t, rec); code != "bypass.ipset_failed" {
		t.Fatalf("error code = %q", code)
	}
}

func TestBypassListIpsetFailureReturns500(t *testing.T) {
	mux := NewMux(APIDeps{Bypass: &BypassDeps{
		Enabled: true, DefaultTTLSec: 120, MaxTTLSec: 600,
		IpsetList: func(_ context.Context, _ string) (string, error) {
			return "", errors.New("ipset: no such set")
		},
	}})
	req := httptest.NewRequest(http.MethodGet, "/api/v1/bypass", nil)
	req.RemoteAddr = "127.0.0.1:5555"
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status %d, want 500", rec.Code)
	}
	if code := errCode(t, rec); code != "bypass.ipset_failed" {
		t.Fatalf("error code = %q", code)
	}
}

// 多地址循环中途失败时不回滚，但必须把已成功的地址通过 detail 回传，
// 否则客户端无法判断哪些还需要在下一轮重试。
func TestBypassRegisterPartialFailureReportsSucceeded(t *testing.T) {
	fake := &fakeIpset{}
	calls := 0
	runFn := func(ctx context.Context, args ...string) error {
		calls++
		fake.mu.Lock()
		fake.calls = append(fake.calls, args)
		fake.mu.Unlock()
		if calls == 2 {
			return errors.New("ipset: boom on second call")
		}
		return nil
	}
	mux := NewMux(APIDeps{Bypass: &BypassDeps{
		Enabled: true, DefaultTTLSec: 120, MaxTTLSec: 600,
		IpsetRun: runFn,
	}})
	rec := postBypass(t, mux, `{"ips":["192.168.50.80","192.168.50.81"]}`)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status %d, want 500", rec.Code)
	}
	var body struct {
		Error struct {
			Detail struct {
				Succeeded []string `json:"succeeded"`
			} `json:"detail"`
		} `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if len(body.Error.Detail.Succeeded) != 1 || body.Error.Detail.Succeeded[0] != "192.168.50.80" {
		t.Fatalf("detail.succeeded = %+v", body.Error.Detail.Succeeded)
	}
}

// json body 超过上限必须在参数校验之前被挡住，且不能碰 ipset。
func TestBypassRegisterRejectsOversizedBody(t *testing.T) {
	fake := &fakeIpset{}
	huge := strings.Repeat("a", maxBypassBodyBytes+1)
	body := `{"ips":["` + huge + `"]}`
	rec := postBypass(t, newBypassMux(fake), body)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status %d, want 400", rec.Code)
	}
	if code := errCode(t, rec); code != "bypass.bad_request" {
		t.Fatalf("error code = %q", code)
	}
	if len(fake.calls) != 0 {
		t.Fatal("must not touch ipset on oversized body")
	}
}

func errCode(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	var body struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode error body %q: %v", rec.Body.String(), err)
	}
	return body.Error.Code
}
