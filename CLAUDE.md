# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## 项目概述

`sing-router` 是部署在华硕路由器 (Merlin / Koolshare + Entware) 上的 sing-box 透明代理"管理器"：负责 install / supervisor / 资源同步 / iptables 编排 / 健康体检，与 sing-box 二进制本身解耦。模块路径：`github.com/moonfruit/sing-router`，Go 1.26.2。所有静态资源（默认 config.d、init.d、firmware 钩子、shell 脚本、daemon.toml 模板、rule_set 内嵌兜底）通过 `//go:embed` 打进单一 ARM64 二进制，运行时无外部依赖。

依赖的 sibling 模块 `github.com/moonfruit/sing2seq`（位于 `/Users/moon/Workspace.localized/go/mod/sing2seq`，已在 `.claude/settings.local.json` 中作为 additionalDirectory 开放）提供 `clef` 包（事件总线 + Emitter，日志/事件管道）。

## 常用命令

- 本机构建：`make build` → `./sing-router`
- 交叉编译 ARM64（路由器目标平台）：`make build-arm64` → `./sing-router-linux-arm64`
- 上传到路由器（依赖 `upload.sh` + 内网可达）：`make upload`
- 全量测试：`make test` 或 `go test ./...`；覆盖率：`make cover`（产出 `coverage.out`）
- 单包测试：`go test ./internal/daemon/...`；单测：`go test ./internal/daemon -run TestSupervisor_Boot`
- 静态检查：`go vet ./...`
- 端到端集成测试（Docker，模拟 Entware + busybox ash）：`make docker-test`；保留容器排障：`KEEP=1 make docker-test`；强制 update 阶段：`WITH_UPDATE=1 make docker-test`
- 构造假 sing-box（供 docker-test 兜底）：`make fakebox`
- 刷新嵌入 cn.txt：`make update-cn`；刷新嵌入 rule_set 兜底：`make update-rule-sets`（需 sops 解密 `secrets/sing-router.env`）；两者一起：`make update-all`
- 路由器上排障（read-only）：`sing-router doctor`（叠加 `--skip-routing` / `--rules-only` / `--color=auto|always|never` / `--json`）。路由/iptables 检查默认就跑；非 Linux 平台、`--skip-routing` 或 `--rules-only`-反向场景才跳过。

`make build*` 注入 `internal/version.Version`：`0.1.0+<git-short-sha>`，可用 `VERSION=...` 覆盖。

## 顶层布局

```
cmd/sing-router/main.go      # 入口：panic-recover + cli.NewRootCmd().Execute()
internal/cli/                # cobra 子命令（status/start/stop/restart/check/reapply-rules/
                             #   shutdown/logs/script/daemon/install/uninstall/doctor/update/version）
internal/daemon/             # daemon 进程：supervisor、状态机、HTTP API、applier、sync_loop、ready check
internal/config/             # daemon.toml 解析、zoo 预处理、rule_set 兜底、sing-box check
internal/install/            # layout / daemon.toml seed / init.d 模板 / 固件钩子写入
internal/firmware/           # koolshare、merlin 两种固件的 install/uninstall/doctor 差异
internal/sync/               # gitee/cdn 拉取 sing-box / zoo / cn.txt 三类资源（共享给 CLI update 与 daemon 后台 loop）
internal/gitee/              # gitee 私仓 raw URL 拼接 + 下载客户端
internal/httpx/              # etag 增量下载、原子写
internal/log/                # 基于 clef 的日志栈：Bus + Emitter + Writer，支持 rotate + SSE 订阅
internal/shell/              # bash runner（startup.sh / teardown.sh / reload-cn-ipset.sh）
internal/state/, internal/version/

assets/                      # //go:embed 根；见下方
testdata/fake-sing-box/      # 假 sing-box（go build 出可执行，docker-test 在无 token 时使用）
tests/docker/                # 端到端集成测试镜像 + 驱动脚本
scripts/fetch-asset.sh       # etag 增量下载工具（被 Makefile 用，更新嵌入资源）
secrets/sing-router.env      # sops-encrypted；含 SING_ROUTER_GITEE_TOKEN
docs/superpowers/{specs,plans}/  # 设计稿与实施计划（按阶段 module-a / firmware-koolshare / sing2seq-refactor）
```

