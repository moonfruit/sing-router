package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/moonfruit/sing2seq/clef"
	"github.com/spf13/cobra"

	"github.com/moonfruit/sing-router/internal/config"
	"github.com/moonfruit/sing-router/internal/log"
)

func newLogsCmd() *cobra.Command {
	var (
		rundir       string
		lines        int
		all          bool
		follow       bool
		followName   bool
		source       string
		level        string
		eventID      string
		asJSON       bool
		colorMode    string
		colorProfile string
	)
	cmd := &cobra.Command{
		Use:   "logs [FILE]",
		Short: "Render sing-router/sing-box CLEF logs from a file (defaults to the daemon's active log)",
		Long: `Render CLEF JSON Lines into human-readable form.

Without FILE the daemon's active log file (log/sing-router.log under rundir) is used:
the path is fetched from the running daemon's /api/v1/status when available, otherwise
derived from <rundir>/daemon.toml.

By default the last 200 lines are rendered. Use --all to render the whole file, or
-n N to pick a specific number. -f follows the open fd (stop on rotate); -F follows
the path (handle rotate / truncate / re-create).

Lines that don't parse as CLEF JSON (e.g. Go runtime panic stacks in stderr.log)
pass through verbatim.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if all && cmd.Flags().Changed("lines") {
				return fmt.Errorf("--all and --lines are mutually exclusive")
			}
			if follow && followName {
				return fmt.Errorf("-f and -F are mutually exclusive")
			}

			path, cfgProfile, err := resolveLogPath(cmd, rundir, args)
			if err != nil {
				return err
			}

			filter, err := parseLogsFilter(source, level, eventID)
			if err != nil {
				return err
			}

			out := cmd.OutOrStdout()
			profile := log.ProfileNone
			if !asJSON {
				profile, err = ResolveLogColor(colorMode, colorProfile, cfgProfile, out)
				if err != nil {
					return err
				}
			}

			tz := time.Local
			renderer := newLogRenderer(out, asJSON, tz, profile, filter)

			f, err := os.Open(path)
			if err != nil {
				return err
			}
			defer func() { _ = f.Close() }()

			startN := lines
			if all {
				startN = 0
			}
			if _, err := SeekToLastN(f, startN); err != nil {
				return err
			}

			if err := EmitLines(f, renderer.emit); err != nil {
				return err
			}

			if !follow && !followName {
				return nil
			}

			emitOff, err := f.Seek(0, io.SeekCurrent)
			if err != nil {
				return err
			}

			ctx, cancel := signal.NotifyContext(cmd.Context(), syscall.SIGINT, syscall.SIGTERM)
			defer cancel()

			return Follow(ctx, path, emitOff, renderer.emit, FollowConfig{FollowName: followName})
		},
	}
	cmd.Flags().StringVarP(&rundir, "rundir", "D", "", "Runtime root directory used when FILE is omitted (default /opt/home/sing-router)")
	cmd.Flags().IntVarP(&lines, "lines", "n", 200, "render the last N lines")
	cmd.Flags().BoolVarP(&all, "all", "a", false, "render the entire file (overrides -n)")
	cmd.Flags().BoolVarP(&follow, "follow", "f", false, "tail -f: follow the file by fd, stop on rotate")
	cmd.Flags().BoolVarP(&followName, "F", "F", false, "tail -F: follow the path, handle rotate/truncate/re-create")
	cmd.Flags().StringVar(&source, "source", "", "filter Source: all|daemon|sing-box (default all)")
	cmd.Flags().StringVar(&level, "level", "", "minimum level: trace|debug|info|warn|error|fatal")
	cmd.Flags().StringVar(&eventID, "event-id", "", "filter by EventID prefix")
	cmd.Flags().BoolVar(&asJSON, "json", false, "emit raw CLEF JSON lines (no rendering, no color)")
	cmd.Flags().StringVar(&colorMode, "color", "auto", "colorize output: auto|always|never")
	cmd.Flags().StringVar(&colorProfile, "color-profile", "", "color palette: auto|truecolor|256|8 (defaults to daemon.toml [log].color_profile, then env)")
	return cmd
}

// resolveLogPath 解析 logs 命令的目标文件路径与 daemon.toml 中的 color_profile。
// 顺序：
//  1. 显式 FILE 参数 → 直接用，cfgProfile 仍尽力从本地 daemon.toml 取
//  2. 在线 GET /api/v1/status → daemon.log_file 字段
//  3. 离线读 <rundir>/daemon.toml → filepath.Join(rundir, cfg.Log.File)
func resolveLogPath(cmd *cobra.Command, rundir string, args []string) (path string, cfgProfile string, err error) {
	if rundir == "" {
		rundir = "/opt/home/sing-router"
	}
	cfg, _ := config.LoadDaemonConfig(filepath.Join(rundir, "daemon.toml"))
	if cfg != nil {
		cfgProfile = cfg.Log.ColorProfile
	}

	if len(args) == 1 {
		return args[0], cfgProfile, nil
	}

	// 优先在线
	client := NewHTTPClient(getDaemonBase(cmd))
	var body map[string]any
	if err := client.GetJSON("/api/v1/status", &body); err == nil {
		if d, ok := body["daemon"].(map[string]any); ok {
			if lf, ok := d["log_file"].(string); ok && lf != "" {
				return lf, cfgProfile, nil
			}
		}
	}

	// 离线兜底
	if cfg == nil || cfg.Log.File == "" {
		return "", cfgProfile, fmt.Errorf("cannot determine log file; pass FILE explicitly or set --rundir")
	}
	return filepath.Join(rundir, cfg.Log.File), cfgProfile, nil
}

// logRenderer 把每行 emit 转化为渲染输出。零拷贝场景下 emit 的字节会被
// json.Unmarshal 解析；写出后进入下一行。
type logRenderer struct {
	out      io.Writer
	asJSON   bool
	tz       *time.Location
	profile  log.Profile
	filter   *logsFilter
	conn     *log.ConnColorizer
	scratch  []byte
	prettyOp log.PrettyOptions
}

func newLogRenderer(out io.Writer, asJSON bool, tz *time.Location, profile log.Profile, filter *logsFilter) *logRenderer {
	r := &logRenderer{
		out:     out,
		asJSON:  asJSON,
		tz:      tz,
		profile: profile,
		filter:  filter,
		conn:    log.NewConnColorizer(profile),
	}
	r.prettyOp = log.PrettyOptions{LocalTZ: tz, Profile: profile, Conn: r.conn}
	return r
}

func (r *logRenderer) emit(line []byte) {
	if len(line) == 0 {
		return
	}
	if r.asJSON {
		if !r.filter.matchLine(line) {
			return
		}
		r.scratch = append(r.scratch[:0], line...)
		r.scratch = append(r.scratch, '\n')
		_, _ = r.out.Write(r.scratch)
		return
	}
	// 非 JSON 行（如 stderr.log 的 panic 栈）原样输出，不应用过滤。
	if len(line) == 0 || line[0] != '{' {
		r.scratch = append(r.scratch[:0], line...)
		r.scratch = append(r.scratch, '\n')
		_, _ = r.out.Write(r.scratch)
		return
	}
	ev, err := decodeOrderedEvent(line)
	if err != nil {
		r.scratch = append(r.scratch[:0], line...)
		r.scratch = append(r.scratch, '\n')
		_, _ = r.out.Write(r.scratch)
		return
	}
	if !r.filter.matchEvent(ev) {
		return
	}
	fmt.Fprintln(r.out, log.Pretty(ev, r.prettyOp))
}

// decodeOrderedEvent 用 json.Decoder 保留键的相对顺序（Go 标准库不保证 map 顺序，
// 因此用 RawMessage 的两遍解析 + 顺序记录恢复）。
//
// 解析数字时启用 UseNumber，避免大整数（如 ConnectionId）被默认 float64 渲染成
// 科学计数法（"1.23e+08"）。json.Number 的底层是字符串，%v 直接输出原文。
func decodeOrderedEvent(line []byte) (*clef.Event, error) {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(line, &raw); err != nil {
		return nil, err
	}
	keys, err := jsonKeys(line)
	if err != nil {
		return nil, err
	}
	ev := clef.NewEvent()
	for _, k := range keys {
		if rv, ok := raw[k]; ok {
			dec := json.NewDecoder(bytes.NewReader(rv))
			dec.UseNumber()
			var v any
			_ = dec.Decode(&v)
			ev.Set(k, v)
		}
	}
	return ev, nil
}

// jsonKeys 顺序提取 JSON 文本的顶层 key。
func jsonKeys(s []byte) ([]string, error) {
	dec := json.NewDecoder(strings.NewReader(string(s)))
	if _, err := dec.Token(); err != nil {
		return nil, err
	}
	var keys []string
	for dec.More() {
		tok, err := dec.Token()
		if err != nil {
			return nil, err
		}
		key, ok := tok.(string)
		if !ok {
			return nil, errors.New("non-string key")
		}
		keys = append(keys, key)
		var skip json.RawMessage
		if err := dec.Decode(&skip); err != nil {
			return nil, err
		}
	}
	return keys, nil
}

