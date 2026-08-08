# LAN 客户端动态 bypass 白名单 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 让「自己已经跑了透明代理的 LAN 客户端」把自身 IP 注册到路由器，使其流量不被 sing-router 二次代理；租约由客户端心跳续约，过期由内核 ipset timeout 负责。

**Architecture:** 客户端（macOS，shell + launchd）每 30s 向路由器 `POST /api/v1/bypass` 续约当前 IP；daemon 校验 token 后调 `ipset -exist add client_bypass <ip> timeout <ttl>`；`startup.sh` 在 `sing-box` / `sing-box-mark` / `sing-box-dns` 三条链首插 `-m set --match-set client_bypass src -j RETURN`。daemon 不维护租约表、不落盘，过期完全交给内核。

**Tech Stack:** Go 1.26.2、cobra、BurntSushi/toml、busybox ash、iptables + ipset v7.6（内核 protocol 6）、launchd

**Spec:** `docs/superpowers/specs/2026-08-08-client-bypass-design.md`

## Global Constraints

- **仅 IPv4。** API 对 IPv6 地址返回 400 `bypass.ipv6_unsupported`，不静默丢弃。不建 v6 set、不动 `startup.sh` 的 v6 规则。
- **命名前缀。** `Routing.EnvVars` 中已有 `BYPASS_MARK`（fwmark `0x7890`），与本功能无关。本功能一律用：环境变量 `CLIENT_BYPASS_*`、ipset `client_bypass` / `client_bypass_static` / `client_bypass_mac`、toml 段 `[bypass]`。
- **默认关闭。** `daemon.toml.tmpl` 默认 `[bypass].enabled = false` 且 `[http].listen = "127.0.0.1:9998"`，不装本功能时行为与今天完全一致。
- **`ipsetRun` 只看退出码。** 目标设备 userspace v7.6 / 内核 protocol 6，每次 `ipset` 调用都可能往 stderr 打 `Warning: Kernel support protocol versions 6-6...`。把 stderr 非空当失败会让每次续约都报错。
- **`teardown.sh` 顺序不可调换。** 必须先拆 iptables 再 `ipset destroy`，已实测 `it is in use by a kernel component`。
- **`client_bypass` 动态 set 在 teardown 时刻意保留**，只有 `uninstall` 才销毁。
- **busybox ash 兼容。** 写入 `assets/shell/` 的脚本禁止 `[ -n "$X" ] && cmd` 形式——`set -eu` 下条件为假时整体退出码为 1，会掀掉整个脚本。一律用 `if/fi`。
- **每个任务结束跑 `go test ./...` 与 `go vet ./...`**；改了 `assets/` 的任务额外跑 `go test ./assets/`。

---

## File Structure

| 文件 | 职责 | 动作 |
|---|---|---|
| `internal/config/bypass.go` | `[bypass]` 段的默认值、校验、环境变量渲染 | 新建 |
| `internal/config/bypass_test.go` | 上者的单测 | 新建 |
| `internal/config/daemon_toml.go` | 加 `BypassConfig` 结构与 `Bypass` 字段 | 修改 |
| `internal/daemon/auth.go` | 按来源分权的鉴权中间件 + LAN 路径白名单 | 新建 |
| `internal/daemon/auth_test.go` | 鉴权矩阵 | 新建 |
| `internal/daemon/bypass.go` | bypass handler + ipset 调用封装 | 新建 |
| `internal/daemon/bypass_test.go` | 参数校验 + argv 断言 | 新建 |
| `internal/daemon/api.go` | `APIDeps` 加 `Bypass`；`ServeHTTP` 改 tcp4 | 修改 |
| `internal/daemon/daemon.go` | `Options` 加 `Bypass` / `HTTPToken`；包中间件 | 修改 |
| `internal/cli/wireup_daemon.go` | 读 `[bypass]` 配置并接线；合并环境变量 | 修改 |
| `assets/shell/startup.sh` | 建 set + 三条链插 RETURN | 修改 |
| `assets/shell/teardown.sh` | 销毁静态 set，保留动态 set | 修改 |
| `assets/daemon.toml.tmpl` | 加 `[bypass]` 段与 `[http].token` 模板变量 | 修改 |
| `assets/embed_test.go` | 静态特征把设计决定钉死 | 修改 |
| `internal/install/seed.go` | `TemplateVars` 加三个字段 | 修改 |
| `internal/cli/install.go` | `--enable-bypass` / `--http-token` | 修改 |
| `internal/cli/uninstall.go` | 销毁 `client_bypass` | 修改 |
| `internal/cli/doctor_bypass.go` | doctor 的 bypass 一节 | 新建 |
| `internal/cli/doctor_routing.go` | `checkRouting` 签名加 `config.Bypass` | 修改 |
| `tests/docker/docker-test.sh` | 端到端 Phase G | 修改 |
| `contrib/macos/bypass-agent.sh` | 客户端心跳脚本 | 新建 |
| `contrib/macos/bypass-agent.conf.example` | 配置样例 | 新建 |
| `contrib/macos/moonfruit.sing-bypass.plist` | launchd 定义 | 新建 |
| `contrib/macos/README.md` | 部署步骤 | 新建 |

---

### Task 1: config 层 —— `[bypass]` 段解析、校验、环境变量

**Files:**
- Create: `internal/config/bypass.go`
- Create: `internal/config/bypass_test.go`
- Modify: `internal/config/daemon_toml.go`（加 `BypassConfig` 结构与 `DaemonConfig.Bypass` 字段）

**Interfaces:**
- Consumes: `config.DaemonConfig`（现有）
- Produces:
  - `config.Bypass{Enabled bool; DefaultTTLSec, MaxTTLSec int; StaticIPs, StaticMACs []string}`
  - `config.DefaultBypass() Bypass`
  - `config.LoadBypass(cfg *DaemonConfig) Bypass`
  - `(Bypass) Validate(httpToken string) error`
  - `(Bypass) EnvVars() map[string]string`

- [ ] **Step 1: 写失败的测试**

创建 `internal/config/bypass_test.go`：

```go
package config

import (
	"strings"
	"testing"
)

func TestDefaultBypassIsDisabled(t *testing.T) {
	b := DefaultBypass()
	if b.Enabled {
		t.Fatal("bypass must default to disabled")
	}
	if b.DefaultTTLSec != 120 || b.MaxTTLSec != 600 {
		t.Fatalf("default ttl = %d, max ttl = %d", b.DefaultTTLSec, b.MaxTTLSec)
	}
}

func TestLoadBypassOverridesDefaults(t *testing.T) {
	ttl, max := 30, 90
	cfg := &DaemonConfig{Bypass: BypassConfig{
		Enabled:       true,
		DefaultTTLSec: &ttl,
		MaxTTLSec:     &max,
		StaticIPs:     []string{"192.168.50.7"},
		StaticMACs:    []string{"00:E0:4C:67:01:46"},
	}}
	b := LoadBypass(cfg)
	if !b.Enabled || b.DefaultTTLSec != 30 || b.MaxTTLSec != 90 {
		t.Fatalf("loaded: %+v", b)
	}
	if len(b.StaticIPs) != 1 || b.StaticIPs[0] != "192.168.50.7" {
		t.Fatalf("static ips: %v", b.StaticIPs)
	}
	if len(b.StaticMACs) != 1 || b.StaticMACs[0] != "00:E0:4C:67:01:46" {
		t.Fatalf("static macs: %v", b.StaticMACs)
	}
}

func TestLoadBypassNilConfigReturnsDefaults(t *testing.T) {
	if LoadBypass(nil).Enabled {
		t.Fatal("nil cfg must yield disabled bypass")
	}
}

// token 是本功能唯一的身份来源，不允许空跑。
func TestValidateRejectsEnabledWithoutToken(t *testing.T) {
	b := DefaultBypass()
	b.Enabled = true
	err := b.Validate("")
	if err == nil {
		t.Fatal("expected error when enabled with empty token")
	}
	if !strings.Contains(err.Error(), "[http].token") {
		t.Fatalf("error should name the missing key: %v", err)
	}
}

func TestValidateAcceptsEnabledWithToken(t *testing.T) {
	b := DefaultBypass()
	b.Enabled = true
	if err := b.Validate("deadbeef"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

// 关闭时不校验任何东西——用户没启用就不该被配置错误挡住启动。
func TestValidateSkipsWhenDisabled(t *testing.T) {
	b := DefaultBypass()
	b.DefaultTTLSec = 0
	if err := b.Validate(""); err != nil {
		t.Fatalf("disabled bypass must not be validated: %v", err)
	}
}

func TestValidateRejectsBadTTLRange(t *testing.T) {
	b := DefaultBypass()
	b.Enabled = true
	b.DefaultTTLSec = 900
	b.MaxTTLSec = 600
	if err := b.Validate("tok"); err == nil {
		t.Fatal("default_ttl_sec > max_ttl_sec must fail")
	}
}

func TestEnvVarsDisabledYieldsEmptyEnabledFlag(t *testing.T) {
	env := DefaultBypass().EnvVars()
	if env["CLIENT_BYPASS_ENABLED"] != "" {
		t.Fatalf("disabled must render empty flag, got %q", env["CLIENT_BYPASS_ENABLED"])
	}
	// 键必须存在——startup.sh 用 ${CLIENT_BYPASS_STATIC_IPS} 无默认值展开。
	for _, k := range []string{"CLIENT_BYPASS_TTL", "CLIENT_BYPASS_STATIC_IPS", "CLIENT_BYPASS_STATIC_MACS"} {
		if _, ok := env[k]; !ok {
			t.Fatalf("env must always define %s", k)
		}
	}
}

func TestEnvVarsEnabledJoinsListsWithSpace(t *testing.T) {
	b := DefaultBypass()
	b.Enabled = true
	b.StaticIPs = []string{"192.168.50.7", "192.168.50.8"}
	b.StaticMACs = []string{"AA:BB:CC:DD:EE:FF"}
	env := b.EnvVars()
	if env["CLIENT_BYPASS_ENABLED"] != "1" {
		t.Fatalf("enabled flag = %q", env["CLIENT_BYPASS_ENABLED"])
	}
	if env["CLIENT_BYPASS_TTL"] != "120" {
		t.Fatalf("ttl = %q", env["CLIENT_BYPASS_TTL"])
	}
	if env["CLIENT_BYPASS_STATIC_IPS"] != "192.168.50.7 192.168.50.8" {
		t.Fatalf("static ips = %q", env["CLIENT_BYPASS_STATIC_IPS"])
	}
	if env["CLIENT_BYPASS_STATIC_MACS"] != "AA:BB:CC:DD:EE:FF" {
		t.Fatalf("static macs = %q", env["CLIENT_BYPASS_STATIC_MACS"])
	}
}
```

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./internal/config/ -run 'Bypass' -v`
Expected: FAIL，编译错误 `undefined: DefaultBypass` / `undefined: BypassConfig`

- [ ] **Step 3: 加 toml 结构**

在 `internal/config/daemon_toml.go` 的 `DaemonConfig` 结构里，紧跟 `HTTP HTTPConfig` 之后加一行：

```go
	Bypass     BypassConfig     `toml:"bypass"`
```

在 `HTTPConfig` 定义之后加：

```go
// BypassConfig 是 [bypass] 段的原始解析结果。指针字段用于区分「用户显式写了 0」
// 与「用户没写」；Enabled 无此需求（零值 false 正是我们要的默认）。
type BypassConfig struct {
	Enabled       bool     `toml:"enabled"`
	DefaultTTLSec *int     `toml:"default_ttl_sec"`
	MaxTTLSec     *int     `toml:"max_ttl_sec"`
	StaticIPs     []string `toml:"static_ips"`
	StaticMACs    []string `toml:"static_macs"`
}
```

- [ ] **Step 4: 实现 bypass.go**

创建 `internal/config/bypass.go`：

```go
package config

import (
	"fmt"
	"strconv"
	"strings"
)

