// Package log 提供 sing-router 的结构化日志原语（Writer/Pretty/Level/wireup）。
// 事件类型与解析器来自 github.com/moonfruit/sing2seq/clef。
package log

import "github.com/moonfruit/sing2seq/clef"

// OrderedEvent 是 clef.Event 的别名，保留旧名以最小化本步骤的改动面。
// Step B 完成后此别名会被删除，调用方直接使用 clef.Event。
type OrderedEvent = clef.Event

// NewEvent 别名 clef.NewEvent。
var NewEvent = clef.NewEvent

// ParseSingBoxLine 别名 clef.ParseSingBoxLine。
var ParseSingBoxLine = clef.ParseSingBoxLine
