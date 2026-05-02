package log

import (
	"fmt"
	"strings"
)

// Level 是 sing-router 内部日志级别；与 sing-box 同源。
type Level int

const (
	LevelTrace Level = iota
	LevelDebug
	LevelInfo
	LevelWarn
	LevelError
	LevelFatal
)

// CLEFName 返回 Serilog (Seq) 兼容的级别名称。
func (l Level) CLEFName() string {
	switch l {
	case LevelTrace:
		return "Verbose"
	case LevelDebug:
		return "Debug"
	case LevelInfo:
		return "Information"
	case LevelWarn:
		return "Warning"
	case LevelError:
		return "Error"
	case LevelFatal:
		return "Fatal"
	default:
		return "Information"
	}
}

// String 返回简短大写形式（与 sing-box 一致），用于 pretty 输出。
func (l Level) String() string {
	switch l {
	case LevelTrace:
		return "TRACE"
	case LevelDebug:
		return "DEBUG"
	case LevelInfo:
		return "INFO"
	case LevelWarn:
		return "WARN"
	case LevelError:
		return "ERROR"
	case LevelFatal:
		return "FATAL"
	default:
		return "INFO"
	}
}

// ParseLevel 解析 daemon.toml [log].level 字符串。
func ParseLevel(s string) (Level, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "trace":
		return LevelTrace, nil
	case "debug":
		return LevelDebug, nil
	case "info", "":
		return LevelInfo, nil
	case "warn", "warning":
		return LevelWarn, nil
	case "error":
		return LevelError, nil
	case "fatal", "panic":
		return LevelFatal, nil
	default:
		return LevelInfo, fmt.Errorf("unknown log level %q", s)
	}
}

// FromCLEFName 把 Seq 级别字符串还原为 Level（用于 pretty 渲染）。
func FromCLEFName(s string) Level {
	switch s {
	case "Verbose":
		return LevelTrace
	case "Debug":
		return LevelDebug
	case "Information":
		return LevelInfo
	case "Warning":
		return LevelWarn
	case "Error":
		return LevelError
	case "Fatal":
		return LevelFatal
	default:
		return LevelInfo
	}
}
