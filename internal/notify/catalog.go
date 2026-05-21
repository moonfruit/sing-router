package notify

import (
	"fmt"
	"strings"
	"time"

	"github.com/moonfruit/sing2seq/clef"
)

// catalogEntry 描述一个可通知的 Kind：它的优先级、节流窗口与文案渲染逻辑。
type catalogEntry struct {
	Priority Priority
	// ThrottleWindow > 0 时，同一 Kind 在窗口内的重复事件被引擎抑制（计数顺带给
	// 下一条真发出的同 Kind）。0 表示不节流。
	ThrottleWindow time.Duration
	// Render 把 clef 事件渲染成通知三段文本。
	Render func(ev *clef.Event) (title, subtitle, body string)
}

// catalog 是静态目录：只有这里登记的 clef EventID 才会被翻译成 Notification
// （catalog-as-code，见 docs/adr/0002）。刻意排除的事件见设计稿（apply.noop /
// sync.item.updated / panic.recovered / notify.* 等）。
var catalog = map[string]catalogEntry{
	// ── 崩溃 / 致命 ───────────────────────────────────────────────
	"supervisor.child.crashed": {
		Priority:       PriorityHigh,
		ThrottleWindow: 5 * time.Minute,
		Render:         renderChildCrashed,
	},
	"supervisor.recovered": {
		Priority: PriorityNormal,
		Render:   renderRecovered,
	},
	"supervisor.crash.unrecovered": {
		Priority: PriorityCritical,
		Render:   renderCrashUnrecovered,
	},
	"supervisor.boot.failed": {
		Priority: PriorityCritical,
		Render:   errRender("🛑 daemon 启动失败", "sing-router daemon 无法启动。", "Err"),
	},

	// ── daemon 生命周期 ──────────────────────────────────────────
	"daemon.started": {
		Priority: PriorityLow,
		Render:   renderDaemonStarted,
	},
	"daemon.stopped": {
		Priority: PriorityLow,
		Render: func(*clef.Event) (string, string, string) {
			return "⚪ daemon 已停止", "", "sing-router daemon 已优雅退出。"
		},
	},

	// ── 路由 / iptables ─────────────────────────────────────────
	"supervisor.route.missing": {
		Priority:       PriorityHigh,
		ThrottleWindow: 5 * time.Minute,
		Render: func(*clef.Event) (string, string, string) {
			return "⚠️ 代理路由丢失", "",
				"检测到 device-bound 路由被外部移除（多半是 sing-box reload 重建 TUN），正在重启恢复。"
		},
	},
	"shell.startup.failed": {
		Priority: PriorityHigh,
		Render: errRender("⚠️ iptables 安装失败",
			"startup.sh 执行失败，流量可能未被正确劫持到代理。", "Err"),
	},
	"shell.teardown.failed": {
		Priority: PriorityHigh,
		Render: errRender("⚠️ iptables 清理失败",
			"teardown.sh 执行失败，可能残留 iptables 规则导致连接异常。", "Err"),
	},

	// ── 资源应用 ─────────────────────────────────────────────────
	"apply.ok": {
		Priority: PriorityNormal,
		Render:   renderApplyOK,
	},
	"apply.check.failed": {
		Priority: PriorityHigh,
		Render: errRender("⚠️ 配置校验失败",
			"新配置未通过 sing-box check，已回滚到上一份配置。", "Err"),
	},
	"apply.restart.failed": {
		Priority: PriorityHigh,
		Render: errRender("⚠️ 应用新资源失败",
			"用新资源重启 sing-box 失败，已回滚并用旧配置恢复运行。", "Err"),
	},
	"apply.recover.failed": {
		Priority: PriorityCritical,
		Render:   renderApplyRecoverFailed,
	},
	"apply.preprocess.failed": {
		Priority: PriorityNormal,
		Render: errRender("⚠️ zoo 预处理失败",
			"应用资源时 zoo 预处理失败，已保留上次成功的配置。", "Err"),
	},

	// ── 后台同步 / 配置 ──────────────────────────────────────────
	"sync.item.failed": {
		Priority: PriorityNormal,
		Render:   renderSyncItemFailed,
	},
	"sync.commit.failed": {
		Priority: PriorityNormal,
		Render: errRender("⚠️ 资源提交失败",
			"sing-box 暂存资源提交失败。", "Err"),
	},
	"config.zoo.preprocess.failed": {
		Priority: PriorityNormal,
		Render: errRender("⚠️ zoo 预处理失败",
			"daemon 启动时 zoo 预处理失败，已保留上次成功的配置。", "Err"),
	},
	"config.rule_sets.supplement.failed": {
		Priority: PriorityNormal,
		Render: errRender("⚠️ rule-set 补全失败",
			"daemon 启动时补全 rule-set 失败。", "Err"),
	},
}

// IsCatalogued 报告事件的 EventID 是否在目录里。供 bus 订阅的 MatchFn 用——
// 提前过滤掉不可通知的事件，避免塞满订阅者 channel。
func IsCatalogued(ev *clef.Event) bool {
	_, ok := catalog[evStr(ev, "EventID")]
	return ok
}

