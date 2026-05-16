package log

import (
	"context"
	"errors"
	"sync"

	"github.com/moonfruit/sing2seq/clef"
)

// EmitterStack 是 daemon 的事件管道：一个 Bus 居中，Emitter 入口、Writer + 可选
// extra subscribers（如 seq.Sink）出口。
//
// Close 关闭顺序（writer 必须留到最后一步才下线）：
//  1. 取消所有 extras 的订阅（停止 bus 把新事件再喂给 sink；sink 内部仍在
//     处理 pending 队列）。**writer 不在这一步动**——下一步 sink 还会通过
//     共享的 seqEmitter 往 bus 上发诊断事件，需要 writer 仍能接住。
//  2. 并行调每个 extra 的 closeFn(ctx)（如 seq.Sink.Close 同步 drain HTTP queue，
//     受 ctx 截止时间限制；drain 失败会经 seqEmitter→bus→writer 落本地 log）。
//  3. Bus.Close —— drain 残留事件给 writer（writer 仍是订阅方），然后断开
//     所有订阅。Bus.Close 已经处理 writer 的 SubscriptionHandle，无需额外
//     Unsubscribe。
//  4. Writer.Close。
//
// 这个顺序的不变量：sink 在 drain 期间产生的诊断事件（特别是
// shutdown_post_failed）一定能落本地 log；超时后放弃 pending 不阻塞 daemon
// 退出，但已经走到 bus 的事件不会丢。
type EmitterStack struct {
	Bus     *clef.Bus
	Emitter *clef.Emitter
	Writer  *Writer

	mu     sync.Mutex
	extras []extraHandle
	closed bool
}

type extraHandle struct {
	name   string
	handle clef.SubscriptionHandle
	close  func(context.Context) error
}

// StackConfig 配置 EmitterStack。
//
// 两个 Level 字段的关系：
//   - MinLevel 控 Emitter 入口（低于此 level 的 emit 调用直接 no-op，省下
//     event 构造与 bus.Publish 开销）。它应该是所有出口（writer / 任何
//     extras）中最低那个，由 caller 计算。
//   - WriterMinLevel 控 Writer 订阅出口（落到本地 log 文件的下限）。零值
//     LevelTrace 等价于不过滤——保留旧行为：caller 不设这个字段时，writer
//     收 emitter 放过来的全部事件。
//
// 拆开两个 level 是为了让"本地 log 文件"与"远程 seq sink"各自有独立阈值：
// 比如 [log].level=warn + [seq].level=info，writer 只落 warn+，seq 收 info+，
// emitter 入口 floor 取两者更低的 info。sing-box stderr 经 PublishExternal
// 进 bus 不走 emitter MinLevel，但仍被 writer/seq 各自的 MatchFn 过滤。
type StackConfig struct {
	Source         string  // 必填
	MinLevel       Level   // Emitter 入口 floor；caller 应取所有 subscriber level 的最小值
	WriterMinLevel Level   // Writer 出口阈值；零值（LevelTrace）= 不额外过滤
	Writer         *Writer // 必填；订阅 Bus 接收事件（带 WriterMinLevel 过滤）
	BusBuffer      int     // 每订阅方 buffer；<= 0 时取默认 256（高于 clef 默认 64，防止 Writer drop）
}

// NewEmitterStack 构造 Bus + Emitter，并把 Writer 注册为 bus subscriber。
// 调用方可后续通过 Attach 注册更多 subscriber（必须在 Close 之前）。
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
	// Writer 订阅 bus 接收事件，按 WriterMinLevel 过滤；handle 不留存——
	// Close 阶段由 Bus.Close 统一断开所有订阅（包括 writer），无需单独
	// Unsubscribe。
	bus.Subscribe(clef.SubscriberFunc{
		MatchFn:   LevelAtLeast(cfg.WriterMinLevel),
		DeliverFn: func(e *clef.Event) { _ = cfg.Writer.Write(e) },
	})
	return stack
}

// Attach 注册一个额外的 bus subscription，并在 Close 阶段调用 closeFn 进行
// drain。name 仅用于排错；handle 必须是 Subscribe 返回的真实 handle；closeFn
// 可为 nil（仅 unsubscribe，不 drain）。
//
// Close 之后再 Attach 会即刻 Unsubscribe + 同步调一次 closeFn(Background)，
// 防止资源泄漏；这是边角情况——正常流程下 Attach 永远先于 Close。
func (s *EmitterStack) Attach(name string, handle clef.SubscriptionHandle, closeFn func(context.Context) error) {
	if name == "" {
		name = "extra"
	}
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		handle.Unsubscribe()
		if closeFn != nil {
			_ = closeFn(context.Background())
		}
		return
	}
	s.extras = append(s.extras, extraHandle{name: name, handle: handle, close: closeFn})
	s.mu.Unlock()
}

// Close 按上文文档顺序拆解 stack。返回的 error 是所有 extra Close 错误与
// writer Close 错误的合并；如果 ctx 已过期，extras 的 closeFn 自己应感知。
// Close 多次调用安全：后续 Close 是 no-op，返回首次结果。
func (s *EmitterStack) Close(ctx context.Context) error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	extras := s.extras
	s.extras = nil
	s.mu.Unlock()

	// 步骤 1：先把 extras 从 bus 上摘掉，避免 sink drain 期间发出的诊断事件
	// 又被路由回 sink 自己。Writer 此刻**仍订阅**，drain 中的诊断事件必须
	// 能落本地 log——这条不变量被一条单测专门守住。
	for _, h := range extras {
		h.handle.Unsubscribe()
	}

	// 步骤 2：并行 drain 每个 extra。
	var (
		mu   sync.Mutex
		errs []error
		wg   sync.WaitGroup
	)
	for _, h := range extras {
		if h.close == nil {
			continue
		}
		wg.Add(1)
		go func(h extraHandle) {
			defer wg.Done()
			if err := h.close(ctx); err != nil {
				mu.Lock()
				errs = append(errs, err)
				mu.Unlock()
			}
		}(h)
	}
	wg.Wait()

	// 步骤 3：Bus.Close 同步 drain 残留事件给 writer，再断开包括 writer 在
	// 内的所有订阅；之后 Writer.Close 把缓冲刷盘。
	s.Bus.Close()
	if s.Writer != nil {
		if err := s.Writer.Close(); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}
