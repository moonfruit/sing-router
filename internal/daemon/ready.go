package daemon

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"time"
)

// ReadyConfig 控制 readiness 检测：拨通所有 TCPDials + 可选 GET ClashAPIURL。
type ReadyConfig struct {
	TCPDials     []string      // host:port 列表
	ClashAPIURL  string        // 例如 http://127.0.0.1:9999/version；空 = 跳过
	TotalTimeout time.Duration // 总超时
	Interval     time.Duration // 轮询间隔
}

// ReadyCheck 阻塞直到全部检测项通过，或超时。
func ReadyCheck(ctx context.Context, cfg ReadyConfig) error {
	deadline := time.Now().Add(cfg.TotalTimeout)
	interval := cfg.Interval
	if interval <= 0 {
		interval = 200 * time.Millisecond
	}

	var lastErr error
	for time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		if err := checkOnce(cfg); err == nil {
			return nil
		} else {
			lastErr = err
		}
		time.Sleep(interval)
	}
	if lastErr != nil {
		return fmt.Errorf("ready check timed out: %w", lastErr)
	}
	return fmt.Errorf("ready check timed out")
}

func checkOnce(cfg ReadyConfig) error {
	dialer := net.Dialer{Timeout: 500 * time.Millisecond}
	for _, addr := range cfg.TCPDials {
		c, err := dialer.Dial("tcp", addr)
		if err != nil {
			return fmt.Errorf("dial %s: %w", addr, err)
		}
		_ = c.Close()
	}
	if cfg.ClashAPIURL != "" {
		client := &http.Client{Timeout: 500 * time.Millisecond}
		resp, err := client.Get(cfg.ClashAPIURL)
		if err != nil {
			return fmt.Errorf("clash api: %w", err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode >= 300 {
			return fmt.Errorf("clash api status %d", resp.StatusCode)
		}
	}
	return nil
}