// Translate 把一个 clef 事件翻译成 Notification。EventID 不在目录里时返回
// (Notification{}, false)。
func Translate(ev *clef.Event) (Notification, bool) {
	id := evStr(ev, "EventID")
	entry, ok := catalog[id]
	if !ok {
		return Notification{}, false
	}
	title, subtitle, body := entry.Render(ev)
	return Notification{
		Kind:     id,
		Title:    title,
		Subtitle: subtitle,
		Body:     body,
		Priority: entry.Priority,
		Fields:   eventFields(ev),
		Time:     evTime(ev),
	}, true
}

// throttleWindow 返回某 Kind 的节流窗口；未登记或不节流返回 0。
func throttleWindow(kind string) time.Duration {
	if e, ok := catalog[kind]; ok {
		return e.ThrottleWindow
	}
	return 0
}

// ── Render 实现 ──────────────────────────────────────────────────

func renderChildCrashed(ev *clef.Event) (string, string, string) {
	backoff := time.Duration(evInt(ev, "BackoffMs")) * time.Millisecond
	body := fmt.Sprintf("第 %d 次崩溃，已拆 iptables 进 DIRECT 直连，%s 后退避重启。",
		evInt(ev, "CrashCount"), backoff)
	return "⚠️ sing-box 崩溃", "", body
}

func renderRecovered(ev *clef.Event) (string, string, string) {
	body := fmt.Sprintf("退避重启成功，本轮共崩溃 %d 次。", evInt(ev, "CrashCount"))
	return "✅ sing-box 已恢复", "", body
}

func renderCrashUnrecovered(ev *clef.Event) (string, string, string) {
	body := fmt.Sprintf("崩溃 %d 次后重启仍失败，已放弃。代理当前不可用。",
		evInt(ev, "CrashCount"))
	return "🛑 sing-box 无法恢复", "", appendErr(body, evStr(ev, "Err"))
}

func renderDaemonStarted(ev *clef.Event) (string, string, string) {
	body := fmt.Sprintf("sing-router %s 已在 %s 启动。",
		evStr(ev, "Version"), evStr(ev, "Rundir"))
	return "🟢 daemon 已启动", "", body
}

func renderApplyOK(ev *clef.Event) (string, string, string) {
	var items []string
	if evBool(ev, "Bin") {
		items = append(items, "- sing-box 二进制")
	}
	if evBool(ev, "Zoo") {
		items = append(items, "- zoo 配置")
	}
	if evBool(ev, "Rule") {
		items = append(items, "- rule-set 规则集")
	}
	if evBool(ev, "CN") {
		items = append(items, "- cn.txt 中国 IP 段")
	}
	body := "sing-box 已用新资源重启。"
	if len(items) > 0 {
		body = "sing-box 已用新资源重启：\n" + strings.Join(items, "\n")
	}
	return "✅ 资源已更新", "", body
}

func renderApplyRecoverFailed(ev *clef.Event) (string, string, string) {
	body := fmt.Sprintf("用新资源重启失败，回滚后仍无法恢复。代理当前不可用。\n\n重启错误：%s\n回滚错误：%s",
		evStr(ev, "RestartErr"), evStr(ev, "RecoverErr"))
	return "🛑 应用与回滚均失败", "", body
}

func renderSyncItemFailed(ev *clef.Event) (string, string, string) {
	body := fmt.Sprintf("后台同步 %s 失败（将在下个周期重试，代理不受影响）。",
		evStr(ev, "Name"))
	return "⚠️ 资源同步失败", "", appendErr(body, evStr(ev, "Err"))
}

// errRender 返回一个固定 title + 固定引导句 + 把 errKey 字段附加为错误明细的 Render。
func errRender(title, lead, errKey string) func(*clef.Event) (string, string, string) {
	return func(ev *clef.Event) (string, string, string) {
		return title, "", appendErr(lead, evStr(ev, errKey))
	}
}

// appendErr 在正文后追加错误明细；err 为空时原样返回。
func appendErr(body, err string) string {
	if err == "" {
		return body
	}
	return body + "\n\n错误：" + err
}

// ── clef 事件字段读取 ────────────────────────────────────────────

// metaKeys 是 clef Emitter 自动注入的元字段，不计入 Notification.Fields。
var metaKeys = map[string]struct{}{
	"@t": {}, "@l": {}, "@mt": {}, "Source": {}, "Module": {}, "EventID": {},
}

func eventFields(ev *clef.Event) map[string]any {
	out := map[string]any{}
	for _, k := range ev.Keys() {
		if _, meta := metaKeys[k]; meta {
			continue
		}
		if v, ok := ev.Get(k); ok {
			out[k] = v
		}
	}
	return out
}

func evStr(ev *clef.Event, k string) string {
	v, ok := ev.Get(k)
	if !ok {
		return ""
	}
	if s, ok := v.(string); ok {
		return s
	}
	return fmt.Sprint(v)
}

func evInt(ev *clef.Event, k string) int {
	v, ok := ev.Get(k)
	if !ok {
		return 0
	}
	switch n := v.(type) {
	case int:
		return n
	case int64:
		return int(n)
	case float64:
		return int(n)
	default:
		return 0
	}
}

func evBool(ev *clef.Event, k string) bool {
	if v, ok := ev.Get(k); ok {
		if b, ok := v.(bool); ok {
			return b
		}
	}
	return false
}

func evTime(ev *clef.Event) time.Time {
	if s := evStr(ev, "@t"); s != "" {
		if t, err := time.Parse(time.RFC3339Nano, s); err == nil {
			return t
		}
	}
	return time.Now()
}
