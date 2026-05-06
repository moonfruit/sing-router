package log

import (
	"github.com/moonfruit/sing2seq/clef"
)

// EmitterStack 是 daemon 的事件管道：一个 Bus 居中，Emitter 入口、Writer + 可选
// pretty subscriber 出口。Close 关闭顺序：先 Bus（drain subscribers），再 Writer。
type EmitterStack struct {
	Bus     *clef.Bus
	Emitter *clef.Emitter
	Writer  *Writer

	writerSub clef.SubscriptionHandle
}

// StackConfig 配置 EmitterStack。
type StackConfig struct {
	Source    string  // 必填
	MinLevel  Level   // Emitter 过滤
	Writer    *Writer // 必填；订阅 Bus 接收所有事件
	BusBuffer int     // 每订阅方 buffer；<= 0 时取默认 256（高于 clef 默认 64，防止 Writer drop）
}

// NewEmitterStack 构造 Bus + Emitter，并把 Writer 注册为 bus subscriber。
func NewEmitterStack(cfg StackConfig) *EmitterStack {
	if cfg.BusBuffer <= 0 {
		cfg.BusBuffer = 256
	}
	bus := clef.NewBus(cfg.BusBuffer)
	em := clef.NewEmitter(clef.EmitterConfig{
		Source:   cfg.Source,
		MinLevel: cfg.MinLevel,
		Bus:      bus,
	})
	stack := &EmitterStack{Bus: bus, Emitter: em, Writer: cfg.Writer}
	stack.writerSub = bus.Subscribe(clef.SubscriberFunc{
		MatchFn:   func(*clef.Event) bool { return true },
		DeliverFn: func(e *clef.Event) { _ = cfg.Writer.Write(e) },
	})
	return stack
}

// Close 取消 writer 订阅，关闭 Bus，最后关闭 Writer。
// 关闭后调用 Emitter 的方法是 no-op。
func (s *EmitterStack) Close() error {
	s.writerSub.Unsubscribe()
	s.Bus.Close()
	if s.Writer != nil {
		return s.Writer.Close()
	}
	return nil
}
