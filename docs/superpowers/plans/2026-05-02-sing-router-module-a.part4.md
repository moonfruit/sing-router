# sing-router Module A 实施计划 — Part 4（终篇）

> 接续 Part 3。本部分覆盖 Phase 10（CLI 子命令）、Phase 11（install/uninstall/doctor 含 jffs_hooks 100%）、Phase 12（main 连线 + cross-compile）。

---

# Phase 10：CLI 子命令

> 所有 CLI 命令（除 `daemon` / `install` / `uninstall` / `doctor` / `version` / `script`）都是 HTTP 客户端 → daemon。共享 httpclient.go。

## Task 31：CLI httpclient

**Files:**
- Create: `internal/cli/httpclient.go`
- Create: `internal/cli/httpclient_test.go`

- [ ] **Step 1：写测试 internal/cli/httpclient_test.go**

```go
package cli

import (
    "encoding/json"
    "net/http"
    "net/http/httptest"
    "testing"
)

func TestHTTPClientGet(t *testing.T) {
    ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        if r.URL.Path != "/api/v1/status" {
            http.NotFound(w, r)
            return
        }
        w.Header().Set("Content-Type", "application/json")
        _, _ = w.Write([]byte(`{"daemon":{"state":"running"}}`))
    }))
    defer ts.Close()

    c := NewHTTPClient(ts.URL)
    var body map[string]any
    if err := c.GetJSON("/api/v1/status", &body); err != nil {
        t.Fatal(err)
    }
    daemon := body["daemon"].(map[string]any)
    if daemon["state"] != "running" {
        t.Fatal("decode failed")
    }
}

func TestHTTPClientPostError(t *testing.T) {
    ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        w.Header().Set("Content-Type", "application/json")
        w.WriteHeader(http.StatusConflict)
        _ = json.NewEncoder(w).Encode(map[string]any{
            "error": map[string]any{
                "code":    "daemon.state_conflict",
                "message": "not running",
            },
        })
    }))
    defer ts.Close()

    c := NewHTTPClient(ts.URL)
    err := c.PostJSON("/api/v1/restart", nil, nil)
    if err == nil {
        t.Fatal("expected error")
    }
    var apiErr *APIError
    if !errAs(err, &apiErr) {
        t.Fatalf("err type %T", err)
    }
    if apiErr.Code != "daemon.state_conflict" {
        t.Fatalf("code: %s", apiErr.Code)
    }
}

func TestHTTPClientNotRunning(t *testing.T) {
    c := NewHTTPClient("http://127.0.0.1:1") // 拒绝
    err := c.GetJSON("/api/v1/status", nil)
    if err == nil {
        t.Fatal("expected dial error")
    }
    if !IsDaemonNotRunning(err) {
        t.Fatalf("expected IsDaemonNotRunning true, got %v", err)
    }
}

// errAs 是 errors.As 的小代理，便于 module 局部测试不引 errors。
func errAs(err error, target any) bool {
    return errorsAs(err, target)
}
```

补一个最小 helper 文件 `internal/cli/errors_helper_test.go`：

```go
package cli

import "errors"

var errorsAs = errors.As
```

- [ ] **Step 2：跑测试看失败**

```bash
go test ./internal/cli/... -run TestHTTPClient
```

- [ ] **Step 3：写实现 internal/cli/httpclient.go**

```go
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
func (c *HTTPClient) PostJSON(path string, body any, out any) error {
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
    if errors.As(err, &sysErr) {
        if sysErr == syscall.ECONNREFUSED {
            return true
        }
    }
    s := err.Error()
    return contains(s, "connection refused") || contains(s, "no such host")
}

func contains(s, sub string) bool {
    return len(s) >= len(sub) && (s == sub || (len(s) > len(sub) &&
        (s[:len(sub)] == sub || s[len(s)-len(sub):] == sub || indexOf(s, sub) >= 0)))
}

func indexOf(s, sub string) int {
    for i := 0; i+len(sub) <= len(s); i++ {
        if s[i:i+len(sub)] == sub {
            return i
        }
    }
    return -1
}
```

- [ ] **Step 4：跑测试 + 提交**

```bash
go test ./internal/cli/... -v
git add internal/cli/httpclient.go internal/cli/httpclient_test.go internal/cli/errors_helper_test.go
git commit -m "feat(cli): http client with structured API error + daemon-not-running detection"
```

---

## Task 32：CLI status

**Files:**
- Create: `internal/cli/status.go`
- Modify: `internal/cli/root.go`（追加注册）

- [ ] **Step 1：写 status.go**

```go
package cli

import (
    "encoding/json"
    "fmt"
    "io"
    "os"

    "github.com/spf13/cobra"
)

func newStatusCmd() *cobra.Command {
    var asJSON bool
    cmd := &cobra.Command{
        Use:   "status",
        Short: "Show daemon + sing-box status",
        RunE: func(cmd *cobra.Command, args []string) error {
            base := getDaemonBase(cmd)
            client := NewHTTPClient(base)
            var body map[string]any
            err := client.GetJSON("/api/v1/status", &body)
            if err != nil {
                if IsDaemonNotRunning(err) {
                    return printOfflineStatus(cmd.OutOrStdout(), asJSON)
                }
                return err
            }
            return printStatus(cmd.OutOrStdout(), body, asJSON)
        },
    }
    cmd.Flags().BoolVar(&asJSON, "json", false, "emit JSON instead of pretty text")
    return cmd
}

// getDaemonBase 解析 --daemon-url 全局 flag；默认 http://127.0.0.1:9998。
func getDaemonBase(cmd *cobra.Command) string {
    base, _ := cmd.Flags().GetString("daemon-url")
    if base == "" {
        base, _ = cmd.Root().PersistentFlags().GetString("daemon-url")
    }
    if base == "" {
        return "http://127.0.0.1:9998"
    }
    return base
}

func printStatus(w io.Writer, body map[string]any, asJSON bool) error {
    if asJSON {
        return json.NewEncoder(w).Encode(body)
    }
    daemon, _ := body["daemon"].(map[string]any)
    sb, _ := body["sing_box"].(map[string]any)
    rules, _ := body["rules"].(map[string]any)
    fmt.Fprintf(w, "daemon:   state=%v  pid=%v  rundir=%v\n", daemon["state"], daemon["pid"], daemon["rundir"])
    fmt.Fprintf(w, "sing-box: pid=%v  restart_count=%v\n", sb["pid"], sb["restart_count"])
    fmt.Fprintf(w, "rules:    iptables_installed=%v\n", rules["iptables_installed"])
    return nil
}

func printOfflineStatus(w io.Writer, asJSON bool) error {
    snap := map[string]any{
        "daemon": map[string]any{
            "state":   "offline",
            "pid":     nil,
            "running": false,
        },
        "hint": "use `S99sing-router start` (Entware init.d) to launch the daemon",
    }
    if asJSON {
        return json.NewEncoder(w).Encode(snap)
    }
    fmt.Fprintln(w, "daemon: not running (use `S99sing-router start` to launch)")
    if _, err := os.Stat("/opt/etc/init.d/S99sing-router"); err != nil {
        fmt.Fprintln(w, "init.d script missing; run `sing-router install` first")
    }
    return nil
}
```

- [ ] **Step 2：在 internal/cli/root.go 中注册 status 与持久 flag**

修改 `NewRootCmd`，在 `cmd.AddCommand(newVersionCmd())` 之前添加：

```go
    cmd.PersistentFlags().String("daemon-url", "http://127.0.0.1:9998", "Daemon HTTP base URL")
```

并把：

```go
    cmd.AddCommand(newVersionCmd())
```

替换为：

```go
    cmd.AddCommand(newVersionCmd(), newStatusCmd())
```

- [ ] **Step 3：sanity 跑构建**

```bash
go build -o /tmp/sr-build ./cmd/sing-router
/tmp/sr-build status
rm /tmp/sr-build
```

期望：在没有 daemon 的环境下输出 `daemon: not running ...`。

- [ ] **Step 4：提交**

```bash
git add internal/cli/status.go internal/cli/root.go
git commit -m "feat(cli): status subcommand with offline degradation"
```

---

## Task 33：CLI start / stop / restart / check / reapply-rules / shutdown

**Files:**
- Create: `internal/cli/start_stop.go`
- Modify: `internal/cli/root.go`

- [ ] **Step 1：写 start_stop.go**

```go
package cli

import (
    "fmt"

    "github.com/spf13/cobra"
)

type postOnlyCmd struct {
    use   string
    short string
    path  string
}

func (p postOnlyCmd) build() *cobra.Command {
    return &cobra.Command{
        Use:   p.use,
        Short: p.short,
        RunE: func(cmd *cobra.Command, args []string) error {
            client := NewHTTPClient(getDaemonBase(cmd))
            if err := client.PostJSON(p.path, nil, nil); err != nil {
                if IsDaemonNotRunning(err) {
                    return fmt.Errorf("daemon not running; use `S99sing-router start` first")
                }
                return err
            }
            fmt.Fprintln(cmd.OutOrStdout(), "ok")
            return nil
        },
    }
}

func newStartCmd() *cobra.Command {
    return postOnlyCmd{use: "start", short: "Start sing-box (from stopped state)", path: "/api/v1/start"}.build()
}

func newStopCmd() *cobra.Command {
    return postOnlyCmd{use: "stop", short: "Stop sing-box + uninstall iptables; daemon stays", path: "/api/v1/stop"}.build()
}

func newRestartCmd() *cobra.Command {
    return postOnlyCmd{use: "restart", short: "Restart sing-box (keep iptables)", path: "/api/v1/restart"}.build()
}

func newCheckCmd() *cobra.Command {
    return postOnlyCmd{use: "check", short: "Validate config.d/* via sing-box check", path: "/api/v1/check"}.build()
}

func newReapplyRulesCmd() *cobra.Command {
    return postOnlyCmd{use: "reapply-rules", short: "Reinstall iptables/ipset (nat-start hook)", path: "/api/v1/reapply-rules"}.build()
}

func newShutdownCmd() *cobra.Command {
    return postOnlyCmd{use: "shutdown", short: "Shut down the daemon (equivalent to init.d stop)", path: "/api/v1/shutdown"}.build()
}
```

