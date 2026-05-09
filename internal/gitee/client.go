// Package gitee 封装 sing-router 与 gitee 私有仓库的交互。
//
// 提供两类能力：
//  1. 用 access_token 直接下载 raw 文件（DownloadRaw / Version）
//  2. 反向代理 handler（见 proxy.go）让 sing-box 等无凭证客户端透传
//
// 所有错误信息中会用 RedactToken 把 token 替换为占位符，避免日志泄漏。
package gitee

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/moonfruit/sing-router/internal/config"
	"github.com/moonfruit/sing-router/internal/httpx"
)

// DefaultAPIBase 是 gitee v5 raw 文件 API 的根。
const DefaultAPIBase = "https://gitee.com/api/v5"

// tokenRedactPlaceholder 是错误信息中替换 token 的占位符。
const tokenRedactPlaceholder = "***REDACTED***"

// Client 与 gitee API 通信。所有方法线程安全。
type Client struct {
	APIBase string
	Owner   string
	Repo    string
	Token   string
	HTTP    *http.Client
	Retries int
}

// NewClient 用 daemon 配置构造 Client。其他可选参数（HTTP、Retries）保留零值时
// 使用 httpx 包默认。Token 为空时仍可工作（适用于公开仓库），但日志中不会做屏蔽。
func NewClient(cfg config.GiteeConfig) *Client {
	return &Client{
		APIBase: DefaultAPIBase,
		Owner:   cfg.Owner,
		Repo:    cfg.Repo,
		Token:   cfg.Token,
	}
}

// RawURL 构造一个 gitee raw 文件下载 URL。path 不应以 / 开头。
//
// 形如：https://gitee.com/api/v5/repos/{owner}/{repo}/raw/{path}?ref={ref}&access_token={token}
func (c *Client) RawURL(ref, path string) string {
	base := strings.TrimSuffix(c.APIBase, "/")
	if base == "" {
		base = DefaultAPIBase
	}
	cleanPath := strings.TrimPrefix(path, "/")
	q := url.Values{}
	if ref != "" {
		q.Set("ref", ref)
	}
	if c.Token != "" {
		q.Set("access_token", c.Token)
	}
	u := fmt.Sprintf("%s/repos/%s/%s/raw/%s", base, c.Owner, c.Repo, cleanPath)
	if encoded := q.Encode(); encoded != "" {
		u += "?" + encoded
	}
	return u
}

// DownloadRaw 增量下载 gitee 仓库中 ref/path 指向的文件到 target。
// 返回 changed=true 表示文件被实际更新（首次下载或 etag 变化）；
// changed=false 表示服务端 304 命中，本地未改动。
func (c *Client) DownloadRaw(ctx context.Context, ref, path, target string) (changed bool, err error) {
	url := c.RawURL(ref, path)
	changed, err = httpx.Download(ctx, url, target, httpx.Options{
		Client:  c.HTTP,
		Retries: c.Retries,
	})
	if err != nil {
		return false, c.wrapErr(err)
	}
	return changed, nil
}

// Version 拉取一个文本资源（如 version.txt）并返回其 trim 后的内容。
// 内部走 etag：若服务端 304，返回 changed=false 但仍能从本地缓存重读出版本号
// 的语义并不直观，所以本方法刻意每次都拉取（绕过 etag 缓存路径，直接读 body）。
//
// 注意：本方法不写本地缓存。调用方若希望缓存版本号文件，自行调用 DownloadRaw。
func (c *Client) Version(ctx context.Context, ref, path string) (string, error) {
	url := c.RawURL(ref, path)
	timeout := 30 * time.Second
	if c.HTTP != nil && c.HTTP.Timeout > 0 {
		timeout = c.HTTP.Timeout
	}
	client := c.HTTP
	if client == nil {
		client = &http.Client{Timeout: timeout}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", c.wrapErr(err)
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", c.wrapErr(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 256))
		return "", c.wrapErr(fmt.Errorf("gitee version: http %d: %s", resp.StatusCode, string(body)))
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if err != nil {
		return "", c.wrapErr(err)
	}
	return strings.TrimSpace(string(body)), nil
}

// wrapErr 把 token 字符串替换为占位符，避免日志泄漏。
func (c *Client) wrapErr(err error) error {
	if err == nil || c.Token == "" {
		return err
	}
	msg := strings.ReplaceAll(err.Error(), c.Token, tokenRedactPlaceholder)
	if msg == err.Error() {
		return err
	}
	return errors.New(msg)
}

// RedactToken 用与 wrapErr 相同的策略屏蔽 token；供反向代理 handler 复用。
func (c *Client) RedactToken(s string) string {
	if c.Token == "" {
		return s
	}
	return strings.ReplaceAll(s, c.Token, tokenRedactPlaceholder)
}
