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

## 为什么默认路径是 `/etc/sing-router/`

这个 agent 是以 root 跑的 LaunchDaemon，配置放 `/etc` 是 Unix 的标准位置，
且不依赖 homebrew 前缀——Intel 上是 `/usr/local`、Apple Silicon 上是
`/opt/homebrew`，写死任一个都会在另一种机器上出错；`/opt/etc` 更是两者都
不对，纯属路由器侧 Entware 的路径习惯，不应该照搬到 macOS 客户端上。

## 配置路径可覆盖

脚本读哪个配置文件，只由 plist 里 `EnvironmentVariables` → `BYPASS_AGENT_CONF`
决定，默认值只是给一个开箱即用的落点。如果你习惯把 sing-box 相关配置集中放
在自己的目录（比如某个 `.../env/etc/sing-box/`），改这一个变量、把
`ProgramArguments` 里的脚本路径也指过去即可，不必迁就 `/etc/sing-router/`。

## 部署

```bash
# 1. 路由器侧启用（会打印 token）
ssh router '/opt/sbin/sing-router install -D /opt/home/sing-router --enable-bypass'

# 2. 本机安装脚本与配置
sudo mkdir -p /etc/sing-router
sudo cp bypass-agent.sh /etc/sing-router/
sudo chmod 755 /etc/sing-router/bypass-agent.sh
sudo cp bypass-agent.conf.example /etc/sing-router/bypass-agent.conf
sudo chmod 600 /etc/sing-router/bypass-agent.conf
sudo vi /etc/sing-router/bypass-agent.conf   # 填入上一步打印的 token

# 3. 装 launchd job
sudo cp moonfruit.sing-bypass.plist /Library/LaunchDaemons/
sudo chown root:wheel /Library/LaunchDaemons/moonfruit.sing-bypass.plist
sudo launchctl load -w /Library/LaunchDaemons/moonfruit.sing-bypass.plist
```

## 日志轮转（可选，建议配置）

`StandardOutPath`/`StandardErrorPath` 指向 `/var/log/moonfruit.sing-bypass.log`。
launchd 本身不轮转这个文件——笔记本频繁换网环境时状态变化不少，不配轮转
会无上限增长。用 macOS 自带的 `newsyslog` 即可，新建
`/etc/newsyslog.d/moonfruit.sing-bypass.conf`：

```
# logfilename                           [owner:group]  mode  count  size(KB)  when  flags
/var/log/moonfruit.sing-bypass.log      root:wheel     644   7      1000      *     Z
```

含义：保留 7 份历史、单份超过 1000KB 就轮转、`Z` 表示轮转后 gzip 压缩、
`when` 用 `*` 表示只看 size 不看时间。`newsyslog` 由系统的周期性任务自动
触发，不需要额外安装或启用服务。

（`newsyslog.conf(5)` 里 `J` 对应的是 bzip2、`Z` 才是 gzip；这里选 `Z` 是因为
日志是纯文本，gzip 排查时 `zcat`/`zless` 随手可用，bzip2 压缩率优势对这种
体量的日志没有意义，解压反而更慢。）

## 验证

```bash
# 本机日志（只在状态变化时输出，稳态静默是正常的）
sudo tail -f /var/log/moonfruit.sing-bypass.log

# 路由器侧确认租约（GET 只允许 loopback，所以要在路由器上执行）
ssh router 'curl -s http://127.0.0.1:9998/api/v1/bypass'
ssh router 'ipset list client_bypass'
ssh router 'sing-router doctor'
```

## 排障

| 现象 | 原因 |
|---|---|
| 日志反复 `local sing-box not ready` | 本机 clash api 没起，检查 `LOCAL_CLASH_API` |
| 日志反复 `no route to <gw>`（偶发一两次即消失） | Wi-Fi 漫游瞬断 / DHCP 续租，正常抖动，不会撤销现有租约 |
| 日志持续 `no route to <gw>` | 不在目标 LAN 上（正常，比如在外面） |
| `renew failed ... http_code=000` | 路由器不可达：daemon 没跑，或 `[http].listen` 仍是 loopback |
| `renew failed ... 401` | `TOKEN` 与路由器 `[http].token` 不一致 |
| `renew failed ... 403` | 路由器 `[bypass].enabled` 为 false |
| `renew failed ... 400` | 请求体被服务端拒绝（IP 格式、数量超限等） |