- [ ] **Step 2：在 root.go 注册**

把 `cmd.AddCommand(newVersionCmd(), newStatusCmd())` 替换为：

```go
    cmd.AddCommand(
        newVersionCmd(),
        newStatusCmd(),
        newStartCmd(),
        newStopCmd(),
        newRestartCmd(),
        newCheckCmd(),
        newReapplyRulesCmd(),
        newShutdownCmd(),
    )
```

- [ ] **Step 3：构建 + sanity**

```bash
go build -o /tmp/sr-build ./cmd/sing-router
/tmp/sr-build --help
rm /tmp/sr-build
```

期望：help 列出全部 8 条子命令。

- [ ] **Step 4：提交**

```bash
git add internal/cli/start_stop.go internal/cli/root.go
git commit -m "feat(cli): start/stop/restart/check/reapply-rules/shutdown subcommands"
```

---

## Task 34：CLI logs（历史 + follow + pretty）

**Files:**
- Create: `internal/cli/logs.go`
- Modify: `internal/cli/root.go`

- [ ] **Step 1：写 logs.go**

```go
package cli

import (
    "bufio"
    "encoding/json"
    "fmt"
    "io"
    "net/url"
    "strconv"
    "strings"
    "time"

    "github.com/spf13/cobra"

    log "github.com/moonfruit/sing-router/internal/log"
)

func newLogsCmd() *cobra.Command {
    var (
        source  string
        n       int
        follow  bool
        level   string
        eventID string
        asJSON  bool
    )
    cmd := &cobra.Command{
        Use:   "logs",
        Short: "Show daemon + sing-box logs",
        RunE: func(cmd *cobra.Command, args []string) error {
            client := NewHTTPClient(getDaemonBase(cmd))
            q := url.Values{}
            if source != "" {
                q.Set("source", source)
            }
            if n > 0 {
                q.Set("n", strconv.Itoa(n))
            }
            if level != "" {
                q.Set("level", level)
            }
            if eventID != "" {
                q.Set("event_id", eventID)
            }
            if follow {
                q.Set("follow", "true")
            }
            resp, err := client.GetStream("/api/v1/logs", q)
            if err != nil {
                if IsDaemonNotRunning(err) {
                    return fmt.Errorf("daemon not running")
                }
                return err
            }
            defer func() { _ = resp.Body.Close() }()
            return streamLogs(cmd.OutOrStdout(), resp.Body, asJSON, time.Local)
        },
    }
    cmd.Flags().StringVar(&source, "source", "", "all|daemon|sing-box")
    cmd.Flags().IntVarP(&n, "n", "n", 100, "tail N lines (history)")
    cmd.Flags().BoolVarP(&follow, "follow", "f", false, "follow new events via SSE")
    cmd.Flags().StringVar(&level, "level", "", "min level (trace|debug|info|warn|error|fatal)")
    cmd.Flags().StringVar(&eventID, "event-id", "", "filter by EventID prefix")
    cmd.Flags().BoolVar(&asJSON, "json", false, "emit raw CLEF JSON lines")
    return cmd
}

// streamLogs 把 resp.Body 当 NDJSON 处理，每行用 pretty 渲染。
// SSE 流由 daemon 用 `data: {...}\n\n` 编码；这里接受两种：纯 NDJSON，或 SSE。
func streamLogs(out io.Writer, r io.Reader, asJSON bool, tz *time.Location) error {
    sc := bufio.NewScanner(r)
    sc.Buffer(make([]byte, 64*1024), 4*1024*1024)
    for sc.Scan() {
        line := strings.TrimSpace(sc.Text())
        if line == "" {
            continue
        }
        // SSE: 取 "data:" 后面的部分
        if strings.HasPrefix(line, "data:") {
            line = strings.TrimSpace(line[5:])
            if line == "" {
                continue
            }
        }
        if asJSON {
            fmt.Fprintln(out, line)
            continue
        }
        ev, err := decodeOrderedEvent(line)
        if err != nil {
            fmt.Fprintln(out, line)
            continue
        }
        fmt.Fprintln(out, log.Pretty(ev, log.PrettyOptions{LocalTZ: tz, DisableColor: false}))
    }
    return sc.Err()
}

// decodeOrderedEvent 用 json.Decoder 保留键的相对顺序（Go 标准库不保证 map 顺序，
// 因此用 RawMessage 的两遍解析 + 顺序记录恢复）。
func decodeOrderedEvent(line string) (*log.OrderedEvent, error) {
    var raw map[string]json.RawMessage
    if err := json.Unmarshal([]byte(line), &raw); err != nil {
        return nil, err
    }
    keys, err := jsonKeys(line)
    if err != nil {
        return nil, err
    }
    ev := log.NewEvent()
    for _, k := range keys {
        if rv, ok := raw[k]; ok {
            var v any
            _ = json.Unmarshal(rv, &v)
            ev.Set(k, v)
        }
    }
    return ev, nil
}

// jsonKeys 顺序提取 JSON 文本的顶层 key。
func jsonKeys(s string) ([]string, error) {
    dec := json.NewDecoder(strings.NewReader(s))
    if _, err := dec.Token(); err != nil {
        return nil, err
    }
    var keys []string
    for dec.More() {
        tok, err := dec.Token()
        if err != nil {
            return nil, err
        }
        keys = append(keys, tok.(string))
        var skip json.RawMessage
        if err := dec.Decode(&skip); err != nil {
            return nil, err
        }
    }
    return keys, nil
}
```

- [ ] **Step 2：在 root.go 注册 newLogsCmd()**

```go
    cmd.AddCommand(
        // ... 已有
        newLogsCmd(),
    )
```

- [ ] **Step 3：编译 sanity + 提交**

```bash
go build ./...
git add internal/cli/logs.go internal/cli/root.go
git commit -m "feat(cli): logs subcommand with pretty/SSE/follow support"
```

---

## Task 35：CLI script

**Files:**
- Create: `internal/cli/script.go`
- Modify: `internal/cli/root.go`

- [ ] **Step 1：写 script.go**

```go
package cli

import (
    "fmt"
    "os"

    "github.com/spf13/cobra"

    "github.com/moonfruit/sing-router/assets"
)

var scriptMap = map[string]string{
    "startup":        "shell/startup.sh",
    "teardown":       "shell/teardown.sh",
    "init.d":         "initd/S99sing-router",
    "nat-start":      "jffs/nat-start.snippet",
    "services-start": "jffs/services-start.snippet",
}

func newScriptCmd() *cobra.Command {
    var remote bool
    cmd := &cobra.Command{
        Use:   "script <name>",
        Short: "Print embedded script (startup|teardown|init.d|nat-start|services-start)",
        Args:  cobra.ExactArgs(1),
        RunE: func(cmd *cobra.Command, args []string) error {
            name := args[0]
            if remote {
                client := NewHTTPClient(getDaemonBase(cmd))
                resp, err := client.GetStream("/api/v1/script/"+name, nil)
                if err != nil {
                    return err
                }
                defer func() { _ = resp.Body.Close() }()
                if resp.StatusCode >= 400 {
                    return fmt.Errorf("daemon returned %d", resp.StatusCode)
                }
                _, err = copyAll(cmd.OutOrStdout(), resp.Body)
                return err
            }
            path, ok := scriptMap[name]
            if !ok {
                return fmt.Errorf("unknown script %q (one of: startup, teardown, init.d, nat-start, services-start)", name)
            }
            data, err := assets.ReadFile(path)
            if err != nil {
                return err
            }
            _, err = os.Stdout.Write(data)
            return err
        },
    }
    cmd.Flags().BoolVar(&remote, "remote", false, "fetch from daemon (HTTP) instead of embedded copy")
    return cmd
}

func copyAll(dst interface{ Write(p []byte) (int, error) }, src interface{ Read(p []byte) (int, error) }) (int64, error) {
    var total int64
    buf := make([]byte, 4096)
    for {
        n, err := src.Read(buf)
        if n > 0 {
            if _, werr := dst.Write(buf[:n]); werr != nil {
                return total, werr
            }
            total += int64(n)
        }
        if err != nil {
            if err.Error() == "EOF" {
                return total, nil
            }
            return total, err
        }
    }
}
```

- [ ] **Step 2：注册 newScriptCmd() + 提交**

```bash
# 在 root.go AddCommand 中加 newScriptCmd()
go build ./...
git add internal/cli/script.go internal/cli/root.go
git commit -m "feat(cli): script subcommand exposes embedded resources"
```

