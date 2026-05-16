package cli

import (
	"context"
	"fmt"
	"time"

	"github.com/moonfruit/sing2seq/clef"
	"github.com/moonfruit/sing2seq/seq"

	"github.com/moonfruit/sing-router/internal/config"
	log "github.com/moonfruit/sing-router/internal/log"
)

// seqSinkSource 是 seq.Sink 自身诊断事件（buffer_overflow / post_failed /
// shutdown_post_failed）的 Source 字段值。**必须**写死 "sing2seq"，与上游
// sing2seq CLI 一致，便于 Seq 端 dashboard / 告警规则跨工具复用。
const seqSinkSource = "sing2seq"

// resolveSeqLevel 解析 [seq].level；非法值 fallback 到 info 并返回一条 warn
// 字符串供 caller 落事件。caller 还应该用这个 level 参与计算 EmitterStack
// 的 MinLevel floor。
func resolveSeqLevel(s string) (level clef.Level, warn string) {
	if s == "" {
		return clef.LevelInfo, ""
	}
	lvl, err := clef.ParseLevel(s)
	if err != nil {
		return clef.LevelInfo, fmt.Sprintf("invalid [seq].level %q; falling back to info", s)
	}
	return lvl, ""
}

// seqWillAttach 判断当前配置是否会真正接入 sink（Enabled 且 URL 非空）。
// 供 caller 在构造 EmitterStack 前预判 emitter floor 是否要考虑 seq level。
func seqWillAttach(cfg config.SeqConfig) bool {
	return cfg.Enabled && cfg.URL != ""
}

// attachSeqSink 按 daemon.toml [seq] 节给 stack 接一个 seq.Sink。
// 调用前必须 seqWillAttach(cfg) == true（caller 自己 gate）。level 由
// caller 用 resolveSeqLevel 预先解析，并与 cfg.Log.Level 一起算 emitter floor。
//
// 接入路径上构造的 seqEmitter 用 Source="sing2seq" 绑定同一 Bus；sink
// 通过 SubscriberFunc 订阅 bus，MatchFn 按 @l 字段过滤到 level 以上。
// stack.Attach 把 sink.Close 注册到 EmitterStack.Close 路径，shutdown 时
// 同步 drain（带 cfg.SeqCloseDrainTimeoutSeconds 总预算）。
func attachSeqSink(stack *log.EmitterStack, cfg config.SeqConfig, level clef.Level) {
	sinkCfg := seq.Config{
		URL:      cfg.URL,
		APIKey:   cfg.APIKey,
		Insecure: cfg.Insecure,
		Emitter: clef.NewEmitter(clef.EmitterConfig{
			Source: seqSinkSource,
			Bus:    stack.Bus,
		}),
	}
	if cfg.BatchSize != nil {
		sinkCfg.BatchSize = *cfg.BatchSize
	}
	if cfg.ChannelBuffer != nil {
		sinkCfg.ChannelBuffer = *cfg.ChannelBuffer
	}
	if cfg.MaxPending != nil {
		sinkCfg.MaxPending = *cfg.MaxPending
	}
	if cfg.DropTarget != nil {
		sinkCfg.DropTarget = *cfg.DropTarget
	}
	if cfg.InitialBackoff != nil {
		sinkCfg.InitialBackoff = time.Duration(*cfg.InitialBackoff) * time.Millisecond
	}
	if cfg.MaxBackoff != nil {
		sinkCfg.MaxBackoff = time.Duration(*cfg.MaxBackoff) * time.Millisecond
	}

	sk := seq.NewSink(sinkCfg)
	sk.Start()
	handle := stack.Bus.Subscribe(clef.SubscriberFunc{
		MatchFn:   log.LevelAtLeast(level),
		DeliverFn: func(ev *clef.Event) { sk.Submit(ev) },
	})
	drainTimeout := time.Duration(cfg.SeqCloseDrainTimeoutSeconds()) * time.Second
	stack.Attach("seq.sink", handle, func(ctx context.Context) error {
		return drainSinkWithCtx(ctx, sk, drainTimeout)
	})
}

// drainSinkWithCtx 包一层 ctx 超时：seq.Sink.Close 是同步阻塞 drain，不接受
// ctx。另起 goroutine 跑 Close，select on ctx.Done，超时则返回 ctx.Err()。
// 超时后 sink 内部 manager goroutine 仍会跑完 / 发 shutdown_post_failed
// 诊断（fallback 到 stderr，bus 此时也快关），daemon 主路径不被卡住。
//
// daemonTimeout 是从 cfg 算出的总预算；ctx 来自 caller 可能更紧——取两者更早的。
func drainSinkWithCtx(ctx context.Context, sk *seq.Sink, daemonTimeout time.Duration) error {
	deadline, hasParentDL := ctx.Deadline()
	parentBudget := time.Until(deadline)
	if !hasParentDL || daemonTimeout < parentBudget {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, daemonTimeout)
		defer cancel()
	}

	done := make(chan error, 1)
	go func() { done <- sk.Close() }()

	select {
	case err := <-done:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}
