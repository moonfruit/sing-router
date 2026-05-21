// Package bark 把 Bark（https://bark.day.app）实现为 notify.Channel。
//
// 它是一个无状态的纯发送器：Send 拼一个 POST 请求把通知推给 Bark 服务器；
// 异步、队列、重试、drain 全部由 notify 引擎提供。利用的 Bark 特性：title /
// subtitle / markdown 富文本正文、level（紧急度）、group（归组）、id（同 Kind
// 原地更新）、isArchive（归档），以及可选的 AES 端到端加密推送。
package bark

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/moonfruit/sing-router/internal/notify"
)

// DefaultBaseURL 是 Bark 官方服务器；用户可在配置里换成自建 bark-server。
const DefaultBaseURL = "https://api.day.app"

// EncryptionConfig 描述一个 Bark 渠道的加密设置。
type EncryptionConfig struct {
	Algorithm string // AES128 / AES192 / AES256
	Mode      string // CBC / ECB / GCM
	Key       string // 长度须与 Algorithm 匹配（16/24/32 字节）
}

// Config 构造一个 Bark Channel。
type Config struct {
	Name       string // 渠道实例名（[[notify.bark]].name）；空则取 "bark"
	BaseURL    string // 空则取 DefaultBaseURL
	Key        string // Bark 设备 key，必填
	Group      string // 归组名；空则不带 group
	Encryption *EncryptionConfig
	HTTPClient *http.Client // 可选，测试注入；nil 取默认 30s 超时客户端
}

// Channel 是一个 Bark 推送渠道实例，实现 notify.Channel。
type Channel struct {
	name    string
	pushURL string
	group   string
	enc     *cipherSpec
	hc      *http.Client
}

// New 校验配置并构造 Channel。Key 为空、加密参数非法时返回 error。
func New(cfg Config) (*Channel, error) {
	if strings.TrimSpace(cfg.Key) == "" {
		return nil, fmt.Errorf("bark: key is required")
	}
	base := strings.TrimRight(cfg.BaseURL, "/")
	if base == "" {
		base = DefaultBaseURL
	}
	name := cfg.Name
	if name == "" {
		name = "bark"
	}
	ch := &Channel{
		name:    "bark/" + name,
		pushURL: base + "/" + url.PathEscape(cfg.Key),
		group:   cfg.Group,
		hc:      cfg.HTTPClient,
	}
	if ch.hc == nil {
		ch.hc = &http.Client{Timeout: 30 * time.Second}
	}
	if cfg.Encryption != nil {
		spec, err := newCipherSpec(cfg.Encryption.Algorithm, cfg.Encryption.Mode, []byte(cfg.Encryption.Key))
		if err != nil {
			return nil, fmt.Errorf("bark/%s: %w", name, err)
		}
		ch.enc = spec
	}
	return ch, nil
}

// Name 返回渠道实例名，如 "bark/phone"。
func (c *Channel) Name() string { return c.name }

// Send 同步推送一条通知到 Bark。加密开启时把参数 JSON 加密成 ciphertext+iv 发送，
// 否则直接 form-encoded 发送。非 2xx 响应返回 error 供引擎重试。
func (c *Channel) Send(ctx context.Context, n notify.Notification) error {
	params := c.buildParams(n)

	form := url.Values{}
	if c.enc != nil {
		plaintext, err := json.Marshal(params)
		if err != nil {
			return fmt.Errorf("bark: marshal payload: %w", err)
		}
		ciphertext, iv, err := c.enc.encrypt(plaintext)
		if err != nil {
			return fmt.Errorf("bark: encrypt: %w", err)
		}
		form.Set("ciphertext", ciphertext)
		if iv != "" {
			form.Set("iv", iv)
		}
	} else {
		for k, v := range params {
			form.Set(k, v)
		}
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.pushURL,
		strings.NewReader(form.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := c.hc.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		snippet, _ := io.ReadAll(io.LimitReader(resp.Body, 256))
		return fmt.Errorf("bark: push rejected: HTTP %d: %s",
			resp.StatusCode, bytes.TrimSpace(snippet))
	}
	return nil
}

// buildParams 把 Notification 翻译成 Bark 推送参数。body 与 markdown 都带上
// 同一份正文：支持 markdown 的服务器用 markdown 渲染，旧服务器回退到 body。
func (c *Channel) buildParams(n notify.Notification) map[string]string {
	body := n.Body
	if sc, ok := n.Fields["suppressed_count"].(int); ok && sc > 0 {
		body += fmt.Sprintf("\n\n_（节流窗口内另有 %d 次同类事件）_", sc)
	}
	p := map[string]string{
		"title":     n.Title,
		"body":      body,
		"markdown":  body,
		"level":     barkLevel(n.Priority),
		"isArchive": "1",
	}
	if n.Subtitle != "" {
		p["subtitle"] = n.Subtitle
	}
	if c.group != "" {
		p["group"] = c.group
	}
	if n.Kind != "" {
		// 同 Kind 稳定 id：重复通知在通知中心原地更新而非堆叠。
		p["id"] = "sing-router." + n.Kind
	}
	return p
}

// barkLevel 把抽象 Priority 映射到 Bark 的 level。
func barkLevel(p notify.Priority) string {
	switch p {
	case notify.PriorityLow:
		return "passive"
	case notify.PriorityHigh:
		return "timeSensitive"
	case notify.PriorityCritical:
		return "critical"
	case notify.PriorityNormal:
		return "active"
	default:
		return "active"
	}
}