// Bypass 是 [bypass] 段填好默认值后的形态：LAN 客户端动态白名单。
//
// 【与 Routing.BypassMark 无关】那个是 fwmark 0x7890，语义为「打了这个 mark 的
// 包不走代理」；这里是「某个 LAN 客户端整体不走代理」。两者同名前缀极易混淆，
// 故本功能的环境变量与 ipset 一律用 CLIENT_BYPASS_ / client_bypass 前缀。
type Bypass struct {
	Enabled       bool
	DefaultTTLSec int
	MaxTTLSec     int
	StaticIPs     []string
	StaticMACs    []string
}

// DefaultBypass 返回固化默认值。默认关闭：不启用时整个功能等于不存在。
func DefaultBypass() Bypass {
	return Bypass{
		Enabled:       false,
		DefaultTTLSec: 120,
		MaxTTLSec:     600,
	}
}

// LoadBypass 用 cfg 中的字段覆盖默认。
func LoadBypass(cfg *DaemonConfig) Bypass {
	b := DefaultBypass()
	if cfg == nil {
		return b
	}
	b.Enabled = cfg.Bypass.Enabled
	if cfg.Bypass.DefaultTTLSec != nil {
		b.DefaultTTLSec = *cfg.Bypass.DefaultTTLSec
	}
	if cfg.Bypass.MaxTTLSec != nil {
		b.MaxTTLSec = *cfg.Bypass.MaxTTLSec
	}
	b.StaticIPs = cfg.Bypass.StaticIPs
	b.StaticMACs = cfg.Bypass.StaticMACs
	return b
}

// Validate 在 daemon 启动时调用，httpToken 传 [http].token 的值。
// 关闭时一律通过——用户没启用就不该被本功能的配置错误挡住启动。
func (b Bypass) Validate(httpToken string) error {
	if !b.Enabled {
		return nil
	}
	if httpToken == "" {
		return fmt.Errorf("[bypass].enabled = true requires a non-empty [http].token " +
			"(token is the only identity source for LAN clients)")
	}
	if b.DefaultTTLSec < 1 || b.MaxTTLSec < 1 || b.DefaultTTLSec > b.MaxTTLSec {
		return fmt.Errorf("[bypass]: need 1 <= default_ttl_sec (%d) <= max_ttl_sec (%d)",
			b.DefaultTTLSec, b.MaxTTLSec)
	}
	return nil
}

// EnvVars 渲染传给 startup.sh / teardown.sh 的环境变量。
// 调用方用 maps.Copy 与 Routing.EnvVars 的结果合并。
//
// 四个键无论启用与否都存在：startup.sh 里 $CLIENT_BYPASS_STATIC_IPS 是无默认值
// 展开，脚本开头 set -u 下缺键会直接报错退出。
func (b Bypass) EnvVars() map[string]string {
	enabled := ""
	if b.Enabled {
		enabled = "1"
	}
	return map[string]string{
		"CLIENT_BYPASS_ENABLED":     enabled,
		"CLIENT_BYPASS_TTL":         strconv.Itoa(b.DefaultTTLSec),
		"CLIENT_BYPASS_STATIC_IPS":  strings.Join(b.StaticIPs, " "),
		"CLIENT_BYPASS_STATIC_MACS": strings.Join(b.StaticMACs, " "),
	}
}
```

- [ ] **Step 5: 跑测试确认通过**

Run: `go test ./internal/config/ -run 'Bypass' -v`
Expected: PASS（8 个用例）

- [ ] **Step 6: 跑全量测试与 vet**

Run: `go test ./... && go vet ./...`
Expected: 全部 PASS

- [ ] **Step 7: 提交**

```bash
git add internal/config/bypass.go internal/config/bypass_test.go internal/config/daemon_toml.go
git commit -m "feat(config): 新增 [bypass] 段解析、校验与环境变量渲染

CLIENT_BYPASS_ 前缀与既有 BYPASS_MARK（fwmark）区分开。
enabled=true 时强制要求非空 [http].token。"
```

---

### Task 2: 鉴权中间件（按来源分权 + LAN 路径白名单）

**Files:**
- Create: `internal/daemon/auth.go`
- Create: `internal/daemon/auth_test.go`

**Interfaces:**
- Consumes: `daemon.writeError`（`api.go` 现有，未导出）
- Produces:
  - `daemon.AuthConfig{Token string; BypassEnabled bool}`
  - `daemon.AuthMiddleware(next http.Handler, cfg AuthConfig) http.Handler`
  - `daemon.isLoopbackAddr(remoteAddr string) bool`（包内）

- [ ] **Step 1: 写失败的测试**

创建 `internal/daemon/auth_test.go`：

```go
package daemon

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// okHandler 是被中间件包住的下游；命中即写 200。
func okHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
}

// doReq 直接调 ServeHTTP，用 httptest.NewRequest 后覆写 RemoteAddr，
// 从而不依赖真实网络就能模拟 loopback / LAN 两种来源。
func doReq(t *testing.T, h http.Handler, method, path, remoteAddr, token string) int {
	t.Helper()
	req := httptest.NewRequest(method, path, nil)
	req.RemoteAddr = remoteAddr
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec.Code
}

func TestLoopbackBypassesTokenOnEveryEndpoint(t *testing.T) {
	h := AuthMiddleware(okHandler(), AuthConfig{Token: "secret", BypassEnabled: true})
	for _, path := range []string{"/api/v1/status", "/api/v1/shutdown", "/api/v1/bypass"} {
		if code := doReq(t, h, http.MethodPost, path, "127.0.0.1:5555", ""); code != http.StatusOK {
			t.Errorf("loopback %s: got %d, want 200", path, code)
		}
	}
}

func TestLoopbackIPv6AlsoBypassesToken(t *testing.T) {
	h := AuthMiddleware(okHandler(), AuthConfig{Token: "secret", BypassEnabled: true})
	if code := doReq(t, h, http.MethodGet, "/api/v1/status", "[::1]:5555", ""); code != http.StatusOK {
		t.Fatalf("got %d, want 200", code)
	}
}

func TestLANWithValidTokenReachesBypassEndpoint(t *testing.T) {
	h := AuthMiddleware(okHandler(), AuthConfig{Token: "secret", BypassEnabled: true})
	if code := doReq(t, h, http.MethodPost, "/api/v1/bypass", "192.168.50.80:5555", "secret"); code != http.StatusOK {
		t.Fatalf("got %d, want 200", code)
	}
}

// 白名单之外的端点即使 token 正确也不给 LAN——这是本中间件存在的理由。
func TestLANCannotReachManagementEndpoints(t *testing.T) {
	h := AuthMiddleware(okHandler(), AuthConfig{Token: "secret", BypassEnabled: true})
	for _, path := range []string{"/api/v1/shutdown", "/api/v1/restart", "/api/v1/apply", "/api/v1/status"} {
		if code := doReq(t, h, http.MethodPost, path, "192.168.50.80:5555", "secret"); code != http.StatusForbidden {
			t.Errorf("LAN %s: got %d, want 403", path, code)
		}
	}
}

// 读操作只给 loopback：持 token 的客户端能写不能枚举当前放行了谁。
func TestLANCannotReadBypassSet(t *testing.T) {
	h := AuthMiddleware(okHandler(), AuthConfig{Token: "secret", BypassEnabled: true})
	if code := doReq(t, h, http.MethodGet, "/api/v1/bypass", "192.168.50.80:5555", "secret"); code != http.StatusForbidden {
		t.Fatalf("got %d, want 403", code)
	}
}

func TestLANWithBadTokenIsUnauthorized(t *testing.T) {
	h := AuthMiddleware(okHandler(), AuthConfig{Token: "secret", BypassEnabled: true})
	if code := doReq(t, h, http.MethodPost, "/api/v1/bypass", "192.168.50.80:5555", "wrong"); code != http.StatusUnauthorized {
		t.Fatalf("got %d, want 401", code)
	}
}

func TestLANWithoutTokenIsUnauthorized(t *testing.T) {
	h := AuthMiddleware(okHandler(), AuthConfig{Token: "secret", BypassEnabled: true})
	if code := doReq(t, h, http.MethodPost, "/api/v1/bypass", "192.168.50.80:5555", ""); code != http.StatusUnauthorized {
		t.Fatalf("got %d, want 401", code)
	}
}

// bypass 关闭时白名单整体失效，LAN 什么都碰不到。
func TestBypassDisabledClosesLANEntirely(t *testing.T) {
	h := AuthMiddleware(okHandler(), AuthConfig{Token: "secret", BypassEnabled: false})
	if code := doReq(t, h, http.MethodPost, "/api/v1/bypass", "192.168.50.80:5555", "secret"); code != http.StatusForbidden {
		t.Fatalf("got %d, want 403", code)
	}
}

// 转发头由客户端控制，采信即等于没有鉴权。
func TestForwardedHeadersAreIgnored(t *testing.T) {
	h := AuthMiddleware(okHandler(), AuthConfig{Token: "secret", BypassEnabled: true})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/shutdown", nil)
	req.RemoteAddr = "192.168.50.80:5555"
	req.Header.Set("X-Forwarded-For", "127.0.0.1")
	req.Header.Set("X-Real-IP", "127.0.0.1")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("got %d, want 403 (forwarded headers must not grant loopback)", rec.Code)
	}
}

// 解析不出来的 RemoteAddr 一律按非 loopback 处理（fail closed）。
func TestUnparsableRemoteAddrIsNotLoopback(t *testing.T) {
	for _, addr := range []string{"", "garbage", "not-an-ip:1"} {
		if isLoopbackAddr(addr) {
			t.Errorf("%q must not be treated as loopback", addr)
		}
	}
}

func TestIsLoopbackAddrRecognisesLocalRanges(t *testing.T) {
	for _, addr := range []string{"127.0.0.1:1", "127.9.9.9:1", "[::1]:1"} {
		if !isLoopbackAddr(addr) {
			t.Errorf("%q must be loopback", addr)
		}
	}
	for _, addr := range []string{"192.168.50.80:1", "10.0.0.1:1", "[fe80::1]:1"} {
		if isLoopbackAddr(addr) {
			t.Errorf("%q must not be loopback", addr)
		}
	}
}
```

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./internal/daemon/ -run 'Auth|Loopback|LAN|Forwarded|Bypass Disabled' -v`
Expected: FAIL，编译错误 `undefined: AuthMiddleware`

- [ ] **Step 3: 实现 auth.go**

创建 `internal/daemon/auth.go`：

