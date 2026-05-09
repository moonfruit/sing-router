package daemon

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"

	"github.com/moonfruit/sing2seq/clef"
)

const defaultLogsTailN = 100

// logsFilter 描述 /logs 的查询过滤条件。
type logsFilter struct {
	allowDaemon   bool
	allowSingBox  bool
	allowOther    bool
	minLevel      clef.Level
	eventIDPrefix string
}

func parseLogsFilter(q url.Values) (*logsFilter, error) {
	f := &logsFilter{minLevel: clef.LevelTrace}
	switch strings.ToLower(strings.TrimSpace(q.Get("source"))) {
	case "", "all":
		f.allowDaemon, f.allowSingBox, f.allowOther = true, true, true
	case "daemon":
		f.allowDaemon = true
	case "sing-box", "singbox":
		f.allowSingBox = true
	default:
		return nil, fmt.Errorf("invalid source")
	}
	if lv := strings.TrimSpace(q.Get("level")); lv != "" {
		l, err := clef.ParseLevel(lv)
		if err != nil {
			return nil, err
		}
		f.minLevel = l
	}
	f.eventIDPrefix = strings.TrimSpace(q.Get("event_id"))
	return f, nil
}

func (f *logsFilter) allowSource(src string) bool {
	switch src {
	case "daemon":
		return f.allowDaemon
	case "sing-box":
		return f.allowSingBox
	default:
		return f.allowOther
	}
}

func (f *logsFilter) matchEvent(ev *clef.Event) bool {
	srcAny, _ := ev.Get("Source")
	src, _ := srcAny.(string)
	if !f.allowSource(src) {
		return false
	}
	lvAny, _ := ev.Get("@l")
	lv, _ := lvAny.(string)
	if clef.FromCLEFName(lv) < f.minLevel {
		return false
	}
	if f.eventIDPrefix != "" {
		eidAny, _ := ev.Get("EventID")
		eid, _ := eidAny.(string)
		if !strings.HasPrefix(eid, f.eventIDPrefix) {
			return false
		}
	}
	return true
}

// matchLine 仅做必要字段的轻量解码，避免历史 tail 阶段对每行做完整 OrderedEvent 重建。
func (f *logsFilter) matchLine(line string) bool {
	var raw struct {
		Source  string `json:"Source"`
		Level   string `json:"@l"`
		EventID string `json:"EventID"`
	}
	if err := json.Unmarshal([]byte(line), &raw); err != nil {
		return false
	}
	if !f.allowSource(raw.Source) {
		return false
	}
	if clef.FromCLEFName(raw.Level) < f.minLevel {
		return false
	}
	if f.eventIDPrefix != "" && !strings.HasPrefix(raw.EventID, f.eventIDPrefix) {
		return false
	}
	return true
}

// tailLogFile 顺序扫描 path，返回最后 n 条匹配的行（chronological）。
// 文件不存在或为空时返回 nil。active 文件受 max_size_mb 约束，故无需逆向 seek。
func tailLogFile(path string, n int, f *logsFilter) ([]string, error) {
	if n <= 0 || path == "" {
		return nil, nil
	}
	file, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	defer func() { _ = file.Close() }()
	sc := bufio.NewScanner(file)
	sc.Buffer(make([]byte, 64*1024), 4*1024*1024)
	buf := make([]string, 0, n)
	for sc.Scan() {
		line := sc.Text()
		if line == "" {
			continue
		}
		if !f.matchLine(line) {
			continue
		}
		if len(buf) < n {
			buf = append(buf, line)
			continue
		}
		copy(buf, buf[1:])
		buf[n-1] = line
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	return buf, nil
}

// handleLogs 实现 GET /api/v1/logs。
// 参数：source(all|daemon|sing-box)、n（尾部 N，默认 100）、level、event_id、follow。
// 非 follow → NDJSON；follow → SSE，先回放历史，再订阅 Bus 推送新事件。
func handleLogs(deps APIDeps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writeError(w, http.StatusMethodNotAllowed, "method.not_allowed", "GET required", nil)
			return
		}
		q := r.URL.Query()
		filter, err := parseLogsFilter(q)
		if err != nil {
			writeError(w, http.StatusBadRequest, "logs.invalid_query", err.Error(), nil)
			return
		}
		n := defaultLogsTailN
		if v := q.Get("n"); v != "" {
			x, err := strconv.Atoi(v)
			if err != nil || x < 0 {
				writeError(w, http.StatusBadRequest, "logs.invalid_query", "n must be >= 0", nil)
				return
			}
			n = x
		}
		follow := q.Get("follow") == "true" || q.Get("follow") == "1"

		hist, err := tailLogFile(deps.LogFile, n, filter)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "logs.read_failed", err.Error(), nil)
			return
		}

		if !follow {
			w.Header().Set("Content-Type", "application/x-ndjson")
			w.WriteHeader(http.StatusOK)
			for _, line := range hist {
				_, _ = w.Write([]byte(line))
				_, _ = w.Write([]byte{'\n'})
			}
			return
		}

		flusher, ok := w.(http.Flusher)
		if !ok {
			writeError(w, http.StatusInternalServerError, "logs.no_flusher", "streaming not supported", nil)
			return
		}
		if deps.Bus == nil {
			writeError(w, http.StatusNotImplemented, "logs.no_bus", "follow not wired", nil)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Connection", "keep-alive")
		w.WriteHeader(http.StatusOK)
		for _, line := range hist {
			writeSSEData(w, []byte(line))
		}
		flusher.Flush()

		eventCh := make(chan *clef.Event, 256)
		sub := deps.Bus.Subscribe(clef.SubscriberFunc{
			MatchFn: func(ev *clef.Event) bool { return filter.matchEvent(ev) },
			DeliverFn: func(ev *clef.Event) {
				select {
				case eventCh <- ev:
				default: // 客户端跟不上 → lossy 丢弃
				}
			},
		})
		defer sub.Unsubscribe()

		daemonDone := daemonDoneChan(deps.Ctx)
		clientDone := r.Context().Done()
		for {
			select {
			case <-clientDone:
				return
			case <-daemonDone:
				return
			case ev := <-eventCh:
				data, err := json.Marshal(ev)
				if err != nil {
					continue
				}
				writeSSEData(w, data)
				flusher.Flush()
			}
		}
	}
}

func writeSSEData(w http.ResponseWriter, data []byte) {
	_, _ = w.Write([]byte("data: "))
	_, _ = w.Write(data)
	_, _ = w.Write([]byte("\n\n"))
}

// daemonDoneChan 让 SSE 在 deps.Ctx 缺省时退化为永不触发的 chan，
// 避免在测试里强制要求注入 Ctx。
func daemonDoneChan(ctx context.Context) <-chan struct{} {
	if ctx == nil {
		return nil
	}
	return ctx.Done()
}
