package daemon

import (
	"context"
	"fmt"
	"os"
	"runtime/debug"
	"sync"
	"time"

	"github.com/moonfruit/sing-router/internal/notify"
)

// panicNotifier 是可选的崩溃通知出口，由 Run 在启动时经 setPanicNotifier 注入。
// 它是包级状态：reportPanic 由多个 goroutine 的 defer 调用、无法逐处传参，与同
// 文件的全局崩溃栈输出同属进程级设施。用 RWMutex 保护并发触发。
var (
	panicNotifierMu sync.RWMutex
	panicNotifier   *notify.Notifier
)

// setPanicNotifier 注入 / 清除 panic 路径的通知出口。Run 启动时设入、退出时清空。
func setPanicNotifier(n *notify.Notifier) {
	panicNotifierMu.Lock()
	panicNotifier = n
	panicNotifierMu.Unlock()
}

// reportPanic 把 panic 现场无条件写到 fd 2。daemon 启动时 wireup 已经把 fd 2
// 重定向到 log/sing-router.err,所以这条信息会落盘,即使后续的 emitter/bus 因 panic
// 不再工作。不走 emitter 是因为 bus 是异步的,panic 路径上来不及保证投递。
//
// 若注入了 Notifier,额外做一次同步 best-effort 崩溃通知（panic 显式路径，见
// docs/adr/0001）：短超时、绕过引擎队列与节流，不依赖异步排空。
func reportPanic(name string, r any) {
	fmt.Fprintf(os.Stderr, "\n=== PANIC in %s @ %s ===\n%v\n%s=== END PANIC ===\n",
		name, time.Now().Format(time.RFC3339Nano), r, debug.Stack())

	panicNotifierMu.RLock()
	pn := panicNotifier
	panicNotifierMu.RUnlock()
	if pn == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	pn.NotifySync(ctx, notify.Notification{
		Kind:     "panic.recovered",
		Title:    "💥 sing-router 异常",
		Body:     fmt.Sprintf("%s 发生 panic（已恢复）：%v\n\n详见 log/sing-router.err。", name, r),
		Priority: notify.PriorityCritical,
		Time:     time.Now(),
	})
}
