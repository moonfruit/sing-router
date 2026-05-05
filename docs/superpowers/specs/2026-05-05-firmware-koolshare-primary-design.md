# 切换为 koolshare 官改优先 — 设计文档（Module A delta）

- 日期：2026-05-05
- 范围：Module A 的增量变更——把"目标固件"从 Merlin 优先反转为 koolshare 优先
- 目标设备：Asus RT-BE88U（aarch64 / koolshare 官改 + Entware）
- 状态：待实施

---

## 0. 背景与动机

`sing-router` 原计划以 **梅林固件 + Entware** 为首要支持目标（详见 `2026-05-02-sing-router-module-a-design.md`）。实际部署时发现：基于较新官方固件构建的 Merlin 跑 sing-box 会崩溃（不明原因），而基于较旧官方固件构建的 **koolshare 官改** 则稳定。RT-BE88U 上唯一可工作的目标是 koolshare 官改。

本 spec 把目标固件优先级反转：

- **首要支持**：koolshare 官改 + Entware
- **后备支持**：梅林固件 + Entware（保留接口与代码骨架，仅做单元测试，不做真机或集成测试，不在文档中承诺一等支持）

程序主体（daemon、supervisor、config、log、shell、state 包）对固件无感知；差异只集中在"安装期 hook 写盘"这一层。本 spec 在 `internal/firmware/` 引入抽象边界，让"固件相关"圈在一处，未来加新目标只需新文件。

本 spec 是 Module A 的 **delta**，不另开模块。原 spec 仍是 Module A 的基础契约；本 spec 覆盖原 spec 中受影响的若干小节（见 §8）。

---

## 1. 抽象层（`internal/firmware/`）

新建包 `internal/firmware/`，对外只暴露固件相关的安装/卸载/体检三类操作。其它包不导入此包以外的固件细节。

```go
package firmware

// Kind 是已知的固件目标。新增目标只在此处加。
type Kind string

const (
    KindKoolshare Kind = "koolshare"
    KindMerlin    Kind = "merlin"
)

// HookCheck 是 doctor 用的只读体检结果项。
type HookCheck struct {
    Kind     string // "file" | "nvram"
    Path     string // 文件路径或 nvram 键名
    Required bool   // 缺失即体检失败
    Present  bool
    Note     string // 人读的简短解释
}

// Target 封装一个固件目标的全部"安装侧"能力。
// 不涉及 daemon/supervisor 的运行时行为——那部分对所有目标统一。
type Target interface {
    Kind() Kind

    // InstallHooks 写入该目标需要的 hook 文件 / 块。幂等。
    // rundir 用于在 hook 内引用 sing-router 二进制路径或 RUNDIR（按需）。
    InstallHooks(rundir string) error

    // RemoveHooks 拆除 InstallHooks 写入的内容。幂等；缺失视为已移除。
    RemoveHooks() error

    // VerifyHooks 返回 doctor 应展示的 hook 体检项。只读。
    VerifyHooks() []HookCheck
}

// Detect 根据当前主机环境推断固件目标。
// 仅用于"防止 koolshare 误装"——见 §2 决议规则。
// 不可判断时返回 ErrUnknown，调用方应要求显式 --firmware。
func Detect() (Kind, error)

// ByName 按字符串解析；未知名字返回错误。
func ByName(s string) (Target, error)

// New 按 Kind 构造 Target；调用方应优先用 ByName 处理用户输入。
func New(k Kind) Target

var ErrUnknown = errors.New("firmware: cannot determine target")
```

### 1.1 包内文件

```
internal/firmware/
  firmware.go        # Kind / HookCheck / Detect / ByName / New / 错误
  firmware_test.go
  koolshare.go       # 实现 Target；写 /koolshare/init.d/N99sing-router.sh
  koolshare_test.go
  merlin.go          # 实现 Target；委托给 internal/install.InjectHook / RemoveHook
  merlin_test.go     # 仅"是否调对了 InjectHook"级别；不在真机验证
  nvram.go           # nvramReader 接口 + 默认 shell-out 实现
  nvram_test.go
```

