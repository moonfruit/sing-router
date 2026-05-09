package gitee

import (
	"io"
	"net/http"
	"strings"
	"time"
)

// ProxyPathPrefix 是反向代理路由前缀。期望路径形如 {prefix}{ref}/{path...}。
const ProxyPathPrefix = "/api/v1/proxy/gitee/"

// 反向代理在响应中保留的 header；其他一律不透传以减少 surface。
var passThroughResponseHeaders = []string{
	"Content-Type",
	"Content-Length",
	"ETag",
	"Last-Modified",
	"Cache-Control",
	"Content-Encoding",
}

// 反向代理向上游转发的请求 header 白名单。Authorization 与 Cookie 一律不透传，
// 避免 sing-box 端意外携带凭证；access_token 由本端在 URL 中追加。
var passThroughRequestHeaders = []string{
	"If-None-Match",
	"If-Modified-Since",
	"Accept",
	"Accept-Encoding",
}

// NewProxyHandler 返回一个 http.Handler，把 GET {ProxyPathPrefix}{ref}/{path...}
// 转发到 gitee raw API 并附上 client 配置中的 access_token。
//
// 行为：
//   - 仅接受 GET / HEAD；其他方法 405。
//   - ref / path 段进行基本校验（禁止空段、`.`、`..`、控制字符、过长）。
//   - 上游 4xx：仅透传状态码与白名单 header，不透传 body（gitee 错误体可能
//     原样回显请求 URL，含 access_token）。
//   - 上游 2xx/3xx：流式 io.Copy 回客户端，透传白名单 header。
//   - 上游连接错误：返回 502 Bad Gateway，错误日志通过 RedactToken 屏蔽。
func (c *Client) NewProxyHandler() http.Handler {
	return http.HandlerFunc(c.handleProxy)
}

func (c *Client) handleProxy(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	rest, ok := strings.CutPrefix(r.URL.Path, ProxyPathPrefix)
	if !ok {
		http.Error(w, "bad path", http.StatusBadRequest)
		return
	}
	ref, path, ok := strings.Cut(rest, "/")
	if !ok || path == "" {
		http.Error(w, "missing ref or path", http.StatusBadRequest)
		return
	}
	if !validRefSegment(ref) {
		http.Error(w, "invalid ref", http.StatusBadRequest)
		return
	}
	if !validRawPath(path) {
		http.Error(w, "invalid path", http.StatusBadRequest)
		return
	}

	upstream := c.RawURL(ref, path)
	req, err := http.NewRequestWithContext(r.Context(), r.Method, upstream, nil)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	for _, h := range passThroughRequestHeaders {
		if v := r.Header.Get(h); v != "" {
			req.Header.Set(h, v)
		}
	}

	client := c.HTTP
	if client == nil {
		client = &http.Client{Timeout: 60 * time.Second}
	}
	resp, err := client.Do(req)
	if err != nil {
		// err 可能在底层包含 upstream URL（含 token），不直接吐回。
		http.Error(w, "upstream fetch failed", http.StatusBadGateway)
		return
	}
	defer func() { _ = resp.Body.Close() }()

	for _, h := range passThroughResponseHeaders {
		if v := resp.Header.Get(h); v != "" {
			w.Header().Set(h, v)
		}
	}

	if resp.StatusCode >= 400 {
		// 不透传 body，避免 gitee 错误回显泄漏 access_token。
		w.WriteHeader(resp.StatusCode)
		return
	}
	w.WriteHeader(resp.StatusCode)
	if r.Method == http.MethodHead {
		return
	}
	_, _ = io.Copy(w, resp.Body)
}

// validRefSegment 检查 ref 段：非空、无路径分隔符、无相对引用、长度合理。
func validRefSegment(s string) bool {
	if s == "" || s == "." || s == ".." || len(s) > 100 {
		return false
	}
	for _, r := range s {
		if r == '/' || r == '\\' || r == '?' || r == '#' || r < 0x20 {
			return false
		}
	}
	return true
}

// validRawPath 检查 path：允许 / 分段，但每段都不能为空、`.`、`..`，且总长合理。
func validRawPath(p string) bool {
	if p == "" || len(p) > 500 {
		return false
	}
	for seg := range strings.SplitSeq(p, "/") {
		if seg == "" || seg == "." || seg == ".." {
			return false
		}
		for _, r := range seg {
			if r == '\\' || r == '?' || r == '#' || r < 0x20 {
				return false
			}
		}
	}
	return true
}
