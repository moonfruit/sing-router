package log

import "github.com/moonfruit/sing2seq/clef"

// LevelAtLeast 返回一个 MatchFn：事件 @l 字段对应级别 >= min 时通过。
// 缺 @l 字段或字段值非合法 CLEF 名时按 Information 处理（与
// clef.FromCLEFName 行为一致）。min = LevelTrace 时等价于不过滤。
func LevelAtLeast(min Level) func(*clef.Event) bool {
	if min <= LevelTrace {
		return func(*clef.Event) bool { return true }
	}
	return func(ev *clef.Event) bool {
		raw, _ := ev.Get("@l")
		name, _ := raw.(string)
		return clef.FromCLEFName(name) >= min
	}
}