`internal/install/jffs_hooks.go` 的 `InjectHook` / `RemoveHook` 不删，但被降级为 `firmware/merlin.go` 的内部依赖；`internal/cli/*` 不再直接调用它。

### 1.2 路径注入（测试性）

每个 Target 实现持有一个 `base` 字段，默认为 `"/"`，测试时由 `t.TempDir()` 注入。所有绝对路径都通过 `filepath.Join(base, "...")` 构造。

```go
type koolshare struct {
    base   string         // 默认 "/" ；测试 t.TempDir()
    assets fs.FS          // embed.FS 注入；测试可替换
}

func newKoolshare(base string, a fs.FS) Target { return &koolshare{base: base, assets: a} }
```

包级 `New(k Kind)` 用默认 `"/"` + 内嵌 `assets`；测试用 `newKoolshare(t.TempDir(), fakeAssets)`。同样适用于 `merlin`。

---

## 2. 决议规则

### 2.1 优先级

固件目标的决议优先级（高到低）：

1. CLI 显式 `--firmware=koolshare|merlin`
2. `daemon.toml` 中 `[install].firmware = "..."`
3. `firmware.Detect()` 返回值
4. fallback：`koolshare`（但 install 命令在此情况下拒绝继续，要求显式 flag）

### 2.2 `Detect()` 实现

仅做"明显是 koolshare"判定，不反向推 Merlin（策略 C）：

```
信号 1（最强）：lstat /jffs/.asusrouter，是软链接，且目标包含 "/koolshare/"
信号 2（强）  ：stat /koolshare/bin/kscore.sh 存在且可执行
两者任一命中 → 返回 KindKoolshare, nil
两者都不命中 → 返回 "", ErrUnknown（不返回 KindMerlin）
```

不使用 `uname -a`、`nvram extendno` 等做信号——它们既不稳定也容易在容器/测试环境产生假阳性。

### 2.3 持久化

`install` 跑完后把决议结果写入 `daemon.toml [install].firmware = "koolshare"|"merlin"`。后续 `uninstall` / `doctor` / 任何需要知道固件的命令直接读 `daemon.toml`，不重新探测。

理由：install 之后路由器环境对 sing-router 来说就是"已知"的，不应再有歧义；用户若刷机切换固件，应当先 `uninstall` 再 `install`。

### 2.4 `install` 的"防误装"行为

```
若 --firmware 未给 且 daemon.toml 无值：
    Detect() 命中 KindKoolshare → 继续，目标=koolshare
    Detect() 返回 ErrUnknown    →
        stderr 输出：
          "Cannot detect firmware. If this is a Merlin router,
           run with --firmware=merlin (note: Merlin path is
           untested, expect manual fixup). If you believe this
           IS a koolshare router, run with --firmware=koolshare
           to override the check."
        exit 2
```

策略 C 的副作用：若 koolshare 上 `/jffs/.asusrouter` 链接异常丢失（理论上不会发生），install 会拒绝并要求加 flag。这正是策略 C 的设计意图——宁可让用户加 flag，也不要在 koolshare 路径上误把 Merlin 风格的 BEGIN/END 块注入到 koolshare-managed 文件里。

---

## 3. koolshare hook 文件

### 3.1 唯一需要安装的文件

`/koolshare/init.d/N99sing-router.sh`

`assets/firmware/koolshare/N99sing-router.sh` 内容：

```sh
#!/bin/sh
# sing-router — koolshare nat-start hook (managed by `sing-router install`; do not edit)
# Invoked by /koolshare/bin/ks-nat-start.sh after NAT/firewall comes up.
# $1 is the action passed by /jffs/scripts/nat-start (currently always "start_nat").

ACTION=$1

# Guard: if /opt isn't mounted yet (early boot before entware), no-op silently.
command -v sing-router >/dev/null 2>&1 || exit 0

case "$ACTION" in
    start_nat|"" )
        sing-router reapply-rules >/dev/null
        ;;
esac
exit 0
```

### 3.2 设计要点

