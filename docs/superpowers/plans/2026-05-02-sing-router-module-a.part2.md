# sing-router Module A 实施计划 — Part 2

> 接续 `2026-05-02-sing-router-module-a.md`。仍由 superpowers:subagent-driven-development 或 superpowers:executing-plans 执行。

本部分覆盖：

- Phase 3（续）：Task 11–12（routing 参数 + sing-box wrapper）
- Phase 4：Task 13–14（assets 嵌入）
- Phase 5：Task 15–17（shell 脚本与 runner）
- Phase 6：Task 18–22（zoo 预处理器，TDD 全分支）

`Phase 7` 起的内容请见 part3。

---

## Task 11：routing 参数与 env 注入

**Files:**
- Create: `internal/config/routing.go`
- Create: `internal/config/routing_test.go`

- [ ] **Step 1：写测试 internal/config/routing_test.go**

```go
package config

import (
    "testing"
)

func TestDefaultRouting(t *testing.T) {
    r := DefaultRouting()
    cases := map[string]any{
        "DnsPort":      1053,
        "RedirectPort": 7892,
        "RouteMark":    "0x7892",
        "BypassMark":   "0x7890",
        "Tun":          "utun",
        "FakeIP":       "28.0.0.0/8",
        "LAN":          "192.168.50.0/24",
        "RouteTable":   111,
        "ProxyPorts":   "22,80,443,8080,8443",
    }
    if r.DnsPort != cases["DnsPort"].(int) {
        t.Fatalf("DnsPort %d", r.DnsPort)
    }
    if r.FakeIP != cases["FakeIP"].(string) {
        t.Fatalf("FakeIP %s", r.FakeIP)
    }
    if r.RouteMark != cases["RouteMark"].(string) {
        t.Fatalf("RouteMark %s", r.RouteMark)
    }
}

func TestLoadRoutingOverridesFromTOML(t *testing.T) {
    p := 9999
    fakeip := "30.0.0.0/8"
    cfg := &DaemonConfig{Router: RouterConfig{
        RedirectPort: &p,
        FakeIP:       &fakeip,
    }}
    r := LoadRouting(cfg)
    if r.RedirectPort != 9999 {
        t.Fatalf("override RedirectPort failed: %d", r.RedirectPort)
    }
    if r.FakeIP != "30.0.0.0/8" {
        t.Fatalf("override FakeIP failed: %s", r.FakeIP)
    }
    // 未覆盖的字段保持默认
    if r.DnsPort != 1053 {
        t.Fatalf("untouched DnsPort changed: %d", r.DnsPort)
    }
}

func TestRoutingEnvVars(t *testing.T) {
    r := DefaultRouting()
    env := r.EnvVars("/opt/home/sing-router/var/cn.txt")
    want := map[string]string{
        "DNS_PORT":      "1053",
        "REDIRECT_PORT": "7892",
        "ROUTE_MARK":    "0x7892",
        "BYPASS_MARK":   "0x7890",
        "TUN":           "utun",
        "FAKEIP":        "28.0.0.0/8",
        "LAN":           "192.168.50.0/24",
        "ROUTE_TABLE":   "111",
        "PROXY_PORTS":   "22,80,443,8080,8443",
        "CN_IP_CIDR":    "/opt/home/sing-router/var/cn.txt",
    }
    for k, v := range want {
        if env[k] != v {
            t.Fatalf("env %s want %q got %q", k, v, env[k])
        }
    }
    if len(env) != len(want) {
        t.Fatalf("env length: want %d got %d", len(want), len(env))
    }
}
```

- [ ] **Step 2：跑测试看失败**

```bash
go test ./internal/config/... -run TestRouting
go test ./internal/config/... -run TestDefaultRouting
go test ./internal/config/... -run TestLoadRouting
```

期望：编译失败。

- [ ] **Step 3：写实现 internal/config/routing.go**

```go
package config

import "strconv"

// Routing 是 startup.sh / teardown.sh 用到的全部路由参数。
// 取值优先级：daemon.toml [router] > Go 默认。
type Routing struct {
    DnsPort      int
    RedirectPort int
    RouteMark    string
    BypassMark   string
    Tun          string
    FakeIP       string
    LAN          string
    RouteTable   int
    ProxyPorts   string
}

// DefaultRouting 返回 Module A 的固化默认值。
// 注意：FakeIP 必须与 dns.json 的 inet4_range 一致。
func DefaultRouting() Routing {
    return Routing{
        DnsPort:      1053,
        RedirectPort: 7892,
        RouteMark:    "0x7892",
        BypassMark:   "0x7890",
        Tun:          "utun",
        FakeIP:       "28.0.0.0/8",
        LAN:          "192.168.50.0/24",
        RouteTable:   111,
        ProxyPorts:   "22,80,443,8080,8443",
    }
}

// LoadRouting 用 cfg 中的指针字段覆盖默认。
func LoadRouting(cfg *DaemonConfig) Routing {
    r := DefaultRouting()
    if cfg == nil {
        return r
    }
    if cfg.Router.DnsPort != nil {
        r.DnsPort = *cfg.Router.DnsPort
    }
    if cfg.Router.RedirectPort != nil {
        r.RedirectPort = *cfg.Router.RedirectPort
    }
    if cfg.Router.RouteMark != nil {
        r.RouteMark = *cfg.Router.RouteMark
    }
    if cfg.Router.BypassMark != nil {
        r.BypassMark = *cfg.Router.BypassMark
    }
    if cfg.Router.Tun != nil {
        r.Tun = *cfg.Router.Tun
    }
    if cfg.Router.FakeIP != nil {
        r.FakeIP = *cfg.Router.FakeIP
    }
    if cfg.Router.LAN != nil {
        r.LAN = *cfg.Router.LAN
    }
    if cfg.Router.RouteTable != nil {
        r.RouteTable = *cfg.Router.RouteTable
    }
    if cfg.Router.ProxyPorts != nil {
        r.ProxyPorts = *cfg.Router.ProxyPorts
    }
    return r
}

// EnvVars 渲染传给 startup.sh / teardown.sh 的环境变量集合。
// cnIPCidrPath 为 cn.txt 的绝对路径。
func (r Routing) EnvVars(cnIPCidrPath string) map[string]string {
    return map[string]string{
        "DNS_PORT":      strconv.Itoa(r.DnsPort),
        "REDIRECT_PORT": strconv.Itoa(r.RedirectPort),
        "ROUTE_MARK":    r.RouteMark,
        "BYPASS_MARK":   r.BypassMark,
        "TUN":           r.Tun,
        "FAKEIP":        r.FakeIP,
        "LAN":           r.LAN,
        "ROUTE_TABLE":   strconv.Itoa(r.RouteTable),
        "PROXY_PORTS":   r.ProxyPorts,
        "CN_IP_CIDR":    cnIPCidrPath,
    }
}
```

- [ ] **Step 4：跑测试**

```bash
go test ./internal/config/... -v
```

期望：所有 routing 测试 + 之前的 daemon_toml 测试 `PASS`。

- [ ] **Step 5：提交**

```bash
git add internal/config/routing.go internal/config/routing_test.go
git commit -m "feat(config): add Routing params with toml override + env injection"
```

---

## Task 12：sing-box wrapper（check）

**Files:**
- Create: `internal/config/singbox.go`
- Create: `internal/config/singbox_test.go`

- [ ] **Step 1：写测试 internal/config/singbox_test.go**

