// Package cli 实现 sing-router 的 cobra 子命令。
package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"syscall"
	"time"
)

// APIError 反序列化 daemon 返回的 4xx/5xx JSON 错误。
type APIError struct {
	Status  int
	Code    string
	Message string
	Detail  any
}

func (e *APIError) Error() string {
	return fmt.Sprintf("api error %d %s: %s", e.Status, e.Code, e.Message)
}

// HTTPClient 是 CLI 用的极简 daemon 客户端。
type HTTPClient struct {
	base string
	hc   *http.Client
}

// NewHTTPClient 构造客户端；base 是 http://host:port，无尾斜杠。
func NewHTTPClient(base string) *HTTPClient {
	return &HTTPClient{base: base, hc: &http.Client{Timeout: 30 * time.Second}}
}

// GetJSON 发 GET，把 200 body 解码到 out（out 可为 nil）。
func (c *HTTPClient) GetJSON(path string, out any) error {
	return c.do(http.MethodGet, path, nil, out)
}

// PostJSON 发 POST。
func (c *HTTPClient) PostJSON(path string, body, out any) error {
	return c.do(http.MethodPost, path, body, out)
}

// GetStream 返回原始 *http.Response，调用方负责关 Body；用于 SSE。
func (c *HTTPClient) GetStream(path string, query url.Values) (*http.Response, error) {
	full := c.base + path
	if len(query) > 0 {
		full += "?" + query.Encode()
	}
	req, err := http.NewRequest(http.MethodGet, full, nil)
	if err != nil {
		return nil, err
	}
	return c.hc.Do(req)
}

func (c *HTTPClient) do(method, path string, body, out any) error {
	var buf io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		buf = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, c.base+path, buf)
	if err != nil {
		return err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.hc.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode >= 400 {
		var raw struct {
			Error struct {
				Code    string `json:"code"`
				Message string `json:"message"`
				Detail  any    `json:"detail"`
			} `json:"error"`
		}
		_ = json.NewDecoder(resp.Body).Decode(&raw)
		return &APIError{
			Status:  resp.StatusCode,
			Code:    raw.Error.Code,
			Message: raw.Error.Message,
			Detail:  raw.Error.Detail,
		}
	}
	if out == nil {
		_, _ = io.Copy(io.Discard, resp.Body)
		return nil
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

// IsDaemonNotRunning 报告 err 是否表示守护进程未跑（dial 拒绝 / EOF 等）。
func IsDaemonNotRunning(err error) bool {
	if err == nil {
		return false
	}
	var sysErr syscall.Errno
	if errors.As(err, &sysErr) && sysErr == syscall.ECONNREFUSED {
		return true
	}
	s := err.Error()
	return strings.Contains(s, "connection refused") || strings.Contains(s, "no such host")
}