1. **N99 序号**：koolshare 的 `ks-nat-start.sh` 用 `sort -k1.20 -n` 排序——按文件名第 20 个字符起的数字排。`N99sing-router.sh` 第 20 个字符之后无数字，等同于 0；但 `N99` 前缀大于其它常见模块。当前 `/koolshare/init.d/` 内无任何 `N*` 脚本，N99 没有冲突。

2. **`command -v sing-router` 守卫**：第一次 boot 的 nat-start 在 entware 挂载之前触发；那时 sing-router 不在 PATH。守卫保证 hook 静默退出，不污染 koolshare syslog。

3. **同步执行（不加 `&`）**：`reapply-rules` 走 HTTP 调 daemon `/api/v1/reapply-rules`；该端点在 `state ≠ running` 时立即返回 409，在 `running` 时仅 exec teardown.sh + startup.sh，子秒级返回。同步执行换来 stderr 透传到 koolshare syslog 便于排错。

4. **stdout 丢弃，stderr 保留**：成功路径无噪声；失败路径 koolshare `_LOG` 已记录调用，sing-router 自身 stderr 进 syslog 提供失败信号。

5. **`case` 容错 `$ACTION` 为空**：理论上 koolshare 总是传 `start_nat`，但避免未来变更时整 hook 静默失效。

### 3.3 写盘 / 拆除动作

```go
func (k *koolshare) InstallHooks(rundir string) error {
    target := filepath.Join(k.base, "koolshare", "init.d", "N99sing-router.sh")
    script, err := fs.ReadFile(k.assets, "firmware/koolshare/N99sing-router.sh")
    if err != nil { return err }
    return atomicWriteExec(target, script, 0o755) // tmp + chmod + rename
}

func (k *koolshare) RemoveHooks() error {
    target := filepath.Join(k.base, "koolshare", "init.d", "N99sing-router.sh")
    if err := os.Remove(target); err != nil && !errors.Is(err, os.ErrNotExist) {
        return err
    }
    return nil
}
```

### 3.4 VerifyHooks() 返回

```go
func (k *koolshare) VerifyHooks() []HookCheck {
    target := filepath.Join(k.base, "koolshare", "init.d", "N99sing-router.sh")
    info, err := os.Stat(target)
    present := err == nil && !info.IsDir() && info.Mode()&0o111 != 0
    return []HookCheck{{
        Kind:     "file",
        Path:     target,
        Required: true,
        Present:  present,
        Note:     "koolshare nat-start hook (replays iptables on WAN/firewall restart)",
    }}
}
```

### 3.5 显式不做的事

- ✗ 不写 `V*sing-router.sh`（services-start，时序在 entware mount 之前，无意义）
- ✗ 不写 `T*sing-router.sh`（services-stop，时序在 entware unmount 之后，无意义）
- ✗ 不写 `S*sing-router.sh`（wan-start，与 nat-start 紧邻且 iptables 未必就绪）
- ✗ 不写 `M*` / `U*`（mount/unmount，与 sing-router 生命周期无关）
- ✗ 不动 `/jffs/scripts/*` 任何文件（koolshare 自管的 dispatcher，碰即错；会被 koolshare 升级覆盖）
- ✗ 不动 `/koolshare/init.d/M70entware.sh` 等已存在的 koolshare 自管文件

理由证据（来自真机 syslog 重建的 boot 序列）：

```
08:01:08  ks-services-start.sh  →  V02debug.sh         (V* 阶段)
08:01:13  ks-wan-start.sh       →  S01softok.sh start  (S* 阶段)
08:01:14  USB ext4 fs mounted on /tmp/mnt/entware
08:01:14  /jffs/scripts/post-mount → ks-mount-start.sh → M70entware.sh
          （此时 entware_config.sh 才 bind-mount /opt 并调用 rc.unslung start）
```

V/S/T/M/U hook 的触发时机均不在我们想要的"sing-router 已运行 + iptables 需重灌"窗口；只有 N（nat-start）符合需求。

---

## 4. Merlin 后备路径的边界

### 4.1 保留的部分

