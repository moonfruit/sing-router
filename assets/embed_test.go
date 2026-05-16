package assets

import (
	"regexp"
	"strings"
	"testing"
)

func TestDefaultConfigsPresent(t *testing.T) {
	for _, p := range []string{
		"config.d.default/clash.json",
		"config.d.default/dns.json",
		"config.d.default/inbounds.json",
		"config.d.default/log.json",
		"config.d.default/cache.json",
		"config.d.default/certificate.json",
		"config.d.default/http.json",
		"config.d.default/outbounds.json",
		"config.d.default/zoo.json",
		"daemon.toml.tmpl",
		"initd/S99sing-router",
		"firmware/koolshare/N99sing-router.sh.tmpl",
		"firmware/merlin/nat-start.snippet",
		"firmware/merlin/services-start.snippet",
		"shell/startup.sh",
		"shell/teardown.sh",
		"shell/reapply-routes.sh",
		"shell/reload-cn-ipset.sh",
	} {
		if _, err := ReadFile(p); err != nil {
			t.Errorf("missing %s: %v", p, err)
		}
	}
}

func TestDNSFakeIPRangeFixed(t *testing.T) {
	data, err := ReadFile("config.d.default/dns.json")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"inet4_range": "28.0.0.0/8"`) {
		t.Fatal("default dns.json should have inet4_range fixed to 28.0.0.0/8")
	}
	if strings.Contains(string(data), `"22.0.0.0/8"`) {
		t.Fatal("default dns.json still references obsolete 22.0.0.0/8")
	}
}

func TestNatStartSnippetMarkers(t *testing.T) {
	data, _ := ReadFile("firmware/merlin/nat-start.snippet")
	s := string(data)
	if !strings.Contains(s, "# BEGIN sing-router") {
		t.Fatal("BEGIN marker missing")
	}
	if !strings.Contains(s, "# END sing-router") {
		t.Fatal("END marker missing")
	}
	if !strings.Contains(s, "{{.Binary}}") {
		t.Fatal("snippet should reference {{.Binary}} so install can bake the absolute path")
	}
	if !strings.Contains(s, "reapply-rules") {
		t.Fatal("snippet should call reapply-rules")
	}
	// nat-start 触发时 PATH 不含 /opt/sbin —— 不许出现 `which sing-router` 这类裸名 lookup。
	// 注释里提历史背景不算违规，与 TestEmbeddedShellScriptsNoCommandBuiltin 同套路。
	if hasNonCommentSubstring(s, "which sing-router") {
		t.Error("snippet must not rely on `which sing-router` PATH lookup; use {{.Binary}} absolute path")
	}
}

func TestKoolshareScriptShape(t *testing.T) {
	data, err := ReadFile("firmware/koolshare/N99sing-router.sh.tmpl")
	if err != nil {
		t.Fatal(err)
	}
	s := string(data)
	if !strings.HasPrefix(s, "#!/bin/sh") {
		t.Error("missing shebang")
	}
	if !strings.Contains(s, "{{.Binary}}") {
		t.Error("script must reference {{.Binary}} so install can bake the absolute path")
	}
	if !strings.Contains(s, "reapply-rules") {
		t.Error("must call reapply-rules")
	}
	if !strings.Contains(s, "start_nat") {
		t.Error("must handle start_nat action")
	}
	// Asus 触发 nat-start 时 PATH=/sbin:/usr/sbin:/bin:/usr/bin（不含 /opt/sbin），
	// 任何 `which sing-router` / 裸名调用都会跳过 hook —— 这是历史上 WAN 重拨后
	// iptables 补不回来的根因。Guard 改用 `[ -x "$BINARY" ]`。注释里出现历史描述不算违规。
	if hasNonCommentSubstring(s, "which sing-router") {
		t.Error("script must not rely on `which sing-router` PATH lookup; use $BINARY/{{.Binary}} absolute path")
	}
}

// hasNonCommentSubstring returns true if needle appears in any line of s
// that is not a shell comment (`#` 开头，允许前导空白）。与 TestEmbeddedShellScriptsNoCommandBuiltin
// 的注释豁免同套路。
func hasNonCommentSubstring(s, needle string) bool {
	for _, line := range strings.Split(s, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "#") {
			continue
		}
		if strings.Contains(line, needle) {
			return true
		}
	}
	return false
}

func TestStartupShellRequiresEnvVars(t *testing.T) {
	data, err := ReadFile("shell/startup.sh")
	if err != nil {
		t.Fatal(err)
	}
	s := string(data)
	for _, name := range []string{"DNS_PORT", "REDIRECT_PORT", "ROUTE_MARK", "BYPASS_MARK",
		"TUN", "ROUTE_TABLE", "PROXY_PORTS", "FAKEIP", "LAN"} {
		// 期望脚本通过 : "${NAME:?...}" 强制要求该变量
		needle := `: "${` + name + `:?`
		if !strings.Contains(s, needle) {
			t.Errorf("startup.sh should hard-require %s via : \"${%s:?...}\"", name, name)
		}
	}
}