```go
package config

import (
    "context"
    "errors"
    "os/exec"
    "testing"
)

func TestSingBoxCheckOK(t *testing.T) {
    // 替换 ExecCommand 为 fake：返回 exit 0 + stdout 空
    orig := execCommand
    execCommand = func(ctx context.Context, name string, args ...string) *exec.Cmd {
        return exec.CommandContext(ctx, "true")
    }
    defer func() { execCommand = orig }()

    err := CheckSingBoxConfig(context.Background(), "/opt/sing-box", "/opt/conf.d")
    if err != nil {
        t.Fatalf("expected nil err, got %v", err)
    }
}

func TestSingBoxCheckFail(t *testing.T) {
    orig := execCommand
    execCommand = func(ctx context.Context, name string, args ...string) *exec.Cmd {
        return exec.CommandContext(ctx, "false")
    }
    defer func() { execCommand = orig }()

    err := CheckSingBoxConfig(context.Background(), "/opt/sing-box", "/opt/conf.d")
    if err == nil {
        t.Fatal("expected error")
    }
    var ce *CheckError
    if !errors.As(err, &ce) {
        t.Fatalf("error type: %T", err)
    }
}

func TestSingBoxCheckMissingBinary(t *testing.T) {
    err := CheckSingBoxConfig(context.Background(), "/nonexistent/sing-box", "/opt/conf.d")
    if err == nil {
        t.Fatal("expected error for missing binary")
    }
}
```

- [ ] **Step 2：跑测试看失败**

```bash
go test ./internal/config/... -run TestSingBox
```

期望：编译失败。

- [ ] **Step 3：写实现 internal/config/singbox.go**

```go
package config

import (
    "bytes"
    "context"
    "fmt"
    "os"
    "os/exec"
)

// execCommand 暴露给测试以便注入 fake exec.Cmd 工厂。
var execCommand = func(ctx context.Context, name string, args ...string) *exec.Cmd {
    return exec.CommandContext(ctx, name, args...)
}

// CheckError 携带 sing-box check 的 stderr 输出，便于上层报告。
type CheckError struct {
    Stderr string
    Err    error
}

func (e *CheckError) Error() string {
    return fmt.Sprintf("sing-box check failed: %v\n%s", e.Err, e.Stderr)
}

func (e *CheckError) Unwrap() error { return e.Err }

// CheckSingBoxConfig 调 `sing-box check -C <dir>` 校验配置。
// 二进制不存在或不可执行时返回非 *CheckError 的 error。
func CheckSingBoxConfig(ctx context.Context, binary, configDir string) error {
    if _, err := os.Stat(binary); err != nil {
        return fmt.Errorf("sing-box binary: %w", err)
    }
    cmd := execCommand(ctx, binary, "check", "-C", configDir)
    var stderr bytes.Buffer
    cmd.Stderr = &stderr
    if err := cmd.Run(); err != nil {
        return &CheckError{Stderr: stderr.String(), Err: err}
    }
    return nil
}
```

- [ ] **Step 4：跑测试**

```bash
go test ./internal/config/... -run TestSingBox -v
```

期望：3 个 case 全部 `PASS`。

- [ ] **Step 5：提交**

```bash
git add internal/config/singbox.go internal/config/singbox_test.go
git commit -m "feat(config): wrap sing-box check with injectable exec for tests"
```

---

# Phase 4：嵌入资源

## Task 13：复制默认 config.d fragment 与 daemon.toml.default 到 assets

**Files:**
- Create: `assets/config.d.default/clash.json`
- Create: `assets/config.d.default/dns.json`
- Create: `assets/config.d.default/inbounds.json`
- Create: `assets/config.d.default/log.json`
- Create: `assets/config.d.default/cache.json`
- Create: `assets/config.d.default/certificate.json`
- Create: `assets/config.d.default/http.json`
- Create: `assets/config.d.default/outbounds.json`
- Create: `assets/daemon.toml.default`

