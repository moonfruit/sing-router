package notify

import (
	"context"
	"maps"
	"sync"
	"time"

	"github.com/moonfruit/sing2seq/clef"
)

// ChannelSpec 把一个 Channel 与它的渠道级最低优先级绑定。渠道级阈值在全局
// MinPriority 之上叠加，只能更严（抬高），不能放宽。
type ChannelSpec struct {
	Channel     Channel
	MinPriority Priority
}

// NotifierConfig 配置 Notifier。
type NotifierConfig struct {
	Channels      []ChannelSpec
	MinPriority   Priority      // 全局最低优先级，低于它的通知被丢弃
	DisabledKinds []string      // 按 Kind（= clef EventID）精确关闭
	Emitter       *clef.Emitter // 自诊断事件出口（Source 应为 "notify"）；可为 nil
	QueueSize     int           // 每渠道队列容量，<=0 取默认 64
	SendTimeout   time.Duration // 每次 Send 的超时，<=0 取默认 10s
	MaxAttempts   int           // 每条通知的投递尝试次数（含首次），<=0 取默认 3
	// RetryBackoffs 是第 1、2…次重试前的等待序列；末档封顶。为空走默认
	// [2s, 5s]——通知迟到太久即失去价值，故刻意短。主要也供测试调快。
	RetryBackoffs []time.Duration
}

// Notifier 是通用异步通知引擎：bus 入口 Dispatch → 全局过滤 → 按 Kind 节流 →
// 扇出到每个 Channel 的独立 worker（缓冲队列 + 有界重试 + drain）。渠道间完全
// 隔离：一个挂掉的渠道不拖累其它渠道。
type Notifier struct {
	channels      []*channelWorker
	minPriority   Priority
	disabledKinds map[string]struct{}

	mu       sync.Mutex
	closed   bool
	closing  chan struct{}
	throttle map[string]*throttleState
}

// throttleState 是单个 Kind 的节流状态。
type throttleState struct {
	lastSentAt time.Time
	suppressed int
}

// NewNotifier 构造并启动 Notifier：每个 Channel 起一个 worker goroutine。
func NewNotifier(cfg NotifierConfig) *Notifier {
	queueSize := cfg.QueueSize
	if queueSize <= 0 {
		queueSize = 64
	}
	sendTimeout := cfg.SendTimeout
	if sendTimeout <= 0 {
		sendTimeout = 10 * time.Second
	}
	maxAttempts := cfg.MaxAttempts
	if maxAttempts <= 0 {
		maxAttempts = 3
	}
	retryBackoffs := cfg.RetryBackoffs
	if len(retryBackoffs) == 0 {
		retryBackoffs = []time.Duration{2 * time.Second, 5 * time.Second}
	}
	n := &Notifier{
		minPriority:   cfg.MinPriority,
		disabledKinds: make(map[string]struct{}, len(cfg.DisabledKinds)),
		closing:       make(chan struct{}),
		throttle:      map[string]*throttleState{},
	}
	for _, k := range cfg.DisabledKinds {
		n.disabledKinds[k] = struct{}{}
	}
	for _, spec := range cfg.Channels {
		w := &channelWorker{
			ch:            spec.Channel,
			minPriority:   spec.MinPriority,
			queue:         make(chan Notification, queueSize),
			done:          make(chan struct{}),
			closing:       n.closing,
			emitter:       cfg.Emitter,
			sendTimeout:   sendTimeout,
			maxAttempts:   maxAttempts,
			retryBackoffs: retryBackoffs,
		}
		n.channels = append(n.channels, w)
		go w.run()
	}
	return n
}

// Dispatch 是 bus 入口：把一条 Notification 经全局过滤、按 Kind 节流后扇出到各
// 渠道队列。非阻塞——队列满则丢弃。Close 之后是 no-op。
func (n *Notifier) Dispatch(notif Notification) {
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.closed {
		return
	}
	if notif.Priority < n.minPriority {
		return
	}
	if _, disabled := n.disabledKinds[notif.Kind]; disabled {
		return
	}
	if win := throttleWindow(notif.Kind); win > 0 {
		st := n.throttle[notif.Kind]
		if st == nil {
			st = &throttleState{}
			n.throttle[notif.Kind] = st
		}
		now := time.Now()
		if !st.lastSentAt.IsZero() && now.Sub(st.lastSentAt) < win {
			st.suppressed++
			return
		}
		if st.suppressed > 0 {
			notif = withSuppressedCount(notif, st.suppressed)
		}
		st.suppressed = 0
		st.lastSentAt = now
	}
	for _, w := range n.channels {
		w.submit(notif)
	}
}