---

## Task 36：CLI daemon（init.d 入口）

**Files:**
- Create: `internal/cli/daemon.go`
- Modify: `internal/cli/root.go`

> 这一步暂不通线 supervisor + daemon.Run（Phase 12 一并搞），但要把 `daemon` 子命令的 flag 与 cobra 结构提前做好。

- [ ] **Step 1：写 daemon.go**

```go
package cli

import (
    "github.com/spf13/cobra"
)

func newDaemonCmd() *cobra.Command {
    var rundir string
    cmd := &cobra.Command{
        Use:   "daemon",
        Short: "Run as long-running supervisor (called by init.d)",
        RunE: func(cmd *cobra.Command, args []string) error {
            return runDaemon(cmd.Context(), rundir)
        },
    }
    cmd.Flags().StringVarP(&rundir, "rundir", "D", "/opt/home/sing-router", "Runtime root directory")
    return cmd
}

// runDaemon 在 Phase 12 由 internal/cli/wireup_daemon.go 真正实现。
// 这里给一个占位，避免把循环 import 引进来。
var runDaemon = func(_ ctxLike, _ string) error {
    return errNotWired
}

type ctxLike = interface{ Done() <-chan struct{} }

var errNotWired = wiringError{}

type wiringError struct{}

func (wiringError) Error() string { return "daemon entry point not wired (run sing-router built from main)" }
```

- [ ] **Step 2：注册 newDaemonCmd() + 提交**

```bash
# 在 root.go 注册
go build ./...
git add internal/cli/daemon.go internal/cli/root.go
git commit -m "feat(cli): daemon subcommand stub (real wiring in Phase 12)"
```

---

# Phase 11：install / uninstall / doctor

## Task 37：install/layout — 创建 $RUNDIR 子目录

**Files:**
- Create: `internal/install/layout.go`
- Create: `internal/install/layout_test.go`

- [ ] **Step 1：写测试 layout_test.go**

```go
package install

import (
    "os"
    "path/filepath"
    "testing"
)

func TestEnsureLayoutCreatesAllDirs(t *testing.T) {
    dir := t.TempDir()
    rundir := filepath.Join(dir, "sing-router")
    if err := EnsureLayout(rundir); err != nil {
        t.Fatal(err)
    }
    for _, sub := range []string{"config.d", "bin", "var", "run", "log"} {
        if info, err := os.Stat(filepath.Join(rundir, sub)); err != nil || !info.IsDir() {
            t.Errorf("missing dir %s: %v", sub, err)
        }
    }
    // ui 不创建
    if _, err := os.Stat(filepath.Join(rundir, "ui")); !os.IsNotExist(err) {
        t.Errorf("ui dir should NOT be created by install: %v", err)
    }
}

func TestEnsureLayoutIdempotent(t *testing.T) {
    dir := t.TempDir()
    rundir := filepath.Join(dir, "sing-router")
    for i := 0; i < 3; i++ {
        if err := EnsureLayout(rundir); err != nil {
            t.Fatalf("attempt %d: %v", i, err)
        }
    }
}
```

- [ ] **Step 2：写实现 layout.go**

```go
// Package install 实施 install/uninstall/doctor 的具体动作。
package install

import (
    "os"
    "path/filepath"
)

// EnsureLayout 创建 $RUNDIR 及其全部固定子目录。
// 注意：不创建 ui/，留给 sing-box clash_api 首次下载时自行创建。
func EnsureLayout(rundir string) error {
    for _, sub := range []string{"config.d", "bin", "var", "run", "log"} {
        if err := os.MkdirAll(filepath.Join(rundir, sub), 0o755); err != nil {
            return err
        }
    }
    return nil
}
```

- [ ] **Step 3：跑测试 + 提交**

```bash
go test ./internal/install/... -v
git add internal/install/layout.go internal/install/layout_test.go
git commit -m "feat(install): EnsureLayout creates RUNDIR subdirs (no ui/)"
```

---

## Task 38：install/seed — 落盘默认 daemon.toml + config.d/*

**Files:**
- Create: `internal/install/seed.go`
- Create: `internal/install/seed_test.go`

- [ ] **Step 1：写测试 seed_test.go**

```go
package install

import (
    "os"
    "path/filepath"
    "strings"
    "testing"
)

func TestSeedWritesDefaultsWhenMissing(t *testing.T) {
    dir := t.TempDir()
    if err := EnsureLayout(dir); err != nil {
        t.Fatal(err)
    }
    if err := SeedDefaults(dir); err != nil {
        t.Fatal(err)
    }
    for _, p := range []string{
        "daemon.toml",
        "config.d/clash.json",
        "config.d/dns.json",
        "config.d/inbounds.json",
        "config.d/log.json",
        "config.d/cache.json",
        "config.d/certificate.json",
        "config.d/http.json",
        "config.d/outbounds.json",
    } {
        if _, err := os.Stat(filepath.Join(dir, p)); err != nil {
            t.Errorf("missing seed file %s: %v", p, err)
        }
    }
}

func TestSeedPreservesExisting(t *testing.T) {
    dir := t.TempDir()
    if err := EnsureLayout(dir); err != nil {
        t.Fatal(err)
    }
    daemonToml := filepath.Join(dir, "daemon.toml")
    if err := os.WriteFile(daemonToml, []byte("# user edit\n"), 0o644); err != nil {
        t.Fatal(err)
    }
    if err := SeedDefaults(dir); err != nil {
        t.Fatal(err)
    }
    data, _ := os.ReadFile(daemonToml)
    if !strings.Contains(string(data), "user edit") {
        t.Fatalf("seed should not overwrite existing daemon.toml; got: %s", data)
    }
}
```

- [ ] **Step 2：写实现 seed.go**

```go
package install

import (
    "errors"
    "os"
    "path/filepath"

    "github.com/moonfruit/sing-router/assets"
)

// SeedDefaults 把内嵌的 daemon.toml 与 config.d/*.json 写入 rundir，
// 仅当目标不存在时才写。已存在的文件保持不动（保留用户编辑）。
func SeedDefaults(rundir string) error {
    seedFiles := map[string]string{
        "config.d.default/clash.json":       "config.d/clash.json",
        "config.d.default/dns.json":         "config.d/dns.json",
        "config.d.default/inbounds.json":    "config.d/inbounds.json",
        "config.d.default/log.json":         "config.d/log.json",
        "config.d.default/cache.json":       "config.d/cache.json",
        "config.d.default/certificate.json": "config.d/certificate.json",
        "config.d.default/http.json":        "config.d/http.json",
        "config.d.default/outbounds.json":   "config.d/outbounds.json",
        "daemon.toml.default":               "daemon.toml",
    }
    for src, dst := range seedFiles {
        full := filepath.Join(rundir, dst)
        if _, err := os.Stat(full); err == nil {
            continue
        } else if !errors.Is(err, os.ErrNotExist) {
            return err
        }
        data, err := assets.ReadFile(src)
        if err != nil {
            return err
        }
        if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
            return err
        }
        if err := os.WriteFile(full, data, 0o644); err != nil {
            return err
        }
    }
    return nil
}
```

- [ ] **Step 3：跑测试 + 提交**

```bash
go test ./internal/install/... -run TestSeed -v
git add internal/install/seed.go internal/install/seed_test.go
git commit -m "feat(install): SeedDefaults writes daemon.toml + config.d defaults idempotently"
```

---

## Task 39：install/initd — 写 /opt/etc/init.d/S99sing-router

**Files:**
- Create: `internal/install/initd.go`
- Create: `internal/install/initd_test.go`

- [ ] **Step 1：写测试 initd_test.go**

```go
package install

import (
    "os"
    "path/filepath"
    "strings"
    "testing"
)

func TestWriteInitdSetsExecutable(t *testing.T) {
    dir := t.TempDir()
    target := filepath.Join(dir, "S99sing-router")
    if err := WriteInitd(target, "/opt/home/sing-router"); err != nil {
        t.Fatal(err)
    }
    info, err := os.Stat(target)
    if err != nil {
        t.Fatal(err)
    }
    if info.Mode().Perm()&0o100 == 0 {
        t.Fatalf("not executable: %v", info.Mode())
    }
    data, _ := os.ReadFile(target)
    if !strings.Contains(string(data), `daemon -D /opt/home/sing-router`) {
        t.Fatalf("ARGS not substituted: %s", data)
    }
}

func TestWriteInitdOverwrites(t *testing.T) {
    dir := t.TempDir()
    target := filepath.Join(dir, "S99sing-router")
    if err := os.WriteFile(target, []byte("garbage"), 0o644); err != nil {
        t.Fatal(err)
    }
    if err := WriteInitd(target, "/opt/x"); err != nil {
        t.Fatal(err)
    }
    data, _ := os.ReadFile(target)
    if strings.Contains(string(data), "garbage") {
        t.Fatal("init.d should be overwritten")
    }
}
```

- [ ] **Step 2：写实现 initd.go**

```go
package install

import (
    "os"
    "path/filepath"
    "strings"

    "github.com/moonfruit/sing-router/assets"
)

// WriteInitd 把内嵌 init.d 模板写到 path，并把模板里的 rundir 占位换成
// 实际值，最后 chmod 0755。
func WriteInitd(path, rundir string) error {
    raw, err := assets.ReadFile("initd/S99sing-router")
    if err != nil {
        return err
    }
    rendered := strings.ReplaceAll(string(raw), "/opt/home/sing-router", rundir)
    if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
        return err
    }
    if err := os.WriteFile(path, []byte(rendered), 0o755); err != nil {
        return err
    }
    return os.Chmod(path, 0o755)
}
```

