// Package httpx 提供 sing-router 共用的 HTTP 工具。
//
// 当前提供 etag 增量下载：把 URL 内容下载到本地路径，自动维护一个
// 旁路 .etag 文件用于 If-None-Match。文件写入是原子的（tmp + rename）。
//
// 调用方负责构造 URL（含任何 query / token），本包不做凭证管理。错误信息
// 中包含的 URL 由调用方自行屏蔽敏感字段（见 internal/gitee）。
package httpx

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// DefaultTimeout 是单次 HTTP 请求的默认超时。
const DefaultTimeout = 60 * time.Second

// Options 控制 Download 的行为。零值即可工作（默认 60s 超时、无重试、无额外 header）。
type Options struct {
	Client  *http.Client
	Headers http.Header
	// Retries 是 5xx / 网络错误的重试次数（不含首次）。4xx 不重试。
	Retries int
}

// Download 把 url 内容增量下载到 target。
//
// 行为：
//   - 若 target+".etag" 存在，自动注入 If-None-Match。
//   - HTTP 304: changed=false，target 与 .etag 文件均不动。
//   - HTTP 2xx: 原子写 target；如果响应携带 ETag header，同步写 target+".etag"。
//   - HTTP 4xx: 立刻返回错误，不重试。
//   - HTTP 5xx 或网络错误: 按 opts.Retries 重试，最后失败返回错误。
//
// 调用方必须传入完整 url（含 query 中的 token 等）。本包不读取/泄漏凭证。
func Download(ctx context.Context, url, target string, opts Options) (changed bool, err error) {
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return false, err
	}
	client := opts.Client
	if client == nil {
		client = &http.Client{Timeout: DefaultTimeout}
	}
	retries := max(0, opts.Retries)

	etagPath := target + ".etag"
	prevETag := readETag(etagPath)

	var lastErr error
	for attempt := 0; attempt <= retries; attempt++ {
		c, err := downloadOnce(ctx, client, url, target, etagPath, prevETag, opts.Headers)
		if err == nil {
			return c, nil
		}
		// 4xx 不重试。
		if httpErr, ok := errors.AsType[*httpStatusError](err); ok && httpErr.status >= 400 && httpErr.status < 500 {
			return false, err
		}
		lastErr = err
		if attempt < retries {
			// 简单线性退避；M1 阶段无需复杂退避。
			select {
			case <-ctx.Done():
				return false, ctx.Err()
			case <-time.After(time.Duration(attempt+1) * time.Second):
			}
		}
	}
	return false, fmt.Errorf("download %s after %d attempts: %w", url, retries+1, lastErr)
}

func downloadOnce(
	ctx context.Context,
	client *http.Client,
	url, target, etagPath, prevETag string,
	headers http.Header,
) (bool, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return false, err
	}
	for k, vs := range headers {
		for _, v := range vs {
			req.Header.Add(k, v)
		}
	}
	if prevETag != "" {
		req.Header.Set("If-None-Match", prevETag)
	}

	resp, err := client.Do(req)
	if err != nil {
		return false, err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode == http.StatusNotModified {
		return false, nil
	}
	if resp.StatusCode >= 400 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return false, &httpStatusError{status: resp.StatusCode, body: string(body)}
	}
	if resp.StatusCode >= 300 {
		// 3xx 非 304：当前不期望出现（client 默认会跟随重定向）；视为错误。
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 256))
		return false, &httpStatusError{status: resp.StatusCode, body: string(body)}
	}

	tmp := target + ".tmp"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return false, err
	}
	if _, err := io.Copy(f, resp.Body); err != nil {
		_ = f.Close()
		_ = os.Remove(tmp)
		return false, err
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(tmp)
		return false, err
	}
	if err := os.Rename(tmp, target); err != nil {
		_ = os.Remove(tmp)
		return false, err
	}

	if etag := resp.Header.Get("ETag"); etag != "" {
		// 写 .etag 失败仅记录为下次失去增量优化，不算下载失败。
		_ = writeETag(etagPath, etag)
	} else {
		// 服务端没给 ETag：保险起见删掉旧 .etag，避免下次发出无意义 If-None-Match。
		_ = os.Remove(etagPath)
	}
	return true, nil
}

func readETag(path string) string {
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	// 末尾 \n / \r 来自手工编辑或 echo > file，会让 If-None-Match 头校验失败
	// （net/http: invalid header field value）。统一去掉首尾空白。
	return strings.TrimSpace(string(b))
}

func writeETag(path, etag string) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(etag), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

type httpStatusError struct {
	status int
	body   string
}

func (e *httpStatusError) Error() string {
	if e.body != "" {
		return fmt.Sprintf("http %d: %s", e.status, e.body)
	}
	return fmt.Sprintf("http %d", e.status)
}

// Status 返回 HTTP 状态码（仅当错误来自 Download 的非 2xx/304 响应时有意义；其他错误返回 0）。
func Status(err error) int {
	if h, ok := errors.AsType[*httpStatusError](err); ok {
		return h.status
	}
	return 0
}
