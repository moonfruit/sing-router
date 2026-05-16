package install

import (
	"fmt"
	"os"
	"path/filepath"
)

// ResolveSelfBinary 返回当前进程二进制的绝对路径，用于把 sing-router
// 自身路径烧进 nat-start hook 模板的 {{.Binary}}。
//
// Asus/Merlin 固件触发 nat-start 时 PATH 不含 /opt/sbin，hook 必须用绝对
// 路径调用 sing-router；用 self-path 而非配置默认值，避免用户把二进制装
// 到非常规位置后忘传 --binary。
//
// 不解析 symlink：用户怎么调起 install（比如 /opt/sbin/sing-router）就把
// 那个路径烧进去，将来 /opt/sbin/sing-router 指向版本化二进制时升级透明。
func ResolveSelfBinary() (string, error) {
	bin, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("resolve self binary path: %w", err)
	}
	if !filepath.IsAbs(bin) {
		// os.Executable() 在 no-error 时承诺返回绝对路径，此分支防御性兜底。
		return "", fmt.Errorf("os.Executable returned non-absolute path %q", bin)
	}
	return bin, nil
}

// ValidateBinaryPath 校验用户通过 --binary 显式传入的路径必须是绝对路径。
// 相对路径在 nat-start 上下文（PATH=/sbin:/usr/sbin:/bin:/usr/bin）下找不到，
// 会让 hook 静默失效——这是上一轮 WAN 重拨后 iptables 补不回来的根因。
func ValidateBinaryPath(p string) error {
	if p == "" {
		return fmt.Errorf("--binary must not be empty")
	}
	if !filepath.IsAbs(p) {
		return fmt.Errorf("--binary must be an absolute path (got %q); nat-start hook context lacks /opt/sbin in PATH", p)
	}
	return nil
}
