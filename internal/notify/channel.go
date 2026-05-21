package notify

import "context"

// Channel 是一条通知投递渠道。实现应是无状态的纯发送器：Send 执行一次同步投递
// （通常一个 HTTP 请求），返回的 error 交由 Notifier 决定是否重试。异步、队列、
// 重试、drain 全部由通用引擎（Notifier）提供——加一个新渠道只需实现这两个方法。
type Channel interface {
	// Name 返回渠道实例名，用于日志与诊断（如 "bark/phone"）。
	Name() string
	// Send 同步投递一条通知。ctx 用于超时控制；返回非 nil error 表示本次失败，
	// 引擎会按重试策略再试。
	Send(ctx context.Context, n Notification) error
}
