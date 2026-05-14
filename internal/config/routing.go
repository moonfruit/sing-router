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
		RouteTable:   7892,
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
