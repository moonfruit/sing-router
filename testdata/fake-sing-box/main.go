// fake-sing-box 是测试专用的 sing-box 桩。它按 flag 指定的端口开 TCP listener
// 与 fake clash API，可以延迟 ready、运行中崩溃、pre-ready 退出，方便驱动 supervisor。
package main

import (
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"
)

func main() {
	var (
		ports         = flag.String("listen", "", "comma-separated TCP ports to bind (skip if empty)")
		clashPort     = flag.Int("clash-port", 0, "clash API port; 0 = disabled")
		readyDelay    = flag.Duration("ready-delay", 0, "wait before binding listeners")
		crashAfter    = flag.Duration("crash-after", 0, "panic after duration; 0 = never")
		preReadyFail  = flag.Bool("pre-ready-fail", false, "exit code 1 immediately")
		emitLog       = flag.Duration("emit-log", 0, "emit a sing-box-like stderr line every N; 0 = no")
		timestampLine = flag.Bool("timestamp", true, "include timezone+date+time prefix in emitted log lines")
	)
	flag.Parse()

	if *preReadyFail {
		fmt.Fprintln(os.Stderr, "fake-sing-box: pre-ready failure")
		os.Exit(1)
	}

	if *readyDelay > 0 {
		time.Sleep(*readyDelay)
	}

	listeners := []net.Listener{}
	for _, p := range splitPorts(*ports) {
		l, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", p))
		if err != nil {
			fmt.Fprintln(os.Stderr, "listen", p, "err:", err)
			os.Exit(1)
		}
		go acceptLoop(l)
		listeners = append(listeners, l)
	}
	if *clashPort > 0 {
		mux := http.NewServeMux()
		mux.HandleFunc("/version", func(w http.ResponseWriter, r *http.Request) {
			_, _ = io.WriteString(w, `{"version":"fake-1.0.0"}`)
		})
		go func() {
			_ = http.ListenAndServe(fmt.Sprintf("127.0.0.1:%d", *clashPort), mux)
		}()
	}

	if *emitLog > 0 {
		go func() {
			t := time.NewTicker(*emitLog)
			for range t.C {
				emit(*timestampLine, "INFO", "router[default]", "outbound connection to fake.example.com:443")
			}
		}()
	}

	if *crashAfter > 0 {
		time.AfterFunc(*crashAfter, func() {
			panic("fake-sing-box scheduled crash")
		})
	}

	// 等待信号优雅退出
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	<-sig
	for _, l := range listeners {
		_ = l.Close()
	}
}

func splitPorts(s string) []int {
	if s == "" {
		return nil
	}
	var out []int
	for _, raw := range strings.Split(s, ",") {
		var n int
		_, _ = fmt.Sscanf(raw, "%d", &n)
		if n > 0 {
			out = append(out, n)
		}
	}
	return out
}

func acceptLoop(l net.Listener) {
	for {
		c, err := l.Accept()
		if err != nil {
			return
		}
		_ = c.Close()
	}
}

func emit(timestamp bool, level, mod, detail string) {
	if timestamp {
		now := time.Now()
		fmt.Fprintf(os.Stderr, "%s %s %s %s %s: %s\n",
			now.Format("-0700"),
			now.Format("2006-01-02"),
			now.Format("15:04:05.000"),
			level, mod, detail,
		)
	} else {
		fmt.Fprintf(os.Stderr, "%s %s: %s\n", level, mod, detail)
	}
}
