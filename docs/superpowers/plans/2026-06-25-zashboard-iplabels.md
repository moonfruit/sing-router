# zashboard iplabels 本地生成 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 让 sing-router daemon 在路由器本地生成 zashboard 的 `source-ip-label-list` 导入文件（`<rundir>/<ui_dir>/zashboard.json`），运行周期与其它在线资源同步一致，并提供手动 CLI 触发。

**Architecture:** 新增纯函数包 `internal/zashboard/`（解析 4 个本地数据源 + 合并 + uuid5 派生 + 原子写）。后台 `sync_loop` 每轮无条件调用一次生成（独立步骤，不进 Applier、不触发 restart）；CLI `update zashboard` / `update all` 复用同一生成逻辑。静态标签表由 `daemon.toml` 的 `[zashboard.static_labels]` 配置。顺便移除目前空壳的 `download_zashboard`。

**Tech Stack:** Go 1.26.2、`github.com/google/uuid`（新增，v5 name-based UUID）、`github.com/BurntSushi/toml`、`github.com/spf13/cobra`、标准库 `os/exec` / `crypto/sha256` / `encoding/json`。

## Global Constraints

- 模块路径 `github.com/moonfruit/sing-router`，Go 1.26.2。
- 单 ARM64 二进制、运行时无外部依赖；新增的 Go 依赖仅 `github.com/google/uuid`（纯 Go、零传递依赖）。
- 输出格式必须逐字节匹配参考 Python 脚本：外层 `{"config/source-ip-label-list": "<compact-json>"}` 带 2 空格缩进；内层 entries 为紧凑 JSON 字符串（无空格分隔、`ensure_ascii=false` 等价：emoji/中文按 UTF-8 原样输出、不转义 `<>&`）。
- uuid5 命名空间常量 `6f1d4c2a-9b3e-5a7c-8d6f-2e4a1b0c9d8e`，与 Python 一致；同一 key 每次得到相同 id。
- 合并优先级：路由器数据优先，静态表只补缺失 key（setdefault 语义）。
- 排序：IPv4 按数值在前 → IPv6 居中 → 其它最后。
- `ui_dir` 不存在即跳过生成（不报错）；任一数据源采集失败降级为空源 + warning，不中断。
- 测试门：`go test ./...` 与 `go vet ./...` 全绿；改 `assets/` 后跑 `go test ./assets/`。
- 提交风格：中文 conventional，scope 按受影响子目录；提交信息结尾加 `Claude-Session: https://claude.ai/code/session_0146k61NbyfqCBh91UDZEXL1`。

---

### Task 1: zashboard 解析与 entry 构建（纯函数 + uuid5）

**Files:**
- Create: `internal/zashboard/labels.go`
- Create: `internal/zashboard/labels_test.go`
- Modify: `go.mod`、`go.sum`（新增 `github.com/google/uuid`）

**Interfaces:**
- Produces:
  - `type Entry struct { Key string \`json:"key"\`; Label string \`json:"label"\`; ID string \`json:"id"\` }`
  - `type RawData struct { Clients, ARP, Leases, Neigh string }`
  - `func BuildEntries(raw RawData, static map[string]string) []Entry`
  - `func makeID(key string) string`（包内）

- [ ] **Step 1: 添加 google/uuid 依赖**

Run:
```bash
cd /Users/moon/Workspace.localized/proxy/sing-router && go get github.com/google/uuid@latest
```
Expected: go.mod 的 require 块出现 `github.com/google/uuid vX.Y.Z`，go.sum 更新。

- [ ] **Step 2: 写失败测试 `internal/zashboard/labels_test.go`**

