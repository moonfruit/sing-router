package daemon

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os/exec"
	"strconv"
	"strings"

	"github.com/moonfruit/sing2seq/clef"
)

// ClientBypassSet 是动态租约 ipset 的名字。
// 静态条目在 client_bypass_static / client_bypass_mac，由 startup.sh 从配置
// flush+重填，daemon 一概不碰——两者生命周期不同，混在一个 set 里会让
// 「配置删掉的静态条目立刻失效」和「动态租约不被清空」这两个需求打架。
const ClientBypassSet = "client_bypass"

// maxBypassIPs 限制单次请求的地址条数，防止一个请求刷爆 set。
const maxBypassIPs = 16

// maxBypassBodyBytes 限制请求体大小。16 个 IPv4 地址编码成 JSON 顶多几百
// 字节；64KiB 对正常心跳请求绰绰有余，同时挡住"把某个数组元素塞成几百
// MB 字符串"这类在条数校验（发生在 json.Decode 之后）生效前就已经把内存
// 吃光的请求。
const maxBypassBodyBytes = 64 * 1024

// BypassDeps 是 bypass handler 的依赖集。
type BypassDeps struct {
	Enabled       bool
	DefaultTTLSec int
	MaxTTLSec     int

	// IpsetRun 执行一次 ipset 调用；nil 时用 realIpsetRun。测试注入 fake 断言 argv。
	IpsetRun func(ctx context.Context, args ...string) error
	// IpsetList 返回 `ipset list <set>` 的原始输出；nil 时用 realIpsetList。
	IpsetList func(ctx context.Context, set string) (string, error)

	// Emitter 可选；非 nil 时 ipset 调用失败会发一条 bypass.ipset_failed 事件，
	// 落进本地 log / seq。为 nil 时静默跳过（测试路径不注入 Emitter）——此前
	// 失败只出现在 HTTP 响应体里，daemon 侧无任何痕迹，doctor 只能报「set
	// missing」，看不出原因。
	Emitter *clef.Emitter
}

type bypassRequest struct {
	IPs    []string `json:"ips"`
	TTLSec *int     `json:"ttl_sec"`
}

// maxIpsetStderrBytes 截断喂进 error 里的 stderr，防止一次异常输出把日志/
// HTTP 响应体撑爆。
const maxIpsetStderrBytes = 512

