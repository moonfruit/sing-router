package main

import (
	"fmt"
	"os"
	"runtime/debug"
	"time"

	"github.com/moonfruit/sing-router/internal/cli"
)

func main() {
	defer func() {
		if r := recover(); r != nil {
			// daemon 模式下 fd 2 已被 wireup 重定向到 log/stderr.log;
			// 其它 CLI 子命令的 panic 直接落到调用方终端。
			fmt.Fprintf(os.Stderr, "\n=== PANIC in main @ %s ===\n%v\n%s=== END PANIC ===\n",
				time.Now().Format(time.RFC3339Nano), r, debug.Stack())
			os.Exit(2)
		}
	}()
	if err := cli.NewRootCmd().Execute(); err != nil {
		os.Exit(1)
	}
}
