package cli

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/moonfruit/sing2seq/clef"
)

// logsFilter 描述 logs 命令的过滤条件。零值通过所有事件。
type logsFilter struct {
	source   string // "" / "all" → 不过滤；否则与 Source 字段精确匹配
	minLevel clef.Level
	eventID  string // EventID 前缀过滤；空表示不过滤
}

// parseLogsFilter 解析 CLI flag 字符串到 logsFilter。
func parseLogsFilter(source, level, eventID string) (*logsFilter, error) {
	f := &logsFilter{
		source:  strings.TrimSpace(source),
		eventID: strings.TrimSpace(eventID),
	}
	if level = strings.TrimSpace(level); level != "" {
		lvl, err := clef.ParseLevel(level)
		if err != nil {
			return nil, fmt.Errorf("unknown level %q: %w", level, err)
		}
		f.minLevel = lvl
	} else {
		f.minLevel = clef.LevelTrace
	}
	switch strings.ToLower(f.source) {
	case "", "all":
		f.source = ""
	}
	return f, nil
}

func (f *logsFilter) allowSource(src string) bool {
	if f.source == "" {
		return true
	}
	return src == f.source
}

// matchEvent 全字段过滤；优先用此函数。
func (f *logsFilter) matchEvent(ev *clef.Event) bool {
	if f == nil {
		return true
	}
	src, _ := ev.Get("Source")
	srcStr, _ := src.(string)
	if !f.allowSource(srcStr) {
		return false
	}
	if f.minLevel > clef.LevelTrace {
		lvlV, _ := ev.Get("@l")
		lvlStr, _ := lvlV.(string)
		lvl := clef.FromCLEFName(lvlStr)
		if lvl < f.minLevel {
			return false
		}
	}
	if f.eventID != "" {
		eidV, _ := ev.Get("EventID")
		eidStr, _ := eidV.(string)
		if !strings.HasPrefix(eidStr, f.eventID) {
			return false
		}
	}
	return true
}

// matchLine 仅做必要字段的轻量解码，用于 --json 模式避免完整 OrderedEvent 重建。
func (f *logsFilter) matchLine(line []byte) bool {
	if f == nil {
		return true
	}
	if f.source == "" && f.minLevel <= clef.LevelTrace && f.eventID == "" {
		return true
	}
	var probe struct {
		Source  string `json:"Source"`
		Level   string `json:"@l"`
		EventID string `json:"EventID"`
	}
	if err := json.Unmarshal(line, &probe); err != nil {
		return false
	}
	if !f.allowSource(probe.Source) {
		return false
	}
	if f.minLevel > clef.LevelTrace {
		if clef.FromCLEFName(probe.Level) < f.minLevel {
			return false
		}
	}
	if f.eventID != "" && !strings.HasPrefix(probe.EventID, f.eventID) {
		return false
	}
	return true
}
