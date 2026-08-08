package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os/exec"
	"strconv"
	"strings"
)

// ClientBypassSet 是动态租约 ipset 的名字。
// 静态条目在 client_bypass_static / client_bypass_mac，由 startup.sh 从配置
// flush+重填，daemon 一概不碰——两者生命周期不同，混在一个 set 里会让
// 「配置删掉的静态条目立刻失效」和「动态租约不被清空」这两个需求打架。
const ClientBypassSet = "client_bypass"

// maxBypassIPs 限制单次请求的地址条数，防止一个请求刷爆 set。
const maxBypassIPs = 16

// BypassDeps 是 bypass handler 的依赖集。
type BypassDeps struct {
	Enabled       bool
	DefaultTTLSec int
	MaxTTLSec     int

	// IpsetRun 执行一次 ipset 调用；nil 时用 realIpsetRun。测试注入 fake 断言 argv。
	IpsetRun func(ctx context.Context, args ...string) error
	// IpsetList 返回 `ipset list <set>` 的原始输出；nil 时用 realIpsetList。
	IpsetList func(ctx context.Context, set string) (string, error)
}

type bypassRequest struct {
	IPs    []string `json:"ips"`
	TTLSec *int     `json:"ttl_sec"`
}

// realIpsetRun 直接把 argv 交给 ipset，不经 shell 解析——IP 已过 net.ParseIP
// 校验，argv 形式再免疫一层注入。
//
// 【只看退出码】目标设备上 ipset userspace(v7.6) 比内核(protocol 6) 新，每次
// 调用都可能往 stderr 打 "Warning: Kernel support protocol versions 6-6 while
// userspace supports protocol versions 6-7"。把 stderr 非空当失败会让每一次
// 心跳续约都报错。
func realIpsetRun(ctx context.Context, args ...string) error {
	if err := exec.CommandContext(ctx, "ipset", args...).Run(); err != nil {
		return fmt.Errorf("ipset %s: %w", strings.Join(args, " "), err)
	}
	return nil
}

func realIpsetList(ctx context.Context, set string) (string, error) {
	out, err := exec.CommandContext(ctx, "ipset", "list", set).Output()
	if err != nil {
		return "", fmt.Errorf("ipset list %s: %w", set, err)
	}
	return string(out), nil
}

func (d BypassDeps) run() func(context.Context, ...string) error {
	if d.IpsetRun != nil {
		return d.IpsetRun
	}
	return realIpsetRun
}

func (d BypassDeps) list() func(context.Context, string) (string, error) {
	if d.IpsetList != nil {
		return d.IpsetList
	}
	return realIpsetList
}

func (d BypassDeps) handle(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodPost:
		d.handleRegister(w, r)
	case http.MethodDelete:
		d.handleRevoke(w, r)
	case http.MethodGet:
		d.handleList(w, r)
	default:
		writeError(w, http.StatusMethodNotAllowed, "method.not_allowed",
			"POST / DELETE / GET required", nil)
	}
}

// parseIPs 校验并规范化地址列表。整个请求全有或全无——不做部分接受，否则
// 客户端会以为注册成功了却少了一个地址，且这种错误在稳态下无声无息。
func parseIPs(raw []string) (ips []string, errCode string, err error) {
	if len(raw) == 0 {
		return nil, "bypass.bad_request", fmt.Errorf("ips must not be empty")
	}
	if len(raw) > maxBypassIPs {
		return nil, "bypass.too_many_ips",
			fmt.Errorf("at most %d ips per request, got %d", maxBypassIPs, len(raw))
	}
	out := make([]string, 0, len(raw))
	for _, s := range raw {
		ip := net.ParseIP(strings.TrimSpace(s))
		if ip == nil {
			return nil, "bypass.bad_ip", fmt.Errorf("not an IP address: %q", s)
		}
		if ip.To4() == nil {
			return nil, "bypass.ipv6_unsupported",
				fmt.Errorf("IPv6 is not supported yet: %q", s)
		}
		out = append(out, ip.String())
	}
	return out, "", nil
}

func (d BypassDeps) resolveTTL(req bypassRequest) (int, error) {
	ttl := d.DefaultTTLSec
	if req.TTLSec != nil {
		ttl = *req.TTLSec
	}
	if ttl < 1 || ttl > d.MaxTTLSec {
		return 0, fmt.Errorf("ttl_sec must be in [1, %d], got %d", d.MaxTTLSec, ttl)
	}
	return ttl, nil
}

func decodeBypassRequest(w http.ResponseWriter, r *http.Request) (bypassRequest, []string, bool) {
	var req bypassRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "bypass.bad_request", err.Error(), nil)
		return req, nil, false
	}
	ips, code, err := parseIPs(req.IPs)
	if err != nil {
		writeError(w, http.StatusBadRequest, code, err.Error(), nil)
		return req, nil, false
	}
	return req, ips, true
}

func (d BypassDeps) handleRegister(w http.ResponseWriter, r *http.Request) {
	req, ips, ok := decodeBypassRequest(w, r)
	if !ok {
		return
	}
	ttl, err := d.resolveTTL(req)
	if err != nil {
		writeError(w, http.StatusBadRequest, "bypass.bad_ttl", err.Error(), nil)
		return
	}
	run := d.run()
	for _, ip := range ips {
		// -exist：条目还在则刷新 TTL；已被内核按 timeout 清掉则重新建。
		// 两种情况续约效果等价，客户端无需关心条目当前是否存在。
		if err := run(r.Context(), "-exist", "add", ClientBypassSet, ip,
			"timeout", strconv.Itoa(ttl)); err != nil {
			writeError(w, http.StatusInternalServerError, "bypass.ipset_failed", err.Error(), nil)
			return
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "accepted": ips, "ttl_sec": ttl})
}

func (d BypassDeps) handleRevoke(w http.ResponseWriter, r *http.Request) {
	_, ips, ok := decodeBypassRequest(w, r)
	if !ok {
		return
	}
	run := d.run()
	for _, ip := range ips {
		// -exist 让「删一个本就不存在的条目」不报错——客户端注销时条目可能
		// 已经自行过期了，那不是错误。
		if err := run(r.Context(), "-exist", "del", ClientBypassSet, ip); err != nil {
			writeError(w, http.StatusInternalServerError, "bypass.ipset_failed", err.Error(), nil)
			return
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "revoked": ips})
}

func (d BypassDeps) handleList(w http.ResponseWriter, r *http.Request) {
	out, err := d.list()(r.Context(), ClientBypassSet)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "bypass.ipset_failed", err.Error(), nil)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok": true, "set": ClientBypassSet, "entries": parseIpsetMembers(out),
	})
}

// parseIpsetMembers 从 `ipset list` 输出中提取成员与剩余 TTL。输出形如：
//
//	Name: client_bypass
//	Header: family inet hashsize 1024 maxelem 65536 timeout 120
//	Members:
//	192.168.50.80 timeout 118
//	192.168.50.81 timeout 0
//
// timeout 0 表示永不过期（静态条目用），不是"已过期"。
func parseIpsetMembers(out string) []map[string]any {
	entries := []map[string]any{}
	inMembers := false
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if line == "Members:" {
			inMembers = true
			continue
		}
		if !inMembers || line == "" {
			continue
		}
		fields := strings.Fields(line)
		entry := map[string]any{"ip": fields[0]}
		if len(fields) >= 3 && fields[1] == "timeout" {
			if n, err := strconv.Atoi(fields[2]); err == nil {
				entry["timeout_sec"] = n
			}
		}
		entries = append(entries, entry)
	}
	return entries
}