- [ ] **Step 3：跑测试 + 提交**

```bash
go test ./internal/install/... -run TestWriteInitd -v
git add internal/install/initd.go internal/install/initd_test.go
git commit -m "feat(install): write init.d script with rundir substitution"
```

---

## Task 40：install/jffs_hooks — BEGIN/END 幂等块（100% 覆盖）

**Files:**
- Create: `internal/install/jffs_hooks.go`
- Create: `internal/install/jffs_hooks_test.go`

> 这是 Module A 第二个必须 100% 覆盖的单元（zoo.go 是第一个）。先把所有 case 一次性铺好。

- [ ] **Step 1：写测试 jffs_hooks_test.go（先全 case）**

```go
package install

import (
    "os"
    "path/filepath"
    "strings"
    "testing"
)

func read(t *testing.T, p string) string {
    t.Helper()
    b, err := os.ReadFile(p)
    if err != nil {
        t.Fatal(err)
    }
    return string(b)
}

func TestInjectCreatesFileIfMissing(t *testing.T) {
    dir := t.TempDir()
    target := filepath.Join(dir, "nat-start")
    if err := InjectHook(target, "sing-router-test", "echo hi"); err != nil {
        t.Fatal(err)
    }
    out := read(t, target)
    if !strings.Contains(out, "# BEGIN sing-router-test") || !strings.Contains(out, "# END sing-router-test") {
        t.Fatalf("markers missing: %s", out)
    }
    if !strings.Contains(out, "echo hi") {
        t.Fatalf("payload missing: %s", out)
    }
    info, _ := os.Stat(target)
    if info.Mode().Perm()&0o100 == 0 {
        t.Fatal("created file should be executable")
    }
}

func TestInjectAppendsToExisting(t *testing.T) {
    dir := t.TempDir()
    target := filepath.Join(dir, "nat-start")
    if err := os.WriteFile(target, []byte("#!/bin/sh\n# user content above\n"), 0o755); err != nil {
        t.Fatal(err)
    }
    if err := InjectHook(target, "sing-router-test", "echo new"); err != nil {
        t.Fatal(err)
    }
    out := read(t, target)
    if !strings.Contains(out, "user content above") {
        t.Fatal("preserved content missing")
    }
    if !strings.Contains(out, "echo new") {
        t.Fatal("payload missing")
    }
}

func TestInjectReplacesExistingBlock(t *testing.T) {
    dir := t.TempDir()
    target := filepath.Join(dir, "nat-start")
    initial := `#!/bin/sh
# preface
# BEGIN sing-router-test
echo old
# END sing-router-test
# postface
`
    if err := os.WriteFile(target, []byte(initial), 0o755); err != nil {
        t.Fatal(err)
    }
    if err := InjectHook(target, "sing-router-test", "echo new"); err != nil {
        t.Fatal(err)
    }
    out := read(t, target)
    if strings.Contains(out, "echo old") {
        t.Fatal("old payload should be replaced")
    }
    if !strings.Contains(out, "echo new") {
        t.Fatal("new payload missing")
    }
    if !strings.Contains(out, "# preface") || !strings.Contains(out, "# postface") {
        t.Fatal("non-block content disturbed")
    }
    if strings.Count(out, "# BEGIN sing-router-test") != 1 {
        t.Fatal("expected exactly one BEGIN marker")
    }
}

func TestInjectIdempotentMultipleTimes(t *testing.T) {
    dir := t.TempDir()
    target := filepath.Join(dir, "nat-start")
    for i := 0; i < 5; i++ {
        if err := InjectHook(target, "sing-router-test", "echo x"); err != nil {
            t.Fatalf("attempt %d: %v", i, err)
        }
    }
    out := read(t, target)
    if strings.Count(out, "# BEGIN sing-router-test") != 1 {
        t.Fatalf("multiple BEGIN markers: %s", out)
    }
}

func TestInjectIgnoresOtherBlocks(t *testing.T) {
    dir := t.TempDir()
    target := filepath.Join(dir, "nat-start")
    initial := `#!/bin/sh
# BEGIN other-tool
echo other
# END other-tool
`
    if err := os.WriteFile(target, []byte(initial), 0o755); err != nil {
        t.Fatal(err)
    }
    if err := InjectHook(target, "sing-router-test", "echo us"); err != nil {
        t.Fatal(err)
    }
    out := read(t, target)
    if !strings.Contains(out, "BEGIN other-tool") || !strings.Contains(out, "echo other") {
        t.Fatal("other block disturbed")
    }
    if !strings.Contains(out, "BEGIN sing-router-test") {
        t.Fatal("our block missing")
    }
}

func TestRemoveExistingBlock(t *testing.T) {
    dir := t.TempDir()
    target := filepath.Join(dir, "nat-start")
    initial := `#!/bin/sh
# preface

# BEGIN sing-router-test
echo us
# END sing-router-test

# trailing
`
    if err := os.WriteFile(target, []byte(initial), 0o755); err != nil {
        t.Fatal(err)
    }
    if err := RemoveHook(target, "sing-router-test"); err != nil {
        t.Fatal(err)
    }
    out := read(t, target)
    if strings.Contains(out, "BEGIN sing-router-test") || strings.Contains(out, "echo us") {
        t.Fatalf("block not removed: %s", out)
    }
    if !strings.Contains(out, "# preface") || !strings.Contains(out, "# trailing") {
        t.Fatal("surrounding content disturbed")
    }
}

func TestRemoveOnMissingFileNoError(t *testing.T) {
    if err := RemoveHook("/nonexistent/path", "sing-router-test"); err != nil {
        t.Fatalf("expected nil err on missing file, got %v", err)
    }
}

func TestRemoveOnFileWithoutBlockKeepsContent(t *testing.T) {
    dir := t.TempDir()
    target := filepath.Join(dir, "nat-start")
    if err := os.WriteFile(target, []byte("#!/bin/sh\necho only-user\n"), 0o755); err != nil {
        t.Fatal(err)
    }
    if err := RemoveHook(target, "sing-router-test"); err != nil {
        t.Fatal(err)
    }
    out := read(t, target)
    if !strings.Contains(out, "echo only-user") {
        t.Fatalf("user content should remain: %s", out)
    }
}
```

- [ ] **Step 2：跑测试看失败**

```bash
go test ./internal/install/... -run "TestInject|TestRemove"
```

期望：编译失败。

- [ ] **Step 3：写实现 jffs_hooks.go**

```go
package install

import (
    "bytes"
    "errors"
    "fmt"
    "os"
)

const (
    beginMarker = "# BEGIN %s (managed by `sing-router install`; do not edit)"
    endMarker   = "# END %s"
)

// InjectHook 在 path 文件中放置/更新 `# BEGIN <name>` ... `# END <name>` 块。
// 文件不存在则创建，含 shebang，权限 0755。已存在的块整段替换，块外内容不动。
func InjectHook(path, name, payload string) error {
    block := renderBlock(name, payload)
    data, err := os.ReadFile(path)
    if err != nil {
        if !errors.Is(err, os.ErrNotExist) {
            return err
        }
        // 新文件
        new := []byte("#!/bin/sh\n\n" + block + "\n")
        return os.WriteFile(path, new, 0o755)
    }
    s := string(data)
    begin := fmt.Sprintf(beginMarker, name)
    end := fmt.Sprintf(endMarker, name)
    if i, j := blockIndices(s, begin, end); i >= 0 && j > i {
        // 替换：保留 [0,i) 与 [j+lenEnd+1,len(s))，中间换成 block
        before := trimTrailingNewline(s[:i])
        after := s[j+len(end):]
        joined := before + "\n" + block + after
        if err := os.WriteFile(path, []byte(joined), 0o755); err != nil {
            return err
        }
        return os.Chmod(path, 0o755)
    }
    // 追加
    var buf bytes.Buffer
    buf.WriteString(s)
    if !bytes.HasSuffix(buf.Bytes(), []byte("\n")) {
        buf.WriteByte('\n')
    }
    buf.WriteString(block + "\n")
    if err := os.WriteFile(path, buf.Bytes(), 0o755); err != nil {
        return err
    }
    return os.Chmod(path, 0o755)
}

// RemoveHook 把 path 文件中我们的 BEGIN/END 块整段删除。
// 文件不存在或无块时为 no-op。
func RemoveHook(path, name string) error {
    data, err := os.ReadFile(path)
    if err != nil {
        if errors.Is(err, os.ErrNotExist) {
            return nil
        }
        return err
    }
    s := string(data)
    begin := fmt.Sprintf(beginMarker, name)
    end := fmt.Sprintf(endMarker, name)
    i, j := blockIndices(s, begin, end)
    if i < 0 || j <= i {
        return nil
    }
    before := trimTrailingNewline(s[:i])
    after := s[j+len(end):]
    if !bytes.HasPrefix([]byte(after), []byte("\n")) {
        after = "\n" + after
    }
    joined := before + after
    return os.WriteFile(path, []byte(joined), 0o755)
}

