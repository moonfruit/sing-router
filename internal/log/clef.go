// Package log 提供 sing-router 的结构化日志原语。
//
// CLEF (Compact Log Event Format) 用于与 Seq 兼容；事件字段保持插入顺序，
// 因为 Seq UI 按顺序展示。orderedEvent 是与 sing2seq 一致的轻量实现。
package log

import (
	"bytes"
	"encoding/json"
)

// OrderedEvent 是 CLEF 事件，字段顺序由 Set 调用顺序决定。
type OrderedEvent struct {
	keys   []string
	values map[string]any
}

// NewEvent 创建一个空事件。
func NewEvent() *OrderedEvent {
	return &OrderedEvent{values: map[string]any{}}
}

// Set 添加或更新一个字段。后写不动顺序。
func (e *OrderedEvent) Set(k string, v any) {
	if _, ok := e.values[k]; !ok {
		e.keys = append(e.keys, k)
	}
	e.values[k] = v
}

// Get 返回字段值；不存在时第二个返回值为 false。
func (e *OrderedEvent) Get(k string) (any, bool) {
	v, ok := e.values[k]
	return v, ok
}

// Keys 返回有序键列表的副本。
func (e *OrderedEvent) Keys() []string {
	out := make([]string, len(e.keys))
	copy(out, e.keys)
	return out
}

// MarshalJSON 按插入顺序序列化字段。
func (e *OrderedEvent) MarshalJSON() ([]byte, error) {
	var buf bytes.Buffer
	buf.WriteByte('{')
	for i, k := range e.keys {
		if i > 0 {
			buf.WriteByte(',')
		}
		kb, err := json.Marshal(k)
		if err != nil {
			return nil, err
		}
		buf.Write(kb)
		buf.WriteByte(':')
		vb, err := json.Marshal(e.values[k])
		if err != nil {
			return nil, err
		}
		buf.Write(vb)
	}
	buf.WriteByte('}')
	return buf.Bytes(), nil
}