```go
package zashboard

import (
	"reflect"
	"testing"
)

func TestParseCustomClientlist(t *testing.T) {
	// 条目以 < 分隔，字段以 > 分隔，[0]=名 [1]=MAC
	in := "<💻笔记本>AA:BB:CC:DD:EE:01>0>><📱手机>aa:bb:cc:dd:ee:02>0>"
	got := parseCustomClientlist(in)
	want := map[string]string{
		"AA:BB:CC:DD:EE:01": "💻笔记本",
		"AA:BB:CC:DD:EE:02": "📱手机",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v want %v", got, want)
	}
}

func TestParseARP(t *testing.T) {
	// 列: IP HWtype Flags HWaddress Mask Device；跳过表头/flags 0x0/全零 MAC
	in := "IP address       HW type     Flags       HW address            Mask     Device\n" +
		"192.168.50.10    0x1         0x2         aa:bb:cc:dd:ee:01     *        br0\n" +
		"192.168.50.11    0x1         0x0         aa:bb:cc:dd:ee:02     *        br0\n" +
		"192.168.50.12    0x1         0x2         00:00:00:00:00:00     *        br0\n"
	got := parseARP(in)
	want := map[string]string{"AA:BB:CC:DD:EE:01": "192.168.50.10"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v want %v", got, want)
	}
}

func TestParseLeases(t *testing.T) {
	// 列: expiry MAC IP hostname clientid；setdefault（首次出现优先）
	in := "1700000000 aa:bb:cc:dd:ee:01 192.168.50.20 host-a 01:aa\n" +
		"1700000001 aa:bb:cc:dd:ee:01 192.168.50.99 host-a 01:aa\n"
	got := parseLeases(in)
	want := map[string]string{"AA:BB:CC:DD:EE:01": "192.168.50.20"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v want %v", got, want)
	}
}

func TestParseIPv6NeighGlobalOnly(t *testing.T) {
	// 仅取全局单播；跳过 fe80 链路本地、fd00 ULA；去重
	in := "2408:1:2:3::abcd dev br0 lladdr aa:bb:cc:dd:ee:01 REACHABLE\n" +
		"fe80::1 dev br0 lladdr aa:bb:cc:dd:ee:01 STALE\n" +
		"fd00::1 dev br0 lladdr aa:bb:cc:dd:ee:01 STALE\n" +
		"2408:1:2:3::abcd dev br0 lladdr aa:bb:cc:dd:ee:01 REACHABLE\n"
	got := parseIPv6Neigh(in)
	want := map[string][]string{"AA:BB:CC:DD:EE:01": {"2408:1:2:3::abcd"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v want %v", got, want)
	}
}

func TestMakeIDKnownVectors(t *testing.T) {
	cases := map[string]string{
		"127.0.0.1":         "25889ea6-b3f0-5e91-940e-0949be5e7472",
		"192.168.50.1":      "07e198c4-5060-5d0b-bae6-03094a177617",
		"fdfe:dcba:9876::1": "68382dcd-409e-5a1b-bb06-9bea25eaeeed",
	}
	for key, want := range cases {
		if got := makeID(key); got != want {
			t.Fatalf("makeID(%q)=%s want %s", key, got, want)
		}
	}
}

func TestBuildEntriesMergeAndSort(t *testing.T) {
	raw := RawData{
		Clients: "<💻笔记本>AA:BB:CC:DD:EE:01>0>",
		Leases:  "1700000000 aa:bb:cc:dd:ee:01 192.168.50.20 host-a 01:aa\n",
		ARP:     "IP address HW type Flags HW address Mask Device\n" +
			"192.168.50.10 0x1 0x2 aa:bb:cc:dd:ee:01 * br0\n",
		Neigh: "2408:1:2:3::1 dev br0 lladdr aa:bb:cc:dd:ee:01 REACHABLE\n",
	}
	static := map[string]string{
		"127.0.0.1":    "💻本机",
		"192.168.50.10": "应被路由器数据覆盖",
	}
	got := BuildEntries(raw, static)

	// 期望: ARP 覆盖 leases → MAC 的 IPv4=192.168.50.10；含 IPv6；静态 127.0.0.1 补入；
	//       192.168.50.10 用路由器名（路由器优先）。排序: IPv4(127.0.0.1 < 192.168.50.10) < IPv6
	want := []Entry{
		{Key: "127.0.0.1", Label: "💻本机", ID: makeID("127.0.0.1")},
		{Key: "192.168.50.10", Label: "💻笔记本", ID: makeID("192.168.50.10")},
		{Key: "2408:1:2:3::1", Label: "💻笔记本", ID: makeID("2408:1:2:3::1")},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %#v\nwant %#v", got, want)
	}
}
```

- [ ] **Step 3: 运行测试确认失败**

Run: `go test ./internal/zashboard/ -run Test -v`
Expected: 编译失败（`undefined: parseCustomClientlist` 等）。

- [ ] **Step 4: 实现 `internal/zashboard/labels.go`**

