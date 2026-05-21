// Package notify 是 sing-router 的多渠道通知子系统：把 daemon 的主要状态变化
// （资源更新、sing-box 崩溃、路由丢失等）翻译成面向人类的推送，投递到 Bark
// 等渠道。它作为 clef.Bus 的订阅者接入，daemon 业务代码无需改动。
//
// 数据流：clef.Event ──Translate(catalog)──▶ Notification ──Notifier──▶ Channel。
package notify

import "time"

// Priority 是 Notification 的抽象紧急度。各 Channel 把它映射到自己的原生紧急度
// （如 Bark 的 passive/active/timeSensitive/critical）。它与 clef 日志 level 是
// 两条独立的轴——一个 Information 级事件也可以产出 High 优先级的通知。
type Priority int

const (
	// PriorityLow 例行信息，渠道应静默投递（Bark passive）。
	PriorityLow Priority = iota
	// PriorityNormal 默认状态变化（Bark active）。
	PriorityNormal
	// PriorityHigh 需要尽快知晓（Bark timeSensitive，穿透专注模式）。
	PriorityHigh
	// PriorityCritical 代理彻底不可用（Bark critical，穿透静音与勿扰）。
	PriorityCritical
)

// String 返回优先级的规范字符串，与 ParsePriority 互逆。
func (p Priority) String() string {
	switch p {
	case PriorityLow:
		return "low"
	case PriorityNormal:
		return "normal"
	case PriorityHigh:
		return "high"
	case PriorityCritical:
		return "critical"
	default:
		return "normal"
	}
}

// ParsePriority 解析配置里的优先级字符串。空串视为 low（不过滤）；非法值返回
// (PriorityLow, false)，由 caller 决定是告警还是兜底。
func ParsePriority(s string) (Priority, bool) {
	switch s {
	case "", "low":
		return PriorityLow, true
	case "normal":
		return PriorityNormal, true
	case "high":
		return PriorityHigh, true
	case "critical":
		return PriorityCritical, true
	default:
		return PriorityLow, false
	}
}

// Notification 是一条渠道无关的、面向人类的状态变化推送。它由 catalog 把 clef
// 事件翻译而来（见 Translate），或在 panic 路径上直接构造。
type Notification struct {
	// Kind 是稳定的事件标识（= clef EventID，如 "apply.ok"），驱动按 Kind 节流、
	// 渠道侧分组/去重（Bark id）。
	Kind string
	// Title / Subtitle / Body 三段文本。无 Subtitle 概念的渠道应把它并入 Body。
	Title    string
	Subtitle string
	Body     string
	// Priority 决定渠道侧的紧急度映射。
	Priority Priority
	// Fields 携带结构化明细，渠道可自行渲染。引擎按 Kind 节流时会写入
	// "suppressed_count"（节流窗口内被抑制的同 Kind 次数）。
	Fields map[string]any
	// Time 是事件发生时间。
	Time time.Time
}
