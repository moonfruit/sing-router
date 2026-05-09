package daemon

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func startTCPListener(t *testing.T) (net.Listener, int) {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = l.Close() })
	return l, l.Addr().(*net.TCPAddr).Port
}

func TestReadyCheckSuccess(t *testing.T) {
	_, p1 := startTCPListener(t)
	_, p2 := startTCPListener(t)

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/version") {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"version":"1.13.5"}`)
	}))
	t.Cleanup(ts.Close)

	cfg := ReadyConfig{
		TCPDials:     []string{fmt.Sprintf("127.0.0.1:%d", p1), fmt.Sprintf("127.0.0.1:%d", p2)},
		ClashAPIURL:  ts.URL + "/version",
		TotalTimeout: 2 * time.Second,
		Interval:     50 * time.Millisecond,
	}
	if err := ReadyCheck(context.Background(), cfg); err != nil {
		t.Fatalf("ReadyCheck: %v", err)
	}
}

func TestReadyCheckTimeoutOnDial(t *testing.T) {
	cfg := ReadyConfig{
		TCPDials:     []string{"127.0.0.1:1"}, // 端口 1 几乎肯定没监听
		TotalTimeout: 200 * time.Millisecond,
		Interval:     50 * time.Millisecond,
	}
	err := ReadyCheck(context.Background(), cfg)
	if err == nil {
		t.Fatal("expected timeout error")
	}
}

func TestReadyCheckTimeoutOnClashAPI(t *testing.T) {
	_, p := startTCPListener(t)
	cfg := ReadyConfig{
		TCPDials:     []string{fmt.Sprintf("127.0.0.1:%d", p)},
		ClashAPIURL:  "http://127.0.0.1:1/version", // 拒绝
		TotalTimeout: 200 * time.Millisecond,
		Interval:     50 * time.Millisecond,
	}
	if err := ReadyCheck(context.Background(), cfg); err == nil {
		t.Fatal("expected error from clash api timeout")
	}
}

func TestReadyCheckClashAPISkipWhenURLEmpty(t *testing.T) {
	_, p := startTCPListener(t)
	cfg := ReadyConfig{
		TCPDials:     []string{fmt.Sprintf("127.0.0.1:%d", p)},
		ClashAPIURL:  "",
		TotalTimeout: 1 * time.Second,
		Interval:     50 * time.Millisecond,
	}
	if err := ReadyCheck(context.Background(), cfg); err != nil {
		t.Fatalf("with empty ClashAPIURL: %v", err)
	}
}