```go
// Package zashboard 在路由器本地生成 zashboard 的 source-ip-label-list 导入文件。
// 数据源与参考 Python 脚本 zashboard-iplabels.py 一致，但 daemon 跑在路由器上，
// 直接本地采集，无需 SSH。
package zashboard

import (
	"math/big"
	"net"
	"sort"
	"strings"

	"github.com/google/uuid"
)

// namespace 与 Python uuid5 的命名空间常量逐字节一致，保证同一 key 得到相同 id。
var namespace = uuid.MustParse("6f1d4c2a-9b3e-5a7c-8d6f-2e4a1b0c9d8e")

// Entry 是导出给 zashboard 的单条 IP → 标签映射。
type Entry struct {
	Key   string `json:"key"`
	Label string `json:"label"`
	ID    string `json:"id"`
}

// RawData 是 4 个本地数据源的原始文本（由 Collect 填充）。
type RawData struct {
	Clients string // nvram get custom_clientlist
	ARP     string // /proc/net/arp
	Leases  string // /var/lib/misc/dnsmasq.leases
	Neigh   string // ip -6 neigh show
}

func normMAC(mac string) string { return strings.ToUpper(strings.TrimSpace(mac)) }

func makeID(key string) string { return uuid.NewSHA1(namespace, []byte(key)).String() }

// parseCustomClientlist: 条目以 < 分隔、字段以 > 分隔，[0]=名 [1]=MAC。
func parseCustomClientlist(text string) map[string]string {
	result := map[string]string{}
	for _, entry := range strings.Split(text, "<") {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		fields := strings.Split(entry, ">")
		if len(fields) < 2 {
			continue
		}
		name, mac := strings.TrimSpace(fields[0]), normMAC(fields[1])
		if name != "" && mac != "" {
			result[mac] = name
		}
	}
	return result
}

// parseARP: 列 IP HWtype Flags HWaddress Mask Device；flag 0x0 / 全零 MAC / 表头跳过。
func parseARP(text string) map[string]string {
	result := map[string]string{}
	for _, line := range strings.Split(text, "\n") {
		cols := strings.Fields(line)
		if len(cols) < 4 || cols[0] == "IP" {
			continue
		}
		ip, flags, mac := cols[0], cols[2], normMAC(cols[3])
		if flags == "0x0" || mac == "00:00:00:00:00:00" {
			continue
		}
		if _, ok := result[mac]; !ok {
			result[mac] = ip
		}
	}
	return result
}

// parseLeases: 列 expiry MAC IP hostname clientid；setdefault。
func parseLeases(text string) map[string]string {
	result := map[string]string{}
	for _, line := range strings.Split(text, "\n") {
		cols := strings.Fields(line)
		if len(cols) < 3 {
			continue
		}
		mac, ip := normMAC(cols[1]), cols[2]
		if _, ok := result[mac]; !ok {
			result[mac] = ip
		}
	}
	return result
}

// parseIPv6Neigh: 列含 lladdr，仅取全局单播地址（跳过 fe80 链路本地、ULA 等），去重。
func parseIPv6Neigh(text string) map[string][]string {
	result := map[string][]string{}
	for _, line := range strings.Split(text, "\n") {
		cols := strings.Fields(line)
		idx := indexOf(cols, "lladdr")
		if len(cols) < 5 || idx < 0 || idx+1 >= len(cols) {
			continue
		}
		addr := cols[0]
		ip := net.ParseIP(addr)
		if ip == nil || ip.To4() != nil || !ip.IsGlobalUnicast() || isULA(ip) {
			continue
		}
		mac := normMAC(cols[idx+1])
		if !contains(result[mac], addr) {
			result[mac] = append(result[mac], addr)
		}
	}
	return result
}

// isULA 判断 fc00::/7（Go 的 IsGlobalUnicast 对 ULA 返回 true，需额外排除）。
func isULA(ip net.IP) bool {
	v6 := ip.To16()
	return v6 != nil && ip.To4() == nil && (v6[0]&0xfe) == 0xfc
}

func indexOf(s []string, v string) int {
	for i, x := range s {
		if x == v {
			return i
		}
	}
	return -1
}

func contains(s []string, v string) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}
	return false
}

// BuildEntries 合并 4 源 + 静态表 → 排序后的 entries。路由器数据优先，静态表补缺失 key。
func BuildEntries(raw RawData, static map[string]string) []Entry {
	names := parseCustomClientlist(raw.Clients)

	macIPv4 := map[string]string{}
	for k, v := range parseLeases(raw.Leases) { // 先 leases
		macIPv4[k] = v
	}
	for k, v := range parseARP(raw.ARP) { // ARP 优先覆盖
		macIPv4[k] = v
	}
	macIPv6 := parseIPv6Neigh(raw.Neigh)

	labels := map[string]string{}
	for mac, name := range names {
		var keys []string
		if ip, ok := macIPv4[mac]; ok {
			keys = append(keys, ip)
		}
		keys = append(keys, macIPv6[mac]...)
		for _, key := range keys {
			labels[key] = name
		}
	}
	for key, label := range static { // 静态表只补路由器缺失的 key
		if _, ok := labels[key]; !ok {
			labels[key] = label
		}
	}

	keys := make([]string, 0, len(labels))
	for k := range labels {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool { return lessKey(keys[i], keys[j]) })

	entries := make([]Entry, 0, len(keys))
	for _, k := range keys {
		entries = append(entries, Entry{Key: k, Label: labels[k], ID: makeID(k)})
	}
	return entries
}

// lessKey: IPv4(0) < IPv6(1) < 其它(2)；同类内 IP 按数值、其它按字符串。
func lessKey(a, b string) bool {
	ca, va := keyRank(a)
	cb, vb := keyRank(b)
	if ca != cb {
		return ca < cb
	}
	if ca == 2 {
		return a < b
	}
	return va.Cmp(vb) < 0
}

func keyRank(key string) (int, *big.Int) {
	ip := net.ParseIP(key)
	if ip == nil {
		return 2, nil
	}
	if v4 := ip.To4(); v4 != nil {
		return 0, new(big.Int).SetBytes(v4)
	}
	return 1, new(big.Int).SetBytes(ip.To16())
}
```

- [ ] **Step 5: 运行测试确认通过**

Run: `go test ./internal/zashboard/ -v && go vet ./internal/zashboard/`
Expected: 全部 PASS，vet 无输出。

- [ ] **Step 6: 提交**

```bash
git add internal/zashboard/labels.go internal/zashboard/labels_test.go go.mod go.sum
git commit -m "feat(zashboard): iplabels 解析/合并/uuid5 纯函数

Claude-Session: https://claude.ai/code/session_0146k61NbyfqCBh91UDZEXL1"
```

---

### Task 2: 本地采集 + 渲染 + 原子写（Generate）

**Files:**
- Create: `internal/zashboard/collect.go`
- Create: `internal/zashboard/generate.go`
- Create: `internal/zashboard/generate_test.go`

**Interfaces:**
- Consumes: `BuildEntries`, `Entry`, `RawData`（Task 1）
- Produces:
  - `func Collect(ctx context.Context) (RawData, []string)`
  - `type Result struct { Skipped bool; Changed bool; Count int; Warnings []string }`
  - `func Generate(ctx context.Context, uiDir string, static map[string]string) (Result, error)`
  - `func renderPayload(entries []Entry) ([]byte, error)`（包内）
  - `func writeIfChanged(path string, content []byte) (bool, error)`（包内）

- [ ] **Step 1: 写失败测试 `internal/zashboard/generate_test.go`**