## 运行时拓扑（路由器侧）

```
/opt/etc/init.d/S99sing-router       (Entware init.d，由 install 写入)
  └─ sing-router daemon -D $RUNDIR    （默认 $RUNDIR = /opt/home/sing-router）
       ├─ writePID → run/sing-router.pid
       ├─ redirectStderr → log/stderr.log  (linux-only：dup3 fd 2，接住 Go runtime panic/fatal)
       ├─ EmitterStack: Bus + Emitter + Writer(log/sing-router.log, rotate+gzip)
       ├─ HTTP listener 127.0.0.1:9998  (/api/v1/{status,start,stop,restart,check,apply,
       │     reload-cn-ipset,reapply-rules,logs,script/,shutdown})
       ├─ Supervisor: sing-box 子进程 (bin/sing-box run -D $RUNDIR -C config.d)
       │     └─ Ready check：TCP dial 127.0.0.1:7890 (mixed-in) + clash API /version
       │        Ready 后跑 StartupHook (assets/shell/startup.sh) → state=running
       │        子进程崩溃 → 退避重启 (BackoffMs ladder)；退避 < IptablesKeepBackoffLtMs 保留 iptables
       └─ SyncLoop (interval>0 时): updater.UpdateAll → applier.ApplyPending（auto_apply=true）
```

固件钩子（install 触发，按 firmware 类型分支）：
- **koolshare**：`/koolshare/init.d/N99sing-router.sh` 监听 `start_nat` → 调 `sing-router reapply-rules`
- **merlin**：写 `nat-start` / `services-start` nvram 钩子（用 BEGIN/END marker 增量注入）

## 关键架构约束（容易踩的坑）

### sing-box 自连导致 CPU 100%（修复见 `c26da8a`, `03bf465`）
Ready check 只允许 dial **mixed-in** 端口（`readyCheckDialMixedPort = 7890`，与 `assets/config.d.default/inbounds.json` 必须一致）。`dns-in (1053)` 与 `redirect-in (7892)` 是 transparent inbound：本机 dial 会触发 SO_ORIGINAL_DST → 命中 `ip_is_private→DIRECT` → outbound 又 dial 回同一端口 → 无限自繁殖。`buildSupervisorWiring` 默认即此行为，daemon.toml 的 `[supervisor].ready_check_dial_inbounds` 控制开关。改 inbounds.json 时**同步改 `internal/cli/wireup_daemon.go` 里的常量**。

### Ready check 默认 60s 总超时
sing-box 冷启要做 cache-file 加载 + rule-set 下载 + router 启动，整体可达 30s+。默认 `TotalTimeout=60s` 是经过验证的下限，缩短前考虑这一点。

### init.d 把 stdout/stderr 重定向到 /dev/null
固件 init.d (`. /opt/etc/init.d/rc.func`) 的标准做法是 `$PROC $ARGS > /dev/null 2>&1 &`。Go runtime 自身的 `fatal error:` 走默认 fd 2 就被丢进黑洞——事故无痕。`internal/cli/stderr_redirect_linux.go` 用 `syscall.Dup3` 把 fd 2 重定向到 `log/stderr.log`，darwin 用 no-op stub（`stderr_redirect_other.go`）。**任何 goroutine 都必须自带 `defer recover()`+`reportPanic`**，否则一个未捕获 panic 会拆掉整个 daemon。

### Applier 的 sha256 真变化闸门
gitee 上游会在内容不变时给新 etag → naive 实现会反复 restart。`internal/daemon/applier.go` 对每个资源（sing-box / zoo.json / rule-set.json / cn.txt）都以最终落盘内容的 sha256 与 `var/apply-state.json` 中上次成功 apply 的快照比对，**完全相同则 no-op**。sync_loop 与 CLI `update --apply` 都走这条路径。

