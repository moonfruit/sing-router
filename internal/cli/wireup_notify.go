package cli

import (
	"context"
	"fmt"
	"time"

	"github.com/moonfruit/sing2seq/clef"

	"github.com/moonfruit/sing-router/internal/config"
	log "github.com/moonfruit/sing-router/internal/log"
	"github.com/moonfruit/sing-router/internal/notify"
	"github.com/moonfruit/sing-router/internal/notify/bark"
)

// notifySinkSource 是通知子系统自诊断事件（notify.send.failed /
// notify.queue.overflow）的 clef Source。这些 notify.* EventID 不在 catalog 里，
// 不会反过来又触发通知——self-loop 防护（见 docs/adr/0001）。
const notifySinkSource = "notify"

// notifyWillAttach 判断配置是否会真正接入通知子系统：Enabled 且至少有一个带
// key 的 enabled 渠道。供 caller 在构造 EmitterStack 前预判 emitter floor。
func notifyWillAttach(cfg config.NotifyConfig) bool {
	if !cfg.Enabled {
		return false
	}
	for _, b := range cfg.Bark {
		if b.Enabled && b.Key != "" {
			return true
		}
	}
	return false
}

// attachNotifySink 按 daemon.toml [notify] 节构造 Notifier 并订阅 bus。返回
// notifier（无可用渠道时为 nil）、实际接入的渠道数、以及构造期的告警字符串
// （由 caller 落成 clef 事件）。
func attachNotifySink(stack *log.EmitterStack, cfg config.NotifyConfig) (*notify.Notifier, int, []string) {
	specs, warns := buildNotifyChannels(cfg)
	if len(specs) == 0 {
		return nil, 0, warns
	}

	globalMin, ok := notify.ParsePriority(cfg.MinPriority)
	if !ok {
		warns = append(warns, fmt.Sprintf("invalid [notify].min_priority %q; using low", cfg.MinPriority))
		globalMin = notify.PriorityLow
	}

	notifier := notify.NewNotifier(notify.NotifierConfig{
		Channels:      specs,
		MinPriority:   globalMin,
		DisabledKinds: cfg.DisabledKinds,
		Emitter: clef.NewEmitter(clef.EmitterConfig{
			Source: notifySinkSource,
			Bus:    stack.Bus,
		}),
	})

	// MatchFn 只放行 catalog 收录的 EventID——而非按 level 过滤。notify.* 自诊断
	// 事件不在 catalog，天然被挡，不会回环。
	handle := stack.Bus.Subscribe(clef.SubscriberFunc{
		MatchFn: notify.IsCatalogued,
		DeliverFn: func(ev *clef.Event) {
			if n, ok := notify.Translate(ev); ok {
				notifier.Dispatch(n)
			}
		},
	})
	drainTimeout := time.Duration(cfg.NotifyCloseDrainTimeoutSeconds()) * time.Second
	stack.Attach("notify.sink", handle, func(ctx context.Context) error {
		return drainNotifierWithCtx(ctx, notifier, drainTimeout)
	})
	return notifier, len(specs), warns
}

// buildNotifyChannels 把配置里所有 enabled 渠道构造成 ChannelSpec；构造失败的
// 渠道被跳过并记一条告警，不影响其它渠道。
func buildNotifyChannels(cfg config.NotifyConfig) ([]notify.ChannelSpec, []string) {
	var specs []notify.ChannelSpec
	var warns []string
	for _, b := range cfg.Bark {
		if !b.Enabled {
			continue
		}
		spec, warn := buildBarkChannelSpec(b)
		if warn != "" {
			warns = append(warns, warn)
			continue
		}
		specs = append(specs, spec)
	}
	return specs, warns
}

// buildBarkChannelSpec 把一个 [[notify.bark]] 配置构造成 ChannelSpec。
func buildBarkChannelSpec(b config.BarkConfig) (notify.ChannelSpec, string) {
	ch, err := bark.New(barkConfigFrom(b))
	if err != nil {
		return notify.ChannelSpec{}, fmt.Sprintf("bark channel %q skipped: %v", b.Name, err)
	}
	minP, ok := notify.ParsePriority(b.MinPriority)
	if !ok {
		minP = notify.PriorityLow
	}
	return notify.ChannelSpec{Channel: ch, MinPriority: minP}, ""
}

// barkConfigFrom 把 daemon.toml 的 BarkConfig 翻成 bark.Config。供 wireup、
// doctor 校验、CLI notify test 共用。
func barkConfigFrom(b config.BarkConfig) bark.Config {
	c := bark.Config{
		Name:    b.Name,
		BaseURL: b.BaseURL,
		Key:     b.Key,
		Group:   b.Group,
	}
	if b.Encryption != nil {
		c.Encryption = &bark.EncryptionConfig{
			Algorithm: b.Encryption.Algorithm,
			Mode:      b.Encryption.Mode,
			Key:       b.Encryption.Key,
		}
	}
	return c
}

// drainNotifierWithCtx 在 daemonTimeout 与 caller ctx 中取更早的 deadline 调用
// Notifier.Close——超时后残余 worker 在后台随进程退出收尾，daemon 不被卡住。
func drainNotifierWithCtx(ctx context.Context, n *notify.Notifier, daemonTimeout time.Duration) error {
	deadline, hasParentDL := ctx.Deadline()
	if !hasParentDL || daemonTimeout < time.Until(deadline) {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, daemonTimeout)
		defer cancel()
	}
	return n.Close(ctx)
}