```go
package zashboard

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRenderPayloadFormat(t *testing.T) {
	entries := []Entry{{Key: "127.0.0.1", Label: "💻本机", ID: "id-1"}}
	got, err := renderPayload(entries)
	if err != nil {
		t.Fatal(err)
	}
	s := string(got)
	// 外层 2 空格缩进 + 单一键
	if !strings.Contains(s, "{\n  \"config/source-ip-label-list\": ") {
		t.Fatalf("outer indent/key wrong:\n%s", s)
	}
	// emoji 原样 UTF-8（不转义为 \uXXXX）
	if !strings.Contains(s, "💻本机") {
		t.Fatalf("emoji escaped:\n%s", s)
	}
	// 内层值是紧凑 JSON 字符串（无空格分隔）
	if !strings.Contains(s, `[{\"key\":\"127.0.0.1\",\"label\":\"💻本机\",\"id\":\"id-1\"}]`) {
		t.Fatalf("inner not compact:\n%s", s)
	}
	// 可被解析回来：外层 map 取值再解析内层数组
	var outer map[string]string
	if err := json.Unmarshal(got, &outer); err != nil {
		t.Fatalf("outer parse: %v", err)
	}
	var back []Entry
	if err := json.Unmarshal([]byte(outer["config/source-ip-label-list"]), &back); err != nil {
		t.Fatalf("inner parse: %v", err)
	}
	if len(back) != 1 || back[0].Key != "127.0.0.1" {
		t.Fatalf("roundtrip mismatch: %#v", back)
	}
}

func TestWriteIfChangedGate(t *testing.T) {
	path := filepath.Join(t.TempDir(), "zashboard.json")
	content := []byte("hello")

	changed, err := writeIfChanged(path, content)
	if err != nil || !changed {
		t.Fatalf("first write changed=%v err=%v", changed, err)
	}
	changed, err = writeIfChanged(path, content) // 内容相同 → 不重写
	if err != nil || changed {
		t.Fatalf("second write changed=%v err=%v (want false)", changed, err)
	}
	changed, err = writeIfChanged(path, []byte("world")) // 内容变化 → 重写
	if err != nil || !changed {
		t.Fatalf("third write changed=%v err=%v (want true)", changed, err)
	}
}

func TestGenerateSkipWhenUIDirAbsent(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "no-such-ui")
	res, err := Generate(context.Background(), missing, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Skipped {
		t.Fatalf("want Skipped, got %#v", res)
	}
}

func TestGenerateWritesStaticOnly(t *testing.T) {
	// mac 上 Collect 的命令/文件都缺失 → 仅静态表生效，端到端验证 Generate。
	ui := t.TempDir()
	static := map[string]string{"127.0.0.1": "💻本机"}
	res, err := Generate(context.Background(), ui, static)
	if err != nil {
		t.Fatal(err)
	}
	if res.Skipped || !res.Changed || res.Count != 1 {
		t.Fatalf("unexpected result %#v", res)
	}
	data, err := os.ReadFile(filepath.Join(ui, "zashboard.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "💻本机") {
		t.Fatalf("file missing label:\n%s", data)
	}
}
```

- [ ] **Step 2: 运行测试确认失败**

Run: `go test ./internal/zashboard/ -run "TestRenderPayload|TestWriteIfChanged|TestGenerate" -v`
Expected: 编译失败（`undefined: renderPayload` 等）。

- [ ] **Step 3: 实现 `internal/zashboard/collect.go`**

```go
package zashboard

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
)

// 数据源文件路径（ASUS Merlin / Koolshare 标准位置）。
const (
	arpPath    = "/proc/net/arp"
	leasesPath = "/var/lib/misc/dnsmasq.leases"
)

// Collect 本地采集 4 个数据源。任一失败 → 对应字段置空 + append warning，不返回 error。
func Collect(ctx context.Context) (RawData, []string) {
	var raw RawData
	var warns []string

	if out, err := runCmd(ctx, "nvram", "get", "custom_clientlist"); err != nil {
		warns = append(warns, fmt.Sprintf("nvram custom_clientlist: %v", err))
	} else {
		raw.Clients = out
	}

	if b, err := os.ReadFile(arpPath); err != nil {
		warns = append(warns, fmt.Sprintf("read %s: %v", arpPath, err))
	} else {
		raw.ARP = string(b)
	}

	if b, err := os.ReadFile(leasesPath); err != nil {
		warns = append(warns, fmt.Sprintf("read %s: %v", leasesPath, err))
	} else {
		raw.Leases = string(b)
	}

	if out, err := runCmd(ctx, "ip", "-6", "neigh", "show"); err != nil {
		warns = append(warns, fmt.Sprintf("ip -6 neigh: %v", err))
	} else {
		raw.Neigh = out
	}

	return raw, warns
}

func runCmd(ctx context.Context, name string, args ...string) (string, error) {
	var stdout bytes.Buffer
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Stdout = &stdout
	if err := cmd.Run(); err != nil {
		return "", err
	}
	return stdout.String(), nil
}
```

- [ ] **Step 4: 实现 `internal/zashboard/generate.go`**

```go
package zashboard

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"os"
	"path/filepath"
)

// configKey 是 zashboard web 导入时识别的配置键。
const configKey = "config/source-ip-label-list"

// Result 汇报一次生成的结果。
type Result struct {
	Skipped  bool     // ui_dir 不存在 → 跳过
	Changed  bool     // 内容相对旧文件有变化（触发写盘）
	Count    int      // entries 数量
	Warnings []string // 采集降级告警
}

// Generate 采集本地数据并把 zashboard.json 写入 uiDir。
// uiDir 不存在 → Skipped（不报错）。任一数据源缺失 → 降级 + warning。
func Generate(ctx context.Context, uiDir string, static map[string]string) (Result, error) {
	if fi, err := os.Stat(uiDir); err != nil || !fi.IsDir() {
		return Result{Skipped: true}, nil
	}
	raw, warns := Collect(ctx)
	entries := BuildEntries(raw, static)
	content, err := renderPayload(entries)
	if err != nil {
		return Result{Warnings: warns}, err
	}
	changed, err := writeIfChanged(filepath.Join(uiDir, "zashboard.json"), content)
	if err != nil {
		return Result{Warnings: warns}, err
	}
	return Result{Changed: changed, Count: len(entries), Warnings: warns}, nil
}

