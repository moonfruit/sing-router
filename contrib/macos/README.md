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