- [ ] **Step 1：把当前 repo 的 config/*.json 复制到 assets/config.d.default/**

```bash
mkdir -p assets/config.d.default
cp config/clash.json assets/config.d.default/clash.json
cp config/dns.json assets/config.d.default/dns.json
cp config/inbounds.json assets/config.d.default/inbounds.json
cp config/log.json assets/config.d.default/log.json
cp config/cache.json assets/config.d.default/cache.json
cp config/certificate.json assets/config.d.default/certificate.json
cp config/http.json assets/config.d.default/http.json
cp config/outbounds.json assets/config.d.default/outbounds.json
```

- [ ] **Step 2：修复 dns.json inet4_range 与 startup.sh 不一致（spec §10 #1）**

打开 `assets/config.d.default/dns.json`，把 `dns-fakeip` 服务器的 `"inet4_range": "22.0.0.0/8"` 改为 `"28.0.0.0/8"`：

执行：

```bash
sed -i.bak 's|"inet4_range": "22.0.0.0/8"|"inet4_range": "28.0.0.0/8"|' assets/config.d.default/dns.json
rm assets/config.d.default/dns.json.bak
```

- [ ] **Step 3：写 assets/daemon.toml.default**

文件内容（与 spec §7 一致）：

```toml
# /opt/home/sing-router/daemon.toml
# Module A 范围内的全部设置；后续模块会以独立 section 增量加入。

[runtime]
# rundir = "/opt/home/sing-router"   # 默认 = -D 参数 / 启动 cwd
sing_box_binary = "bin/sing-box"
config_dir      = "config.d"
ui_dir          = "ui"

[http]
listen          = "127.0.0.1:9998"
# token         = ""                 # A 阶段不启用，D 阶段加

[log]
level           = "info"
file            = "log/sing-router.log"
rotate          = "internal"
max_size_mb     = 10
max_backups     = 5
disable_color   = false
include_stack   = false

[supervisor]
# ready_check_dial_inbounds       = true
# ready_check_clash_api           = true
# ready_check_timeout_ms          = 5000
# ready_check_interval_ms         = 200
# crash_pre_ready_action          = "fatal"
# crash_post_ready_backoff_ms     = [1000, 2000, 4000, 8000, 16000, 32000, 64000, 128000, 256000, 512000, 600000]
# iptables_keep_when_backoff_lt_ms = 10000
# stop_grace_seconds              = 5

[zoo]
extract_keys              = ["outbounds", "route.rules", "route.rule_set", "route.final"]
rule_set_dedup_strategy   = "builtin_wins"
outbound_collision_action = "reject"

[download]
mirror_prefix            = ""
sing_box_url_template    = "https://github.com/SagerNet/sing-box/releases/download/v{version}/sing-box-{version}-linux-arm64.tar.gz"
sing_box_default_version = "latest"
cn_list_url              = "https://raw.githubusercontent.com/17mon/china_ip_list/master/china_ip_list.txt"
http_timeout_seconds     = 60
http_retries             = 3

[router]
# dns_port      = 1053
# redirect_port = 7892
# route_mark    = "0x7892"
# bypass_mark   = "0x7890"
# tun           = "utun"
# fakeip        = "28.0.0.0/8"   # 必须与 dns.json inet4_range 一致
# lan           = "192.168.50.0/24"
# route_table   = 111
# proxy_ports   = "22,80,443,8080,8443"

[install]
download_sing_box   = true
download_cn_list    = true
download_zashboard  = false
auto_start          = false
```

- [ ] **Step 4：提交**

```bash
git add assets/config.d.default assets/daemon.toml.default
git commit -m "feat(assets): seed default config.d/* and daemon.toml; fix fakeip inet4_range to 28.0.0.0/8"
```

---

## Task 14：embed.go + 验收测试

**Files:**
- Create: `assets/embed.go`
- Create: `assets/embed_test.go`
- Create: `assets/initd/S99sing-router`
- Create: `assets/jffs/nat-start.snippet`
- Create: `assets/jffs/services-start.snippet`
- Create: `assets/shell/startup.sh`（占位空文件，Task 15 完整化）
- Create: `assets/shell/teardown.sh`（占位空文件，Task 16 完整化）

- [ ] **Step 1：写 assets/initd/S99sing-router**

```bash
#!/bin/sh
# Entware init.d script for sing-router (managed by `sing-router install`)

ENABLED=yes
PROCS=sing-router
ARGS="daemon -D /opt/home/sing-router"
PREARGS=""
DESC="sing-router transparent proxy supervisor"
PATH=/opt/sbin:/opt/bin:/usr/sbin:/usr/bin:/sbin:/bin

. /opt/etc/init.d/rc.func
```

- [ ] **Step 2：写 assets/jffs/nat-start.snippet**

```sh
# BEGIN sing-router (managed by `sing-router install`; do not edit)
command -v sing-router >/dev/null && sing-router reapply-rules >/dev/null 2>&1 &
# END sing-router
```

- [ ] **Step 3：写 assets/jffs/services-start.snippet**

```sh
# BEGIN sing-router (managed by `sing-router install`; do not edit)
/opt/etc/init.d/S99sing-router start &
# END sing-router
```

- [ ] **Step 4：创建占位 shell 文件（内容稍后由 Task 15/16 填充）**

```bash
mkdir -p assets/shell
echo '#!/usr/bin/env bash' > assets/shell/startup.sh
echo '#!/usr/bin/env bash' > assets/shell/teardown.sh
```

- [ ] **Step 5：写 assets/embed.go**

```go
// Package assets 用 //go:embed 把 sing-router 所需的全部静态资源（默认配置、
// shell 脚本、init.d 与 jffs 模板、daemon.toml 模板）打进二进制。
package assets

import (
    "embed"
    "io/fs"
)

//go:embed config.d.default/*.json daemon.toml.default initd/* jffs/*.snippet shell/*.sh
var fsys embed.FS

// FS 返回根 fs.FS，可被 install 模块用 fs.Sub 取子树。
func FS() fs.FS { return fsys }

// MustReadFile 读取嵌入文件；不存在时 panic（属于程序员错误）。
func MustReadFile(name string) []byte {
    data, err := fs.ReadFile(fsys, name)
    if err != nil {
        panic("assets.MustReadFile " + name + ": " + err.Error())
    }
    return data
}

// ReadFile 读取嵌入文件并返回 error。
func ReadFile(name string) ([]byte, error) {
    return fs.ReadFile(fsys, name)
}
```

- [ ] **Step 6：写 assets/embed_test.go**

```go
package assets

import (
    "strings"
    "testing"
)

func TestDefaultConfigsPresent(t *testing.T) {
    for _, p := range []string{
        "config.d.default/clash.json",
        "config.d.default/dns.json",
        "config.d.default/inbounds.json",
        "config.d.default/log.json",
        "config.d.default/cache.json",
        "config.d.default/certificate.json",
        "config.d.default/http.json",
        "config.d.default/outbounds.json",
        "daemon.toml.default",
        "initd/S99sing-router",
        "jffs/nat-start.snippet",
        "jffs/services-start.snippet",
        "shell/startup.sh",
        "shell/teardown.sh",
    } {
        if _, err := ReadFile(p); err != nil {
            t.Errorf("missing %s: %v", p, err)
        }
    }
}

func TestDNSFakeIPRangeFixed(t *testing.T) {
    data, err := ReadFile("config.d.default/dns.json")
    if err != nil {
        t.Fatal(err)
    }
    if !strings.Contains(string(data), `"inet4_range": "28.0.0.0/8"`) {
        t.Fatal("default dns.json should have inet4_range fixed to 28.0.0.0/8")
    }
    if strings.Contains(string(data), `"22.0.0.0/8"`) {
        t.Fatal("default dns.json still references obsolete 22.0.0.0/8")
    }
}

func TestNatStartSnippetMarkers(t *testing.T) {
    data, _ := ReadFile("jffs/nat-start.snippet")
    if !strings.Contains(string(data), "# BEGIN sing-router") {
        t.Fatal("BEGIN marker missing")
    }
    if !strings.Contains(string(data), "# END sing-router") {
        t.Fatal("END marker missing")
    }
    if !strings.Contains(string(data), "sing-router reapply-rules") {
        t.Fatal("snippet should call reapply-rules")
    }
}
```

- [ ] **Step 7：跑测试**

```bash
go test ./assets/...
```

期望：3 个 case 全部 `PASS`。

- [ ] **Step 8：提交**

```bash
git add assets
git commit -m "feat(assets): add embed.FS with init.d, jffs snippets, shell placeholders"
```

---

# Phase 5：Shell 脚本与 runner

## Task 15：把 bin/startup.sh 改造为 env-driven 版本，落进 assets/shell/

**Files:**
- Modify: `assets/shell/startup.sh`（替换占位内容）

- [ ] **Step 1：写 assets/shell/startup.sh 完整内容**

替换 `assets/shell/startup.sh` 全部内容：

```bash
#!/usr/bin/env bash
# ============================================================
# sing-router startup script (managed; embedded into the Go binary).
# 全部参数从环境变量读取（由 sing-router daemon 注入），不在脚本内硬编码。
# 与 spec 第 3.4 / §10 节描述一致。
# ============================================================

set -eu

: "${DNS_PORT:?DNS_PORT not set}"
: "${REDIRECT_PORT:?REDIRECT_PORT not set}"
: "${ROUTE_MARK:?ROUTE_MARK not set}"
: "${BYPASS_MARK:?BYPASS_MARK not set}"
: "${TUN:?TUN not set}"
: "${ROUTE_TABLE:?ROUTE_TABLE not set}"
: "${PROXY_PORTS:?PROXY_PORTS not set}"
: "${FAKEIP:?FAKEIP not set}"
: "${LAN:?LAN not set}"

CN_IP_CIDR="${CN_IP_CIDR:-}"

# IPv4 保留地址 + 私有网段（reserve_ipv4）
BYPASS="0.0.0.0/8 10.0.0.0/8 127.0.0.0/8 169.254.0.0/16 172.16.0.0/12 192.168.0.0/16 224.0.0.0/4 240.0.0.0/4 255.255.255.255/32"

# ===================== ipset：CN IP 集合 =====================
if [ -n "$CN_IP_CIDR" ] && [ -f "$CN_IP_CIDR" ]; then
    ipset destroy cn 2>/dev/null || true
    {
        echo "create cn hash:net family inet hashsize 10240 maxelem 10240"
        awk '!/^$/ && !/^#/ {print "add cn", $0}' "$CN_IP_CIDR"
    } | ipset -! restore
fi

# ===================== 1. 路由表 =====================
ip route replace default dev "$TUN" table "$ROUTE_TABLE"
ip rule add fwmark "$ROUTE_MARK" table "$ROUTE_TABLE" 2>/dev/null || true

# ===================== 2.1 TCP 透明代理 =====================
iptables -t nat -N sing-box 2>/dev/null || iptables -t nat -F sing-box
iptables -t nat -A sing-box -p tcp --dport 53 -j RETURN
iptables -t nat -A sing-box -p udp --dport 53 -j RETURN
iptables -t nat -A sing-box -m mark --mark "$BYPASS_MARK" -j RETURN
for ip in $BYPASS; do
    iptables -t nat -A sing-box -d "$ip" -j RETURN
done
if [ -n "$CN_IP_CIDR" ] && [ -f "$CN_IP_CIDR" ]; then
    iptables -t nat -A sing-box -m set --match-set cn dst -j RETURN
fi
iptables -t nat -A sing-box -p tcp -s "$LAN" -j REDIRECT --to-ports "$REDIRECT_PORT"
iptables -t nat -C PREROUTING -p tcp -m multiport --dports "$PROXY_PORTS" -j sing-box 2>/dev/null \
    || iptables -t nat -I PREROUTING -p tcp -m multiport --dports "$PROXY_PORTS" -j sing-box
iptables -t nat -C PREROUTING -p tcp -d "$FAKEIP" -j sing-box 2>/dev/null \
    || iptables -t nat -I PREROUTING -p tcp -d "$FAKEIP" -j sing-box

# ===================== 2.2 UDP 透明代理 =====================
iptables -C FORWARD -o "$TUN" -j ACCEPT 2>/dev/null \
    || iptables -I FORWARD -o "$TUN" -j ACCEPT
iptables -t mangle -N sing-box-mark 2>/dev/null || iptables -t mangle -F sing-box-mark
iptables -t mangle -A sing-box-mark -p tcp --dport 53 -j RETURN
iptables -t mangle -A sing-box-mark -p udp --dport 53 -j RETURN
iptables -t mangle -A sing-box-mark -m mark --mark "$BYPASS_MARK" -j RETURN
for ip in $BYPASS; do
    iptables -t mangle -A sing-box-mark -d "$ip" -j RETURN
done
if [ -n "$CN_IP_CIDR" ] && [ -f "$CN_IP_CIDR" ]; then
    iptables -t mangle -A sing-box-mark -m set --match-set cn dst -j RETURN
fi
iptables -t mangle -A sing-box-mark -p udp -s "$LAN" -j MARK --set-mark "$ROUTE_MARK"
iptables -t mangle -C PREROUTING -p udp -m multiport --dports "$PROXY_PORTS" -j sing-box-mark 2>/dev/null \
    || iptables -t mangle -I PREROUTING -p udp -m multiport --dports "$PROXY_PORTS" -j sing-box-mark
iptables -t mangle -C PREROUTING -p udp -d "$FAKEIP" -j sing-box-mark 2>/dev/null \
    || iptables -t mangle -I PREROUTING -p udp -d "$FAKEIP" -j sing-box-mark

# ===================== 2.3 DNS 劫持 =====================
iptables -t nat -N sing-box-dns 2>/dev/null || iptables -t nat -F sing-box-dns
iptables -t nat -A sing-box-dns -m mark --mark "$BYPASS_MARK" -j RETURN
iptables -t nat -A sing-box-dns -p tcp -s "$LAN" -j REDIRECT --to-ports "$DNS_PORT"
iptables -t nat -A sing-box-dns -p udp -s "$LAN" -j REDIRECT --to-ports "$DNS_PORT"
iptables -t nat -C PREROUTING -p tcp --dport 53 -j sing-box-dns 2>/dev/null \
    || iptables -t nat -I PREROUTING -p tcp --dport 53 -j sing-box-dns
iptables -t nat -C PREROUTING -p udp --dport 53 -j sing-box-dns 2>/dev/null \
    || iptables -t nat -I PREROUTING -p udp --dport 53 -j sing-box-dns

# ===================== 2.4 IPv6 DNS 兜底 =====================
ip6tables -C INPUT -p tcp --dport 53 -j REJECT 2>/dev/null \
    || ip6tables -I INPUT -p tcp --dport 53 -j REJECT
ip6tables -C INPUT -p udp --dport 53 -j REJECT 2>/dev/null \
    || ip6tables -I INPUT -p udp --dport 53 -j REJECT

echo "sing-router startup: rules installed"
```

- [ ] **Step 2：补一个 assets/shell 的简单 sanity 测试**

把以下追加到 `assets/embed_test.go`：

```go
func TestStartupShellRequiresEnvVars(t *testing.T) {
    data, err := ReadFile("shell/startup.sh")
    if err != nil {
        t.Fatal(err)
    }
    s := string(data)
    for _, name := range []string{"DNS_PORT", "REDIRECT_PORT", "ROUTE_MARK", "BYPASS_MARK",
        "TUN", "ROUTE_TABLE", "PROXY_PORTS", "FAKEIP", "LAN"} {
        if !strings.Contains(s, ":\""+`${`+name+`:?`) && !strings.Contains(s, ": \"${"+name+":?") {
            t.Errorf("startup.sh should hard-require %s via : \"${%s:?...}\"", name, name)
        }
    }
}
```

- [ ] **Step 3：跑测试**

```bash
go test ./assets/...
```

期望：所有 assets 测试 `PASS`。

- [ ] **Step 4：提交**

```bash
git add assets/shell/startup.sh assets/embed_test.go
git commit -m "feat(assets): replace startup.sh with env-driven idempotent version"
```

---

## Task 16：teardown.sh

**Files:**
- Modify: `assets/shell/teardown.sh`

- [ ] **Step 1：写 assets/shell/teardown.sh 完整内容**

```bash
#!/usr/bin/env bash
# ============================================================
# sing-router teardown script. 撤销 startup.sh 安装的所有规则。
# 幂等：每条规则用 -C 检测后再 -D；不存在不报错。
# ============================================================

set -u

: "${DNS_PORT:?DNS_PORT not set}"
: "${REDIRECT_PORT:?REDIRECT_PORT not set}"
: "${ROUTE_MARK:?ROUTE_MARK not set}"
: "${TUN:?TUN not set}"
: "${ROUTE_TABLE:?ROUTE_TABLE not set}"
: "${PROXY_PORTS:?PROXY_PORTS not set}"
: "${FAKEIP:?FAKEIP not set}"

del_iptables() {
    while iptables "$@" -C 2>/dev/null; do :; done
    iptables "$@" -D 2>/dev/null || true
}

# ---- DNS 劫持入口 ----
iptables -t nat -D PREROUTING -p tcp --dport 53 -j sing-box-dns 2>/dev/null || true
iptables -t nat -D PREROUTING -p udp --dport 53 -j sing-box-dns 2>/dev/null || true
iptables -t nat -F sing-box-dns 2>/dev/null || true
iptables -t nat -X sing-box-dns 2>/dev/null || true

# ---- TCP 入口 ----
iptables -t nat -D PREROUTING -p tcp -m multiport --dports "$PROXY_PORTS" -j sing-box 2>/dev/null || true
iptables -t nat -D PREROUTING -p tcp -d "$FAKEIP" -j sing-box 2>/dev/null || true
iptables -t nat -F sing-box 2>/dev/null || true
iptables -t nat -X sing-box 2>/dev/null || true

# ---- UDP 入口 ----
iptables -t mangle -D PREROUTING -p udp -m multiport --dports "$PROXY_PORTS" -j sing-box-mark 2>/dev/null || true
iptables -t mangle -D PREROUTING -p udp -d "$FAKEIP" -j sing-box-mark 2>/dev/null || true
iptables -t mangle -F sing-box-mark 2>/dev/null || true
iptables -t mangle -X sing-box-mark 2>/dev/null || true

# ---- TUN forward ----
iptables -D FORWARD -o "$TUN" -j ACCEPT 2>/dev/null || true

# ---- IPv6 DNS 兜底 ----
ip6tables -D INPUT -p tcp --dport 53 -j REJECT 2>/dev/null || true
ip6tables -D INPUT -p udp --dport 53 -j REJECT 2>/dev/null || true

# ---- 路由表 + rule ----
ip rule del fwmark "$ROUTE_MARK" table "$ROUTE_TABLE" 2>/dev/null || true
ip route flush table "$ROUTE_TABLE" 2>/dev/null || true

# ---- ipset ----
ipset destroy cn 2>/dev/null || true

echo "sing-router teardown: rules removed"
```

- [ ] **Step 2：跑 assets 测试**

```bash
go test ./assets/...
```

期望：现有所有 case 仍 `PASS`（这一步只换内容、不影响 embed 列表）。

- [ ] **Step 3：提交**

```bash
git add assets/shell/teardown.sh
git commit -m "feat(assets): add idempotent teardown.sh that mirrors startup.sh"
```

---

## Task 17：shell.runner

**Files:**
- Create: `internal/shell/runner.go`
- Create: `internal/shell/runner_test.go`

- [ ] **Step 1：写测试 internal/shell/runner_test.go**

```go
package shell

import (
    "context"
    "errors"
    "strings"
    "sync"
    "testing"
)

func TestRunnerExecutesScriptWithEnv(t *testing.T) {
    r := NewRunner(RunnerConfig{
        Bash: "/bin/bash",
        Env:  map[string]string{"FOO": "bar", "BAZ": "qux"},
    })
    var stderr strings.Builder
    err := r.Run(context.Background(), "echo $FOO-$BAZ 1>&2", &stderr)
    if err != nil {
        t.Fatalf("Run: %v", err)
    }
    if !strings.Contains(stderr.String(), "bar-qux") {
        t.Fatalf("stderr missing expected line: %q", stderr.String())
    }
}

func TestRunnerRequiredEnvAbsentFails(t *testing.T) {
    r := NewRunner(RunnerConfig{Bash: "/bin/bash"})
    var stderr strings.Builder
    script := `set -eu; : "${MUST_EXIST:?MUST_EXIST not set}"; echo ok`
    err := r.Run(context.Background(), script, &stderr)
    if err == nil {
        t.Fatal("expected error from missing env")
    }
    var rerr *Error
    if !errors.As(err, &rerr) {
        t.Fatalf("err type %T", err)
    }
    if rerr.ExitCode == 0 {
        t.Fatal("exit code should be non-zero")
    }
}

func TestRunnerStreamsStderrLineByLine(t *testing.T) {
    r := NewRunner(RunnerConfig{Bash: "/bin/bash"})
    var mu sync.Mutex
    lines := []string{}
    r.OnStderr = func(line string) {
        mu.Lock()
        defer mu.Unlock()
        lines = append(lines, line)
    }
    var stderr strings.Builder
    script := "echo line1 1>&2; echo line2 1>&2; echo line3 1>&2"
    if err := r.Run(context.Background(), script, &stderr); err != nil {
        t.Fatal(err)
    }
    mu.Lock()
    defer mu.Unlock()
    if len(lines) != 3 || lines[0] != "line1" {
        t.Fatalf("stderr lines: %v", lines)
    }
}
```

- [ ] **Step 2：跑测试看失败**

```bash
go test ./internal/shell/...
```

期望：编译失败。

- [ ] **Step 3：写实现 internal/shell/runner.go**

```go
// Package shell 用 bash 跑嵌入脚本；env 由调用方注入。
package shell

import (
    "bufio"
    "context"
    "fmt"
    "io"
    "os/exec"
    "strings"
)

// RunnerConfig 控制 Runner 的行为。
type RunnerConfig struct {
    Bash string            // bash 可执行路径，默认 "/bin/bash"
    Env  map[string]string // 注入子进程的环境变量
}

// Runner 通过 stdin 把脚本喂给 bash 跑，避免落盘。
// 并发使用安全（每次 Run 独立的 cmd）。
type Runner struct {
    cfg RunnerConfig
    // OnStderr 可选：每行 stderr 触发一次回调（已 trim 行尾换行）。
    OnStderr func(line string)
}

// Error 携带退出码与最后一段 stderr，便于上层报告。
type Error struct {
    ExitCode int
    Stderr   string
    Cause    error
}

func (e *Error) Error() string {
    return fmt.Sprintf("script failed (exit=%d): %v\n%s", e.ExitCode, e.Cause, e.Stderr)
}

func (e *Error) Unwrap() error { return e.Cause }

// NewRunner 构造 Runner。
func NewRunner(cfg RunnerConfig) *Runner {
    if cfg.Bash == "" {
        cfg.Bash = "/bin/bash"
    }
    return &Runner{cfg: cfg}
}

// Run 在 ctx 控制下用 bash 执行 script；stderr 同时写入 capture（用于 Error.Stderr）
// 与 Runner.OnStderr 回调（用于实时事件流转）。
func (r *Runner) Run(ctx context.Context, script string, capture io.Writer) error {
    cmd := exec.CommandContext(ctx, r.cfg.Bash, "-s")
    cmd.Stdin = strings.NewReader(script)
    cmd.Env = r.envSlice()

    pr, pw := io.Pipe()
    cmd.Stderr = pw
    cmd.Stdout = io.Discard

    var stderrCopy strings.Builder
    done := make(chan struct{})
    go func() {
        defer close(done)
        sc := bufio.NewScanner(pr)
        sc.Buffer(make([]byte, 64*1024), 4*1024*1024)
        for sc.Scan() {
            line := sc.Text()
            if r.OnStderr != nil {
                r.OnStderr(line)
            }
            if capture != nil {
                _, _ = capture.Write([]byte(line + "\n"))
            }
            stderrCopy.WriteString(line)
            stderrCopy.WriteByte('\n')
        }
    }()

    if err := cmd.Start(); err != nil {
        _ = pw.Close()
        return &Error{ExitCode: -1, Cause: err}
    }
    waitErr := cmd.Wait()
    _ = pw.Close()
    <-done

    if waitErr != nil {
        if ee, ok := waitErr.(*exec.ExitError); ok {
            return &Error{ExitCode: ee.ExitCode(), Stderr: stderrCopy.String(), Cause: waitErr}
        }
        return &Error{ExitCode: -1, Stderr: stderrCopy.String(), Cause: waitErr}
    }
    return nil
}

func (r *Runner) envSlice() []string {
    out := make([]string, 0, len(r.cfg.Env))
    for k, v := range r.cfg.Env {
        out = append(out, k+"="+v)
    }
    return out
}
```

- [ ] **Step 4：跑测试**

```bash
go test ./internal/shell/... -v
```

期望：3 个 case 全部 `PASS`。

- [ ] **Step 5：提交**

```bash
git add internal/shell
git commit -m "feat(shell): add bash runner with env injection and stderr line callback"
```

---

# Phase 6：zoo 预处理器（TDD 全分支，目标 100% 覆盖）

> 本 Phase 是 Module A 最易出 bug 的部分；每一项行为都有独立测试。先写完整 zoo_test.go 框架（Task 18），再分四个步骤补齐实现（Task 19–22），最后加 atomic write + last-good rollback（Task 23）。

## Task 18：zoo 预处理器骨架 + 类型定义

**Files:**
- Create: `internal/config/zoo.go`
- Create: `internal/config/zoo_test.go`

- [ ] **Step 1：写 zoo.go 骨架**

```go
package config

import "encoding/json"

// PreprocessStats 是单次 zoo 预处理的统计结果，进入 status API。
type PreprocessStats struct {
    OutboundCount             int      `json:"outbound_count"`
    RuleSetCount              int      `json:"rule_set_count"`
    RuleSetDedupDropped       int      `json:"rule_set_dedup_dropped"`
    OutboundCollisionRejected bool     `json:"outbound_collision_rejected"`
    DroppedFields             []string `json:"dropped_fields"`
}

// PreprocessInput 描述一次预处理的输入。
type PreprocessInput struct {
    Raw                  []byte
    BuiltinRuleSetIndex  []RuleSetEntry // 来自所有静态 fragment 的 route.rule_set
    BuiltinOutboundTags  []string       // 静态 outbounds.json 的 tag 列表（如 DIRECT、REJECT）
}

// RuleSetEntry 描述 rule_set 的最少字段（tag + url）；按 url 去重。
type RuleSetEntry struct {
    Tag string `json:"tag"`
    URL string `json:"url,omitempty"`
}

// PreprocessResult 是一次成功预处理的产出。
type PreprocessResult struct {
    Rendered []byte
    Stats    PreprocessStats
}

// PreprocessError 表示预处理本身或其结果不接受（应回滚到 last-good）。
type PreprocessError struct {
    Stage string
    Err   error
}

func (e *PreprocessError) Error() string {
    return e.Stage + ": " + e.Err.Error()
}

func (e *PreprocessError) Unwrap() error { return e.Err }

// Preprocess 对 zoo.raw.json 的字节做白名单过滤、URL 去重、引用改写、撞名校验，
// 返回最终可写入 config.d/zoo.json 的字节。
//
// 当前未实现 —— Task 19–22 分步补全。
func Preprocess(in PreprocessInput) (*PreprocessResult, error) {
    var raw map[string]json.RawMessage
    if err := json.Unmarshal(in.Raw, &raw); err != nil {
        return nil, &PreprocessError{Stage: "parse", Err: err}
    }
    _ = raw // 后续 Task 在此基础上扩展
    return &PreprocessResult{}, nil
}
```

- [ ] **Step 2：写 zoo_test.go 全部测试用例（一次性铺好）**

```go
package config

import (
    "encoding/json"
    "errors"
    "strings"
    "testing"
)

// 帮助函数：建一个 zoo 输入字节
func zoo(t *testing.T, m map[string]any) []byte {
    t.Helper()
    b, err := json.Marshal(m)
    if err != nil {
        t.Fatal(err)
    }
    return b
}

func mustParse(t *testing.T, data []byte) map[string]any {
    t.Helper()
    var m map[string]any
    if err := json.Unmarshal(data, &m); err != nil {
        t.Fatalf("parse rendered: %v", err)
    }
    return m
}

// ---- Task 19: filter ----

func TestPreprocessKeepsOnlyWhitelistedKeys(t *testing.T) {
    in := PreprocessInput{
        Raw: zoo(t, map[string]any{
            "log":       map[string]any{"level": "trace"},
            "dns":       map[string]any{"servers": []any{}},
            "outbounds": []any{map[string]any{"type": "direct", "tag": "via-vps"}},
            "route": map[string]any{
                "rules":     []any{},
                "rule_set":  []any{},
                "final":     "via-vps",
                "auto_detect_interface": true, // 不在白名单
            },
            "experimental": map[string]any{"clash_api": map[string]any{}},
        }),
    }
    res, err := Preprocess(in)
    if err != nil {
        t.Fatalf("Preprocess: %v", err)
    }
    rendered := mustParse(t, res.Rendered)
    if _, ok := rendered["log"]; ok {
        t.Fatal("log should be dropped")
    }
    if _, ok := rendered["dns"]; ok {
        t.Fatal("dns should be dropped")
    }
    if _, ok := rendered["experimental"]; ok {
        t.Fatal("experimental should be dropped")
    }
    route, _ := rendered["route"].(map[string]any)
    if _, ok := route["auto_detect_interface"]; ok {
        t.Fatal("route.auto_detect_interface should be dropped")
    }
    if route["final"] != "via-vps" {
        t.Fatal("route.final preserved")
    }
    expect := []string{"dns", "log", "experimental", "route.auto_detect_interface"}
    for _, want := range expect {
        if !contains(res.Stats.DroppedFields, want) {
            t.Errorf("expected dropped field %q in stats", want)
        }
    }
    if res.Stats.OutboundCount != 1 {
        t.Fatal("OutboundCount mismatch")
    }
}

// ---- Task 20: dedup by URL ----

func TestPreprocessDedupRuleSetByURL(t *testing.T) {
    in := PreprocessInput{
        Raw: zoo(t, map[string]any{
            "outbounds": []any{},
            "route": map[string]any{
                "rule_set": []any{
                    map[string]any{"tag": "geosites-cn", "url": "https://x/geosites-cn.srs"},
                    map[string]any{"tag": "lan",         "url": "https://x/lan.srs"},
                    map[string]any{"tag": "extra",       "url": "https://x/extra.srs"},
                },
                "rules": []any{},
            },
        }),
        BuiltinRuleSetIndex: []RuleSetEntry{
            {Tag: "GeoSites@CN", URL: "https://x/geosites-cn.srs"},
            {Tag: "Lan",         URL: "https://x/lan.srs"},
        },
    }
    res, err := Preprocess(in)
    if err != nil {
        t.Fatal(err)
    }
    if res.Stats.RuleSetDedupDropped != 2 {
        t.Fatalf("expected 2 dropped, got %d", res.Stats.RuleSetDedupDropped)
    }
    if res.Stats.RuleSetCount != 1 {
        t.Fatalf("expected 1 remaining, got %d", res.Stats.RuleSetCount)
    }
    rendered := mustParse(t, res.Rendered)
    rs := rendered["route"].(map[string]any)["rule_set"].([]any)
    if len(rs) != 1 || rs[0].(map[string]any)["tag"] != "extra" {
        t.Fatalf("unexpected remaining rule_set: %#v", rs)
    }
}

// ---- Task 21: rewrite references ----

func TestPreprocessRewritesRouteRulesRefsToBuiltinTags(t *testing.T) {
    in := PreprocessInput{
        Raw: zoo(t, map[string]any{
            "outbounds": []any{},
            "route": map[string]any{
                "rule_set": []any{
                    map[string]any{"tag": "geosites-cn", "url": "https://x/geosites-cn.srs"},
                },
                "rules": []any{
                    map[string]any{"rule_set": "geosites-cn", "outbound": "DIRECT"},
                    map[string]any{"rule_set": "extra",       "outbound": "proxy"},
                },
            },
        }),
        BuiltinRuleSetIndex: []RuleSetEntry{
            {Tag: "GeoSites@CN", URL: "https://x/geosites-cn.srs"},
        },
    }
    res, err := Preprocess(in)
    if err != nil {
        t.Fatal(err)
    }
    rendered := mustParse(t, res.Rendered)
    rules := rendered["route"].(map[string]any)["rules"].([]any)
    if rules[0].(map[string]any)["rule_set"] != "GeoSites@CN" {
        t.Fatalf("rewrite failed: %#v", rules[0])
    }
    if rules[1].(map[string]any)["rule_set"] != "extra" {
        t.Fatalf("non-deduped rule_set should remain unchanged: %#v", rules[1])
    }
}

// ---- Task 22: outbound collision ----

func TestPreprocessRejectsOutboundTagCollision(t *testing.T) {
    in := PreprocessInput{
        Raw: zoo(t, map[string]any{
            "outbounds": []any{
                map[string]any{"type": "direct", "tag": "DIRECT"},
            },
            "route": map[string]any{"rules": []any{}, "rule_set": []any{}, "final": "DIRECT"},
        }),
        BuiltinOutboundTags: []string{"DIRECT", "REJECT"},
    }
    _, err := Preprocess(in)
    if err == nil {
        t.Fatal("expected collision error")
    }
    var pe *PreprocessError
    if !errors.As(err, &pe) {
        t.Fatalf("err type %T", err)
    }
    if pe.Stage != "outbound_collision" {
        t.Fatalf("stage: %s", pe.Stage)
    }
}

// ---- 集成：综合场景 ----

func TestPreprocessIntegrationWalkAll(t *testing.T) {
    in := PreprocessInput{
        Raw: zoo(t, map[string]any{
            "log":       map[string]any{"level": "trace"},
            "outbounds": []any{
                map[string]any{"type": "anytls", "tag": "jp"},
                map[string]any{"type": "anytls", "tag": "us"},
            },
            "route": map[string]any{
                "rule_set": []any{
                    map[string]any{"tag": "geosites-cn", "url": "https://x/geosites-cn.srs"},
                    map[string]any{"tag": "ads",         "url": "https://x/ads.srs"},
                },
                "rules": []any{
                    map[string]any{"rule_set": "geosites-cn", "outbound": "DIRECT"},
                    map[string]any{"rule_set": "ads",         "outbound": "REJECT"},
                },
                "final": "jp",
            },
        }),
        BuiltinRuleSetIndex: []RuleSetEntry{
            {Tag: "GeoSites@CN", URL: "https://x/geosites-cn.srs"},
        },
        BuiltinOutboundTags: []string{"DIRECT", "REJECT"},
    }
    res, err := Preprocess(in)
    if err != nil {
        t.Fatal(err)
    }
    if !contains(res.Stats.DroppedFields, "log") {
        t.Fatal("log should be marked dropped")
    }
    if res.Stats.OutboundCount != 2 || res.Stats.RuleSetCount != 1 || res.Stats.RuleSetDedupDropped != 1 {
        t.Fatalf("stats wrong: %+v", res.Stats)
    }
    rendered := mustParse(t, res.Rendered)
    rules := rendered["route"].(map[string]any)["rules"].([]any)
    if rules[0].(map[string]any)["rule_set"] != "GeoSites@CN" {
        t.Fatal("rewrite failed in integration")
    }
    if rendered["route"].(map[string]any)["final"] != "jp" {
        t.Fatal("final preserved")
    }
}

func TestPreprocessParseError(t *testing.T) {
    _, err := Preprocess(PreprocessInput{Raw: []byte("not json")})
    if err == nil {
        t.Fatal("expected parse error")
    }
    var pe *PreprocessError
    if !errors.As(err, &pe) {
        t.Fatalf("err type %T", err)
    }
    if pe.Stage != "parse" {
        t.Fatalf("stage: %s", pe.Stage)
    }
}

func TestPreprocessJSONOutputRetainsKeyOrder(t *testing.T) {
    // outbounds 应在 route 之前；rule_set 在 rules 之前；rules 在 final 之前。
    in := PreprocessInput{
        Raw: zoo(t, map[string]any{
            "route": map[string]any{
                "final":    "DIRECT",
                "rules":    []any{},
                "rule_set": []any{},
            },
            "outbounds": []any{},
        }),
    }
    res, err := Preprocess(in)
    if err != nil {
        t.Fatal(err)
    }
    s := string(res.Rendered)
    if !(strings.Index(s, `"outbounds"`) < strings.Index(s, `"route"`)) {
        t.Fatalf("expected outbounds before route in rendered JSON: %s", s)
    }
}

func contains(haystack []string, needle string) bool {
    for _, s := range haystack {
        if s == needle {
            return true
        }
    }
    return false
}
```

- [ ] **Step 3：跑测试看失败（预期大量 FAIL）**

```bash
go test ./internal/config/... -run TestPreprocess -v
```

期望：所有 TestPreprocess* 失败（实现尚为空）。这是基线，证明测试在跑。

- [ ] **Step 4：提交骨架（红色基线）**

```bash
git add internal/config/zoo.go internal/config/zoo_test.go
git commit -m "test(config): add full zoo preprocessor red-baseline tests"
```

---

## Task 19：实现白名单过滤

**Files:**
- Modify: `internal/config/zoo.go`

- [ ] **Step 1：在 zoo.go 中实现白名单 + JSON 序列化（保持顺序）**

把 `Preprocess` 函数与新增 helper 写为：

```go
package config

import (
    "bytes"
    "encoding/json"
    "fmt"
    "sort"
)

const (
    keyOutbounds      = "outbounds"
    keyRoute          = "route"
    keyRouteRules     = "rules"
    keyRouteRuleSet   = "rule_set"
    keyRouteFinal     = "final"
)

// 白名单（顶层 / route 子键）
var (
    topLevelWhitelist = map[string]struct{}{
        keyOutbounds: {}, keyRoute: {},
    }
    routeWhitelist = map[string]struct{}{
        keyRouteRules: {}, keyRouteRuleSet: {}, keyRouteFinal: {},
    }
)

func Preprocess(in PreprocessInput) (*PreprocessResult, error) {
    var raw map[string]json.RawMessage
    if err := json.Unmarshal(in.Raw, &raw); err != nil {
        return nil, &PreprocessError{Stage: "parse", Err: err}
    }

    var dropped []string
    for k := range raw {
        if _, ok := topLevelWhitelist[k]; !ok {
            dropped = append(dropped, k)
            delete(raw, k)
        }
    }

    // 处理 route 子键
    var route map[string]json.RawMessage
    if raw[keyRoute] != nil {
        if err := json.Unmarshal(raw[keyRoute], &route); err != nil {
            return nil, &PreprocessError{Stage: "parse_route", Err: err}
        }
        for k := range route {
            if _, ok := routeWhitelist[k]; !ok {
                dropped = append(dropped, "route."+k)
                delete(route, k)
            }
        }
    }
    sort.Strings(dropped)

    // 解析 outbounds 与 rule_set，便于统计与后续步骤
    var outbounds []map[string]any
    if raw[keyOutbounds] != nil {
        if err := json.Unmarshal(raw[keyOutbounds], &outbounds); err != nil {
            return nil, &PreprocessError{Stage: "parse_outbounds", Err: err}
        }
    }

    var ruleSetEntries []map[string]any
    if route != nil && route[keyRouteRuleSet] != nil {
        if err := json.Unmarshal(route[keyRouteRuleSet], &ruleSetEntries); err != nil {
            return nil, &PreprocessError{Stage: "parse_rule_set", Err: err}
        }
    }

    stats := PreprocessStats{
        OutboundCount: len(outbounds),
        RuleSetCount:  len(ruleSetEntries),
        DroppedFields: dropped,
    }

    // Task 20/21/22 在此基础上插入：
    // 1. dedup ruleSetEntries by URL → 改写 stats.RuleSetCount / Dropped
    // 2. rewrite route.rules tag refs
    // 3. outbound collision check

    rendered, err := renderZoo(outbounds, ruleSetEntries, route)
    if err != nil {
        return nil, &PreprocessError{Stage: "render", Err: err}
    }
    return &PreprocessResult{Rendered: rendered, Stats: stats}, nil
}

// renderZoo 输出顺序固定为 outbounds → route.{rule_set, rules, final}
func renderZoo(outbounds []map[string]any, ruleSet []map[string]any, route map[string]json.RawMessage) ([]byte, error) {
    out := newOrderedJSON()
    if outbounds != nil {
        out.Set(keyOutbounds, outbounds)
    }

    routeOut := newOrderedJSON()
    if ruleSet != nil {
        routeOut.Set(keyRouteRuleSet, ruleSet)
    }
    if route != nil && route[keyRouteRules] != nil {
        var rules []map[string]any
        if err := json.Unmarshal(route[keyRouteRules], &rules); err != nil {
            return nil, fmt.Errorf("parse route.rules: %w", err)
        }
        routeOut.Set(keyRouteRules, rules)
    }
    if route != nil && route[keyRouteFinal] != nil {
        var f any
        _ = json.Unmarshal(route[keyRouteFinal], &f)
        routeOut.Set(keyRouteFinal, f)
    }
    if len(routeOut.keys) > 0 {
        out.Set(keyRoute, routeOut)
    }

    var buf bytes.Buffer
    if err := json.NewEncoder(&buf).Encode(out); err != nil {
        return nil, err
    }
    return buf.Bytes(), nil
}

// orderedJSON 是局部使用的有序 map，确保输出顺序可预测。
// 与 internal/log/clef.OrderedEvent 类似，但避免跨包循环依赖。
type orderedJSON struct {
    keys   []string
    values map[string]any
}

func newOrderedJSON() *orderedJSON { return &orderedJSON{values: map[string]any{}} }

func (o *orderedJSON) Set(k string, v any) {
    if _, ok := o.values[k]; !ok {
        o.keys = append(o.keys, k)
    }
    o.values[k] = v
}

func (o *orderedJSON) MarshalJSON() ([]byte, error) {
    var buf bytes.Buffer
    buf.WriteByte('{')
    for i, k := range o.keys {
        if i > 0 {
            buf.WriteByte(',')
        }
        kb, _ := json.Marshal(k)
        buf.Write(kb)
        buf.WriteByte(':')
        vb, err := json.Marshal(o.values[k])
        if err != nil {
            return nil, err
        }
        buf.Write(vb)
    }
    buf.WriteByte('}')
    return buf.Bytes(), nil
}
```

- [ ] **Step 2：跑测试**

```bash
go test ./internal/config/... -run TestPreprocess -v
```

期望：`TestPreprocessKeepsOnlyWhitelistedKeys` / `TestPreprocessJSONOutputRetainsKeyOrder` / `TestPreprocessParseError` 通过；其它仍 FAIL。

- [ ] **Step 3：提交**

```bash
git add internal/config/zoo.go
git commit -m "feat(config): zoo preprocess implements whitelist filter + ordered render"
```

---

## Task 20：实现 rule_set 按 URL 去重

**Files:**
- Modify: `internal/config/zoo.go`

- [ ] **Step 1：在 Preprocess 中插入去重逻辑**

在 `stats := PreprocessStats{...}` 与 `rendered, err := renderZoo(...)` 之间插入以下代码（同时把 `_ = stats` 这类占位移除）：

```go
    // ---- dedup rule_set by URL（builtin_wins） ----
    rewriteMap := map[string]string{} // zooTag -> builtinTag
    var deduped []map[string]any
    if len(ruleSetEntries) > 0 {
        builtinByURL := map[string]string{}
        for _, e := range in.BuiltinRuleSetIndex {
            builtinByURL[e.URL] = e.Tag
        }
        for _, entry := range ruleSetEntries {
            url, _ := entry["url"].(string)
            tag, _ := entry["tag"].(string)
            if builtinTag, ok := builtinByURL[url]; ok && url != "" {
                rewriteMap[tag] = builtinTag
                stats.RuleSetDedupDropped++
                continue
            }
            deduped = append(deduped, entry)
        }
        ruleSetEntries = deduped
        stats.RuleSetCount = len(ruleSetEntries)
    }

    _ = rewriteMap // Task 21 启用
```

- [ ] **Step 2：跑测试**

```bash
go test ./internal/config/... -run TestPreprocess -v
```

期望：`TestPreprocessDedupRuleSetByURL` 通过；过滤/解析相关 case 仍通过。`Rewrite/Collision/Integration` 还会 FAIL。

- [ ] **Step 3：提交**

```bash
git add internal/config/zoo.go
git commit -m "feat(config): zoo preprocess deduplicates rule_set by URL (builtin wins)"
```

---

## Task 21：实现 route.rules 中 rule_set 引用改写

**Files:**
- Modify: `internal/config/zoo.go`

- [ ] **Step 1：使用 rewriteMap 改写 rules**

在 `renderZoo(...)` 调用之前，把 `route[keyRouteRules]` 的内容反序列化、按 `rewriteMap` 改写后再写回：

```go
    // ---- rewrite route.rules[*].rule_set 引用 ----
    if route != nil && len(rewriteMap) > 0 && route[keyRouteRules] != nil {
        var rules []map[string]any
        if err := json.Unmarshal(route[keyRouteRules], &rules); err != nil {
            return nil, &PreprocessError{Stage: "parse_route_rules", Err: err}
        }
        for _, r := range rules {
            if v, ok := r["rule_set"].(string); ok {
                if newTag, ok := rewriteMap[v]; ok {
                    r["rule_set"] = newTag
                }
            }
        }
        // 写回 route map（保持后续 renderZoo 一致）
        b, err := json.Marshal(rules)
        if err != nil {
            return nil, &PreprocessError{Stage: "render_route_rules", Err: err}
        }
        route[keyRouteRules] = b
    }
```

- [ ] **Step 2：跑测试**

```bash
go test ./internal/config/... -run TestPreprocess -v
```

期望：`TestPreprocessRewritesRouteRulesRefsToBuiltinTags` 通过；`Collision` 还 FAIL；`Integration` 通过（前提是 Outbound collision 不触发——本测试场景没有撞名）。

- [ ] **Step 3：提交**

```bash
git add internal/config/zoo.go
git commit -m "feat(config): rewrite zoo route.rules refs to deduped builtin tags"
```

---

## Task 22：实现 outbound tag 撞名拒绝

**Files:**
- Modify: `internal/config/zoo.go`

- [ ] **Step 1：在解析 outbounds 之后立刻插入撞名校验**

在 `outbounds` 解析后插入：

```go
    // ---- outbound tag collision ----
    if len(in.BuiltinOutboundTags) > 0 && len(outbounds) > 0 {
        builtinSet := map[string]struct{}{}
        for _, t := range in.BuiltinOutboundTags {
            builtinSet[t] = struct{}{}
        }
        for _, o := range outbounds {
            tag, _ := o["tag"].(string)
            if _, hit := builtinSet[tag]; hit {
                stats.OutboundCollisionRejected = true
                return nil, &PreprocessError{
                    Stage: "outbound_collision",
                    Err:   fmt.Errorf("zoo outbound tag %q collides with builtin", tag),
                }
            }
        }
    }
```

- [ ] **Step 2：跑测试**

```bash
go test ./internal/config/... -run TestPreprocess -v
```

期望：所有 TestPreprocess* 通过，包括 `TestPreprocessRejectsOutboundTagCollision` 与 `TestPreprocessIntegrationWalkAll`。

- [ ] **Step 3：跑覆盖率检查**

```bash
go test ./internal/config -coverprofile=/tmp/zoo.cov -run TestPreprocess
go tool cover -func=/tmp/zoo.cov | grep zoo.go
```

期望：`zoo.go` 行覆盖率 100%。如果未达标，补测试用例直到达标。

- [ ] **Step 4：提交**

```bash
git add internal/config/zoo.go
git commit -m "feat(config): reject zoo with outbound tag collisions; preprocessor 100% covered"
```

---

# 后续 Phase 接 part3

> 接下来的 Phase 7（state + state machine + ready）、Phase 8（fake-sing-box + supervisor 全流程）、Phase 9（HTTP API）、Phase 10（CLI）、Phase 11（install/uninstall/doctor）、Phase 12（main wire-up + cross-compile）请见 `2026-05-02-sing-router-module-a.part3.md`。

`part3` 同样按"测试先 → 看失败 → 实现 → 看通过 → 提交"的节奏推进，每个 Task 同样附上完整代码与命令。