```go
package daemon

import (
	"crypto/subtle"
	"net"
	"net/http"
	"strings"
)

// lanAllowed 是允许非 loopback 来源访问的路径白名单。
//
// 【白名单而非黑名单是刻意的】新增端点默认不在表里，失败模式因此从「忘了加
// 检查就全暴露」反转成「忘了加白名单就用不了」——后者会在测试里立刻暴露，
// 前者要等到被人扫到端口才知道。往这里加路径前先想清楚它是否该对 LAN 开放。
var lanAllowed = map[string]bool{
	"/api/v1/bypass": true,
}

// AuthConfig 控制 AuthMiddleware 的行为。
type AuthConfig struct {
	Token         string // [http].token；为空时非 loopback 一律拒绝
	BypassEnabled bool   // [bypass].enabled；false 时 LAN 白名单整体失效
}

// AuthMiddleware 按来源分权：
//   - loopback：免 token，全部端点可用（CLI 行为完全不变）
//   - 其它来源：必须带 token，只能访问 lanAllowed 中的路径，且只能写不能读
//
// 【安全约束一】绝不能在这个 handler 前面挂反向代理。那会让所有请求的
// RemoteAddr 都变成 127.0.0.1，等于全世界免 token 拿到 /shutdown。
//
// 【安全约束二】刻意不读 X-Forwarded-For / X-Real-IP。转发头由客户端控制，
// 采信它等于把鉴权交给攻击者自己填。
func AuthMiddleware(next http.Handler, cfg AuthConfig) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if isLoopbackAddr(r.RemoteAddr) {
			next.ServeHTTP(w, r)
			return
		}
		if !cfg.BypassEnabled || !lanAllowed[r.URL.Path] {
			writeError(w, http.StatusForbidden, "auth.lan_forbidden",
				"this endpoint is only reachable from loopback", nil)
			return
		}
		// 读操作只给 loopback：持 token 的客户端能写不能枚举当前放行了谁。
		if r.Method == http.MethodGet {
			writeError(w, http.StatusForbidden, "auth.lan_forbidden",
				"reading the bypass set is only allowed from loopback", nil)
			return
		}
		if cfg.Token == "" || !tokenEqual(bearerToken(r), cfg.Token) {
			writeError(w, http.StatusUnauthorized, "auth.bad_token",
				"missing or invalid bearer token", nil)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// isLoopbackAddr 解析 http.Request.RemoteAddr（形如 "IP:port" 或 "[v6]:port"）
// 判断是否环回。解析失败一律按非 loopback 处理（fail closed）。
func isLoopbackAddr(remoteAddr string) bool {
	host, _, err := net.SplitHostPort(remoteAddr)
	if err != nil {
		host = remoteAddr
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return false
	}
	return ip.IsLoopback()
}

func bearerToken(r *http.Request) string {
	if after, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer "); ok {
		return after
	}
	return ""
}

// tokenEqual 用常数时间比较，避免按字节早退泄露 token 前缀。
func tokenEqual(got, want string) bool {
	return subtle.ConstantTimeCompare([]byte(got), []byte(want)) == 1
}
```

- [ ] **Step 4: 跑测试确认通过**

Run: `go test ./internal/daemon/ -run 'Loopback|LAN|Forwarded|Unparsable|BypassDisabled' -v`
Expected: PASS

- [ ] **Step 5: 跑全量测试与 vet**

Run: `go test ./... && go vet ./...`
Expected: 全部 PASS

- [ ] **Step 6: 提交**

```bash
git add internal/daemon/auth.go internal/daemon/auth_test.go
git commit -m "feat(daemon): 按来源分权的鉴权中间件

loopback 免 token 全权；LAN 需 token 且只能访问白名单路径、只写不读。
白名单而非黑名单：新增端点默认不暴露。不信任 X-Forwarded-For。"
```

---

### Task 3: bypass handler + ipset 调用

**Files:**
- Create: `internal/daemon/bypass.go`
- Create: `internal/daemon/bypass_test.go`
- Modify: `internal/daemon/api.go`（`APIDeps` 加 `Bypass *BypassDeps`；`NewMux` 注册路由）

**Interfaces:**
- Consumes: `daemon.writeJSON` / `daemon.writeError`（`api.go` 现有）
- Produces:
  - `daemon.ClientBypassSet = "client_bypass"`（常量）
  - `daemon.BypassDeps{Enabled bool; DefaultTTLSec, MaxTTLSec int; IpsetRun func(ctx, ...string) error; IpsetList func(ctx, string) (string, error)}`
  - `APIDeps.Bypass *BypassDeps`

- [ ] **Step 1: 写失败的测试**

创建 `internal/daemon/bypass_test.go`：

```go
package daemon

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// fakeIpset 记录收到的每一次 argv，供逐字断言。
type fakeIpset struct {
	mu    sync.Mutex
	calls [][]string
	err   error
}

func (f *fakeIpset) run(_ context.Context, args ...string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, args)
	return f.err
}

func (f *fakeIpset) got(i int) []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls[i]
}

func newBypassMux(fake *fakeIpset) http.Handler {
	return NewMux(APIDeps{Bypass: &BypassDeps{
		Enabled:       true,
		DefaultTTLSec: 120,
		MaxTTLSec:     600,
		IpsetRun:      fake.run,
	}})
}

func postBypass(t *testing.T, h http.Handler, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/bypass", strings.NewReader(body))
	req.RemoteAddr = "127.0.0.1:5555"
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestBypassRegisterInvokesIpsetWithExactArgv(t *testing.T) {
	fake := &fakeIpset{}
	rec := postBypass(t, newBypassMux(fake), `{"ips":["192.168.50.80"],"ttl_sec":120}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d body %s", rec.Code, rec.Body.String())
	}
	want := []string{"-exist", "add", "client_bypass", "192.168.50.80", "timeout", "120"}
	got := fake.got(0)
	if len(got) != len(want) {
		t.Fatalf("argv = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("argv = %v, want %v", got, want)
		}
	}
}

func TestBypassRegisterUsesDefaultTTLWhenOmitted(t *testing.T) {
	fake := &fakeIpset{}
	rec := postBypass(t, newBypassMux(fake), `{"ips":["192.168.50.80"]}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d", rec.Code)
	}
	got := fake.got(0)
	if got[5] != "120" {
		t.Fatalf("ttl arg = %q, want 120", got[5])
	}
}

func TestBypassRegisterAcceptsMultipleIPs(t *testing.T) {
	fake := &fakeIpset{}
	rec := postBypass(t, newBypassMux(fake), `{"ips":["192.168.50.80","192.168.50.81"]}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d", rec.Code)
	}
	if len(fake.calls) != 2 {
		t.Fatalf("expected 2 ipset calls, got %d", len(fake.calls))
	}
}

// 本次仅 IPv4：v6 显式拒绝而非静默丢弃，否则用户注册了却查不出为何不生效。
func TestBypassRejectsIPv6Explicitly(t *testing.T) {
	fake := &fakeIpset{}
	rec := postBypass(t, newBypassMux(fake), `{"ips":["2408:820c::1"]}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status %d, want 400", rec.Code)
	}
	if code := errCode(t, rec); code != "bypass.ipv6_unsupported" {
		t.Fatalf("error code = %q", code)
	}
	if len(fake.calls) != 0 {
		t.Fatal("must not touch ipset on validation failure")
	}
}

// 全有或全无：一个地址不合法则整个请求失败，避免客户端以为全部注册成功。
func TestBypassRejectsWholeRequestOnOneBadIP(t *testing.T) {
	fake := &fakeIpset{}
	rec := postBypass(t, newBypassMux(fake), `{"ips":["192.168.50.80","nonsense"]}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status %d, want 400", rec.Code)
	}
	if code := errCode(t, rec); code != "bypass.bad_ip" {
		t.Fatalf("error code = %q", code)
	}
	if len(fake.calls) != 0 {
		t.Fatal("must not partially apply")
	}
}

func TestBypassRejectsTTLAboveMax(t *testing.T) {
	fake := &fakeIpset{}
	rec := postBypass(t, newBypassMux(fake), `{"ips":["192.168.50.80"],"ttl_sec":99999}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status %d, want 400", rec.Code)
	}
	if code := errCode(t, rec); code != "bypass.bad_ttl" {
		t.Fatalf("error code = %q", code)
	}
}

func TestBypassRejectsEmptyAndOversizedIPList(t *testing.T) {
	fake := &fakeIpset{}
	if rec := postBypass(t, newBypassMux(fake), `{"ips":[]}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("empty list: status %d, want 400", rec.Code)
	}
	many := make([]string, 0, 17)
	for i := 0; i < 17; i++ {
		many = append(many, `"192.168.50.80"`)
	}
	body := `{"ips":[` + strings.Join(many, ",") + `]}`
	if rec := postBypass(t, newBypassMux(fake), body); rec.Code != http.StatusBadRequest {
		t.Fatalf("oversized list: status %d, want 400", rec.Code)
	}
}

func TestBypassRevokeInvokesIpsetDel(t *testing.T) {
	fake := &fakeIpset{}
	req := httptest.NewRequest(http.MethodDelete, "/api/v1/bypass",
		strings.NewReader(`{"ips":["192.168.50.80"]}`))
	req.RemoteAddr = "127.0.0.1:5555"
	rec := httptest.NewRecorder()
	newBypassMux(fake).ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d", rec.Code)
	}
	want := []string{"-exist", "del", "client_bypass", "192.168.50.80"}
	got := fake.got(0)
	if len(got) != len(want) {
		t.Fatalf("argv = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("argv = %v, want %v", got, want)
		}
	}
}

