package config

import (
	"fmt"
	"net"
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
	// static_ips / static_macs 一路无校验地传给 startup.sh：`ipset -exist add
	// client_bypass_static "$ip"` 没有 `|| true`，脚本又是 set -eu——一个 typo
	// 就会在链创建之前掀掉整个 startup.sh，Supervisor.Startup 直接 Fatal，
	// 而用户只能在 stderr 里看到一行 "ipset v7.6: Syntax error"。
	// 本期只做 IPv4（.To4() != nil）：static_ips 是喂给 hash:ip 类型的 ipset，
	// 与 bypassRequest 校验（parseIPs）的 IPv6 限制保持一致。
	for i, raw := range b.StaticIPs {
		ip := net.ParseIP(strings.TrimSpace(raw))
		if ip == nil || ip.To4() == nil {
			return fmt.Errorf("[bypass].static_ips[%d] = %q is not a valid IPv4 address", i, raw)
		}
	}
	for i, raw := range b.StaticMACs {
		if _, err := net.ParseMAC(strings.TrimSpace(raw)); err != nil {
			return fmt.Errorf("[bypass].static_macs[%d] = %q is not a valid MAC address: %w", i, raw, err)
		}
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
