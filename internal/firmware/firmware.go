// Package firmware 封装 sing-router 在不同路由器固件下的 install / uninstall /
// doctor 三类操作差异。daemon/supervisor 等运行时不依赖此包。
package firmware

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/moonfruit/sing-router/assets"
)

// Kind 是已知的固件目标。新增目标只在此处加。
type Kind string

const (
	KindKoolshare Kind = "koolshare"
	KindMerlin    Kind = "merlin"
)

// allKinds enumerates known firmware targets. Keep in sync with the const block above.
// Used for human-readable error messages in ByName.
var allKinds = []Kind{KindKoolshare, KindMerlin}

// HookCheck 是 doctor 用的只读体检结果项。
type HookCheck struct {
	Type     string // "file" | "nvram" — what kind of medium this check inspects
	Path     string // 文件路径或 nvram 键名
	Required bool
	Present  bool
	Note     string
}

// Target 封装一个固件目标的全部"安装侧"能力。
// 不涉及 daemon/supervisor 的运行时行为——那部分对所有目标统一。
//
// binary 是 sing-router 二进制在路由器上的绝对路径，渲染进 nat-start hook
// 模板的 {{.Binary}}。固件触发 nat-start 时 PATH 不含 /opt/sbin，hook 不能
// 依赖 $PATH 查找 sing-router；调用方必须传绝对路径，默认 /opt/sbin/sing-router。
type Target interface {
	Kind() Kind
	InstallHooks(rundir, binary string) error
	RemoveHooks() error
	VerifyHooks() []HookCheck
}

// ErrUnknown 由 Detect 返回，表示当前环境无法被强证为任何已知固件。
var ErrUnknown = errors.New("firmware: cannot determine target")

// New constructs a Target with default base "/" and embedded assets.
func New(k Kind) Target {
	switch k {
	case KindKoolshare:
		return &koolshare{base: "/", assets: assets.FS()}
	case KindMerlin:
		return &merlin{base: "/", assets: assets.FS(), nvram: shellNvram{}}
	default:
		panic(fmt.Sprintf("firmware.New: unsupported Kind %q (use ByName for user input)", k))
	}
}

// ByName parses a user-facing string into a Target. Empty / unknown strings error.
func ByName(s string) (Target, error) {
	switch Kind(s) {
	case KindKoolshare, KindMerlin:
		return New(Kind(s)), nil
	default:
		names := make([]string, len(allKinds))
		for i, k := range allKinds {
			names[i] = string(k)
		}
		return nil, fmt.Errorf("firmware: unknown kind %q (valid: %s)", s, strings.Join(names, ", "))
	}
}

// detectBase is the root used by Detect. Default "/"; tests override.
var detectBase = "/"

// Detect inspects the host environment for proof of koolshare. Returns
// ErrUnknown otherwise — does NOT speculatively return KindMerlin.
func Detect() (Kind, error) {
	link := filepath.Join(detectBase, "jffs/.asusrouter")
	if info, err := os.Lstat(link); err == nil && info.Mode()&os.ModeSymlink != 0 {
		if target, err := os.Readlink(link); err == nil && strings.Contains(target, "/koolshare/") {
			return KindKoolshare, nil
		}
	}
	kscore := filepath.Join(detectBase, "koolshare/bin/kscore.sh")
	if info, err := os.Stat(kscore); err == nil && !info.IsDir() && info.Mode()&0o111 != 0 {
		return KindKoolshare, nil
	}
	return "", ErrUnknown
}
