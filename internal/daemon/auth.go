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
