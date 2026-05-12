package daemon

import (
	"fmt"
	"os"
	"runtime/debug"
	"time"
)

// reportPanic 把 panic 现场无条件写到 fd 2。daemon 启动时 wireup 已经把 fd 2
// 重定向到 log/stderr.log,所以这条信息会落盘,即使后续的 emitter/bus 因 panic
// 不再工作。不走 emitter 是因为 bus 是异步的,panic 路径上来不及保证投递。
func reportPanic(name string, r any) {
	fmt.Fprintf(os.Stderr, "\n=== PANIC in %s @ %s ===\n%v\n%s=== END PANIC ===\n",
		name, time.Now().Format(time.RFC3339Nano), r, debug.Stack())
}
