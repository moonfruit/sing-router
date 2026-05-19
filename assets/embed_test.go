package assets

import (
	"regexp"
	"strings"
	"testing"
)

func TestDefaultConfigsPresent(t *testing.T) {
	for _, p := range []string{
		"config.d/clash.json",
		"config.d/dns.json",
		"config.d/inbounds.json",
		"config.d/log.json",
		"config.d/cache.json",
		"config.d/certificate.json",
		"config.d/http.json",
		"config.d/outbounds.json",
		"config.d/zoo.json",
		"daemon.toml.tmpl",
		"initd/S99sing-router",
		"firmware/koolshare/N99sing-router.sh.tmpl",
		"firmware/merlin/nat-start.snippet",
		"firmware/merlin/services-start.snippet",
		"shell/startup.sh",
		"shell/teardown.sh",
	} {
		if _, err := ReadFile(p); err != nil {
			t.Errorf("missing %s: %v", p, err)
		}
	}
}

// TestDaemonTomlTemplateHasSeqSection 守护：daemon.toml.tmpl 必须包含 [seq]
// 段，默认 commented + 注明 enabled = false 默认值，避免首次安装意外发
// 远程日志。
func TestDaemonTomlTemplateHasSeqSection(t *testing.T) {
	data, err := ReadFile("daemon.toml.tmpl")
	if err != nil {
		t.Fatal(err)
	}
	s := string(data)
	if !strings.Contains(s, "[seq]") {
		t.Error("daemon.toml.tmpl missing [seq] section")
	}
	if !strings.Contains(s, "enabled  = false") && !strings.Contains(s, "enabled = false") {
		t.Error("daemon.toml.tmpl [seq] must default enabled to false (commented or otherwise)")
	}
	// Source 命名约定写在注释里——dashboard 维护者会读这一段。
	for _, src := range []string{"daemon", "sing-box", "sing2seq"} {
		if !strings.Contains(s, src) {
			t.Errorf("daemon.toml.tmpl [seq] should document Source value %q", src)
		}
	}
}

func TestDNSFakeIPRangeFixed(t *testing.T) {
	data, err := ReadFile("config.d/dns.json")
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
	if !strings.Contains(s, "restart") {
		t.Fatal("snippet should call `sing-router restart` (full Shutdown+Startup cycle)")
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
	if !strings.Contains(s, "restart") {
		t.Error("must call `sing-router restart` (full Shutdown+Startup cycle)")
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
		"TUN", "ROUTE_TABLE", "PROXY_PORTS", "FAKEIP", "LAN", "LAN_IFACE"} {
		// 期望脚本通过 : "${NAME:?...}" 强制要求该变量
		needle := `: "${` + name + `:?`
		if !strings.Contains(s, needle) {
			t.Errorf("startup.sh should hard-require %s via : \"${%s:?...}\"", name, name)
		}
	}
}

// TestEmbeddedShellScriptsBusyboxSleep 守护：嵌入 shell 脚本不得使用小数秒 sleep。
// 路由器（Entware / firmware）的 sleep 多不支持 `sleep 0.2`，配合脚本里的 set -eu
// 会在第一处小数 sleep 直接中止。
func TestEmbeddedShellScriptsBusyboxSleep(t *testing.T) {
	fracSleep := regexp.MustCompile(`\bsleep[[:space:]]+[0-9]*\.[0-9]`)
	for _, p := range []string{
		"shell/startup.sh",
		"shell/teardown.sh",
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