// renderPayload 产出与 Python 逐字节一致的字节：
// 内层 entries 紧凑 JSON 字符串、外层对象 2 空格缩进，均关闭 HTML 转义（保留 emoji 原样）。
func renderPayload(entries []Entry) ([]byte, error) {
	if entries == nil {
		entries = []Entry{}
	}
	inner, err := marshalNoEscape(entries, "")
	if err != nil {
		return nil, err
	}
	return marshalNoEscape(map[string]string{configKey: string(inner)}, "  ")
}

// marshalNoEscape: SetEscapeHTML(false) 保留 emoji/中文与 <>& 原样；indent 为空即紧凑。
// Encoder.Encode 会追加换行，去掉以匹配 json.Marshal 风格。
func marshalNoEscape(v any, indent string) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if indent != "" {
		enc.SetIndent("", indent)
	}
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return bytes.TrimRight(buf.Bytes(), "\n"), nil
}

// writeIfChanged: 内容 sha256 与现有文件相同则跳过；否则原子写（.tmp + rename）。
func writeIfChanged(path string, content []byte) (bool, error) {
	if old, err := os.ReadFile(path); err == nil {
		if sha256.Sum256(old) == sha256.Sum256(content) {
			return false, nil
		}
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, content, 0o644); err != nil {
		return false, err
	}
	if err := os.Rename(tmp, path); err != nil {
		return false, err
	}
	return true, nil
}
```

- [ ] **Step 5: 运行测试确认通过**

Run: `go test ./internal/zashboard/ -v && go vet ./internal/zashboard/`
Expected: 全部 PASS，vet 无输出。

- [ ] **Step 6: 提交**

```bash
git add internal/zashboard/collect.go internal/zashboard/generate.go internal/zashboard/generate_test.go
git commit -m "feat(zashboard): 本地采集 + 渲染 + 原子写 Generate