| 范围 | 内容 |
|---|---|
| `Target.Kind()` | 返回 `KindMerlin` |
| `Target.InstallHooks(rundir)` | 调 `internal/install.InjectHook` 注入 `/jffs/scripts/nat-start` 与 `/jffs/scripts/services-start` 两个 BEGIN/END 块（payload 同现有 snippet） |
| `Target.RemoveHooks()` | 调 `internal/install.RemoveHook` 拆两个块 |
| `Target.VerifyHooks()` | 见 §4.2 |
| `assets/firmware/merlin/{nat-start,services-start}.snippet` | 从 `assets/jffs/` 迁移，内容不变 |
| 单元测试 | 验证 InjectHook/RemoveHook 被以正确路径与 payload 调用；`internal/install/jffs_hooks.go` 自身 100% 覆盖率不丢 |

### 4.2 VerifyHooks() 返回

```go
[]HookCheck{
    {
        Kind:     "nvram",
        Path:     "jffs2_scripts",
        Required: true,
        Present:  nvramReader.Get("jffs2_scripts") == "1",
        Note:     "Merlin custom scripts must be enabled or hooks won't fire",
    },
    {
        Kind:     "file",
        Path:     filepath.Join(m.base, "jffs/scripts/nat-start"),
        Required: true,
        Present:  含我们的 BEGIN/END 块,
        Note:     "Merlin nat-start hook (replays iptables on WAN/firewall restart)",
    },
    {
        Kind:     "file",
        Path:     filepath.Join(m.base, "jffs/scripts/services-start"),
        Required: true,
        Present:  含我们的 BEGIN/END 块,
        Note:     "Merlin services-start hook (ensures init.d S99sing-router runs)",
    },
}
```

doctor 渲染层按 `Kind` 选不同前缀（`file:` / `nvram:`）。

### 4.3 `nvramReader` 接口

```go
// internal/firmware/nvram.go
type nvramReader interface {
    Get(key string) (string, error)
}

// 默认实现：shell out 到 `nvram get <key>`，trim 换行。
type shellNvram struct{}
func (shellNvram) Get(key string) (string, error) { ... }

// 测试实现：内存 map。
type fakeNvram map[string]string
func (f fakeNvram) Get(key string) (string, error) { return f[key], nil }
```

`merlin` 持有 `nvram nvramReader`；`New(KindMerlin)` 用 `shellNvram{}`，测试用 `fakeNvram{...}`。

### 4.4 明确不做的部分

- ✗ 不在真机上验证 Merlin 路径（无环境）
- ✗ 不写 Merlin 路径的集成测试（无环境）
- ✗ 不维护 Merlin 上的 `startup.sh` / `teardown.sh` 差异（假设两固件 shell 行为一致；事实上两者都是 Asus + Entware 栈，差异基本不存在）
- ✗ 不在 README/spec 中承诺"Merlin 受支持"——措辞为 "Merlin path is wired but untested, accepting community fixes"

### 4.5 `install --firmware=merlin` 的交互确认

```
$ sing-router install --firmware=merlin
WARNING: Merlin firmware support is best-effort and untested.
         The hook injection logic compiles and unit-tests pass, but no
         real Merlin device has validated this path. File issues if
         you hit problems.
Continue? [y/N]
```

`--yes` flag 跳过交互（用于脚本化场景）。`uninstall` / `doctor` 不警告——用户已经在跑了，警告无意义。

### 4.6 未来升级 Merlin 为一等支持

无代码骨架变动——抽象层一次到位：

1. 取得真机
2. 在 firmware/merlin_test.go 之外加 Merlin 集成测试
3. 去掉 `install --firmware=merlin` 的交互确认
4. README 改措辞

---

## 5. CLI 行为变化

### 5.1 `sing-router install` 新增 flag

```
--firmware string   "auto" | "koolshare" | "merlin"  默认 "auto"
--yes               跳过 Merlin 警告交互（仅 --firmware=merlin 用）
```

`--skip-jffs` 直接重命名为 `--skip-firmware-hooks`，不留 alias（项目目前无外部用户）。

### 5.2 `install` 执行顺序变更

与原 spec §8.1 对照，原步骤 6/7（注入 jffs hooks）替换为：