// NotifySync 绕过队列与节流，并行同步投递到所有渠道，供 panic 等不能依赖异步
// 排空的路径用。ctx 控制总超时；本方法不会 panic（每个渠道发送自带 recover）。
func (n *Notifier) NotifySync(ctx context.Context, notif Notification) {
	var wg sync.WaitGroup
	for _, w := range n.channels {
		if notif.Priority < w.minPriority {
			continue
		}
		wg.Add(1)
		go func(c Channel) {
			defer wg.Done()
			defer func() { _ = recover() }() // 显式路径常在 panic recover 中跑，自己别再炸
			_ = c.Send(ctx, notif)
		}(w.ch)
	}
	wg.Wait()
}

// Close 停止接收新通知，drain 各渠道队列里剩余的通知；阻塞直到全部 worker 退出
// 或 ctx 到期（到期返回 ctx.Err()，残余 worker 在后台随进程退出收尾）。幂等。
func (n *Notifier) Close(ctx context.Context) error {
	n.mu.Lock()
	if n.closed {
		n.mu.Unlock()
		return nil
	}
	n.closed = true
	close(n.closing)
	for _, w := range n.channels {
		close(w.queue)
	}
	n.mu.Unlock()
	for _, w := range n.channels {
		select {
		case <-w.done:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return nil
}

// withSuppressedCount 返回一个 Fields 带 suppressed_count 的副本，不改原 Notification。
func withSuppressedCount(n Notification, count int) Notification {
	fields := make(map[string]any, len(n.Fields)+1)
	maps.Copy(fields, n.Fields)
	fields["suppressed_count"] = count
	n.Fields = fields
	return n
}

// channelWorker 服务单个 Channel：一个 goroutine 消费队列、带超时与有界重试投递。
type channelWorker struct {
	ch            Channel
	minPriority   Priority
	queue         chan Notification
	done          chan struct{}
	closing       <-chan struct{}
	emitter       *clef.Emitter
	sendTimeout   time.Duration
	maxAttempts   int
	retryBackoffs []time.Duration
}

func (w *channelWorker) run() {
	defer close(w.done)
	for notif := range w.queue {
		w.deliver(notif)
	}
}

// submit 非阻塞入队；队列满即丢弃并发一条诊断。调用方须持 Notifier.mu，
// 以保证此刻队列未被 Close 关闭。
func (w *channelWorker) submit(notif Notification) {
	select {
	case w.queue <- notif:
	default:
		w.diagWarn("notify.queue.overflow",
			"channel {Channel} queue full; dropped {Kind}",
			map[string]any{"Channel": w.ch.Name(), "Kind": notif.Kind})
	}
}

func (w *channelWorker) deliver(notif Notification) {
	if notif.Priority < w.minPriority {
		return
	}
	var lastErr error
	for attempt := 0; attempt < w.maxAttempts; attempt++ {
		if attempt > 0 {
			select {
			case <-time.After(w.retryBackoffs[min(attempt-1, len(w.retryBackoffs)-1)]):
			case <-w.closing:
				// 关停中：放弃后续重试，让 drain 尽快结束。
				return
			}
		}
		sctx, cancel := context.WithTimeout(context.Background(), w.sendTimeout)
		err := w.ch.Send(sctx, notif)
		cancel()
		if err == nil {
			return
		}
		lastErr = err
	}
	w.diagWarn("notify.send.failed",
		"channel {Channel} failed to deliver {Kind} after {Attempts} attempts: {Error}",
		map[string]any{
			"Channel":  w.ch.Name(),
			"Kind":     notif.Kind,
			"Attempts": w.maxAttempts,
			"Error":    errString(lastErr),
		})
}

// diagWarn 发一条 notify 子系统自诊断事件。Source 由 emitter 固定为 "notify"，
// 这些 notify.* EventID 不在 catalog 里——self-loop 防护（见 docs/adr/0001）。
func (w *channelWorker) diagWarn(eventID, mt string, fields map[string]any) {
	if w.emitter != nil {
		w.emitter.Warn("notify.sink", eventID, mt, fields)
	}
}

func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}