Claude-Session: https://claude.ai/code/session_0146k61NbyfqCBh91UDZEXL1"
```

---

### Task 3: 配置 `[zashboard]` 段 + 移除空壳 download_zashboard

**Files:**
- Modify: `internal/config/daemon_toml.go`（加 `ZashboardConfig` + 挂到 `DaemonConfig`；删 `InstallConfig.DownloadZashboard` 字段及默认值）
- Modify: `internal/install/seed.go:17-23`（删 `TemplateVars.DownloadZashboard`）
- Modify: `internal/cli/install.go:97`（删传 `DownloadZashboard` 行）
- Modify: `assets/daemon.toml.tmpl`（删 `download_zashboard` 行；加 `[zashboard.static_labels]` 默认表）
- Modify: `internal/config/daemon_toml_test.go:38-40`（删 fixture 中 `download_zashboard` 行；加 zashboard 解析断言）
- Modify: `internal/install/seed_test.go:18-24, 61-67`（删两处 `DownloadZashboard: false`）

**Interfaces:**
- Produces: `type ZashboardConfig struct { StaticLabels map[string]string \`toml:"static_labels"\` }`；`DaemonConfig.Zashboard ZashboardConfig \`toml:"zashboard"\``

- [ ] **Step 1: 写失败测试 — 在 `daemon_toml_test.go` 的 `sampleTOML` 删除 download_zashboard、追加 zashboard 段，并加断言**

把 fixture 里这三行（约 36-40 行）：
```toml
[install]
download_sing_box  = true
download_cn_list   = true
download_zashboard = false
auto_start         = false
```
改为：
```toml
[install]
download_sing_box  = true
download_cn_list   = true
auto_start         = false

[zashboard.static_labels]
"127.0.0.1" = "💻本机"
```
在 `TestLoadDaemonConfig` 函数体末尾（最后一个 `}` 之前）追加：
```go
	if got := cfg.Zashboard.StaticLabels["127.0.0.1"]; got != "💻本机" {
		t.Fatalf("Zashboard.StaticLabels[127.0.0.1]=%q want 💻本机", got)
	}
```

- [ ] **Step 2: 运行测试确认失败**

Run: `go test ./internal/config/ -run TestLoadDaemonConfig -v`
Expected: 编译失败（`cfg.Zashboard undefined`）。

- [ ] **Step 3: 改 `internal/config/daemon_toml.go`**

在 `DaemonConfig` 结构里 `Install InstallConfig` 那行后加一行：
```go
	Install    InstallConfig    `toml:"install"`
	Zashboard  ZashboardConfig  `toml:"zashboard"`
```
在 `InstallConfig` 结构定义后新增类型：
```go
// ZashboardConfig 控制 ui/zashboard.json（source-ip-label-list）生成。
// StaticLabels 为补充路由器客户端列表里没有的基础设施项；冲突时路由器数据优先。
type ZashboardConfig struct {
	StaticLabels map[string]string `toml:"static_labels"`
}
```
删除 `InstallConfig` 结构里的 `DownloadZashboard bool \`toml:"download_zashboard"\`` 字段；删除 `defaultConfig()` 中 `Install: InstallConfig{...}` 里的 `DownloadZashboard: false,` 行。

- [ ] **Step 4: 改 `internal/install/seed.go`**

删除 `TemplateVars` 结构里的 `DownloadZashboard bool` 字段。

- [ ] **Step 5: 改 `internal/cli/install.go`**

删除第 97 行 `DownloadZashboard: cfg.Install.DownloadZashboard,`。

- [ ] **Step 6: 改 `assets/daemon.toml.tmpl`**

删除 `download_zashboard  = {{ .DownloadZashboard }}` 行（注意文件里 `[install]` 段出现两次——`[router]` 注释块下方与文件尾部，均需删该行）。
在文件**末尾**追加新段：
```toml

[zashboard]
# ui/zashboard.json（zashboard web 可导入的 source-ip-label-list）生成所用的静态标签表。
# ui_dir 存在即每轮 sync 生成；路由器 custom_clientlist 数据优先，下表只补缺失的 key。
[zashboard.static_labels]
"127.0.0.1"         = "💻本机"
"11.0.0.1"          = "🌐TUN"
"fdfe:dcba:9876::1" = "🌐TUN"
"192.168.50.1"      = "🛜主路由器"
"192.168.50.2"      = "🛜副路由器"
"192.168.50.3"      = "🛜小路由器"
```

- [ ] **Step 7: 改 `internal/install/seed_test.go`**

删除两处 `vars := TemplateVars{...}` 中的 `DownloadZashboard: false,` 行（约 22、65 行）。

- [ ] **Step 8: 运行测试确认通过**

Run: `go test ./internal/config/ ./internal/install/ ./internal/cli/ ./assets/ && go vet ./...`
Expected: 全部 PASS，vet 无输出。（`go build ./...` 隐含通过；若有遗漏的 `DownloadZashboard` 引用会编译失败。）

- [ ] **Step 9: 提交**

```bash
git add internal/config/daemon_toml.go internal/config/daemon_toml_test.go internal/install/seed.go internal/install/seed_test.go internal/cli/install.go assets/daemon.toml.tmpl
git commit -m "feat(config,install): 新增 [zashboard] 段，移除空壳 download_zashboard

Claude-Session: https://claude.ai/code/session_0146k61NbyfqCBh91UDZEXL1"
```

---

### Task 4: sync_loop 集成（每轮无条件生成）

**Files:**
- Modify: `internal/daemon/sync_loop.go`（`SyncLoopConfig` 加字段；`runSyncOnce` 末尾调 Generate）
- Modify: `internal/cli/wireup_daemon.go:302-306`（填充新字段）
- Modify: `internal/daemon/sync_loop_test.go`（加生成被调用的测试）

**Interfaces:**
- Consumes: `zashboard.Generate`（Task 2）、`config.ZashboardConfig`（Task 3）
- Produces: `SyncLoopConfig` 新增 `ZashboardUIDir string`、`ZashboardStaticLabels map[string]string`

- [ ] **Step 1: 写失败测试 — 在 `internal/daemon/sync_loop_test.go` 追加**

```go
func TestRunSyncOnceGeneratesZashboard(t *testing.T) {
	ui := t.TempDir()
	em := newTestEmitter(t) // 复用本测试文件已有的 emitter 构造；若不存在则用 clef 新建
	cfg := SyncLoopConfig{
		ZashboardUIDir:        ui,
		ZashboardStaticLabels: map[string]string{"127.0.0.1": "💻本机"},
	}
	generateZashboard(context.Background(), em, cfg)

	if _, err := os.Stat(filepath.Join(ui, "zashboard.json")); err != nil {
		t.Fatalf("zashboard.json not generated: %v", err)
	}
}
```
> 注：本步骤把生成逻辑抽成包内可单测的 `generateZashboard(ctx, em, cfg)`，避免在测试里跑整段 `runSyncOnce`（它会真的拉网络资源）。测试需要的 import：`context`、`os`、`path/filepath`、`testing`。`newTestEmitter` 若该测试文件没有现成 helper，则改用 `clef` 直接构造一个丢弃输出的 emitter（参照文件内其它测试的 emitter 用法）。

- [ ] **Step 2: 运行测试确认失败**

Run: `go test ./internal/daemon/ -run TestRunSyncOnceGeneratesZashboard -v`
Expected: 编译失败（`undefined: generateZashboard` / `ZashboardUIDir`）。

- [ ] **Step 3: 改 `internal/daemon/sync_loop.go`**

在 `SyncLoopConfig` 结构追加字段：
```go
	AutoApply       bool // true:拉到新资源后自动 apply(zoo/sing-box → restart;cn.txt → ipset reload);false:仅 log
	// ZashboardUIDir 非空时,每轮 sync 末尾本地生成 <UIDir>/zashboard.json(独立步骤,不进 Applier)。
	ZashboardUIDir        string
	ZashboardStaticLabels map[string]string
```
在文件顶部 import 块加：
```go
	"github.com/moonfruit/sing-router/internal/zashboard"
```
在 `runSyncOnce` 函数体**末尾**（最后一个 `}` 之前、所有 return 路径之外）——为确保所有路径都执行生成，把生成放到函数最前面用 `defer`，或在每个 return 前调用。最简方案：在 `runSyncOnce` 第一行加 `defer generateZashboard(ctx, em, cfgForGen)`。但 `runSyncOnce` 当前签名只收 `autoApply bool`，不收 `cfg`。改 `runSyncOnce` 签名为接收整个 `cfg SyncLoopConfig`：

把 `StartSyncLoop` 里两处 `runSyncOnce(ctx, updater, em, applier, cfg.AutoApply)` 改为 `runSyncOnce(ctx, updater, em, applier, cfg)`，并把函数签名与内部 `autoApply` 引用改掉：
```go
func runSyncOnce(ctx context.Context, u *syncpkg.Updater, em *clef.Emitter, applier *Applier, cfg SyncLoopConfig) {
	defer generateZashboard(ctx, em, cfg)
	autoApply := cfg.AutoApply
	r := u.UpdateAll(ctx)
	// ...（其余函数体不变）
```
在文件末尾新增 `generateZashboard`：
```go
// generateZashboard 本地生成 ui/zashboard.json(source-ip-label-list)。
// 独立于资源 apply:不进 Applier、不触发 restart。ui_dir 不存在则静默跳过。
func generateZashboard(ctx context.Context, em *clef.Emitter, cfg SyncLoopConfig) {
	if cfg.ZashboardUIDir == "" {
		return
	}
	res, err := zashboard.Generate(ctx, cfg.ZashboardUIDir, cfg.ZashboardStaticLabels)
	for _, w := range res.Warnings {
		em.Debug("zashboard", "zashboard.collect.degraded", "{Warn}", map[string]any{"Warn": w})
	}
	switch {
	case err != nil:
		em.Warn("zashboard", "zashboard.generate.failed", "zashboard generate failed: {Err}",
			map[string]any{"Err": err.Error()})
	case res.Skipped:
		em.Debug("zashboard", "zashboard.generate.skipped", "ui_dir absent; zashboard generation skipped", nil)
	case res.Changed:
		em.Info("zashboard", "zashboard.generate.updated", "zashboard.json updated ({Count} entries)",
			map[string]any{"Count": res.Count})
	default:
		em.Debug("zashboard", "zashboard.generate.unchanged", "zashboard.json unchanged", nil)
	}
}
```

- [ ] **Step 4: 改 `internal/cli/wireup_daemon.go`**

把 `Sync: daemon.SyncLoopConfig{...}` 块（约 302-306 行）改为：
```go
		Sync: daemon.SyncLoopConfig{
			IntervalSec:           cfg.Sync.SyncIntervalSeconds(),
			OnStartDelaySec:       cfg.Sync.SyncOnStartDelaySec(),
			AutoApply:             cfg.Sync.SyncAutoApply(),
			ZashboardUIDir:        filepath.Join(rundir, cfg.Runtime.UIDir),
			ZashboardStaticLabels: cfg.Zashboard.StaticLabels,
		},
```
确认该文件已 import `path/filepath`（若未，则加入 import 块）。

- [ ] **Step 5: 运行测试确认通过**

Run: `go test ./internal/daemon/ ./internal/cli/ -v 2>&1 | tail -30 && go vet ./...`
Expected: 新测试 PASS，原有 sync_loop 测试不回归，vet 无输出。

- [ ] **Step 6: 提交**

```bash
git add internal/daemon/sync_loop.go internal/daemon/sync_loop_test.go internal/cli/wireup_daemon.go
git commit -m "feat(daemon,cli): sync 每轮本地生成 ui/zashboard.json

Claude-Session: https://claude.ai/code/session_0146k61NbyfqCBh91UDZEXL1"
```

---

### Task 5: CLI `update zashboard` / 纳入 `update all`

**Files:**
- Modify: `internal/cli/update.go`（新增 `zashboard` case；`all` 末尾跑生成；token 检查排除 zashboard）
- Modify: `internal/cli/update_test.go`（若存在则加 case；否则新建轻量测试）

**Interfaces:**
- Consumes: `zashboard.Generate`（Task 2）、`config.ZashboardConfig` / `RuntimeConfig.UIDir`（Task 3）

- [ ] **Step 1: 写失败测试 — `internal/cli/update_test.go`**

先确认文件是否存在：`ls internal/cli/update_test.go`。
若不存在则新建：
```go
package cli

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/moonfruit/sing-router/internal/zashboard"
)

func TestRunZashboardUpdateWritesFile(t *testing.T) {
	ui := t.TempDir()
	res, err := zashboard.Generate(context.Background(), ui,
		map[string]string{"127.0.0.1": "💻本机"})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Changed || res.Count != 1 {
		t.Fatalf("unexpected %#v", res)
	}
	if _, err := os.Stat(filepath.Join(ui, "zashboard.json")); err != nil {
		t.Fatalf("not written: %v", err)
	}
}
```
> 注：CLI 命令逻辑较薄，此处只对生成路径做冒烟测试；命令本身的 wiring 由 `go build` + 手动 `update zashboard` 验证。

- [ ] **Step 2: 运行测试确认失败/通过基线**

Run: `go test ./internal/cli/ -run TestRunZashboardUpdate -v`
Expected: PASS（仅验证 Generate 可用；此步骤建立基线）。

- [ ] **Step 3: 改 `internal/cli/update.go` — token 检查排除 zashboard**

把 token 前置检查那行：
```go
			if cfg.Gitee.Token == "" && (target == "sing-box" || target == "zoo" || target == "all") {
```
保持不变即可（zashboard 不在其中，天然跳过 token 检查）。无需改动——确认此行逻辑后继续。

- [ ] **Step 4: 改 `internal/cli/update.go` — 新增 zashboard case 与 all 末尾生成**

在 `switch target {` 的 `case "zoo":` 之后、`case "all":` 之前插入：
```go
		case "zashboard":
			res, err := zashboard.Generate(ctx, filepath.Join(rundir, cfg.Runtime.UIDir), cfg.Zashboard.StaticLabels)
			if err != nil {
				return fmt.Errorf("generate zashboard: %w", err)
			}
			printZashboard(out, res)
			return nil
```
在 `case "all":` 块内、`anyChanged = ...` 那行之前插入对 zashboard 的生成（all 也跑一次，不参与 --apply）：
```go
			zres, zerr := zashboard.Generate(ctx, filepath.Join(rundir, cfg.Runtime.UIDir), cfg.Zashboard.StaticLabels)
			if zerr != nil {
				printItem(out, "zashboard", false, "", zerr)
			} else {
				printZashboard(out, zres)
			}
```
把 `default:` 的错误信息更新为包含 zashboard：
```go
			return fmt.Errorf("unknown target %q (want sing-box | cn | zoo | zashboard | all)", target)
```
更新命令 `Use` 与 `Short`：
```go
		Use:   "update [sing-box|cn|zoo|zashboard|all]",
		Short: "Pull latest sing-box / cn.txt / zoo.json from gitee; locally regenerate ui/zashboard.json",
```
在文件末尾（`printItem` 之后）新增打印 helper：
```go
func printZashboard(out interface {
	Write([]byte) (int, error)
}, res zashboard.Result,
) {
	for _, w := range res.Warnings {
		fmt.Fprintf(out, "⚠ %-10s  %s\n", "zashboard", w)
	}
	switch {
	case res.Skipped:
		fmt.Fprintf(out, "ℹ %-10s  skipped (ui_dir absent)\n", "zashboard")
	case res.Changed:
		fmt.Fprintf(out, "✓ %-10s  updated  (%d entries)\n", "zashboard", res.Count)
	default:
		fmt.Fprintf(out, "✓ %-10s  unchanged  (%d entries)\n", "zashboard", res.Count)
	}
}
```
在 import 块加入 `"github.com/moonfruit/sing-router/internal/zashboard"`（`fmt`、`path/filepath` 已在用）。

- [ ] **Step 5: 运行测试 + 构建确认通过**

Run: `go build ./... && go test ./internal/cli/ -v 2>&1 | tail -20 && go vet ./...`
Expected: 构建通过、测试 PASS、vet 无输出。

- [ ] **Step 6: 手动冒烟验证 CLI 形态**

Run: `go run ./cmd/sing-router update zashboard -D $(mktemp -d)`
Expected: 因临时 rundir 无 `ui/` 目录，输出 `ℹ zashboard   skipped (ui_dir absent)`，退出码 0。

- [ ] **Step 7: 提交**

```bash
git add internal/cli/update.go internal/cli/update_test.go
git commit -m "feat(cli): update zashboard 本地生成 + 纳入 update all

Claude-Session: https://claude.ai/code/session_0146k61NbyfqCBh91UDZEXL1"
```

---

### Task 6: 全量回归 + 文档收尾

**Files:**
- Modify: `CLAUDE.md`（在「运行时拓扑」SyncLoop 行与「配置与凭证」补一句 zashboard 生成）

- [ ] **Step 1: 全量测试 + vet**

Run: `go test ./... && go vet ./...`
Expected: 全绿，vet 无输出。

- [ ] **Step 2: 交叉编译 ARM64 确认目标平台可构建**

Run: `make build-arm64`
Expected: 产出 `sing-router-linux-arm64`，无错误。

- [ ] **Step 3: 更新 `CLAUDE.md`**

在「运行时拓扑」段 `SyncLoop` 那行末尾补充：
```
       └─ SyncLoop (interval>0 时): updater.UpdateAll → applier.ApplyAll（auto_apply=true）；
          每轮末尾另调 zashboard.Generate 本地生成 ui/zashboard.json（独立步骤，不进 Applier）
```
在「配置与凭证」段末尾新增一段：
```
`[zashboard].static_labels` 控制 `ui/zashboard.json`（zashboard web 可导入的 source-ip-label-list）生成。
daemon 本地采集 nvram custom_clientlist + /proc/net/arp + dnsmasq.leases + ip -6 neigh，与 [[zashboard.static_labels]]
合并（路由器数据优先）。ui_dir 存在即每轮 sync 生成；CLI `update zashboard` / `update all` 手动触发。无独立 enable flag。
```
（注：上面 `[[zashboard.static_labels]]` 仅为强调，实际为单表 `[zashboard.static_labels]`。）

- [ ] **Step 4: 提交**

```bash
git add CLAUDE.md
git commit -m "docs(claude): 记录 zashboard 本地生成集成点

Claude-Session: https://claude.ai/code/session_0146k61NbyfqCBh91UDZEXL1"
```

---

## Self-Review

**Spec coverage:**
- 4 数据源采集 → Task 2 `collect.go`。✓
- 纯解析 + 合并优先级 + 排序 + uuid5 → Task 1。✓
- 输出格式逐字节匹配 Python → Task 2 `renderPayload` + `TestRenderPayloadFormat`。✓
- `[zashboard.static_labels]` 配置 → Task 3。✓
- 移除空壳 download_zashboard → Task 3。✓
- sync 每轮无条件生成（独立步骤、不 restart）→ Task 4。✓
- CLI `update zashboard` + `update all` + 跳过 token → Task 5。✓
- ui_dir 缺失跳过、源缺失降级 → Task 2（`Generate` stat 短路 + `Collect` warnings）。✓
- 测试门 / ARM64 构建 / 文档 → Task 6。✓

**Placeholder scan:** 无 TBD/TODO；每个代码步骤都给了完整代码。Task 4 Step 1 的 `newTestEmitter` 标注了 fallback（依现有测试文件 helper 或直接用 clef），属可执行指引而非占位。

**Type consistency:** `Entry{Key,Label,ID}`、`RawData{Clients,ARP,Leases,Neigh}`、`Result{Skipped,Changed,Count,Warnings}`、`Generate(ctx,uiDir,static)`、`BuildEntries(raw,static)`、`makeID(key)`、`renderPayload`、`writeIfChanged`、`generateZashboard(ctx,em,cfg)`、`ZashboardConfig.StaticLabels`、`SyncLoopConfig.ZashboardUIDir/ZashboardStaticLabels` 在各 Task 间签名一致。✓