```
  1. 决议 $RUNDIR
  2. mkdir -p $RUNDIR/{config.d,bin,var,run,log}
  3. 落盘默认 daemon.toml（如不存在）
  4. 落盘默认 config.d/*.json
  5. 写 /opt/etc/init.d/S99sing-router
  6. 决议 firmware：CLI flag > daemon.toml > Detect() > 拒绝
       （全新装机：步骤 3 刚写入的默认 daemon.toml 中 firmware 为注释空值，
        故步骤 6 自然落到 Detect() 这一档；后续装机才会命中 daemon.toml 一档）
  7. firmware == merlin 时打警告 → 等 --yes 或交互确认
       （无论 firmware 是 CLI flag、daemon.toml 还是 Detect 决出的，
        只要值是 merlin 就警告——避免脚本化复装时静默走 Merlin 路径）
  8. firmware.Target.InstallHooks(rundir)
  9. 把决议结果写回 daemon.toml [install].firmware
 10. 按 daemon.toml [install] + flag 决定可选下载（sing-box / cn.txt）
 11. --start 或 [install].auto_start：调 init.d start
 12. 输出 next-steps 提示
```

`--skip-firmware-hooks` 跳过 step 8（步骤 9 仍写 toml；让用户后续可手动跑 `install` 补 hook 而不影响其它 step）。

### 5.3 `sing-router uninstall` 变更

```
  1. /opt/etc/init.d/S99sing-router stop（如服务在跑）
  2. 读 daemon.toml [install].firmware
       缺失或解析失败 → 默认按 koolshare（新装默认值）
  3. firmware.Target.RemoveHooks()
  4. 删 /opt/etc/init.d/S99sing-router（除非 --keep-init）
  5. --purge 时 rm -rf $RUNDIR
  6. 不删 /opt/sbin/sing-router 二进制；提示路径
```

`--skip-jffs` 同样改名为 `--skip-firmware-hooks`，作用是跳过 uninstall 流程的步骤 3（`RemoveHooks`）。其它步骤照旧执行——例如二进制 init.d 仍会被删（除非另加 `--keep-init`）。

### 5.4 `sing-router doctor` 变更

体检项变化（替换原"jffs hook 块"两条）：

```diff
- /jffs/scripts/nat-start 含我们的 BEGIN/END 块                yes
- /jffs/scripts/services-start 含我们的 BEGIN/END 块           yes
+ firmware target = <kind from daemon.toml>                  info
+ <foreach Target.VerifyHooks() check>                       yes/warn
```

doctor 渲染层按 `HookCheck.Kind` 选前缀：

```
[OK]   file: /koolshare/init.d/N99sing-router.sh
       (koolshare nat-start hook ...)
[FAIL] nvram: jffs2_scripts = "0"
       (Merlin custom scripts must be enabled or hooks won't fire)
```

### 5.5 `/api/v1/status` schema 增加字段

```diff
  {
    "daemon": {
      "version": "...",
      "pid": 1234,
      "uptime_seconds": 3725,
      "rundir": "/opt/home/sing-router",
      "state": "running",
      "last_error": null,
+     "firmware": "koolshare"
    },
    ...
  }
```

`firmware` 来自 `daemon.toml [install].firmware`；toml 缺失时返回 `"unknown"`。

CLI `status` 输出头部多打一行：`firmware: koolshare`。

### 5.6 `daemon.toml` 新字段

```toml
[install]
# 现有字段不变
download_sing_box   = true
download_cn_list    = true
download_zashboard  = false
auto_start          = false
# 新增：install 时由 sing-router 自动写入；用户通常不需手动改。
firmware            = "koolshare"   # "koolshare" | "merlin"
```

`assets/daemon.toml.default` 中此字段以注释形式存在（不写值），install 时根据决议结果覆盖。

---

## 6. `assets/` 目录重组

