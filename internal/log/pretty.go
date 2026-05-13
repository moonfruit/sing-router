package log

import (
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/moonfruit/sing2seq/clef"
)

// PrettyOptions 控制 Pretty 渲染。
type PrettyOptions struct {
	LocalTZ *time.Location // 与守护进程当前时区相同时省略 TZ 段
	Profile Profile        // 颜色 profile；ProfileNone（默认）时不上色
	Conn    *ConnColorizer // 可选；非 nil 时给 ConnectionId 着色（profile=none 自动 no-op）
}

var placeholderRe = regexp.MustCompile(`\{([A-Za-z_][A-Za-z0-9_]*)\}`)

// Pretty 把 CLEF 事件渲染为人类可读的一行（不含末尾换行）。
func Pretty(e *clef.Event, opts PrettyOptions) string {
	if opts.LocalTZ == nil {
		opts.LocalTZ = time.Local
	}

	var sb strings.Builder

	// 时区段
	tsStr, _ := getString(e, "@t")
	var ts time.Time
	if tsStr != "" {
		if parsed, err := time.Parse(time.RFC3339Nano, tsStr); err == nil {
			ts = parsed
		}
	}
	showTZ := false
	if !ts.IsZero() {
		wantOff := offsetSeconds(opts.LocalTZ, ts)
		gotOff := offsetSeconds(ts.Location(), ts)
		showTZ = wantOff != gotOff
	}
	if showTZ {
		sb.WriteString(formatOffset(ts))
		sb.WriteByte(' ')
	}

	// 日期与时间
	if !ts.IsZero() {
		sb.WriteString(ts.Format("2006-01-02 15:04:05.000"))
	} else {
		sb.WriteString("???? ??:??:??.???")
	}
	sb.WriteByte(' ')

	// 级别（带颜色）
	levelName, _ := getString(e, "@l")
	lvl := FromCLEFName(levelName)
	if pre := LevelColorPrefix(opts.Profile, lvl); pre != "" {
		sb.WriteString(pre)
		sb.WriteString(padRight(lvl.String(), 5))
		sb.WriteString(ansiReset)
	} else {
		sb.WriteString(padRight(lvl.String(), 5))
	}
	sb.WriteByte(' ')

	// Source 段
	src, _ := getString(e, "Source")
	if src != "" {
		sb.WriteByte('[')
		sb.WriteString(src)
		sb.WriteByte(']')
		sb.WriteByte(' ')
	}

	// 消息：把 @mt 模板里的 {Field} 替换为对应值
	mt, _ := getString(e, "@mt")
	if mt == "" {
		mod, _ := getString(e, "Module")
		det, _ := getString(e, "Detail")
		if mod != "" && det != "" {
			sb.WriteString(mod)
			sb.WriteString(": ")
			sb.WriteString(det)
		} else if det != "" {
			sb.WriteString(det)
		}
	} else {
		sb.WriteString(renderTemplate(e, mt, opts))
	}
	return sb.String()
}

func renderTemplate(e *clef.Event, tmpl string, opts PrettyOptions) string {
	return placeholderRe.ReplaceAllStringFunc(tmpl, func(match string) string {
		name := match[1 : len(match)-1]
		v, ok := e.Get(name)
		if !ok {
			return match
		}
		s := fmt.Sprintf("%v", v)
		if name == "ConnectionId" && opts.Conn != nil {
			return opts.Conn.Wrap(s)
		}
		return s
	})
}

func getString(e *clef.Event, key string) (string, bool) {
	v, ok := e.Get(key)
	if !ok {
		return "", false
	}
	s, ok := v.(string)
	return s, ok
}

func offsetSeconds(loc *time.Location, ref time.Time) int {
	_, off := ref.In(loc).Zone()
	return off
}

func formatOffset(t time.Time) string {
	_, off := t.Zone()
	sign := byte('+')
	if off < 0 {
		sign = '-'
		off = -off
	}
	h := off / 3600
	m := (off % 3600) / 60
	return fmt.Sprintf("%c%02d%02d", sign, h, m)
}

func padRight(s string, width int) string {
	if len(s) >= width {
		return s
	}
	return s + strings.Repeat(" ", width-len(s))
}