func renderBlock(name, payload string) string {
    return fmt.Sprintf(beginMarker+"\n%s\n"+endMarker, name, payload, name)
}

// blockIndices 返回 BEGIN 行起始字节与 END 行起始字节；找不到返回 (-1, -1)。
func blockIndices(s, begin, end string) (int, int) {
    i := indexOfLine(s, begin)
    if i < 0 {
        return -1, -1
    }
    rest := s[i:]
    j := indexOfLine(rest, end)
    if j < 0 {
        return -1, -1
    }
    return i, i + j
}

// indexOfLine 找以 prefix 整行开头的位置（行首匹配），-1 如不存在。
func indexOfLine(s, prefix string) int {
    if len(s) >= len(prefix) && s[:len(prefix)] == prefix {
        return 0
    }
    needle := "\n" + prefix
    for i := 0; i+len(needle) <= len(s); i++ {
        if s[i:i+len(needle)] == needle {
            return i + 1
        }
    }
    return -1
}

func trimTrailingNewline(s string) string {
    for len(s) > 0 && (s[len(s)-1] == '\n' || s[len(s)-1] == '\r') {
        s = s[:len(s)-1]
    }
    return s
}
```

- [ ] **Step 4：跑测试 + 覆盖率**

```bash
go test ./internal/install -coverprofile=/tmp/jffs.cov -run "TestInject|TestRemove"
go tool cover -func=/tmp/jffs.cov | grep jffs_hooks.go
```

期望：所有 case `PASS`；`jffs_hooks.go` 行覆盖率 100%（如果不到 100%，按 `go tool cover -html=/tmp/jffs.cov` 看缺口补 case）。

- [ ] **Step 5：提交**

```bash
git add internal/install/jffs_hooks.go internal/install/jffs_hooks_test.go
git commit -m "feat(install): idempotent BEGIN/END hook injection (100% covered)"
```

---

## Task 41：install/download — 镜像前缀下载器

**Files:**
- Create: `internal/install/download.go`
- Create: `internal/install/download_test.go`

- [ ] **Step 1：写测试 download_test.go**

```go
package install

import (
    "fmt"
    "io"
    "net/http"
    "net/http/httptest"
    "os"
    "path/filepath"
    "strings"
    "testing"
)

func TestRenderURL(t *testing.T) {
    cases := []struct {
        prefix, tmpl, version, want string
    }{
        {"", "https://gh/v{version}.tgz", "1.0.0", "https://gh/v1.0.0.tgz"},
        {"https://mirror/", "https://gh/v{version}.tgz", "1.0.0", "https://mirror/https://gh/v1.0.0.tgz"},
        {"https://mirror", "https://gh/v{version}.tgz", "1.0.0", "https://mirror/https://gh/v1.0.0.tgz"},
    }
    for _, c := range cases {
        if got := RenderURL(c.prefix, c.tmpl, c.version); got != c.want {
            t.Fatalf("prefix=%q want %q got %q", c.prefix, c.want, got)
        }
    }
}

func TestDownloadFileAtomic(t *testing.T) {
    payload := strings.Repeat("hello-", 1000)
    ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        _, _ = io.WriteString(w, payload)
    }))
    defer ts.Close()
    dir := t.TempDir()
    target := filepath.Join(dir, "out", "file.bin")
    if err := DownloadFile(ts.URL, target, 1, 5); err != nil {
        t.Fatal(err)
    }
    data, _ := os.ReadFile(target)
    if string(data) != payload {
        t.Fatal("payload mismatch")
    }
    // 没有遗留 tmp
    if _, err := os.Stat(target + ".tmp"); !os.IsNotExist(err) {
        t.Fatal("tmp survived")
    }
}

func TestDownloadFileRetries(t *testing.T) {
    var attempts int
    ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        attempts++
        if attempts < 3 {
            http.Error(w, "boom", http.StatusInternalServerError)
            return
        }
        _, _ = io.WriteString(w, "ok")
    }))
    defer ts.Close()
    dir := t.TempDir()
    target := filepath.Join(dir, "f")
    if err := DownloadFile(ts.URL, target, 5, 3); err != nil {
        t.Fatal(err)
    }
    if attempts != 3 {
        t.Fatalf("attempts=%d want 3", attempts)
    }
}

func TestDownloadFileFailsAfterMaxRetries(t *testing.T) {
    ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        http.Error(w, "boom", http.StatusInternalServerError)
    }))
    defer ts.Close()
    dir := t.TempDir()
    err := DownloadFile(ts.URL, filepath.Join(dir, "x"), 1, 2)
    if err == nil {
        t.Fatal("expected error")
    }
    if !strings.Contains(err.Error(), "boom") && !strings.Contains(err.Error(), "500") {
        t.Logf("err: %v (acceptable)", err)
    }
    _ = fmt.Sprint(err)
}
```

- [ ] **Step 2：写实现 download.go**

```go
package install

import (
    "fmt"
    "io"
    "net/http"
    "os"
    "path/filepath"
    "strings"
    "time"
)

// RenderURL 拼接 mirror_prefix + 已渲染 {version} 的 raw URL。
func RenderURL(mirrorPrefix, template, version string) string {
    raw := strings.ReplaceAll(template, "{version}", version)
    if mirrorPrefix == "" {
        return raw
    }
    if !strings.HasSuffix(mirrorPrefix, "/") {
        mirrorPrefix += "/"
    }
    return mirrorPrefix + raw
}

// DownloadFile 把 url 下载到 target；带原子写、超时与重试。
// timeoutSec：单次请求超时；retries：失败后重试次数（首次不计）。
func DownloadFile(url, target string, timeoutSec, retries int) error {
    if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
        return err
    }
    if timeoutSec <= 0 {
        timeoutSec = 60
    }
    if retries < 0 {
        retries = 0
    }
    client := &http.Client{Timeout: time.Duration(timeoutSec) * time.Second}

    var lastErr error
    for attempt := 0; attempt <= retries; attempt++ {
        if err := downloadOnce(client, url, target); err != nil {
            lastErr = err
            time.Sleep(time.Duration(attempt+1) * time.Second)
            continue
        }
        return nil
    }
    return fmt.Errorf("download %s after %d attempts: %w", url, retries+1, lastErr)
}

func downloadOnce(client *http.Client, url, target string) error {
    resp, err := client.Get(url)
    if err != nil {
        return err
    }
    defer func() { _ = resp.Body.Close() }()
    if resp.StatusCode >= 300 {
        body, _ := io.ReadAll(io.LimitReader(resp.Body, 256))
        return fmt.Errorf("http %d: %s", resp.StatusCode, body)
    }
    tmp := target + ".tmp"
    f, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
    if err != nil {
        return err
    }
    if _, err := io.Copy(f, resp.Body); err != nil {
        _ = f.Close()
        _ = os.Remove(tmp)
        return err
    }
    if err := f.Close(); err != nil {
        return err
    }
    return os.Rename(tmp, target)
}
```

- [ ] **Step 3：跑测试 + 提交**

```bash
go test ./internal/install/... -run "TestRenderURL|TestDownload" -v
git add internal/install/download.go internal/install/download_test.go
git commit -m "feat(install): mirror_prefix-aware atomic file downloader with retries"
```

---

## Task 42：CLI install 命令（编排上面所有 install/* 单元）

**Files:**
- Create: `internal/cli/install.go`
- Modify: `internal/cli/root.go`

- [ ] **Step 1：写 install.go**

```go
package cli

import (
    "fmt"
    "os"
    "path/filepath"

    "github.com/spf13/cobra"

    "github.com/moonfruit/sing-router/assets"
    "github.com/moonfruit/sing-router/internal/config"
    "github.com/moonfruit/sing-router/internal/install"
)