func TestReapplyRoutesShellShape(t *testing.T) {
	data, err := ReadFile("shell/reapply-routes.sh")
	if err != nil {
		t.Fatal(err)
	}
	s := string(data)
	// 必须 hard-require 三个 env
	for _, name := range []string{"TUN", "ROUTE_TABLE", "ROUTE_MARK"} {
		needle := `: "${` + name + `:?`
		if !strings.Contains(s, needle) {
			t.Errorf("reapply-routes.sh should hard-require %s via : \"${%s:?...}\"", name, name)
		}
	}
	// 必须装两条与 TUN 设备相关的规则
	if !strings.Contains(s, `ip route replace default dev "$TUN" table "$ROUTE_TABLE"`) {
		t.Error("reapply-routes.sh must install default route via `ip route replace default dev $TUN table $ROUTE_TABLE`")
	}
	if !strings.Contains(s, `ip rule add fwmark "$ROUTE_MARK" table "$ROUTE_TABLE"`) {
		t.Error("reapply-routes.sh must add fwmark rule")
	}
	// 不许执行 iptables / ipset 命令（职责清晰：只管路由）。匹配命令调用形式，
	// 注释里出现这些词不算违规。
	for _, badCmd := range []string{"\niptables ", "\nip6tables ", "\nipset ",
		"\tiptables ", "\tip6tables ", "\tipset "} {
		if strings.Contains(s, badCmd) {
			t.Errorf("reapply-routes.sh must NOT invoke %q", strings.TrimSpace(badCmd))
		}
	}
}

// TestEmbeddedShellScriptsBusyboxSleep 守护：嵌入 shell 脚本不得使用小数秒 sleep。
// 路由器（Entware / firmware）的 sleep 多不支持 `sleep 0.2`，配合脚本里的 set -eu
// 会在第一处小数 sleep 直接中止 —— reapply-routes.sh 曾因此在 sing-box 重启后
// 无法补回 device-bound 默认路由（实机测试套件 D1 暴露）。
func TestEmbeddedShellScriptsBusyboxSleep(t *testing.T) {
	fracSleep := regexp.MustCompile(`\bsleep[[:space:]]+[0-9]*\.[0-9]`)
	for _, p := range []string{
		"shell/startup.sh",
		"shell/teardown.sh",
		"shell/reapply-routes.sh",
		"shell/reload-cn-ipset.sh",
		"initd/S99sing-router",
		"firmware/koolshare/N99sing-router.sh.tmpl",
	} {
		data, err := ReadFile(p)
		if err != nil {
			t.Errorf("missing %s: %v", p, err)
			continue
		}
		for i, line := range strings.Split(string(data), "\n") {
			if strings.HasPrefix(strings.TrimSpace(line), "#") {
				continue // 注释里提到小数 sleep 不算违规
			}
			if fracSleep.MatchString(line) {
				t.Errorf("%s:%d uses fractional `sleep` (busybox sleep rejects it): %q",
					p, i+1, strings.TrimSpace(line))
			}
		}
	}
}

// TestEmbeddedShellScriptsNoCommandBuiltin 守护：嵌入 shell 脚本不得用 `command`
// builtin（含 `command -v`）。路由器固件的 BusyBox（实测 1.24.1）ash 可能没编进
// CONFIG_ASH_CMDCMD，`command -v` 会直接 "command: not found" —— 钩子里的
// `if ! command -v sing-router` guard 因此恒为真、永远跳过（实机测试套件 R3 暴露）。
// 改用 `which`。
func TestEmbeddedShellScriptsNoCommandBuiltin(t *testing.T) {
	cmdBuiltin := regexp.MustCompile(`(^|[;&|]|\bif |\b! )[[:space:]]*command[[:space:]]`)
	for _, p := range []string{
		"shell/startup.sh",
		"shell/teardown.sh",
		"shell/reapply-routes.sh",
		"shell/reload-cn-ipset.sh",
		"initd/S99sing-router",
		"firmware/koolshare/N99sing-router.sh.tmpl",
		"firmware/merlin/nat-start.snippet",
		"firmware/merlin/services-start.snippet",
	} {
		data, err := ReadFile(p)
		if err != nil {
			t.Errorf("missing %s: %v", p, err)
			continue
		}
		for i, line := range strings.Split(string(data), "\n") {
			if strings.HasPrefix(strings.TrimSpace(line), "#") {
				continue // 注释里提到 command 不算违规
			}
			if cmdBuiltin.MatchString(line) {
				t.Errorf("%s:%d uses `command` builtin (busybox 1.24.1 ash lacks it; use `which`): %q",
					p, i+1, strings.TrimSpace(line))
			}
		}
	}
}