func TestBypassListParsesMembersWithTTL(t *testing.T) {
	out := "Name: client_bypass\nType: hash:ip\nHeader: family inet timeout 120\n" +
		"Members:\n192.168.50.80 timeout 118\n192.168.50.81 timeout 0\n"
	mux := NewMux(APIDeps{Bypass: &BypassDeps{
		Enabled: true, DefaultTTLSec: 120, MaxTTLSec: 600,
		IpsetList: func(_ context.Context, _ string) (string, error) { return out, nil },
	}})
	req := httptest.NewRequest(http.MethodGet, "/api/v1/bypass", nil)
	req.RemoteAddr = "127.0.0.1:5555"
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d", rec.Code)
	}
	var body struct {
		Entries []struct {
			IP         string `json:"ip"`
			TimeoutSec int    `json:"timeout_sec"`
		} `json:"entries"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if len(body.Entries) != 2 {
		t.Fatalf("entries = %+v", body.Entries)
	}
	if body.Entries[0].IP != "192.168.50.80" || body.Entries[0].TimeoutSec != 118 {
		t.Fatalf("entry 0 = %+v", body.Entries[0])
	}
}

// 未启用时端点根本不注册，命中 mux 的 404 而非 handler。
func TestBypassEndpointAbsentWhenDisabled(t *testing.T) {
	mux := NewMux(APIDeps{})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/bypass", strings.NewReader(`{"ips":["1.2.3.4"]}`))
	req.RemoteAddr = "127.0.0.1:5555"
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status %d, want 404", rec.Code)
	}
}

func errCode(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	var body struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode error body %q: %v", rec.Body.String(), err)
	}
	return body.Error.Code
}
```

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./internal/daemon/ -run 'Bypass' -v`
Expected: FAIL，编译错误 `undefined: BypassDeps`

- [ ] **Step 3: 实现 bypass.go**

创建 `internal/daemon/bypass.go`：

```go
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
```

- [ ] **Step 4: 在 api.go 注册路由**

在 `internal/daemon/api.go` 的 `APIDeps` 结构末尾（`ShutdownHook` 之后）加：

```go
	// Bypass 非 nil 且 Enabled 时才注册 /api/v1/bypass。未启用时端点根本不存在，
	// 连 404 之外的信息都不泄露。
	Bypass *BypassDeps
```

在 `NewMux` 里 `mux.HandleFunc("/api/v1/shutdown", ...)` 之后、`return mux` 之前加：

```go
	if deps.Bypass != nil && deps.Bypass.Enabled {
		mux.HandleFunc("/api/v1/bypass", deps.Bypass.handle)
	}
```

- [ ] **Step 5: 跑测试确认通过**

Run: `go test ./internal/daemon/ -run 'Bypass' -v`
Expected: PASS（10 个用例）

- [ ] **Step 6: 跑全量测试与 vet**

Run: `go test ./... && go vet ./...`
Expected: 全部 PASS

- [ ] **Step 7: 提交**

```bash
git add internal/daemon/bypass.go internal/daemon/bypass_test.go internal/daemon/api.go
git commit -m "feat(daemon): bypass 注册端点与 ipset 调用

POST/DELETE/GET /api/v1/bypass；-exist add 续约（对已过期条目等价于重建）。
v6 显式 400 拒绝；参数校验全有或全无。ipsetRun 只看退出码不看 stderr。"
```

---

### Task 4: ServeHTTP 改 tcp4 + daemon 接线

**Files:**
- Modify: `internal/daemon/api.go`（`ServeHTTP` 改用 `net.Listen("tcp4", …)`）
- Modify: `internal/daemon/daemon.go`（`Options` 加 `HTTPToken` / `Bypass`；用 `AuthMiddleware` 包 mux）
- Modify: `internal/cli/wireup_daemon.go`（读配置、合并环境变量、校验）
- Modify: `internal/daemon/api_test.go`（补 tcp4 测试）

**Interfaces:**
- Consumes: `config.LoadBypass`（Task 1）、`daemon.AuthMiddleware` / `AuthConfig`（Task 2）、`daemon.BypassDeps`（Task 3）
- Produces: `daemon.Options.HTTPToken string`、`daemon.Options.Bypass *BypassDeps`

- [ ] **Step 1: 写失败的测试**

在 `internal/daemon/api_test.go` 末尾追加：

```go
// ServeHTTP 必须只监听 IPv4。路由器的 v6 地址是公网直接可达的（没有 NAT
// 兜底），一旦监听 v6，管理 API 就挂在公网上了。
func TestServeHTTPListensOnIPv4Only(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	mux := NewMux(APIDeps{Supervisor: newTestSupervisor(t), Version: "t", Rundir: "/tmp"})
	port := freePort(t)
	addr := fmt.Sprintf("127.0.0.1:%d", port)
	done := make(chan error, 1)
	go func() { done <- ServeHTTP(ctx, mux, addr) }()

	// 等 listener 就绪
	var resp *http.Response
	var err error
	for i := 0; i < 50; i++ {
		resp, err = http.Get("http://" + addr + "/api/v1/status")
		if err == nil {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if err != nil {
		t.Fatalf("v4 request failed: %v", err)
	}
	_ = resp.Body.Close()

	// 同端口的 v6 环回必须连不上——证明没有走双栈监听。
	c, derr := net.DialTimeout("tcp6", fmt.Sprintf("[::1]:%d", port), 300*time.Millisecond)
	if derr == nil {
		_ = c.Close()
		t.Fatal("v6 loopback is reachable; ServeHTTP must bind tcp4 only")
	}
	cancel()
	<-done
}

// 显式配 v6 监听地址应当报错而不是静默降级。
func TestServeHTTPRejectsIPv6ListenAddress(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	mux := NewMux(APIDeps{})
	err := ServeHTTP(ctx, mux, fmt.Sprintf("[::1]:%d", freePort(t)))
	if err == nil {
		t.Fatal("expected error for IPv6 listen address")
	}
	if !strings.Contains(err.Error(), "tcp4") {
		t.Fatalf("error should mention tcp4: %v", err)
	}
}
```

`api_test.go` 顶部 import 需要补 `"net"`（其余 `context` / `fmt` / `http` / `strings` / `time` 已在）。

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./internal/daemon/ -run 'ServeHTTP' -v`
Expected: FAIL —— `TestServeHTTPListensOnIPv4Only` 报 "v6 loopback is reachable"

- [ ] **Step 3: 改 ServeHTTP**

把 `internal/daemon/api.go` 末尾的 `ServeHTTP` 整体替换为：

```go
// ServeHTTP 是 daemon.go 用的薄包装；阻塞直到 ctx 取消。
//
// 【必须 tcp4】不能用 http.Server.Addr + ListenAndServe——那走 net.Listen("tcp")，
// 在双栈内核上会同时监听 v6。路由器的 v6 地址是公网直接可达的（v4 有 NAT 兜底，
// v6 没有），一旦监听 v6，/api/v1/shutdown 就挂在公网上了。
// 配了 v6 监听地址时直接报错，不静默降级——静默降级会让用户以为配置生效了。
func ServeHTTP(ctx context.Context, mux http.Handler, listen string) error {
	ln, err := net.Listen("tcp4", listen)
	if err != nil {
		return fmt.Errorf("listen tcp4 %q: %w", listen, err)
	}
	srv := &http.Server{
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}
	errCh := make(chan error, 1)
	go func() { errCh <- srv.Serve(ln) }()
	select {
	case <-ctx.Done():
		sctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = srv.Shutdown(sctx)
		return nil
	case err := <-errCh:
		if err == http.ErrServerClosed {
			return nil
		}
		return err
	}
}
```

`api.go` 的 import 补 `"fmt"` 与 `"net"`。

- [ ] **Step 4: 跑测试确认通过**

Run: `go test ./internal/daemon/ -run 'ServeHTTP' -v`
Expected: PASS

- [ ] **Step 5: daemon.Options 接线**

在 `internal/daemon/daemon.go` 的 `Options` 结构里，`ScriptByName` 之后加：

```go
	// HTTPToken 是 [http].token；非 loopback 来源的鉴权凭据。
	HTTPToken string
	// Bypass 非 nil 且 Enabled 时启用 LAN 客户端 bypass 注册端点。
	Bypass *BypassDeps
```

把 `Run` 里的 `mux := NewMux(deps)` 一行改为：

```go
	deps.Bypass = opts.Bypass
	mux := NewMux(deps)

	// 鉴权中间件包住【整个】mux：loopback 免 token 全权，LAN 只能走白名单。
	// 包在最外层而不是逐端点加检查——后者漏一个端点就是全暴露。
	bypassEnabled := opts.Bypass != nil && opts.Bypass.Enabled
	handler := AuthMiddleware(mux, AuthConfig{
		Token:         opts.HTTPToken,
		BypassEnabled: bypassEnabled,
	})
```

并把下面 `httpDone <- ServeHTTP(ctx, mux, opts.Listen)` 中的 `mux` 改为 `handler`。

- [ ] **Step 6: wireup 接线**

在 `internal/cli/wireup_daemon.go` 中，找到构造 `shell.NewRunner` 的位置（约 180 行），把 `Env: routing.EnvVars(cnPath),` 改为：

```go
		Env: mergedShellEnv(routing, bypassCfg, cnPath),
```

在同文件末尾加辅助函数：

```go
// mergedShellEnv 合并路由参数与 bypass 参数，一起注入 startup.sh / teardown.sh。
// 两个结构体各管各的键空间：Routing 用 DNS_PORT/PROXY_PORTS/BYPASS_MARK 等，
// Bypass 用 CLIENT_BYPASS_* 前缀，互不覆盖。
func mergedShellEnv(routing config.Routing, bypass config.Bypass, cnPath string) map[string]string {
	env := routing.EnvVars(cnPath)
	maps.Copy(env, bypass.EnvVars())
	return env
}
```

`wireup_daemon.go` 的 import 补 `"maps"`。

在同文件构造 `daemon.Options` 的位置（约 294 行 `Listen: cfg.HTTP.Listen,` 附近），先在函数中较早处解析并校验配置：

```go
	bypassCfg := config.LoadBypass(cfg)
	if err := bypassCfg.Validate(cfg.HTTP.Token); err != nil {
		return fmt.Errorf("bypass config: %w", err)
	}
	// 启用了却只监听 loopback：LAN 客户端根本连不上，但这不该阻止 daemon 启动。
	if bypassCfg.Enabled && listenIsLoopback(cfg.HTTP.Listen) {
		em.Warn("config", "bypass.listen_loopback",
			"[bypass].enabled = true but [http].listen is {Listen}; LAN clients cannot register",
			map[string]any{"Listen": cfg.HTTP.Listen})
	}
```

并在 `mergedShellEnv` 旁边加：

```go
// listenIsLoopback 判断监听地址是否只对本机可见。用于「bypass 启用却没放开
// 监听面」的 warn——不阻止启动，只提醒 LAN 客户端连不上。
// 不用 strings.HasPrefix(l, "127.")：那会漏掉 localhost:9998 与 [::1]:9998。
func listenIsLoopback(listen string) bool {
	host, _, err := net.SplitHostPort(listen)
	if err != nil {
		return false
	}
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
```

`wireup_daemon.go` 的 import 还需补 `"net"`。

在 `daemon.Options{...}` 里 `Listen: cfg.HTTP.Listen,` 之后加：

```go
		HTTPToken: cfg.HTTP.Token,
		Bypass: &daemon.BypassDeps{
			Enabled:       bypassCfg.Enabled,
			DefaultTTLSec: bypassCfg.DefaultTTLSec,
			MaxTTLSec:     bypassCfg.MaxTTLSec,
		},
```

（`IpsetRun` / `IpsetList` 留 nil，落到 `realIpsetRun` / `realIpsetList`。）

- [ ] **Step 7: 跑全量测试与 vet**

Run: `go test ./... && go vet ./...`
Expected: 全部 PASS

- [ ] **Step 8: 提交**

```bash
git add internal/daemon/api.go internal/daemon/api_test.go internal/daemon/daemon.go internal/cli/wireup_daemon.go
git commit -m "feat(daemon,cli): ServeHTTP 强制 tcp4，鉴权中间件包住整个 mux

v6 下路由器地址公网可达且无 NAT 兜底，绝不监听。
配了 v6 监听地址直接报错而非静默降级。
bypass 环境变量与路由环境变量合并注入 startup.sh。"
```

---

### Task 5: `startup.sh` / `teardown.sh` + 静态特征测试

**Files:**
- Modify: `assets/shell/startup.sh`
- Modify: `assets/shell/teardown.sh`
- Modify: `assets/embed_test.go`

**Interfaces:**
- Consumes: `CLIENT_BYPASS_ENABLED` / `_TTL` / `_STATIC_IPS` / `_STATIC_MACS`（Task 1 的 `EnvVars`）
- Produces: ipset `client_bypass` / `client_bypass_static` / `client_bypass_mac`；三条链首的 RETURN 规则

- [ ] **Step 1: 写失败的测试**

在 `assets/embed_test.go` 末尾追加：

```go
// startup.sh 必须在三条链首都装 bypass RETURN，且位置在 REDIRECT 之前——
// 装在 REDIRECT 之后等于完全不生效。
func TestStartupInstallsClientBypassReturnsBeforeRedirect(t *testing.T) {
	data, err := ReadFile("shell/startup.sh")
	if err != nil {
		t.Fatal(err)
	}
	s := string(data)
	for _, chain := range []string{"nat sing-box", "mangle sing-box-mark", "nat sing-box-dns"} {
		if !strings.Contains(s, "add_client_bypass_returns "+chain) {
			t.Errorf("startup.sh must call add_client_bypass_returns for %q", chain)
		}
	}
	if !strings.Contains(s, "--match-set client_bypass src -j RETURN") {
		t.Error("startup.sh must add a RETURN rule matching the client_bypass set")
	}
	// helper 调用必须早于该链的 REDIRECT，否则 RETURN 永远轮不到。
	helperAt := strings.Index(s, "add_client_bypass_returns nat sing-box\n")
	redirectAt := strings.Index(s, "-j REDIRECT --to-ports \"$REDIRECT_PORT\"")
	if helperAt < 0 || redirectAt < 0 || helperAt > redirectAt {
		t.Errorf("bypass RETURN must be installed before REDIRECT (helper@%d redirect@%d)",
			helperAt, redirectAt)
	}
}

// 动态 set 不能 destroy——租约是客户端持续声明的状态，被 Restart 冲掉会让
// 客户端最坏约 90s 被误代理。这条断言防止日后有人"顺手补全"。
func TestTeardownKeepsDynamicClientBypassSet(t *testing.T) {
	data, err := ReadFile("shell/teardown.sh")
	if err != nil {
		t.Fatal(err)
	}
	s := string(data)
	for _, set := range []string{"client_bypass_static", "client_bypass_mac"} {
		if !strings.Contains(s, "ipset destroy "+set) {
			t.Errorf("teardown.sh must destroy %s", set)
		}
	}
	for _, line := range strings.Split(s, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "ipset destroy client_bypass ") ||
			trimmed == "ipset destroy client_bypass" {
			t.Fatal("teardown.sh must NOT destroy the dynamic client_bypass set " +
				"(leases survive Restart by design; only uninstall clears it)")
		}
	}
}

// set -eu 下 `[ -n "$X" ] && cmd` 在条件为假时整体退出码为 1，会掀掉整个脚本。
func TestEmbeddedShellScriptsNoAndListGuards(t *testing.T) {
	for _, name := range []string{"shell/startup.sh", "shell/teardown.sh"} {
		data, err := ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		for i, line := range strings.Split(string(data), "\n") {
			trimmed := strings.TrimSpace(line)
			if strings.HasPrefix(trimmed, "#") {
				continue
			}
			if strings.HasPrefix(trimmed, "[ ") && strings.Contains(trimmed, " ] && ") {
				t.Errorf("%s:%d uses an AND-list guard under set -eu; use if/fi instead: %s",
					name, i+1, trimmed)
			}
		}
	}
}
```

`embed_test.go` 若尚未 import `"strings"` 则补上。

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./assets/ -run 'ClientBypass|AndList' -v`
Expected: FAIL —— 三个用例均报缺少对应内容

- [ ] **Step 3: 改 startup.sh**

在 `assets/shell/startup.sh` 中，`CN_IP_CIDR="${CN_IP_CIDR:-}"` 那行之后加：

```bash
CLIENT_BYPASS_ENABLED="${CLIENT_BYPASS_ENABLED:-}"
CLIENT_BYPASS_TTL="${CLIENT_BYPASS_TTL:-120}"
CLIENT_BYPASS_STATIC_IPS="${CLIENT_BYPASS_STATIC_IPS:-}"
CLIENT_BYPASS_STATIC_MACS="${CLIENT_BYPASS_STATIC_MACS:-}"
```

在 `# ===================== ipset：CN IP 集合 =====================` 那一段的**结尾**（`fi` 之后）插入：

```bash
# ============ ipset：LAN 客户端 bypass 白名单 ============
# 给"自己已经跑了透明代理的客户端"用：它们的流量不该被这台路由器二次代理。
# 注意与上面的 $BYPASS_MARK (fwmark 0x7890) 完全无关——那是按包打标记，
# 这里是按源地址整机放行，故一律用 client_bypass / CLIENT_BYPASS_ 前缀。
if [ -n "$CLIENT_BYPASS_ENABLED" ]; then
    # 动态租约 set：create 幂等且【不清空】。租约是客户端持续心跳声明的状态，
    # 不能被 Restart (Shutdown+Startup) 冲掉——ready check 最长 60s，加上客户端
    # 下一轮心跳 30s，清掉就意味着最坏约 90s 被误代理。过期交给内核 timeout。
    ipset create client_bypass hash:ip timeout "$CLIENT_BYPASS_TTL" 2>/dev/null || true

    # 静态 set：配置驱动，flush 后重填，保证配置里删掉的条目立刻失效。
    # 与动态 set 分家正是为了能安全地 flush。
    if [ -n "$CLIENT_BYPASS_STATIC_IPS" ]; then
        ipset create client_bypass_static hash:ip 2>/dev/null || true
        ipset flush client_bypass_static
        for ip in $CLIENT_BYPASS_STATIC_IPS; do
            ipset -exist add client_bypass_static "$ip"
        done
    fi
    if [ -n "$CLIENT_BYPASS_STATIC_MACS" ]; then
        ipset create client_bypass_mac hash:mac 2>/dev/null || true
        ipset flush client_bypass_mac
        for mac in $CLIENT_BYPASS_STATIC_MACS; do
            ipset -exist add client_bypass_mac "$mac"
        done
    fi
fi

# add_client_bypass_returns <table> <chain>
# 往指定链追加 bypass RETURN。必须在链创建后、其余 -A 之前调用，即装在链首：
# 省掉后续 match 开销，语义也最直白（"这个源与我们无关，立即返回"）。
# 一条 iptables 规则只能引用一个 set，所以三个 set 就是三条规则。
#
# 【busybox ash + set -eu】这里一律用 if/fi 而不是 `[ -n "$X" ] && cmd`——
# AND-list 在条件为假时整体退出码为 1，会直接掀掉整个 startup。
add_client_bypass_returns() {
    if [ -z "$CLIENT_BYPASS_ENABLED" ]; then
        return 0
    fi
    iptables -t "$1" -A "$2" -m set --match-set client_bypass src -j RETURN
    if [ -n "$CLIENT_BYPASS_STATIC_IPS" ]; then
        iptables -t "$1" -A "$2" -m set --match-set client_bypass_static src -j RETURN
    fi
    if [ -n "$CLIENT_BYPASS_STATIC_MACS" ]; then
        iptables -t "$1" -A "$2" -m set --match-set client_bypass_mac src -j RETURN
    fi
    return 0
}
```

然后在三处链创建之后紧跟调用。具体地：

`iptables -t nat -N sing-box 2>/dev/null || iptables -t nat -F sing-box` 之后加一行：

```bash
add_client_bypass_returns nat sing-box
```

`iptables -t mangle -N sing-box-mark 2>/dev/null || iptables -t mangle -F sing-box-mark` 之后加一行：

```bash
add_client_bypass_returns mangle sing-box-mark
```

`iptables -t nat -N sing-box-dns 2>/dev/null || iptables -t nat -F sing-box-dns` 之后加一行：

```bash
add_client_bypass_returns nat sing-box-dns
```

- [ ] **Step 4: 改 teardown.sh**

把 `assets/shell/teardown.sh` 末尾的 `ipset destroy cn 2>/dev/null || true` 替换为：

```bash
ipset destroy cn 2>/dev/null || true
# 静态 set 随规则一起走：它们是配置的投影，下次 startup 会按配置重建。
ipset destroy client_bypass_static 2>/dev/null || true
ipset destroy client_bypass_mac 2>/dev/null || true
# client_bypass 动态 set 【刻意保留】——见 startup.sh 中的说明。此刻已无 iptables
# 规则引用它（链在上面被 -F/-X 掉了），下次 startup 直接复用；daemon 长期停止时
# 条目会按各自 timeout 自行过期。只有 uninstall 才销毁它。
```

- [ ] **Step 5: 跑测试确认通过**

Run: `go test ./assets/ -v`
Expected: PASS（含新增 3 个用例与既有全部用例）

- [ ] **Step 6: busybox 语法检查**

Run: `busybox sh -n assets/shell/startup.sh && busybox sh -n assets/shell/teardown.sh`
Expected: 无输出（语法正确）

若本机没有 busybox：`brew install busybox`。

- [ ] **Step 7: 跑全量测试与 vet**

Run: `go test ./... && go vet ./...`
Expected: 全部 PASS

- [ ] **Step 8: 提交**

```bash
git add assets/shell/startup.sh assets/shell/teardown.sh assets/embed_test.go
git commit -m "feat(assets): startup/teardown 装配 client_bypass ipset 与链首 RETURN

三条链各插 RETURN，位置在 REDIRECT 之前。静态 set flush 重填、动态 set 保留。
embed_test 钉死三点：RETURN 在 REDIRECT 前、teardown 不销毁动态 set、
禁止 set -eu 下的 AND-list guard。"
```

---

### Task 6: `daemon.toml.tmpl` + install flag

**Files:**
- Modify: `assets/daemon.toml.tmpl`
- Modify: `internal/install/seed.go`（`TemplateVars` 加三字段）
- Modify: `internal/cli/install.go`（两个 flag + token 生成）
- Modify: `internal/install/seed_test.go`（补渲染测试）

**Interfaces:**
- Consumes: `install.TemplateVars`（现有）
- Produces: `TemplateVars.HTTPListen string`、`TemplateVars.HTTPToken string`、`TemplateVars.BypassEnabled bool`

- [ ] **Step 1: 写失败的测试**

在 `internal/install/seed_test.go` 末尾追加：

```go
// 不传任何 bypass 参数时，渲染结果必须与今天完全一致：只监听 loopback、
// bypass 关闭。这是"不装本功能行为不变"的保证。
func TestSeedRendersBypassDisabledByDefault(t *testing.T) {
	dir := t.TempDir()
	if err := SeedDefaults(dir, TemplateVars{Firmware: "koolshare"}); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(dir, "daemon.toml"))
	if err != nil {
		t.Fatal(err)
	}
	s := string(data)
	if !strings.Contains(s, `listen          = "127.0.0.1:9998"`) {
		t.Errorf("default listen must stay loopback:\n%s", s)
	}
	if !strings.Contains(s, "enabled         = false") {
		t.Errorf("bypass must default to disabled:\n%s", s)
	}
	if !strings.Contains(s, `token           = ""`) {
		t.Errorf("token must default to empty:\n%s", s)
	}
}

func TestSeedRendersBypassEnabled(t *testing.T) {
	dir := t.TempDir()
	vars := TemplateVars{
		Firmware:      "koolshare",
		HTTPListen:    "0.0.0.0:9998",
		HTTPToken:     "cafebabe",
		BypassEnabled: true,
	}
	if err := SeedDefaults(dir, vars); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(dir, "daemon.toml"))
	if err != nil {
		t.Fatal(err)
	}
	s := string(data)
	if !strings.Contains(s, `listen          = "0.0.0.0:9998"`) {
		t.Errorf("listen not rendered:\n%s", s)
	}
	if !strings.Contains(s, `token           = "cafebabe"`) {
		t.Errorf("token not rendered:\n%s", s)
	}
	if !strings.Contains(s, "enabled         = true") {
		t.Errorf("bypass enabled not rendered:\n%s", s)
	}
}
```

在 `assets/embed_test.go` 末尾追加：

```go
func TestDaemonTomlTemplateHasBypassSection(t *testing.T) {
	data, err := ReadFile("daemon.toml.tmpl")
	if err != nil {
		t.Fatal(err)
	}
	s := string(data)
	for _, want := range []string{
		"[bypass]",
		"{{ .BypassEnabled }}",
		"{{ .HTTPListen }}",
		"{{ .HTTPToken }}",
		"default_ttl_sec",
		"max_ttl_sec",
		"static_ips",
		"static_macs",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("daemon.toml.tmpl missing %q", want)
		}
	}
}
```

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./internal/install/ ./assets/ -run 'Bypass' -v`
Expected: FAIL —— `TemplateVars` 无 `HTTPListen` 字段；模板缺 `[bypass]`

- [ ] **Step 3: 改模板**

在 `assets/daemon.toml.tmpl` 中，把 `[http]` 一节替换为：

```toml
[http]
# listen 默认只监听 loopback。仅当启用 [bypass]（LAN 客户端自助注册）时才需要
# 放开到 0.0.0.0——install --enable-bypass 会自动渲染成 0.0.0.0:9998。
# 【只监听 IPv4】daemon 强制 net.Listen("tcp4")：路由器的 v6 地址是公网直接
# 可达的（v4 有 NAT 兜底、v6 没有），写 v6 监听地址会直接启动失败。
listen          = "{{ .HTTPListen }}"
# token 只对【非 loopback】来源生效：loopback 免 token 且可访问全部端点（CLI
# 走这条路，行为不变）；LAN 来源必须带 Authorization: Bearer <token>，且只能
# 访问 /api/v1/bypass，只能写不能读。
token           = "{{ .HTTPToken }}"
```

在 `[router]` 一节之后、`[install]` 之前插入：

```toml
[bypass]
# LAN 客户端动态 bypass 白名单：让"自己已经跑了透明代理的客户端"（例如开着
# TUN 的笔记本）把自身 IP 注册进来，其流量便不再被本路由器二次代理。
#
# 客户端每 30s 心跳续约一次，条目过期完全交给内核 ipset timeout——daemon 不
# 维护租约表、不落盘。停止心跳（客户端睡眠 / 离开 LAN / 自身代理挂掉）后条目
# 自动消失，路由器重新接管。
#
# 身份靠 [http].token 而非 IP/MAC：笔记本经 USB Dock 接入时 MAC 属于 Dock，
# 换一台机器插同一个 Dock 会拿到相同 MAC，按 MAC 或 DHCP 保留都区分不了。
#
# enabled = true 时必须同时设置非空 [http].token，否则 daemon 拒绝启动。
enabled         = {{ .BypassEnabled }}
# 客户端不指定 ttl_sec 时使用的租约秒数，同时也是 ipset 的 set 级默认 timeout。
default_ttl_sec = 120
# 客户端能申请的租约上限；超过直接 400。
max_ttl_sec     = 600
# 永久放行的地址 / MAC（不参与心跳）。留空则对应的 ipset 与 iptables 规则都不创建。
# static_ips  = ["192.168.50.7"]
# static_macs = ["00:E0:4C:67:01:46"]
static_ips      = []
static_macs     = []
```

- [ ] **Step 4: 扩展 TemplateVars**

在 `internal/install/seed.go` 的 `TemplateVars` 结构里加三个字段：

```go
	HTTPListen      string // [http].listen；空字符串 → 回填 "127.0.0.1:9998"
	HTTPToken       string // [http].token；空字符串 → 渲染为 token = ""
	BypassEnabled   bool   // [bypass].enabled
```

在 `renderDaemonToml` 函数开头加默认回填（保证不传时行为与今天一致）：

```go
	if vars.HTTPListen == "" {
		vars.HTTPListen = "127.0.0.1:9998"
	}
```

- [ ] **Step 5: 加 install flag**

在 `internal/cli/install.go` 的 flag 变量声明处加：

```go
		enableBypass bool
		httpToken    string
```

在 `cmd.Flags()` 注册区加：

```go
	cmd.Flags().BoolVar(&enableBypass, "enable-bypass", false,
		"Enable LAN client bypass registration (opens [http].listen to 0.0.0.0 and requires a token)")
	cmd.Flags().StringVar(&httpToken, "http-token", "",
		"Token for LAN API auth; auto-generated when --enable-bypass is set and this is empty")
```

在 `RunE` 里构造 `vars := install.TemplateVars{...}` 之前加：

```go
			// bypass 的身份完全来自 token，没有 token 就不该启用。用户没给就
			// 生成一个，并在结尾打印出来让他拷到客户端。
			if enableBypass && httpToken == "" {
				generated, err := generateHTTPToken()
				if err != nil {
					return fmt.Errorf("generate http token: %w", err)
				}
				httpToken = generated
			}
			httpListen := "127.0.0.1:9998"
			if enableBypass {
				httpListen = "0.0.0.0:9998"
			}
```

在 `vars := install.TemplateVars{` 内加三行：

```go
				HTTPListen:    httpListen,
				HTTPToken:     httpToken,
				BypassEnabled: enableBypass,
```

在 `RunE` 结尾的成功输出之前加：

```go
			if enableBypass {
				fmt.Fprintf(cmd.OutOrStdout(),
					"\nLAN client bypass enabled.\n  listen: %s\n  token:  %s\n"+
						"Copy this token into the client agent config (contrib/macos/bypass-agent.conf).\n",
					httpListen, httpToken)
			}
```

在文件末尾加：

```go
// generateHTTPToken 生成 32 hex 字符（16 字节）的随机 token。
func generateHTTPToken() (string, error) {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf), nil
}
```

`install.go` 的 import 补 `"crypto/rand"` 与 `"encoding/hex"`。

- [ ] **Step 6: 跑测试确认通过**

Run: `go test ./internal/install/ ./assets/ -v`
Expected: PASS

- [ ] **Step 7: 手工验证 install 渲染**

```bash
go build -o /tmp/sr ./cmd/sing-router
/tmp/sr install --rundir /tmp/sr-probe --skip-firmware-hooks --enable-bypass 2>&1 | tail -5
grep -A3 '^\[http\]' /tmp/sr-probe/daemon.toml
grep -A5 '^\[bypass\]' /tmp/sr-probe/daemon.toml
rm -rf /tmp/sr-probe /tmp/sr
```

Expected: `listen = "0.0.0.0:9998"`、`token` 为 32 位 hex、`enabled = true`

（若 `install` 在非路由器环境因固件探测失败，追加 `--firmware koolshare`。）

- [ ] **Step 8: 跑全量测试与 vet**

Run: `go test ./... && go vet ./...`
Expected: 全部 PASS

- [ ] **Step 9: 提交**

```bash
git add assets/daemon.toml.tmpl assets/embed_test.go internal/install/seed.go internal/install/seed_test.go internal/cli/install.go
git commit -m "feat(assets,install,cli): daemon.toml 的 [bypass] 段与 install flag

--enable-bypass 放开 listen 到 0.0.0.0 并自动生成 32 hex token 打印出来。
不传参数时渲染结果与今天完全一致（loopback + bypass 关闭）。"
```

---

### Task 7: uninstall 清理动态 set

**Files:**
- Modify: `internal/cli/uninstall.go`

**Interfaces:**
- Consumes: `daemon.ClientBypassSet`（Task 3）
- Produces: 无（终端行为）

- [ ] **Step 1: 加清理逻辑**

`uninstall` 不调用 `teardown.sh`——它 SIGTERM daemon，由 daemon 的 defer 触发 teardown。而 teardown 刻意保留 `client_bypass`，所以这里是它唯一的清理点。

在 `internal/cli/uninstall.go` 的 `stopDaemonByPidFile(...)` 调用之后、注释 `// 2. resolve firmware...` 之前插入：

```go
				// teardown.sh 刻意保留 client_bypass 动态 set（租约要活过 Restart），
				// 所以这里是它唯一的清理点。此刻 daemon 已退出、teardown 已跑完，
				// 没有 iptables 规则引用它，destroy 必定成功。
				// best-effort：非 Linux 平台、set 本就不存在、ipset 未安装都属正常。
				destroyClientBypassSet()
```

在文件末尾加：

```go
// destroyClientBypassSet 销毁动态 bypass ipset。所有失败都静默忽略——
// uninstall 不该因为一个可选功能的残留清理失败而中断。
func destroyClientBypassSet() {
	_ = exec.Command("ipset", "destroy", daemon.ClientBypassSet).Run()
}
```

`uninstall.go` 的 import 补 `"os/exec"` 与 `"github.com/moonfruit/sing-router/internal/daemon"`。

- [ ] **Step 2: 跑全量测试与 vet**

Run: `go test ./... && go vet ./...`
Expected: 全部 PASS

- [ ] **Step 3: 提交**

```bash
git add internal/cli/uninstall.go
git commit -m "feat(cli): uninstall 销毁 client_bypass 动态 ipset

teardown 刻意保留它以让租约活过 Restart，uninstall 是唯一的清理点。"
```

---

### Task 8: doctor 增加 bypass 一节

**Files:**
- Create: `internal/cli/doctor_bypass.go`
- Modify: `internal/cli/doctor_routing.go`（`checkRouting` 签名加 `config.Bypass`）
- Modify: `internal/cli/doctor.go`（`runRoutingChecks` 传入 `config.LoadBypass(cfg)`）
- Create: `internal/cli/doctor_bypass_test.go`

**Interfaces:**
- Consumes: `config.Bypass`（Task 1）、`doctorCheck{Name, Status, Detail}`（现有）
- Produces: `checkClientBypass(b config.Bypass) []doctorCheck`

- [ ] **Step 1: 写失败的测试**

创建 `internal/cli/doctor_bypass_test.go`：

```go
package cli

import (
	"strings"
	"testing"

	"github.com/moonfruit/sing-router/internal/config"
)

func TestCheckClientBypassDisabledYieldsInfo(t *testing.T) {
	checks := checkClientBypass(config.DefaultBypass())
	if len(checks) != 1 {
		t.Fatalf("expected a single info check, got %d", len(checks))
	}
	if checks[0].Status != "info" {
		t.Fatalf("status = %q, want info", checks[0].Status)
	}
	if !strings.Contains(checks[0].Detail, "disabled") {
		t.Fatalf("detail = %q", checks[0].Detail)
	}
}

func TestParseIpsetListEntries(t *testing.T) {
	out := "Name: client_bypass\nType: hash:ip\n" +
		"Header: family inet hashsize 1024 maxelem 65536 timeout 120\n" +
		"Number of entries: 2\nMembers:\n" +
		"192.168.50.80 timeout 118\n192.168.50.81 timeout 0\n"
	entries := parseIpsetListEntries(out)
	if len(entries) != 2 {
		t.Fatalf("entries = %v", entries)
	}
	if entries[0] != "192.168.50.80 timeout 118" {
		t.Fatalf("entry 0 = %q", entries[0])
	}
}

func TestParseIpsetListEntriesEmptySet(t *testing.T) {
	out := "Name: client_bypass\nNumber of entries: 0\nMembers:\n"
	if entries := parseIpsetListEntries(out); len(entries) != 0 {
		t.Fatalf("expected no entries, got %v", entries)
	}
}
```

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./internal/cli/ -run 'ClientBypass|IpsetList' -v`
Expected: FAIL，编译错误 `undefined: checkClientBypass`

- [ ] **Step 3: 实现 doctor_bypass.go**

创建 `internal/cli/doctor_bypass.go`：

```go
package cli

import (
	"fmt"
	"strings"

	"github.com/moonfruit/sing-router/internal/config"
)

// bypassSets 是三个 set 与各自"无配置时不该存在"的判断依据。
// 动态 set 只要功能启用就必须存在；两个静态 set 仅在配置非空时才创建。
type bypassSetSpec struct {
	name     string
	expected func(config.Bypass) bool
	role     string
}

var bypassSets = []bypassSetSpec{
	{"client_bypass", func(config.Bypass) bool { return true }, "dynamic leases"},
	{"client_bypass_static", func(b config.Bypass) bool { return len(b.StaticIPs) > 0 }, "static IPs"},
	{"client_bypass_mac", func(b config.Bypass) bool { return len(b.StaticMACs) > 0 }, "static MACs"},
}

// checkClientBypass 巡检 LAN 客户端 bypass 的 ipset 与链规则。只读。
func checkClientBypass(b config.Bypass) []doctorCheck {
	if !b.Enabled {
		return []doctorCheck{{
			Name:   "client bypass",
			Status: "info",
			Detail: "disabled ([bypass].enabled = false)",
		}}
	}
	var out []doctorCheck
	for _, spec := range bypassSets {
		out = append(out, checkBypassSet(spec, b))
	}
	out = append(out, checkBypassChainRules()...)
	return out
}

func checkBypassSet(spec bypassSetSpec, b config.Bypass) doctorCheck {
	name := "ipset " + spec.name
	out, _, err := runCmd("ipset", "list", spec.name)
	want := spec.expected(b)
	if err != nil {
		if !want {
			return doctorCheck{Name: name, Status: "pass",
				Detail: fmt.Sprintf("absent as expected (no %s configured)", spec.role)}
		}
		return doctorCheck{Name: name, Status: "fail",
			Detail: fmt.Sprintf("missing (%s); is startup.sh installed?", spec.role)}
	}
	entries := parseIpsetListEntries(out)
	if !want {
		return doctorCheck{Name: name, Status: "warn",
			Detail: fmt.Sprintf("exists but no %s configured; stale set from an earlier config", spec.role)}
	}
	detail := fmt.Sprintf("%d entrie(s) [%s]", len(entries), spec.role)
	if len(entries) > 0 {
		detail += ": " + strings.Join(entries, ", ")
	}
	return doctorCheck{Name: name, Status: "pass", Detail: detail}
}

// checkBypassChainRules 确认三条链各有 client_bypass 的 RETURN，且位置在
// 该链的终结规则（REDIRECT / MARK）之前——装在其后等于完全不生效。
//
// 不接受 config.Bypass：动态 set 的 RETURN 只要功能启用就必须存在，与静态
// 配置无关，调用方已在 checkClientBypass 里判过 Enabled。
func checkBypassChainRules() []doctorCheck {
	targets := []struct{ table, chain string }{
		{"nat", "sing-box"},
		{"mangle", "sing-box-mark"},
		{"nat", "sing-box-dns"},
	}
	var out []doctorCheck
	for _, tgt := range targets {
		name := fmt.Sprintf("%s/%s bypass RETURN", tgt.table, tgt.chain)
		listing, _, err := runCmd("iptables", "-t", tgt.table, "-S", tgt.chain)
		if err != nil {
			out = append(out, doctorCheck{Name: name, Status: "fail",
				Detail: "chain not found; sing-router rules not installed"})
			continue
		}
		returnAt, terminalAt := -1, -1
		for i, line := range strings.Split(listing, "\n") {
			if returnAt < 0 && strings.Contains(line, "--match-set client_bypass src") &&
				strings.Contains(line, "-j RETURN") {
				returnAt = i
			}
			if terminalAt < 0 && (strings.Contains(line, "-j REDIRECT") ||
				strings.Contains(line, "-j MARK")) {
				terminalAt = i
			}
		}
		switch {
		case returnAt < 0:
			out = append(out, doctorCheck{Name: name, Status: "fail",
				Detail: "no RETURN rule for the client_bypass set"})
		case terminalAt >= 0 && returnAt > terminalAt:
			out = append(out, doctorCheck{Name: name, Status: "fail",
				Detail: fmt.Sprintf("RETURN is at position %d, after the terminal rule at %d; "+
					"it will never be reached", returnAt, terminalAt)})
		default:
			out = append(out, doctorCheck{Name: name, Status: "pass",
				Detail: "RETURN installed ahead of the terminal rule"})
		}
	}
	return out
}

// parseIpsetListEntries 提取 `ipset list` 输出中 Members: 之后的非空行。
func parseIpsetListEntries(out string) []string {
	var entries []string
	inMembers := false
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if line == "Members:" {
			inMembers = true
			continue
		}
		if inMembers && line != "" {
			entries = append(entries, line)
		}
	}
	return entries
}
```

- [ ] **Step 4: 接进 checkRouting**

在 `internal/cli/doctor_routing.go` 中，把 `checkRouting` 的签名与末尾改为：

```go
// checkRouting 跑全套运行时网络规则检查；非 root 则跳过。
func checkRouting(r config.Routing, b config.Bypass) []doctorCheck {
```

并在 `out = append(out, checkUPnPJumps()...)` 之后加一行：

```go
	out = append(out, checkClientBypass(b)...)
```

在 `internal/cli/doctor.go` 中，把 `return checkRouting(config.LoadRouting(cfg))` 改为：

```go
	return checkRouting(config.LoadRouting(cfg), config.LoadBypass(cfg))
```

- [ ] **Step 5: 跑测试确认通过**

Run: `go test ./internal/cli/ -run 'ClientBypass|IpsetList' -v`
Expected: PASS

- [ ] **Step 6: 跑全量测试与 vet**

Run: `go test ./... && go vet ./...`
Expected: 全部 PASS

- [ ] **Step 7: 提交**

```bash
git add internal/cli/doctor_bypass.go internal/cli/doctor_bypass_test.go internal/cli/doctor_routing.go internal/cli/doctor.go
git commit -m "feat(cli): doctor 增加 client bypass 一节

巡检三个 ipset 的存在性与条目，并确认三条链的 RETURN 位置在终结规则之前。
这是本功能唯一的可观测手段。"
```

---

### Task 9: docker-test 端到端

**Files:**
- Modify: `tests/docker/docker-test.sh`

**Interfaces:**
- Consumes: 前 8 个任务的全部产出
- Produces: 无（测试）

- [ ] **Step 1: 加 Phase G**

在 `tests/docker/docker-test.sh` 的 `step "Phase F  uninstall..."` **之前**插入以下整段。`ex()` 是文件里已有的容器内执行辅助函数。

```bash
step "Phase G  client bypass (ipset TTL + LAN auth)"

# 用容器自己的 eth0 地址发请求 = 非 loopback 来源，从而真正走到 LAN 分支。
CT_IP=$(ex "ip -4 -o addr show eth0 | awk '{print \$4}' | cut -d/ -f1")
echo "container eth0 = $CT_IP"
BP_TOKEN="dockertesttoken0123456789abcdef"

# 重新 install 打开 bypass，并把 token 固定成已知值（install 会渲染 daemon.toml）。
ex "/opt/sbin/sing-router install -D /opt/home/sing-router --firmware koolshare \
      --enable-bypass --http-token $BP_TOKEN >/dev/null"
ex "grep -q 'enabled         = true' /opt/home/sing-router/daemon.toml" \
    || { echo "FAIL: bypass not enabled in daemon.toml"; exit 1; }
ex "grep -q 'listen          = \"0.0.0.0:9998\"' /opt/home/sing-router/daemon.toml" \
    || { echo "FAIL: listen not opened to 0.0.0.0"; exit 1; }

ex "/opt/etc/init.d/S99sing-router start >/dev/null 2>&1 || true"
sleep 5

# --- G1: 动态 set 与三条链的 RETURN 都已就位 ---
ex "ipset list client_bypass >/dev/null" \
    || { echo "FAIL: client_bypass set missing"; exit 1; }
# 用 ${var%%:*} / ${var##*:} 拆，不用 `set -- $spec`——后者会清掉脚本自身的
# 位置参数，而这个文件顶上是 set -euo pipefail。
for spec in "nat:sing-box" "mangle:sing-box-mark" "nat:sing-box-dns"; do
    tbl=${spec%%:*}; chn=${spec##*:}
    ex "iptables -t $tbl -S $chn | grep -q 'match-set client_bypass src.*RETURN'" \
        || { echo "FAIL: no bypass RETURN in $tbl/$chn"; exit 1; }
done
echo "G1 ok: set + 3 chain RETURNs installed"

# --- G2: 错 token 401、管理端点 403 ---
code=$(ex "curl -s -o /dev/null -w '%{http_code}' -X POST \
    -H 'Authorization: Bearer wrong' -d '{\"ips\":[\"$CT_IP\"]}' \
    http://$CT_IP:9998/api/v1/bypass")
[ "$code" = "401" ] || { echo "FAIL: bad token gave $code, want 401"; exit 1; }

code=$(ex "curl -s -o /dev/null -w '%{http_code}' -X POST \
    -H 'Authorization: Bearer $BP_TOKEN' http://$CT_IP:9998/api/v1/shutdown")
[ "$code" = "403" ] || { echo "FAIL: LAN shutdown gave $code, want 403"; exit 1; }

code=$(ex "curl -s -o /dev/null -w '%{http_code}' \
    -H 'Authorization: Bearer $BP_TOKEN' http://$CT_IP:9998/api/v1/bypass")
[ "$code" = "403" ] || { echo "FAIL: LAN GET gave $code, want 403"; exit 1; }
echo "G2 ok: auth matrix enforced"

# --- G3: 注册成功且条目带递减的 TTL ---
ex "curl -sf -X POST -H 'Authorization: Bearer $BP_TOKEN' \
    -d '{\"ips\":[\"192.168.99.7\"],\"ttl_sec\":300}' \
    http://$CT_IP:9998/api/v1/bypass >/dev/null" \
    || { echo "FAIL: register rejected"; exit 1; }
ex "ipset list client_bypass | grep -q '192.168.99.7 timeout'" \
    || { echo "FAIL: entry not in set"; exit 1; }
echo "G3 ok: entry registered with timeout"

# --- G4: Restart 后租约存活（本设计最关键的行为）---
ex "/opt/sbin/sing-router restart >/dev/null 2>&1 || true"
sleep 3
ex "ipset list client_bypass | grep -q '192.168.99.7 timeout'" \
    || { echo "FAIL: lease lost across restart -- teardown must NOT destroy client_bypass"; exit 1; }
ex "iptables -t nat -S sing-box | grep -q 'match-set client_bypass src.*RETURN'" \
    || { echo "FAIL: RETURN rule not reinstalled after restart"; exit 1; }
echo "G4 ok: lease survived restart"

# --- G5: 注销后条目消失 ---
ex "curl -sf -X DELETE -H 'Authorization: Bearer $BP_TOKEN' \
    -d '{\"ips\":[\"192.168.99.7\"]}' http://$CT_IP:9998/api/v1/bypass >/dev/null" \
    || { echo "FAIL: revoke rejected"; exit 1; }
# 这里期望的是 grep 失败（条目已消失）。写成 `cmd && { exit 1; }` 会在 cmd
# 按预期失败时让整个语句返回非 0，被文件顶部的 set -e 掀掉——必须用 if/fi。
if ex "ipset list client_bypass | grep -q '192.168.99.7'"; then
    echo "FAIL: entry still present after revoke"; exit 1
fi
echo "G5 ok: revoke removed the entry"

# --- G6: v6 地址被显式拒绝 ---
code=$(ex "curl -s -o /dev/null -w '%{http_code}' -X POST \
    -H 'Authorization: Bearer $BP_TOKEN' -d '{\"ips\":[\"2408:820c::1\"]}' \
    http://$CT_IP:9998/api/v1/bypass")
[ "$code" = "400" ] || { echo "FAIL: v6 register gave $code, want 400"; exit 1; }
echo "G6 ok: IPv6 explicitly rejected"

# --- G7: teardown 保留动态 set、销毁静态 set ---
ex "/opt/etc/init.d/S99sing-router stop >/dev/null 2>&1 || true"
sleep 2
ex "ipset list client_bypass >/dev/null" \
    || { echo "FAIL: dynamic set destroyed by teardown"; exit 1; }
echo "G7 ok: dynamic set survived teardown"
```

- [ ] **Step 2: 跑 docker-test**

Run: `make docker-test`
Expected: 全部 Phase 通过，Phase G 打印 `G1 ok` 至 `G7 ok`

若失败，用 `KEEP=1 make docker-test` 保留容器，`docker exec -it <name> /opt/bin/sh` 进去排障。

- [ ] **Step 3: 提交**

```bash
git add tests/docker/docker-test.sh
git commit -m "test(docker): client bypass 端到端 Phase G

覆盖鉴权矩阵、注册/注销、v6 拒绝，以及最关键的
「Restart 后租约存活」与「teardown 保留动态 set」。"
```

---

### Task 10: macOS 客户端 agent

**Files:**
- Create: `contrib/macos/bypass-agent.sh`
- Create: `contrib/macos/bypass-agent.conf.example`
- Create: `contrib/macos/moonfruit.sing-bypass.plist`
- Create: `contrib/macos/README.md`

**Interfaces:**
- Consumes: `POST` / `DELETE /api/v1/bypass`（Task 3 的契约）
- Produces: 无（客户端交付物，不嵌入二进制）

- [ ] **Step 1: 写 agent 脚本**

创建 `contrib/macos/bypass-agent.sh`（`chmod +x`）：

```bash
#!/usr/bin/env bash
# ============================================================
# sing-router LAN client bypass agent (macOS)
#
# 每次运行做一件事：如果本机 sing-box 健康且我们正连在目标 LAN 上，就把当前
# 出口 IP 续约到路由器的 bypass 白名单；否则撤销。由 launchd 按 StartInterval
# 反复拉起——脚本本身跑完即退，不常驻。
#
# 为什么是续约而不是"启动时注册一次"：本机 sing-box 是常驻的，从插 Dock 到
# 拔 Dock 全程不重启，以进程生命周期为触发点的话钩子一次都不会触发。真正在
# 变的是网络位置。
# ============================================================

set -eu

CONF="${BYPASS_AGENT_CONF:-/opt/etc/sing-box/bypass-agent.conf}"
if [ ! -r "$CONF" ]; then
    echo "bypass-agent: cannot read $CONF" >&2
    exit 1
fi
# shellcheck source=/dev/null
. "$CONF"

: "${ROUTER_URL:?ROUTER_URL not set}"
: "${TOKEN:?TOKEN not set}"
: "${GATEWAY:?GATEWAY not set}"
LOCAL_CLASH_API="${LOCAL_CLASH_API:-http://127.0.0.1:9999}"
TTL="${TTL:-120}"
STATE_FILE="${STATE_FILE:-/var/run/sing-bypass-agent.ip}"

# 只在状态变化时说话：每 30s 一次、一天 2880 次，稳态输出会把日志刷爆。
log() { echo "$(date '+%Y-%m-%d %H:%M:%S') $*"; }

revoke() {
    _ip="$1"
    # 失败静默：路由器不可达时这个请求本来也发不出去，TTL 会兜住。
    curl -sf --max-time 3 -X DELETE \
        -H "Authorization: Bearer $TOKEN" \
        -H "Content-Type: application/json" \
        -d "{\"ips\":[\"$_ip\"]}" \
        "$ROUTER_URL/api/v1/bypass" >/dev/null 2>&1 || true
    rm -f "$STATE_FILE"
}

# 本机代理不健康时必须撤销：此刻我们还在 LAN 内，注销请求发得出去，路由器
# 立刻接管，本机不至于既没自己代理、又被路由器放行而裸奔。
give_up() {
    _reason="$1"
    if [ -f "$STATE_FILE" ]; then
        _prev=$(cat "$STATE_FILE" 2>/dev/null || true)
        if [ -n "$_prev" ]; then
            log "revoking $_prev ($_reason)"
            revoke "$_prev"
        fi
    fi
    exit 0
}

# 1) 本机 sing-box 是否 ready。用 clash api /version，与 sing-router 自己的
#    ready check 采用同一个信号。
if ! curl -sf --max-time 2 "$LOCAL_CLASH_API/version" >/dev/null 2>&1; then
    give_up "local sing-box not ready"
fi

# 2) 找出通往网关的接口与源地址。
#    不用 default 路由——本机 TUN 装了 auto_route，default 会被它接管；网关是
#    同网段私有地址，走的是直连路由，拿到的必定是物理口。
IFACE=$(route -n get "$GATEWAY" 2>/dev/null | awk '/interface:/{print $2}')
if [ -z "$IFACE" ]; then
    give_up "no route to $GATEWAY (not on the target LAN)"
fi
IP=$(ifconfig "$IFACE" inet 2>/dev/null | awk '/inet /{print $2; exit}')
if [ -z "$IP" ]; then
    give_up "interface $IFACE has no IPv4 address"
fi

# 3) IP 变了先注销旧的，把切换窗口从整个 TTL 压到接近 0。
PREV=""
if [ -f "$STATE_FILE" ]; then
    PREV=$(cat "$STATE_FILE" 2>/dev/null || true)
fi
if [ -n "$PREV" ] && [ "$PREV" != "$IP" ]; then
    log "address changed $PREV -> $IP, revoking old lease"
    revoke "$PREV"
fi

# 4) 续约。-exist 语义在服务端：条目还在就刷新 TTL，已过期就重建。
if curl -sf --max-time 5 -X POST \
        -H "Authorization: Bearer $TOKEN" \
        -H "Content-Type: application/json" \
        -d "{\"ips\":[\"$IP\"],\"ttl_sec\":$TTL}" \
        "$ROUTER_URL/api/v1/bypass" >/dev/null 2>&1; then
    if [ "$PREV" != "$IP" ]; then
        log "registered $IP (iface $IFACE, ttl ${TTL}s)"
    fi
    mkdir -p "$(dirname "$STATE_FILE")"
    echo "$IP" > "$STATE_FILE"
else
    # 路由器不可达：【不】撤销。请求本来也发不出去，靠 TTL 自然过期即可。
    log "renew failed for $IP (router unreachable?); leaving lease to expire"
    exit 0
fi
```

- [ ] **Step 2: 写配置样例与 plist**

创建 `contrib/macos/bypass-agent.conf.example`：

```bash
# sing-router bypass agent 配置
# 部署到 /opt/etc/sing-box/bypass-agent.conf 并 chmod 600（含 token）。

# 路由器 API 基址。daemon 强制 tcp4 监听，这里必须用 IPv4 地址。
ROUTER_URL="http://192.168.50.1:9998"

# [http].token 的值。install --enable-bypass 会在结尾打印出来。
TOKEN="替换成 install 打印的 token"

# 用于反查出口接口的网关地址；同时也是"是否在目标 LAN 上"的判据。
GATEWAY="192.168.50.1"

# 本机 sing-box 的 clash api，用于探活。
LOCAL_CLASH_API="http://127.0.0.1:9999"

# 申请的租约秒数，须 <= 路由器的 [bypass].max_ttl_sec。
# 建议约为 launchd StartInterval 的 4 倍，留足重试余量。
TTL="120"

# 上次注册 IP 的记录，用于地址变化时立刻注销旧值。
STATE_FILE="/var/run/sing-bypass-agent.ip"
```

创建 `contrib/macos/moonfruit.sing-bypass.plist`：

```xml
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>Label</key>
	<string>moonfruit.sing-bypass</string>
	<key>ProgramArguments</key>
	<array>
		<string>/opt/etc/sing-box/bypass-agent.sh</string>
	</array>
	<key>EnvironmentVariables</key>
	<dict>
		<key>BYPASS_AGENT_CONF</key>
		<string>/opt/etc/sing-box/bypass-agent.conf</string>
	</dict>
	<key>RunAtLoad</key>
	<true/>
	<key>StartInterval</key>
	<integer>30</integer>
	<key>WatchPaths</key>
	<array>
		<string>/etc/resolv.conf</string>
		<string>/Library/Preferences/SystemConfiguration/</string>
	</array>
	<key>StandardOutPath</key>
	<string>/tmp/moonfruit.sing-bypass.log</string>
	<key>StandardErrorPath</key>
	<string>/tmp/moonfruit.sing-bypass.log</string>
</dict>
</plist>
```

创建 `contrib/macos/README.md`：

````markdown
# macOS 客户端 bypass agent

让本机（自己跑着 sing-box TUN）向路由器注册自身 IP，从而不被 sing-router 二次代理。

## 为什么必须是 LaunchDaemon

本机 sing-box 是 LaunchDaemon（root，`KeepAlive`）。若 agent 做成 LaunchAgent，
用户登出或未登录时它会停 → 租约过期 → 本机被路由器代理 → 与本机自己的代理
形成双重代理。两者生命周期必须对齐。

## 为什么 token 不写在 plist 里

`/Library/LaunchDaemons/*.plist` 是 644，任何本地用户可读。token 单独放
`bypass-agent.conf` 并 `chmod 600`，plist 只通过 `EnvironmentVariables`
传配置文件路径。

## 部署

```bash
# 1. 路由器侧启用（会打印 token）
ssh router '/opt/sbin/sing-router install -D /opt/home/sing-router --enable-bypass'

# 2. 本机安装脚本与配置
sudo mkdir -p /opt/etc/sing-box
sudo cp bypass-agent.sh /opt/etc/sing-box/
sudo chmod 755 /opt/etc/sing-box/bypass-agent.sh
sudo cp bypass-agent.conf.example /opt/etc/sing-box/bypass-agent.conf
sudo chmod 600 /opt/etc/sing-box/bypass-agent.conf
sudo vi /opt/etc/sing-box/bypass-agent.conf   # 填入上一步打印的 token

# 3. 装 launchd job
sudo cp moonfruit.sing-bypass.plist /Library/LaunchDaemons/
sudo chown root:wheel /Library/LaunchDaemons/moonfruit.sing-bypass.plist
sudo launchctl load -w /Library/LaunchDaemons/moonfruit.sing-bypass.plist
```

## 验证

```bash
# 本机日志（只在状态变化时输出，稳态静默是正常的）
tail -f /tmp/moonfruit.sing-bypass.log

# 路由器侧确认租约（GET 只允许 loopback，所以要在路由器上执行）
ssh router 'curl -s http://127.0.0.1:9998/api/v1/bypass'
ssh router 'ipset list client_bypass'
ssh router 'sing-router doctor'
```

## 排障

| 现象 | 原因 |
|---|---|
| 日志反复 `local sing-box not ready` | 本机 clash api 没起，检查 `LOCAL_CLASH_API` |
| 日志反复 `no route to <gw>` | 不在目标 LAN 上（正常，比如在外面） |
| `renew failed` | 路由器 daemon 没跑，或 `[http].listen` 仍是 loopback |
| 路由器上 401 | `TOKEN` 与 `[http].token` 不一致 |
| 路由器上 403 | `[bypass].enabled` 为 false |
````

- [ ] **Step 3: 语法检查**

Run: `bash -n contrib/macos/bypass-agent.sh && plutil -lint contrib/macos/moonfruit.sing-bypass.plist`
Expected: 无错误 + `OK`

- [ ] **Step 4: 干跑验证失败路径**

在未部署配置的情况下确认脚本优雅退出而不是崩：

```bash
BYPASS_AGENT_CONF=/nonexistent bash contrib/macos/bypass-agent.sh; echo "exit=$?"
```

Expected: 输出 `bypass-agent: cannot read /nonexistent`，`exit=1`

再造一个指向不存在路由器的配置，确认"路由器不可达时不撤销、不崩"：

```bash
cat > /tmp/ba.conf <<'EOF'
ROUTER_URL="http://127.0.0.1:1"
TOKEN="x"
GATEWAY="127.0.0.1"
LOCAL_CLASH_API="http://127.0.0.1:1"
STATE_FILE="/tmp/ba.ip"
EOF
BYPASS_AGENT_CONF=/tmp/ba.conf bash contrib/macos/bypass-agent.sh; echo "exit=$?"
rm -f /tmp/ba.conf /tmp/ba.ip
```

Expected: `exit=0`（探活失败走 `give_up`，静默退出）

- [ ] **Step 5: 提交**

```bash
git add contrib/macos/
git commit -m "feat(contrib): macOS 客户端 bypass 心跳 agent

LaunchDaemon + StartInterval 30s + WatchPaths；token 独立 600 配置文件。
本机代理不健康时主动撤销（不裸奔），路由器不可达时不撤销（靠 TTL 兜底）。"
```

---

## Self-Review

**1. Spec coverage**

| Spec 章节 | 覆盖任务 |
|---|---|
| 信任模型（loopback 免 token / LAN 白名单 / 不信任转发头） | Task 2 |
| 命名约定（`CLIENT_BYPASS_*` / `client_bypass*`） | Task 1、Task 5、全局约束 |
| 配置（`[bypass]` 段、`Validate`、install flag） | Task 1、Task 6 |
| API 契约（POST/DELETE/GET、校验顺序与错误码） | Task 3 |
| 鉴权中间件（白名单、GET 不给 LAN） | Task 2 |
| 平台验证导出的三条约束（`-exist` 重建 / 只看退出码 / teardown 顺序） | Task 3（前两条注释）、Task 5（第三条） |
| ipset 布局与生命周期（三个 set、动态 set 保留） | Task 5、Task 7 |
| 脚本改动（startup / teardown / uninstall） | Task 5、Task 7 |
| daemon 组件（`bypass.go`、`ServeHTTP` tcp4） | Task 3、Task 4 |
| 客户端 agent（LaunchDaemon、失败语义、conf 字段） | Task 10 |
| 可观测性（doctor 一节） | Task 8 |
| 测试策略（鉴权矩阵 / embed 静态特征 / docker Phase G） | Task 2、Task 5、Task 9 |

无遗漏。

**2. Placeholder scan**

已核查：无 TBD / TODO / "类似 Task N" / "适当处理错误"。每个代码步骤均给出完整可粘贴的代码。

**3. Type consistency**

- `config.Bypass` 字段名在 Task 1 定义、Task 4/8 使用，一致（`Enabled` / `DefaultTTLSec` / `MaxTTLSec` / `StaticIPs` / `StaticMACs`）。
- `daemon.BypassDeps` 在 Task 3 定义、Task 4 构造，字段一致。
- `AuthConfig{Token, BypassEnabled}` 在 Task 2 定义、Task 4 构造，一致。
- `ClientBypassSet` 常量在 Task 3 定义、Task 7 引用，一致。
- ipset 名在 Task 3（Go 常量）、Task 5（shell）、Task 8（doctor）、Task 9（docker-test）四处出现，均为 `client_bypass` / `client_bypass_static` / `client_bypass_mac`。
- 环境变量名在 Task 1（Go）与 Task 5（shell）两处出现，均为 `CLIENT_BYPASS_ENABLED` / `_TTL` / `_STATIC_IPS` / `_STATIC_MACS`。
- `checkRouting` 签名改动在 Task 8 内同时改了定义处与唯一调用处。