```diff
  assets/
    config.d.default/                    # 不变
    daemon.toml.default                  # 改：注释中提及 [install].firmware
    initd/
      S99sing-router                     # 不变（两固件共用）
    shell/
      startup.sh                         # 不变
      teardown.sh                        # 不变
-   jffs/
-     nat-start.snippet
-     services-start.snippet
+   firmware/
+     koolshare/
+       N99sing-router.sh                # 新；§3.1 内容
+     merlin/
+       nat-start.snippet                # 从 jffs/ 迁移
+       services-start.snippet           # 从 jffs/ 迁移
    embed.go                             # 改：embed 路径覆盖 firmware/**
    embed_test.go                        # 改：测试新路径可读
```

`assets/embed.go` 当前的 `//go:embed` 指令需要保证 `firmware/**` 被包含。现有指令应该已是通配；如不是则改写。

`internal/cli/install.go` 中现有的 `payloadOnly()` helper（从 snippet 抽 BEGIN/END 之间内容）下沉到 `internal/firmware/merlin.go` 的私有方法——它本来就只服务于 Merlin 路径。

旧路径 `assets/jffs/` 整目录删除，无兼容旧路径的 fallback。

---

## 7. 测试策略

### 7.1 单元测试

| 测试点 | 工具 | 覆盖目标 |
|---|---|---|
| `firmware.Detect()` 三种结果（链接命中 / 文件命中 / 都不命中） | 在 `t.TempDir()` 下伪造 `jffs/.asusrouter` 软链接和 `koolshare/bin/kscore.sh` | 100% |
| `firmware.ByName / New` 名字解析（含未知名字错误） | table-driven | 100% |
| `firmware.koolshare.InstallHooks` 写文件原子性 + 0755 权限 | base 注入 t.TempDir | 100% |
| `firmware.koolshare.RemoveHooks` 幂等（不存在时不报错） | 同上 | 100% |
| `firmware.koolshare.VerifyHooks` 三态（不存在 / 存在但不可执行 / 正常） | 同上 | 100% |
| `firmware.merlin.InstallHooks` 调用了正确的 InjectHook（路径 + payload） | 通过函数指针注入拦 InjectHook | 100% |
| `firmware.merlin.RemoveHooks` 同上 | 同上 | 100% |
| `firmware.merlin.VerifyHooks` 含 nvram + 两个文件检查 | nvramReader 注入 fake | 100% |
| `firmware.shellNvram.Get` 调 `nvram get` 命令 + trim | 用 `os/exec` 替身 | 100% |
| `internal/install/jffs_hooks.go` 现有 100% 覆盖 | 不变 | 维持 |

### 7.2 集成测试

保留现 spec §9 的 `fake-sing-box` + supervisor 路径，**不**新增固件维度：

- 默认 firmware 强制为 `koolshare`，构造伪 base（含 `koolshare/init.d/`）
- 跑通 install → supervisor boot → reapply-rules → uninstall 全流程
- 验证 `/koolshare/init.d/N99sing-router.sh` 在 install 后存在、uninstall 后不存在

### 7.3 不写的测试

- ✗ 真实 `nvram` 二进制调用（永远走接口）
- ✗ 真实 `/koolshare/` 路径（永远走 base 注入）
- ✗ Merlin 真机 / QEMU 路径
- ✗ `Detect()` 在容器里通过假装环境去试 koolshare/Merlin/Unknown 之外的任何固件

### 7.4 真机验收

仅 koolshare 一种环境，覆盖以下场景（手动；记录在实施计划的验收清单中）：

- 首次 install → reboot → `sing-router status` 在 sing-box ready 后显示 running
- 模拟 WAN 重启 → 观察 `/koolshare/init.d/N99sing-router.sh` 被触发 → iptables 链恢复
- `uninstall --purge` 后路由器干净（`/koolshare/init.d/N99sing-router.sh` 不存在）
- `doctor` 输出符合预期（含 `firmware: koolshare` 与 N99 hook 体检项）

Merlin 路径不做真机验收。

---

## 8. 对原 Module A spec 的覆盖关系

本 spec 覆盖 `2026-05-02-sing-router-module-a-design.md` 的以下小节；其余小节保持不变。

