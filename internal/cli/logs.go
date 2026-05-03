package cli

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	log "github.com/moonfruit/sing-router/internal/log"
)

func newLogsCmd() *cobra.Command {
	var (
		source  string
		n       int
		follow  bool
		level   string
		eventID string
		asJSON  bool
	)
	cmd := &cobra.Command{
		Use:   "logs",
		Short: "Show daemon + sing-box logs",
		RunE: func(cmd *cobra.Command, args []string) error {
			client := NewHTTPClient(getDaemonBase(cmd))
			q := url.Values{}
			if source != "" {
				q.Set("source", source)
			}
			if n > 0 {
				q.Set("n", strconv.Itoa(n))
			}
			if level != "" {
				q.Set("level", level)
			}
			if eventID != "" {
				q.Set("event_id", eventID)
			}
			if follow {
				q.Set("follow", "true")
			}
			resp, err := client.GetStream("/api/v1/logs", q)
			if err != nil {
				if IsDaemonNotRunning(err) {
					return fmt.Errorf("daemon not running")
				}
				return err
			}
			defer func() { _ = resp.Body.Close() }()
			return streamLogs(cmd.OutOrStdout(), resp.Body, asJSON, time.Local)
		},
	}
	cmd.Flags().StringVar(&source, "source", "", "all|daemon|sing-box")
	cmd.Flags().IntVarP(&n, "n", "n", 100, "tail N lines (history)")
	cmd.Flags().BoolVarP(&follow, "follow", "f", false, "follow new events via SSE")
	cmd.Flags().StringVar(&level, "level", "", "min level (trace|debug|info|warn|error|fatal)")
	cmd.Flags().StringVar(&eventID, "event-id", "", "filter by EventID prefix")
	cmd.Flags().BoolVar(&asJSON, "json", false, "emit raw CLEF JSON lines")
	return cmd
}

// streamLogs 把 resp.Body 当 NDJSON 处理，每行用 pretty 渲染。
// SSE 流由 daemon 用 `data: {...}\n\n` 编码；这里接受两种：纯 NDJSON，或 SSE。
func streamLogs(out io.Writer, r io.Reader, asJSON bool, tz *time.Location) error {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64*1024), 4*1024*1024)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		// SSE: 取 "data:" 后面的部分
		if strings.HasPrefix(line, "data:") {
			line = strings.TrimSpace(line[5:])
			if line == "" {
				continue
			}
		}
		if asJSON {
			fmt.Fprintln(out, line)
			continue
		}
		ev, err := decodeOrderedEvent(line)
		if err != nil {
			fmt.Fprintln(out, line)
			continue
		}
		fmt.Fprintln(out, log.Pretty(ev, log.PrettyOptions{LocalTZ: tz, DisableColor: false}))
	}
	return sc.Err()
}

// decodeOrderedEvent 用 json.Decoder 保留键的相对顺序（Go 标准库不保证 map 顺序，
// 因此用 RawMessage 的两遍解析 + 顺序记录恢复）。
func decodeOrderedEvent(line string) (*log.OrderedEvent, error) {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal([]byte(line), &raw); err != nil {
		return nil, err
	}
	keys, err := jsonKeys(line)
	if err != nil {
		return nil, err
	}
	ev := log.NewEvent()
	for _, k := range keys {
		if rv, ok := raw[k]; ok {
			var v any
			_ = json.Unmarshal(rv, &v)
			ev.Set(k, v)
		}
	}
	return ev, nil
}

// jsonKeys 顺序提取 JSON 文本的顶层 key。
func jsonKeys(s string) ([]string, error) {
	dec := json.NewDecoder(strings.NewReader(s))
	if _, err := dec.Token(); err != nil {
		return nil, err
	}
	var keys []string
	for dec.More() {
		tok, err := dec.Token()
		if err != nil {
			return nil, err
		}
		keys = append(keys, tok.(string))
		var skip json.RawMessage
		if err := dec.Decode(&skip); err != nil {
			return nil, err
		}
	}
	return keys, nil
}
