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