| 原 spec 小节 | 本 spec 处理 |
|---|---|
| §0 范围与定位 | 不变；目标设备表述更新为"koolshare 官改优先 + Merlin 后备" |
| §1.4 Go 包结构 | 增加 `internal/firmware/` 目录 |
| §2.1 固定位置 | 增加 `/koolshare/init.d/N99sing-router.sh`（仅 koolshare）；Merlin 行保留并标注"理论可运行不测" |
| §2.4 卸载策略 | 不变；具体动作由 firmware.Target.RemoveHooks 完成 |
| §5.4 status schema | 增加 `daemon.firmware` 字段（见 §5.5） |
| §7 daemon.toml | 增加 `[install].firmware` 字段（见 §5.6） |
| §8.1 install 流程 | 步骤 6/7 替换为决议 + InstallHooks + 回写 toml（见 §5.2） |
| §8.2 BEGIN/END 块格式 | 仅在 Merlin 路径生效；koolshare 路径不用 |
| §8.3 uninstall 流程 | 步骤 2 替换为读 toml + RemoveHooks（见 §5.3） |
| §8.4 doctor 体检 | jffs hook 两行替换为 firmware target + Target.VerifyHooks（见 §5.4） |
| §9 测试策略 | 增加 `internal/firmware/` 单元测试要求；集成测试不引入固件维度（见 §7） |
| §10 现存 bug 清单 | 不变 |
| §11 范围之外 | 增加："不维护 koolshare/Merlin 之外的固件目标" |
| §12 实施风险 | 增加：见本 spec §9 |
| §13 实施完成标准 | 增加：见本 spec §10 |

---

## 9. 实施风险与缓解

| 风险 | 缓解 |
|---|---|
| koolshare 升级覆盖 `/jffs/scripts/*` dispatcher 一行脚本 | 不影响——我们不碰这些文件；`/koolshare/init.d/N99sing-router.sh` 是独立文件，koolshare 升级不会动它 |
| 用户在 koolshare 上手动改了 `/jffs/scripts/nat-start`，破坏 dispatcher | 不在 sing-router 责任范围；doctor 仅检查我们写的 N99 脚本，不检查 koolshare 自管文件 |
| Merlin 真机第一次实测可能挖出 bug | 接受；merlin.go 接口稳定即可；未来真机修问题不需要改抽象 |
| N99 序号被未来 koolshare 模块抢占 | 不太可能（当前无 N\* 模块）；如发生，调整 N99 → N50 或 N20 即可，无接口变更 |
| `Detect()` 在 koolshare 异常环境（链接丢失）误判为 unknown | 用户加 `--firmware=koolshare` 显式覆盖；策略 C 设计意图就是"宁可让用户加 flag" |
| 用户从 koolshare 刷机到 Merlin（或反向）后 daemon.toml 中 firmware 错误 | 用户需先 `uninstall` 再 `install`；spec 在 install 输出与 README 中写明 |

---

## 10. 实施完成的标准（在原 spec §13 基础上增量）

- [ ] `go test ./internal/firmware/...` 全部通过；新包覆盖率 100%
- [ ] `assets/jffs/` 已删除；`assets/firmware/{koolshare,merlin}/` 就位；embed_test 验证两侧资源可读
- [ ] `internal/cli/install.go` 不再直接 import `internal/install.InjectHook` / `RemoveHook`——改走 `firmware.Target`
- [ ] 真机 RT-BE88U + koolshare 环境跑通：
  - [ ] `sing-router install`（无 flag）→ daemon.toml 中 `[install].firmware = "koolshare"`
  - [ ] `/koolshare/init.d/N99sing-router.sh` 写入；权限 0755
  - [ ] reboot 后 sing-box 经 entware rc.unslung 自动启动
  - [ ] 模拟 `service restart_firewall` → N99 hook 触发 → iptables 链恢复
  - [ ] `sing-router status` 包含 `firmware: koolshare`
  - [ ] `sing-router doctor` 输出含 N99 hook 体检项
  - [ ] `sing-router uninstall --purge` 后 N99 文件消失，`$RUNDIR` 删除
- [ ] `sing-router install --firmware=merlin --yes` 在伪 base（t.TempDir）下集成测试跑通；真机不验
- [ ] README / 原 spec 引用本 spec 的位置都加 link；原 spec 不内联重写

---

文档结束。