func newInstallCmd() *cobra.Command {
    var (
        rundir            string
        downloadSingBox   bool
        downloadCNList    bool
        autoStart         bool
        mirrorPrefix      string
        singBoxVersion    string
        skipJffs          bool
        dryRun            bool
        downloadSingBoxSet bool
        downloadCNListSet  bool
        autoStartSet       bool
    )
    cmd := &cobra.Command{
        Use:   "install",
        Short: "Install sing-router on this router",
        RunE: func(cmd *cobra.Command, args []string) error {
            // 1. 决议 RUNDIR
            if rundir == "" {
                rundir = "/opt/home/sing-router"
            }
            // 2. 读 daemon.toml（若存在），用其 [install] 默认填充未指定的 flag
            tomlPath := filepath.Join(rundir, "daemon.toml")
            cfg, _ := config.LoadDaemonConfig(tomlPath)
            if !downloadSingBoxSet {
                downloadSingBox = cfg.Install.DownloadSingBox
            }
            if !downloadCNListSet {
                downloadCNList = cfg.Install.DownloadCNList
            }
            if !autoStartSet {
                autoStart = cfg.Install.AutoStart
            }
            if mirrorPrefix == "" {
                mirrorPrefix = cfg.Download.MirrorPrefix
            }
            if singBoxVersion == "" {
                singBoxVersion = cfg.Download.SingBoxDefaultVersion
            }

            run := func(label string, fn func() error) error {
                if dryRun {
                    fmt.Fprintln(cmd.OutOrStdout(), "[dry-run]", label)
                    return nil
                }
                fmt.Fprintln(cmd.OutOrStdout(), "→", label)
                return fn()
            }

            // 3. layout
            if err := run("ensure rundir layout", func() error { return install.EnsureLayout(rundir) }); err != nil {
                return err
            }
            // 4. seed
            if err := run("seed default config.d/* and daemon.toml", func() error { return install.SeedDefaults(rundir) }); err != nil {
                return err
            }
            // 5. init.d
            if err := run("write /opt/etc/init.d/S99sing-router", func() error {
                return install.WriteInitd("/opt/etc/init.d/S99sing-router", rundir)
            }); err != nil {
                return err
            }
            // 6/7. jffs hooks
            if !skipJffs {
                natPayload, _ := assets.ReadFile("jffs/nat-start.snippet")
                svcPayload, _ := assets.ReadFile("jffs/services-start.snippet")
                if err := run("inject /jffs/scripts/nat-start", func() error {
                    return install.InjectHook("/jffs/scripts/nat-start", "sing-router", payloadOnly(string(natPayload)))
                }); err != nil {
                    return err
                }
                if err := run("inject /jffs/scripts/services-start", func() error {
                    return install.InjectHook("/jffs/scripts/services-start", "sing-router", payloadOnly(string(svcPayload)))
                }); err != nil {
                    return err
                }
            }
            // 8. downloads
            if downloadSingBox {
                version := singBoxVersion
                if version == "latest" {
                    version = resolveLatestSingBoxVersion(cmd.OutOrStdout(), mirrorPrefix)
                }
                if version == "" {
                    return fmt.Errorf("cannot resolve sing-box version (provide --sing-box-version explicitly)")
                }
                url := install.RenderURL(mirrorPrefix, cfg.Download.SingBoxURLTemplate, version)
                tarball := filepath.Join(rundir, "var", "sing-box.tar.gz")
                if err := run("download sing-box "+url, func() error {
                    return install.DownloadFile(url, tarball, cfg.Download.HTTPTimeoutSeconds, cfg.Download.HTTPRetries)
                }); err != nil {
                    return err
                }
                if err := run("extract sing-box to bin/", func() error {
                    return extractSingBox(tarball, filepath.Join(rundir, "bin", "sing-box"))
                }); err != nil {
                    return err
                }
            }
            if downloadCNList {
                url := mirrorPrefix
                if url != "" && url[len(url)-1] != '/' {
                    url += "/"
                }
                url += cfg.Download.CNListURL
                if cfg.Download.CNListURL != "" && (len(cfg.Download.CNListURL) >= 4 && cfg.Download.CNListURL[:4] == "http") && mirrorPrefix == "" {
                    url = cfg.Download.CNListURL
                }
                if err := run("download cn.txt "+url, func() error {
                    return install.DownloadFile(url, filepath.Join(rundir, "var", "cn.txt"), cfg.Download.HTTPTimeoutSeconds, cfg.Download.HTTPRetries)
                }); err != nil {
                    return err
                }
            }

            // 9. auto-start
            if autoStart {
                if err := run("start init.d service", func() error {
                    return runShell("/opt/etc/init.d/S99sing-router", "start")
                }); err != nil {
                    return err
                }
            }

            // 10. next steps
            fmt.Fprintln(cmd.OutOrStdout())
            fmt.Fprintln(cmd.OutOrStdout(), "Next steps:")
            fmt.Fprintln(cmd.OutOrStdout(), "  1. Edit", filepath.Join(rundir, "daemon.toml"), "to taste")
            fmt.Fprintln(cmd.OutOrStdout(), "  2. Place your zoo.json at", filepath.Join(rundir, "var", "zoo.raw.json"))
            fmt.Fprintln(cmd.OutOrStdout(), "  3. Run `S99sing-router start` (if --start not used) and `sing-router status`")
            return nil
        },
    }
    cmd.Flags().StringVarP(&rundir, "rundir", "D", "", "Runtime root directory (default /opt/home/sing-router)")
    cmd.Flags().BoolVar(&downloadSingBox, "download-sing-box", true, "Download sing-box into bin/")
    cmd.Flags().BoolVar(&downloadCNList, "download-cn-list", true, "Download cn.txt into var/")
    cmd.Flags().BoolVar(&autoStart, "start", false, "Start init.d service after install")
    cmd.Flags().StringVar(&mirrorPrefix, "mirror-prefix", "", "Download mirror prefix (e.g. https://ghproxy.com/)")
    cmd.Flags().StringVar(&singBoxVersion, "sing-box-version", "", "sing-box version to download (default latest)")
    cmd.Flags().BoolVar(&skipJffs, "skip-jffs", false, "Skip /jffs/scripts/* hook injection")
    cmd.Flags().BoolVar(&dryRun, "dry-run", false, "Print actions without executing")

    // 标记 flag 是否被显式设置（解决"未传时用 toml 默认"问题）
    cmd.PreRun = func(cmd *cobra.Command, args []string) {
        downloadSingBoxSet = cmd.Flags().Changed("download-sing-box")
        downloadCNListSet = cmd.Flags().Changed("download-cn-list")
        autoStartSet = cmd.Flags().Changed("start")
    }
    return cmd
}

// payloadOnly 从 snippet 文件里抽出 BEGIN/END 之间的内容（snippet 文件本身已包含
// 完整 BEGIN/END，但 InjectHook 期望只接收 payload）。
func payloadOnly(snippet string) string {
    lines := splitLines(snippet)
    var inside bool
    var out []string
    for _, l := range lines {
        if hasPrefix(l, "# BEGIN") {
            inside = true
            continue
        }
        if hasPrefix(l, "# END") {
            inside = false
            continue
        }
        if inside {
            out = append(out, l)
        }
    }
    return joinLines(out)
}

func splitLines(s string) []string {
    var out []string
    var cur []byte
    for i := 0; i < len(s); i++ {
        if s[i] == '\n' {
            out = append(out, string(cur))
            cur = cur[:0]
            continue
        }
        cur = append(cur, s[i])
    }
    if len(cur) > 0 {
        out = append(out, string(cur))
    }
    return out
}

func joinLines(ls []string) string {
    s := ""
    for i, l := range ls {
        if i > 0 {
            s += "\n"
        }
        s += l
    }
    return s
}

func hasPrefix(s, p string) bool { return len(s) >= len(p) && s[:len(p)] == p }

// resolveLatestSingBoxVersion 当前简单返回硬编码 fallback，避免在 install 阶段强依赖
// GitHub releases API（B 阶段会做完整版本解析）。
func resolveLatestSingBoxVersion(_ interface{ Write(p []byte) (int, error) }, _ string) string {
    return "1.13.5"
}

// extractSingBox 简化：调用宿主 tar 命令解压并把 sing-box 移到目标位置。
// 在 RT-BE88U 与本地都假设 entware 提供 tar；不可用时报错。
func extractSingBox(tarball, target string) error {
    if _, err := os.Stat(tarball); err != nil {
        return err
    }
    tmpDir := filepath.Join(filepath.Dir(target), ".extract")
    if err := os.MkdirAll(tmpDir, 0o755); err != nil {
        return err
    }
    if err := runShell("tar", "-xzf", tarball, "-C", tmpDir); err != nil {
        return err
    }
    found, err := findSingBoxBinary(tmpDir)
    if err != nil {
        return err
    }
    if err := os.Rename(found, target+".new"); err != nil {
        return err
    }
    if err := os.Chmod(target+".new", 0o755); err != nil {
        return err
    }
    if err := os.Rename(target+".new", target); err != nil {
        return err
    }
    return os.RemoveAll(tmpDir)
}

func findSingBoxBinary(dir string) (string, error) {
    var found string
    err := filepath.Walk(dir, func(p string, info os.FileInfo, walkErr error) error {
        if walkErr != nil || info.IsDir() {
            return walkErr
        }
        if filepath.Base(p) == "sing-box" {
            found = p
        }
        return nil
    })
    if err != nil {
        return "", err
    }
    if found == "" {
        return "", fmt.Errorf("sing-box binary not found in tarball")
    }
    return found, nil
}

func runShell(cmd string, args ...string) error {
    c := osexecCommand(cmd, args...)
    return c.Run()
}

// osexecCommand 暴露给测试做 mock；默认走 os/exec。
var osexecCommand = func(name string, args ...string) interface{ Run() error } {
    panic("osexecCommand must be replaced with os/exec.Command in main wiring")
}
```

> 注：`osexecCommand` 在测试中被 mock；Phase 12 main wire-up 时把它指向 `exec.Command`。

- [ ] **Step 2：在 root.go 注册 newInstallCmd()**

- [ ] **Step 3：编译 sanity + 提交**

```bash
go build ./...
git add internal/cli/install.go internal/cli/root.go
git commit -m "feat(cli): install command orchestrating layout/seed/initd/jffs/downloads"
```

---

## Task 43：CLI uninstall

**Files:**
- Create: `internal/cli/uninstall.go`
- Modify: `internal/cli/root.go`

- [ ] **Step 1：写 uninstall.go**

```go
package cli

import (
    "fmt"
    "os"

    "github.com/spf13/cobra"

    "github.com/moonfruit/sing-router/internal/install"
)