### zoo 预处理是"种子 → 用户配置 → 自动补 rule_set"链
1. install 时把 `assets/config.d.default/zoo.json` 当种子写入 `config.d/zoo.json`（仅 1 个 outbound）。
2. daemon 启动时若 `var/zoo.raw.json` 存在（由 sync 拉自 gitee `config.json`），`config.PreprocessZooFile` 按白名单提取 `outbounds` + `route.{rules,rule_set,final}` 写入 `config.d/zoo.json`；缺失即跳过（保留上次成功的 zoo.json）。
3. `config.EnsureRequiredRuleSets` 根据 `DefaultRequiredRuleSets` 检查所有引用的 rule_set tag，缺失的写入 `config.d/rule-set.json`：有 token → remote 条目（gitee raw URL，sing-box 自己 etag 拉取），无 token → local 条目指向 install 落到 `var/rules/` 的内嵌兜底。

### install 默认不下载资源（参见 memory）
`sing-router install` 首次安装默认 `download_sing_box=false` / `download_cn_list=false`，仅当 CLI 显式传 `--download-sing-box` 等 flag 时下载。Docker 集成测试无 token 时落到 fake-sing-box（`testdata/fake-sing-box`）。

### gitee token 三处来源
优先级：CLI `--gitee-token` ＞ 环境变量 `SING_ROUTER_GITEE_TOKEN` ＞ `daemon.toml [gitee].token`。`config.LoadDaemonConfig` 在解析后会读 env 覆盖；CLI `install` 把 `--gitee-token` 同时塞进 cfg 与渲染模板的 `TemplateVars.GiteeToken`。空 token 时后台 sync 禁用、`update sing-box/zoo/all` 报错，但 daemon 仍可启动（用嵌入 rule_set 兜底）。

### docker-test 的 ash 兼容性硬约束
集成测试镜像里 `/opt/bin/{sh,bash}` 软链到 busybox（PATH 优先），`/bin/sh` 仍是 debian dash。`#!/usr/bin/env bash` 与 PATH lookup 都会落到 busybox ash，匹配真机 Entware。**所有写入 `/opt/etc/init.d/` 与 `/koolshare/init.d/` 的脚本必须 busybox-ash 兼容**；assets/embed_test.go 用静态特征覆盖嵌入 shell 脚本，docker-test 跑 `busybox sh -n` 与真实 exec 来双重把关。

## 配置与凭证

`daemon.toml` 的模板见 `assets/daemon.toml.tmpl`，首次 install 渲染后由用户维护（后续 `install` 不会覆盖）。关键节：
- `[runtime]`：`sing_box_binary` / `config_dir` / `ui_dir` 都相对 `$RUNDIR`
- `[supervisor]`：ready check / backoff ladder / stop grace
- `[gitee]` + `[gitee.sing_box]` + `[gitee.zoo]`：私仓 token + ref + 文件路径
- `[sync]`：`interval_seconds=0` 即禁用后台 loop（保留 CLI `update`）；`on_start_delay_sec` 避开启动洪峰
- `[install]`：`download_*` / `auto_start` / `firmware` 等首次 install 的参数（之后只读，不再被 install 覆写）

`secrets/sing-router.env` 用 sops 加密，含 `SING_ROUTER_GITEE_TOKEN`；解密策略见 `.sops.yaml`。`tests/docker/docker-test.sh` 会尝试 sops 解密作为 token fallback。**绝不要把解密后的内容落盘或提交。**

## 测试与质量门

- `make test` 是默认门；新功能要带单测（包内同目录 `_test.go`）。
- 涉及固件 / iptables / supervisor 交互的改动跑 `make docker-test`；CI 与本地都用同一脚本。
- 修改 `assets/` 下任何脚本或默认 config 后，先跑 `go test ./assets/`——`embed_test.go` 用静态特征防住"看起来对实际不能跑"的回归（如 dns.json 的 fakeip range、startup.sh 的必填环境变量、merlin snippet 的 BEGIN/END marker、koolshare 脚本调 `reapply-rules`）。
- 改 `internal/cli/wireup_daemon.go` 里的 ready-check 端口常量或 inbounds.json 的 listen_port 时，要同步——`wireup_daemon_test.go` 会守住默认 wiring。

## Git / 提交

- 主分支：`main`；提交风格见近期 `git log`（中文 conventional 风格：`fix(daemon,cli): ...` / `feat(daemon,sync,cli): ...`），按受影响的子目录列 scope。
- 不要把 `sing-router-linux-arm64`、`sing-router.log` 提交（`.gitignore` 已忽略）。
- `secrets/` 内文件必须保持 sops 加密；commit 前确认未泄露明文。
