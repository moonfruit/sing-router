package cli

import (
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/moonfruit/sing-router/internal/log"
)

// resolveColor 把 --color 取值（auto/always/never）+ stdout TTY 状态 + NO_COLOR 环境变量
// 解析为最终是否着色。NO_COLOR（任意非空值）等同于 --color=never，但 --color=always 显式压过。
func resolveColor(mode string, w io.Writer) (bool, error) {
	switch mode {
	case "always":
		return true, nil
	case "never":
		return false, nil
	case "", "auto":
		if os.Getenv("NO_COLOR") != "" {
			return false, nil
		}
		return isTerminal(w), nil
	default:
		return false, fmt.Errorf("--color must be auto|always|never, got %q", mode)
	}
}

// isTerminal 判定 w 是否为字符设备（终端）。不引入第三方依赖：os.File.Stat() + ModeCharDevice
// 在 darwin/linux 上都可用；非 *os.File（如测试里的 bytes.Buffer）一律视为非 TTY。
func isTerminal(w io.Writer) bool {
	f, ok := w.(*os.File)
	if !ok {
		return false
	}
	info, err := f.Stat()
	if err != nil {
		return false
	}
	return info.Mode()&os.ModeCharDevice != 0
}

// autoLogProfile 按环境变量推断终端调色板：
//
//	NO_COLOR 非空 → ProfileNone
//	COLORTERM in {truecolor, 24bit} → ProfileTrueColor
//	TERM 含 "256color" → Profile256
//	TERM 非空且非 "dumb" → Profile8
//	其他 → ProfileNone
func autoLogProfile() log.Profile {
	if os.Getenv("NO_COLOR") != "" {
		return log.ProfileNone
	}
	switch strings.ToLower(os.Getenv("COLORTERM")) {
	case "truecolor", "24bit":
		return log.ProfileTrueColor
	}
	term := os.Getenv("TERM")
	if strings.Contains(term, "256color") {
		return log.Profile256
	}
	if term != "" && term != "dumb" {
		return log.Profile8
	}
	return log.ProfileNone
}

// ResolveLogColor 把 --color (auto|always|never) 与 --color-profile (auto|truecolor|256|8|none)
// 加 daemon.toml 兜底配置串、env 推断综合为最终的 log.Profile：
//
//	--color=never                                       → ProfileNone
//	--color=always                                      → 用 profileFlag → 缺则 cfgProfile → 缺则 env → 仍缺则 Profile8
//	--color=auto + 非 TTY                                → ProfileNone
//	--color=auto + TTY                                  → 用 profileFlag → cfgProfile → env
//
// w 用于判定 TTY；空 string 在 cfgProfile 时视为 "auto"。
func ResolveLogColor(colorMode, profileFlag, cfgProfile string, w io.Writer) (log.Profile, error) {
	cm := strings.ToLower(strings.TrimSpace(colorMode))
	if cm == "" {
		cm = "auto"
	}

	switch cm {
	case "never":
		return log.ProfileNone, nil
	case "auto":
		if !isTerminal(w) || os.Getenv("NO_COLOR") != "" {
			return log.ProfileNone, nil
		}
	case "always":
		// fallthrough：profile 仍按 flag→cfg→env 顺序
	default:
		return log.ProfileNone, fmt.Errorf("--color must be auto|always|never, got %q", colorMode)
	}

	if pf := strings.TrimSpace(profileFlag); pf != "" {
		p, auto, err := log.ParseProfile(pf)
		if err != nil {
			return log.ProfileNone, fmt.Errorf("--color-profile: %w", err)
		}
		if !auto {
			return p, nil
		}
	}
	if cp := strings.TrimSpace(cfgProfile); cp != "" {
		p, auto, err := log.ParseProfile(cp)
		if err == nil && !auto {
			return p, nil
		}
	}
	if p := autoLogProfile(); p != log.ProfileNone {
		return p, nil
	}
	if cm == "always" {
		// --color=always 强制必上色：env 没探测到也兜底到 8 色。
		return log.Profile8, nil
	}
	return log.ProfileNone, nil
}