func newUninstallCmd() *cobra.Command {
    var (
        purge      bool
        skipJffs   bool
        keepInit   bool
        rundir     string
    )
    cmd := &cobra.Command{
        Use:   "uninstall",
        Short: "Uninstall sing-router (init.d + jffs hooks; --purge to delete RUNDIR)",
        RunE: func(cmd *cobra.Command, args []string) error {
            if rundir == "" {
                rundir = "/opt/home/sing-router"
            }
            // 1. stop service if present
            if _, err := os.Stat("/opt/etc/init.d/S99sing-router"); err == nil {
                _ = runShell("/opt/etc/init.d/S99sing-router", "stop")
            }
            // 2. remove jffs hooks
            if !skipJffs {
                if err := install.RemoveHook("/jffs/scripts/nat-start", "sing-router"); err != nil {
                    return err
                }
                if err := install.RemoveHook("/jffs/scripts/services-start", "sing-router"); err != nil {
                    return err
                }
            }
            // 3. remove init.d
            if !keepInit {
                _ = os.Remove("/opt/etc/init.d/S99sing-router")
            }
            // 4. purge rundir
            if purge {
                if err := os.RemoveAll(rundir); err != nil {
                    return err
                }
            }
            // 5. binary stays
            fmt.Fprintln(cmd.OutOrStdout(), "uninstalled. /opt/sbin/sing-router binary preserved (delete manually if desired).")
            return nil
        },
    }
    cmd.Flags().BoolVar(&purge, "purge", false, "Also delete RUNDIR (lose all user config and downloaded artifacts)")
    cmd.Flags().BoolVar(&skipJffs, "skip-jffs", false, "Don't touch /jffs/scripts/")
    cmd.Flags().BoolVar(&keepInit, "keep-init", false, "Don't delete /opt/etc/init.d/S99sing-router")
    cmd.Flags().StringVarP(&rundir, "rundir", "D", "", "Runtime root directory (for --purge)")
    return cmd
}
```

- [ ] **Step 2：注册 + 编译 + 提交**

```bash
# 在 root.go 注册 newUninstallCmd()
go build ./...
git add internal/cli/uninstall.go internal/cli/root.go
git commit -m "feat(cli): uninstall command (idempotent jffs removal + optional purge)"
```

---

## Task 44：CLI doctor

**Files:**
- Create: `internal/cli/doctor.go`
- Modify: `internal/cli/root.go`

- [ ] **Step 1：写 doctor.go**

```go
package cli

import (
    "encoding/json"
    "fmt"
    "io"
    "os"
    "path/filepath"
    "strings"

    "github.com/spf13/cobra"
)

type doctorCheck struct {
    Name   string `json:"name"`
    Status string `json:"status"` // pass | warn | fail
    Detail string `json:"detail,omitempty"`
}

func newDoctorCmd() *cobra.Command {
    var (
        rundir string
        asJSON bool
    )
    cmd := &cobra.Command{
        Use:   "doctor",
        Short: "Read-only health check of all sing-router files and runtime expectations",
        RunE: func(cmd *cobra.Command, args []string) error {
            if rundir == "" {
                rundir = "/opt/home/sing-router"
            }
            checks := runDoctorChecks(rundir)
            return printDoctor(cmd.OutOrStdout(), checks, asJSON)
        },
    }
    cmd.Flags().StringVarP(&rundir, "rundir", "D", "", "Runtime root directory")
    cmd.Flags().BoolVar(&asJSON, "json", false, "JSON output")
    return cmd
}

func runDoctorChecks(rundir string) []doctorCheck {
    var out []doctorCheck

    fileExists := func(path string) bool {
        info, err := os.Stat(path)
        return err == nil && !info.IsDir()
    }

    out = append(out, checkExistsExec("/opt/sbin/sing-router"))
    out = append(out, checkDirExists(rundir, "rundir"))
    for _, sub := range []string{"config.d", "bin", "var", "run", "log"} {
        out = append(out, checkDirExists(filepath.Join(rundir, sub), "rundir/"+sub))
    }
    out = append(out, checkExistsExec(filepath.Join(rundir, "bin", "sing-box")))
    for _, c := range []string{"clash.json", "dns.json", "inbounds.json", "log.json"} {
        out = append(out, checkExistsAs(filepath.Join(rundir, "config.d", c), "config.d/"+c, "fail"))
    }
    out = append(out, checkExistsAs(filepath.Join(rundir, "config.d", "zoo.json"), "config.d/zoo.json", "warn"))
    out = append(out, checkExistsAs(filepath.Join(rundir, "var", "cn.txt"), "var/cn.txt", "warn"))
    out = append(out, checkExistsExec("/opt/etc/init.d/S99sing-router"))
    out = append(out, checkJffsHook("/jffs/scripts/nat-start"))
    out = append(out, checkJffsHook("/jffs/scripts/services-start"))

    // dns.json inet4_range 与 routing FAKEIP 一致性检查（spec 6.4 hint）
    dnsPath := filepath.Join(rundir, "config.d", "dns.json")
    if fileExists(dnsPath) {
        data, _ := os.ReadFile(dnsPath)
        if strings.Contains(string(data), `"inet4_range": "22.0.0.0/8"`) {
            out = append(out, doctorCheck{Name: "dns.json inet4_range", Status: "warn", Detail: "still 22.0.0.0/8; daemon expects 28.0.0.0/8"})
        } else {
            out = append(out, doctorCheck{Name: "dns.json inet4_range", Status: "pass"})
        }
    }
    // log.timestamp = true（vendored sing2seq parser 硬依赖）
    logPath := filepath.Join(rundir, "config.d", "log.json")
    if fileExists(logPath) {
        data, _ := os.ReadFile(logPath)
        if strings.Contains(string(data), `"timestamp": true`) {
            out = append(out, doctorCheck{Name: "log.json timestamp", Status: "pass"})
        } else {
            out = append(out, doctorCheck{Name: "log.json timestamp", Status: "warn", Detail: "must be true; otherwise sing-box log parsing degrades"})
        }
    }
    return out
}

func checkExistsExec(path string) doctorCheck {
    info, err := os.Stat(path)
    if err != nil {
        return doctorCheck{Name: path, Status: "fail", Detail: err.Error()}
    }
    if info.Mode().Perm()&0o100 == 0 {
        return doctorCheck{Name: path, Status: "fail", Detail: "not executable"}
    }
    return doctorCheck{Name: path, Status: "pass"}
}

func checkDirExists(path, label string) doctorCheck {
    info, err := os.Stat(path)
    if err != nil {
        return doctorCheck{Name: label, Status: "fail", Detail: err.Error()}
    }
    if !info.IsDir() {
        return doctorCheck{Name: label, Status: "fail", Detail: "not a directory"}
    }
    return doctorCheck{Name: label, Status: "pass"}
}

func checkExistsAs(path, label, warnOrFail string) doctorCheck {
    if _, err := os.Stat(path); err != nil {
        return doctorCheck{Name: label, Status: warnOrFail, Detail: err.Error()}
    }
    return doctorCheck{Name: label, Status: "pass"}
}

func checkJffsHook(path string) doctorCheck {
    data, err := os.ReadFile(path)
    if err != nil {
        return doctorCheck{Name: path, Status: "fail", Detail: err.Error()}
    }
    if !strings.Contains(string(data), "BEGIN sing-router") {
        return doctorCheck{Name: path, Status: "fail", Detail: "BEGIN sing-router block missing"}
    }
    return doctorCheck{Name: path, Status: "pass"}
}

func printDoctor(w io.Writer, checks []doctorCheck, asJSON bool) error {
    if asJSON {
        return json.NewEncoder(w).Encode(checks)
    }
    for _, c := range checks {
        marker := "PASS"
        switch c.Status {
        case "warn":
            marker = "WARN"
        case "fail":
            marker = "FAIL"
        }
        if c.Detail == "" {
            fmt.Fprintf(w, "  %s  %s\n", marker, c.Name)
        } else {
            fmt.Fprintf(w, "  %s  %s — %s\n", marker, c.Name, c.Detail)
        }
    }
    return nil
}
```

- [ ] **Step 2：注册 + 编译 + 提交**

```bash
# 在 root.go 注册 newDoctorCmd()
go build ./...
git add internal/cli/doctor.go internal/cli/root.go
git commit -m "feat(cli): doctor read-only health checks"
```

---

# Phase 12：main 连线 + 交叉编译

## Task 45：把 daemon 子命令真正连到 supervisor + http server

**Files:**
- Create: `internal/cli/wireup_daemon.go`
- Modify: `internal/cli/install.go`（osexecCommand 真值）
- Modify: `internal/cli/daemon.go`

- [ ] **Step 1：写 wireup_daemon.go**

```go
package cli

import (
    "context"
    "fmt"
    "os"
    "os/exec"
    "path/filepath"
    "strings"

    "github.com/moonfruit/sing-router/assets"
    "github.com/moonfruit/sing-router/internal/config"
    "github.com/moonfruit/sing-router/internal/daemon"
    log "github.com/moonfruit/sing-router/internal/log"
    "github.com/moonfruit/sing-router/internal/shell"
    "github.com/moonfruit/sing-router/internal/version"
)

// init 替换占位的 runDaemon 与 osexecCommand 为真实实现。
func init() {
    osexecCommand = func(name string, args ...string) interface{ Run() error } {
        return exec.Command(name, args...)
    }
    runDaemon = realRunDaemon
}