// realIpsetRun 直接把 argv 交给 ipset，不经 shell 解析——IP 已过 net.ParseIP
// 校验，argv 形式再免疫一层注入。
//
// 【只看退出码】目标设备上 ipset userspace(v7.6) 比内核(protocol 6) 新，每次
// 调用都可能往 stderr 打 "Warning: Kernel support protocol versions 6-6 while
// userspace supports protocol versions 6-7"。把 stderr 非空当失败会让每一次
// 心跳续约都报错——判定成败只看退出码，不受此影响。
//
// 捕获 stderr 仅用于错误信息（失败时排障用），不参与成败判定：exec.Cmd.Run()
// 只在非零退出码时返回 error，stderr 内容不会改变这一点。之前 stderr 被整个
// 丢弃，失败时只剩一句 "exit status 1"，排障要靠猜。
func realIpsetRun(ctx context.Context, args ...string) error {
	cmd := exec.CommandContext(ctx, "ipset", args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if len(msg) > maxIpsetStderrBytes {
			msg = msg[:maxIpsetStderrBytes] + "...(truncated)"
		}
		if msg != "" {
			return fmt.Errorf("ipset %s: %w: %s", strings.Join(args, " "), err, msg)
		}
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

// reportIpsetFailure 发一条 bypass.ipset_failed 事件，落进本地 log / seq。
// d.Emitter 为 nil 时跳过（测试路径不注入 Emitter）。
func (d BypassDeps) reportIpsetFailure(op string, err error) {
	if d.Emitter == nil {
		return
	}
	d.Emitter.Warn("bypass", "bypass.ipset_failed", "ipset {Op} failed: {Err}",
		map[string]any{"Op": op, "Err": err.Error()})
}

// EnsureSet 在 daemon 启动早期（HTTP listener 起来之前）确保动态租约 set 已
// 存在。不这样做的话，client_bypass 只由 startup.sh 创建——而 startup.sh 要
// 等 Supervisor.Startup 走完 ready check（默认总超时 60s）才跑，HTTP listener
// 起得早得多：冷启动窗口内客户端心跳会全部拿到 500；auto_start=false、拿不到
// sing-box、或 sing-box 崩溃循环时甚至永久 500。
//
// -exist 幂等，startup.sh 之后还会再建一次不受影响；这里失败只记 warn，不阻止
// daemon 启动——真正兜底还是 startup.sh。
func (d BypassDeps) EnsureSet(ctx context.Context) {
	if !d.Enabled {
		return
	}
	run := d.run()
	if err := run(ctx, "-exist", "create", ClientBypassSet, "hash:ip",
		"timeout", strconv.Itoa(d.DefaultTTLSec)); err != nil {
		d.reportIpsetFailure("create", err)
	}
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
	// MaxBytesReader 必须包在条数校验（parseIPs 里的 maxBypassIPs）之前生效，
	// 否则一个单元素但超大字符串的数组会在计数前就把整个 body 读进内存。
	r.Body = http.MaxBytesReader(w, r.Body, maxBypassBodyBytes)
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
	// 中途失败不回滚：客户端是周期性心跳，下一轮会带着全部地址重新发一次
	// 请求；已经写入的条目挂着 TTL，到期会自行清除，所以"部分应用"是安全、
	// 可自愈的中间状态，不值得为它引入回滚的复杂度和额外失败面。
	// 注意 parseIPs 的"全有或全无"只约束参数校验阶段（防止一个明显有错的
	// 请求被当合法处理掉一半）——一旦进入这里开始真正调用 ipset 就不再是
	// 原子操作，失败时把已成功的地址通过 detail 回传，方便客户端判断哪些
	// 还需要重试，避免误以为整批都没生效。
	succeeded := make([]string, 0, len(ips))
	for _, ip := range ips {
		// -exist：条目还在则刷新 TTL；已被内核按 timeout 清掉则重新建。
		// 两种情况续约效果等价，客户端无需关心条目当前是否存在。
		if err := run(r.Context(), "-exist", "add", ClientBypassSet, ip,
			"timeout", strconv.Itoa(ttl)); err != nil {
			d.reportIpsetFailure("add", err)
			writeError(w, http.StatusInternalServerError, "bypass.ipset_failed", err.Error(),
				map[string]any{"succeeded": succeeded})
			return
		}
		succeeded = append(succeeded, ip)
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "accepted": ips, "ttl_sec": ttl})
}

func (d BypassDeps) handleRevoke(w http.ResponseWriter, r *http.Request) {
	_, ips, ok := decodeBypassRequest(w, r)
	if !ok {
		return
	}
	run := d.run()
	// 同 handleRegister：中途失败不回滚，已删除的条目通过 detail.succeeded
	// 回传；未删除的条目客户端可以重试，也可以放着让它自然过期，两者都不
	// 是错误状态。
	succeeded := make([]string, 0, len(ips))
	for _, ip := range ips {
		// -exist 让「删一个本就不存在的条目」不报错——客户端注销时条目可能
		// 已经自行过期了，那不是错误。
		if err := run(r.Context(), "-exist", "del", ClientBypassSet, ip); err != nil {
			d.reportIpsetFailure("del", err)
			writeError(w, http.StatusInternalServerError, "bypass.ipset_failed", err.Error(),
				map[string]any{"succeeded": succeeded})
			return
		}
		succeeded = append(succeeded, ip)
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "revoked": ips})
}

func (d BypassDeps) handleList(w http.ResponseWriter, r *http.Request) {
	out, err := d.list()(r.Context(), ClientBypassSet)
	if err != nil {
		d.reportIpsetFailure("list", err)
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
