package log

import "time"

// EmitterConfig 配置 Emitter。
type EmitterConfig struct {
	Source   string  // 事件 Source 字段，例如 "daemon"
	MinLevel Level   // 低于此级别的事件被丢弃
	Writer   *Writer // 必须；CLEF 落盘
	Bus      *Bus    // 可选；nil 时不广播
}

// Emitter 是 daemon 自身事件的入口。所有调用都经此构造 OrderedEvent，
// 写入 Writer 并广播到 Bus。
type Emitter struct {
	cfg EmitterConfig
}

// NewEmitter 创建 emitter。
func NewEmitter(cfg EmitterConfig) *Emitter {
	if cfg.Source == "" {
		cfg.Source = "daemon"
	}
	return &Emitter{cfg: cfg}
}

func (e *Emitter) Trace(module, eventID, mt string, fields map[string]any) {
	e.emit(LevelTrace, module, eventID, mt, fields)
}
func (e *Emitter) Debug(module, eventID, mt string, fields map[string]any) {
	e.emit(LevelDebug, module, eventID, mt, fields)
}
func (e *Emitter) Info(module, eventID, mt string, fields map[string]any) {
	e.emit(LevelInfo, module, eventID, mt, fields)
}
func (e *Emitter) Warn(module, eventID, mt string, fields map[string]any) {
	e.emit(LevelWarn, module, eventID, mt, fields)
}
func (e *Emitter) Error(module, eventID, mt string, fields map[string]any) {
	e.emit(LevelError, module, eventID, mt, fields)
}
func (e *Emitter) Fatal(module, eventID, mt string, fields map[string]any) {
	e.emit(LevelFatal, module, eventID, mt, fields)
}

// PublishExternal 接收已经构建好的事件（如 sing-box stderr 解析得到），
// 走与 Emitter 同一条出口（Writer + Bus）。MinLevel 不过滤外部事件。
func (e *Emitter) PublishExternal(ev *OrderedEvent) {
	if e.cfg.Writer != nil {
		_ = e.cfg.Writer.Write(ev)
	}
	if e.cfg.Bus != nil {
		e.cfg.Bus.Publish(ev)
	}
}

func (e *Emitter) emit(level Level, module, eventID, mt string, fields map[string]any) {
	if level < e.cfg.MinLevel {
		return
	}
	ev := NewEvent()
	ev.Set("@t", time.Now().Format(time.RFC3339Nano))
	ev.Set("@l", level.CLEFName())
	if mt != "" {
		ev.Set("@mt", mt)
	}
	ev.Set("Source", e.cfg.Source)
	if module != "" {
		ev.Set("Module", module)
	}
	if eventID != "" {
		ev.Set("EventID", eventID)
	}
	for k, v := range fields {
		ev.Set(k, v)
	}
	if e.cfg.Writer != nil {
		_ = e.cfg.Writer.Write(ev)
	}
	if e.cfg.Bus != nil {
		e.cfg.Bus.Publish(ev)
	}
}