// realRunDaemon 是 daemon 子命令的真实入口。
func realRunDaemon(ctx interface{ Done() <-chan struct{} }, rundir string) error {
    // 把 ctxLike 还原成 context.Context（占位 alias 实际是同接口）
    realCtx := ctx.(context.Context)

    if rundir == "" {
        rundir = "/opt/home/sing-router"
    }
    if err := os.Chdir(rundir); err != nil {
        return fmt.Errorf("chdir %s: %w", rundir, err)
    }
    cfg, err := config.LoadDaemonConfig(filepath.Join(rundir, "daemon.toml"))
    if err != nil {
        return err
    }

    level, _ := log.ParseLevel(cfg.Log.Level)
    writer, err := log.NewWriter(log.WriterConfig{
        Path:       filepath.Join(rundir, cfg.Log.File),
        MaxSize:    int64(cfg.Log.MaxSizeMB) * 1024 * 1024,
        MaxBackups: cfg.Log.MaxBackups,
        Gzip:       true,
    })
    if err != nil {
        return err
    }
    defer func() { _ = writer.Close() }()
    bus := log.NewBus(4096)
    defer bus.Close()
    em := log.NewEmitter(log.EmitterConfig{
        Source:   "daemon",
        MinLevel: level,
        Writer:   writer,
        Bus:      bus,
    })
    em.Info("supervisor", "supervisor.boot.started", "starting daemon at {Rundir}", map[string]any{"Rundir": rundir})

    routing := config.LoadRouting(cfg)
    cnPath := filepath.Join(rundir, "var", "cn.txt")
    runner := shell.NewRunner(shell.RunnerConfig{
        Bash: "/bin/bash",
        Env:  routing.EnvVars(cnPath),
    })
    runner.OnStderr = func(line string) {
        em.Info("shell", "shell.stderr", "{Line}", map[string]any{"Line": line})
    }
    startup := assets.MustReadFile("shell/startup.sh")
    teardown := assets.MustReadFile("shell/teardown.sh")

    sup := daemon.New(daemon.SupervisorConfig{
        Emitter:       em,
        SingBoxBinary: filepath.Join(rundir, cfg.Runtime.SingBoxBinary),
        SingBoxArgs:   []string{"run", "-D", rundir, "-C", cfg.Runtime.ConfigDir},
        SingBoxDir:    rundir,
        ReadyConfig: daemon.ReadyConfig{
            TCPDials: []string{
                fmt.Sprintf("127.0.0.1:%d", routing.DnsPort),
                fmt.Sprintf("127.0.0.1:%d", routing.RedirectPort),
            },
            ClashAPIURL:  "http://127.0.0.1:9999/version",
            TotalTimeout: 5 * time_seconds_5(),
            Interval:     time_milliseconds_200(),
        },
        StartupHook: func(ctx context.Context) error {
            em.Info("shell", "shell.startup.exec", "running startup.sh", nil)
            err := runner.Run(ctx, string(startup), nil)
            if err != nil {
                em.Error("shell", "shell.startup.failed", "startup failed: {Err}", map[string]any{"Err": err.Error()})
                return err
            }
            em.Info("shell", "shell.startup.completed", "iptables installed", nil)
            return nil
        },
        TeardownHook: func(ctx context.Context) error {
            em.Info("shell", "shell.teardown.exec", "running teardown.sh", nil)
            err := runner.Run(ctx, string(teardown), nil)
            if err != nil {
                em.Warn("shell", "shell.teardown.failed", "teardown failed: {Err}", map[string]any{"Err": err.Error()})
                return err
            }
            em.Info("shell", "shell.teardown.completed", "iptables removed", nil)
            return nil
        },
    })

    return daemon.Run(realCtx, daemon.Options{
        Rundir:     rundir,
        Listen:     cfg.HTTP.Listen,
        Version:    version.String(),
        Emitter:    em,
        Supervisor: sup,
        ReapplyRules: func(ctx context.Context) error {
            if err := runner.Run(ctx, string(teardown), nil); err != nil {
                em.Warn("shell", "shell.teardown.failed", "teardown best-effort failed: {Err}", map[string]any{"Err": err.Error()})
            }
            return runner.Run(ctx, string(startup), nil)
        },
        CheckConfig: func(ctx context.Context) error {
            return config.CheckSingBoxConfig(ctx, filepath.Join(rundir, cfg.Runtime.SingBoxBinary),
                filepath.Join(rundir, cfg.Runtime.ConfigDir))
        },
        StatusExtra: func() map[string]any {
            return map[string]any{
                "config": map[string]any{
                    "config_dir": filepath.Join(rundir, cfg.Runtime.ConfigDir),
                },
            }
        },
        ScriptByName: func(name string) ([]byte, error) {
            path, ok := scriptMap[name]
            if !ok {
                return nil, fmt.Errorf("unknown script %q", name)
            }
            return assets.ReadFile(path)
        },
    })
}

// 微小的时间 helper，避免 import "time" 在 daemon 包内
// 因为 ReadyConfig.TotalTimeout 是 time.Duration，下面这两个返回 time.Duration
func time_seconds_5() time.Duration { return 5 * time.Second }
func time_milliseconds_200() time.Duration { return 200 * time.Millisecond }
```

> 注：上面 `time` 标识符未 import 会编译失败；最终编辑时把 `time` 直接 import 进来：

```go
import "time"
```

并把 `time_seconds_5()` 与 `time_milliseconds_200()` 移除，改为 inline `5*time.Second` 与 `200*time.Millisecond`。

- [ ] **Step 2：编译 + sanity**

```bash
go build -o /tmp/sr ./cmd/sing-router
/tmp/sr daemon --help
rm /tmp/sr
```

- [ ] **Step 3：提交**

```bash
git add internal/cli/wireup_daemon.go internal/cli/install.go internal/cli/daemon.go
git commit -m "feat(daemon): wire supervisor + http + log + shell into daemon subcommand"
```

---

## Task 46：交叉编译验证 + smoke test on darwin

**Files:**
- Modify: `cmd/sing-router/main.go`（确保 ldflags 注入版本）
- Create: `Makefile`

- [ ] **Step 1：写 Makefile**

```makefile
GO          ?= go
BIN         ?= sing-router
PKG         := github.com/moonfruit/sing-router/internal/version
VERSION     ?= 0.1.0+$(shell git rev-parse --short HEAD 2>/dev/null || echo dev)
LDFLAGS     := -s -w -X $(PKG).Version=$(VERSION)

.PHONY: build build-arm64 test cover fakebox

build:
	$(GO) build -trimpath -ldflags '$(LDFLAGS)' -o $(BIN) ./cmd/sing-router

build-arm64:
	GOOS=linux GOARCH=arm64 $(GO) build -trimpath -ldflags '$(LDFLAGS)' -o $(BIN)-linux-arm64 ./cmd/sing-router

test:
	$(GO) test ./...

cover:
	$(GO) test ./... -coverprofile=coverage.out
	$(GO) tool cover -func=coverage.out

fakebox:
	testdata/fake-sing-box/build.sh
```

- [ ] **Step 2：darwin smoke test**

```bash
make build
./sing-router version          # 期望输出 0.1.0+xxxx
./sing-router status           # 期望 daemon: not running
./sing-router script startup | head -5
./sing-router doctor -D /tmp/nope || true
rm sing-router
```

- [ ] **Step 3：linux/arm64 cross-compile**

```bash
make build-arm64
file sing-router-linux-arm64       # 期望 ELF 64-bit LSB ... ARM aarch64
ls -lh sing-router-linux-arm64     # 期望 < 15 MB
rm sing-router-linux-arm64
```

- [ ] **Step 4：全包覆盖率快照**

```bash
make cover | tail -30
```

期望：
- `internal/config/zoo.go` 100%
- `internal/install/jffs_hooks.go` 100%
- 其它 > 70%

- [ ] **Step 5：提交**

```bash
git add Makefile
git commit -m "build: Makefile with linux/arm64 cross-compile + cover targets"
```

---

# 完成标准（执行 part1+2+3+4 全部任务后）

- [ ] `make test` 全 PASS
- [ ] `internal/config/zoo.go` 与 `internal/install/jffs_hooks.go` 行覆盖率 100%
- [ ] `make build` 在 darwin 产出可执行；`./sing-router status` / `script startup` / `doctor` 均能跑
- [ ] `make build-arm64` 产出 `sing-router-linux-arm64`，大小 < 15 MB
- [ ] git log 体现"每 Task 一组小提交"的节奏，Phase 间有完整集成 sanity（执行 plan 时由 subagent-driven-development 把控）
- [ ] spec §13 的 7 项验收标准在 RT-BE88U 真机上手动跑过（不在 plan 范围内自动化，但 plan 实施完毕后必做）

---

# 整体执行建议

1. **顺序推进**：Phase 1 → 2 → ... → 12，不要跳跃；后面 Phase 依赖前面的接口。
2. **每个 Task 内严格 TDD**：测试先 → 看失败 → 实现 → 看通过 → 提交。
3. **每 Phase 结束做集成 sanity**：`go test ./... && go build ./cmd/sing-router`。出现新失败立刻修，不要积压。
4. **修改记录追溯**：每条 git commit message 都用 `feat(<area>):` / `test(<area>):` 前缀，便于以后 git log 查找。
5. **遇到接口冲突**：例如 supervisor 实际签名与 plan 写的有差异时，**以 supervisor.go 为准**，更新 plan 中下游 task 引用，不要破坏类型一致性。

Plan 完成。
