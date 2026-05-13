package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/spf13/cobra"

	"github.com/moonfruit/sing-router/internal/config"
	"github.com/moonfruit/sing-router/internal/firmware"
)

type doctorOpts struct {
	skipRouting bool // --skip-routing
	rulesOnly   bool // --rules-only
}

type doctorCheck struct {
	Name   string `json:"name"`
	Status string `json:"status"` // pass | warn | fail | info
	Detail string `json:"detail,omitempty"`
}

func newDoctorCmd() *cobra.Command {
	var (
		rundir    string
		asJSON    bool
		colorMode string
		opts      doctorOpts
	)
	cmd := &cobra.Command{
		Use:   "doctor",
		Short: "Read-only health check of all sing-router files and runtime expectations",
		RunE: func(cmd *cobra.Command, args []string) error {
			if rundir == "" {
				rundir = "/opt/home/sing-router"
			}
			useColor, err := resolveColor(colorMode, cmd.OutOrStdout())
			if err != nil {
				return err
			}
			checks := runDoctorChecks(rundir, opts)
			return printDoctor(cmd.OutOrStdout(), checks, asJSON, useColor)
		},
	}
	cmd.Flags().StringVarP(&rundir, "rundir", "D", "", "Runtime root directory")
	cmd.Flags().BoolVar(&asJSON, "json", false, "JSON output")
	cmd.Flags().BoolVar(&opts.skipRouting, "skip-routing", false, "Skip runtime ip/iptables checks entirely")
	cmd.Flags().BoolVar(&opts.rulesOnly, "rules-only", false, "Only run runtime ip/iptables checks (skip file/dir/firmware checks)")
	cmd.Flags().StringVar(&colorMode, "color", "auto", "Colorize PASS/WARN/FAIL/INFO output: auto|always|never")
	return cmd
}

func runDoctorChecks(rundir string, opts doctorOpts) []doctorCheck {
	var out []doctorCheck

	fileExists := func(path string) bool {
		info, err := os.Stat(path)
		return err == nil && !info.IsDir()
	}

	cfg, _ := config.LoadDaemonConfig(filepath.Join(rundir, "daemon.toml"))

	if !opts.rulesOnly {
		out = append(out, checkExistsExec("/opt/sbin/sing-router"))
		out = append(out, checkDirExists(rundir, "rundir"))
		for _, sub := range []string{"config.d", "bin", "var", "run", "log"} {
			out = append(out, checkDirExists(filepath.Join(rundir, sub), "rundir/"+sub))
		}
		out = append(out, checkExistsExec(filepath.Join(rundir, "bin", "sing-box")))
		for _, c := range []string{"clash.json", "dns.json", "inbounds.json", "log.json", "zoo.json"} {
			out = append(out, checkExistsAs(filepath.Join(rundir, "config.d", c), "config.d/"+c, "fail"))
		}
		out = append(out, checkExistsAs(filepath.Join(rundir, "var", "cn.txt"), "var/cn.txt", "warn"))
		out = append(out, checkExistsExec("/opt/etc/init.d/S99sing-router"))

		// Firmware target + hook checks.
		kind := cfg.Install.Firmware
		if kind == "" {
			kind = "unknown"
		}
		out = append(out, doctorCheck{Name: "firmware target", Status: "info", Detail: kind})
		if target, err := firmware.ByName(kind); err == nil {
			for _, hc := range target.VerifyHooks() {
				out = append(out, doctorHookCheck(hc))
			}
		}

		// dns.json inet4_range consistency
		dnsPath := filepath.Join(rundir, "config.d", "dns.json")
		if fileExists(dnsPath) {
			data, _ := os.ReadFile(dnsPath)
			if strings.Contains(string(data), `"inet4_range": "22.0.0.0/8"`) {
				out = append(out, doctorCheck{Name: "dns.json inet4_range", Status: "warn", Detail: "still 22.0.0.0/8; daemon expects 28.0.0.0/8"})
			} else {
				out = append(out, doctorCheck{Name: "dns.json inet4_range", Status: "pass"})
			}
		}
		// log.timestamp = true
		logPath := filepath.Join(rundir, "config.d", "log.json")
		if fileExists(logPath) {
			data, _ := os.ReadFile(logPath)
			if strings.Contains(string(data), `"timestamp": true`) {
				out = append(out, doctorCheck{Name: "log.json timestamp", Status: "pass"})
			} else {
				out = append(out, doctorCheck{Name: "log.json timestamp", Status: "warn", Detail: "must be true; otherwise sing-box log parsing degrades"})
			}
		}
	}

	out = append(out, runRoutingChecks(cfg, opts)...)
	return out
}

// runRoutingChecks 默认就跑路由/iptables 检查；--skip-routing 才跳过。
// 非 Linux 平台天然跳过；TUN 缺失 / iptables 不通会落到下层 check 的 FAIL，
// 这是用户希望看到的真实信号，不应在上游沉默吞掉。
func runRoutingChecks(cfg *config.DaemonConfig, opts doctorOpts) []doctorCheck {
	if opts.skipRouting {
		return []doctorCheck{{Name: "routing checks", Status: "info", Detail: "skipped (--skip-routing)"}}
	}
	if runtime.GOOS != "linux" {
		return []doctorCheck{{Name: "routing checks", Status: "info", Detail: "skipped on " + runtime.GOOS}}
	}
	return checkRouting(config.LoadRouting(cfg))
}

func doctorHookCheck(hc firmware.HookCheck) doctorCheck {
	name := hc.Path
	if hc.Type != "file" {
		name = hc.Type + ": " + hc.Path
	}
	if hc.Present {
		return doctorCheck{Name: name, Status: "pass", Detail: hc.Note}
	}
	status := "warn"
	if hc.Required {
		status = "fail"
	}
	return doctorCheck{Name: name, Status: status, Detail: hc.Note}
}

func checkExistsExec(path string) doctorCheck {
	info, err := os.Stat(path)
	if err != nil {
		return doctorCheck{Name: path, Status: "fail", Detail: err.Error()}
	}
	if info.Mode().Perm()&0o100 == 0 {
		return doctorCheck{Name: path, Status: "fail", Detail: "not executable"}
	}
	return doctorCheck{Name: path, Status: "pass"}
}

func checkDirExists(path, label string) doctorCheck {
	info, err := os.Stat(path)
	if err != nil {
		return doctorCheck{Name: label, Status: "fail", Detail: err.Error()}
	}
	if !info.IsDir() {
		return doctorCheck{Name: label, Status: "fail", Detail: "not a directory"}
	}
	return doctorCheck{Name: label, Status: "pass"}
}

func checkExistsAs(path, label, warnOrFail string) doctorCheck {
	if _, err := os.Stat(path); err != nil {
		return doctorCheck{Name: label, Status: warnOrFail, Detail: err.Error()}
	}
	return doctorCheck{Name: label, Status: "pass"}
}

func printDoctor(w io.Writer, checks []doctorCheck, asJSON, useColor bool) error {
	if asJSON {
		return json.NewEncoder(w).Encode(checks)
	}
	for _, c := range checks {
		marker := "PASS"
		switch c.Status {
		case "warn":
			marker = "WARN"
		case "fail":
			marker = "FAIL"
		case "info":
			marker = "INFO"
		}
		marker = colorize(useColor, ansiCodeFor(c.Status), marker)
		if c.Detail == "" {
			fmt.Fprintf(w, "  %s  %s\n", marker, c.Name)
		} else {
			fmt.Fprintf(w, "  %s  %s — %s\n", marker, c.Name, c.Detail)
		}
	}
	return nil
}

// ----------------------- 着色辅助 -----------------------

const (
	ansiReset  = "\x1b[0m"
	ansiGreen  = "\x1b[32m"
	ansiYellow = "\x1b[33m"
	ansiRed    = "\x1b[31m"
	ansiCyan   = "\x1b[36m"
)

// resolveColor 把 --color 取值（auto/always/never）+ stdout TTY 状态 + NO_COLOR 环境变量
// 解析为最终是否着色。NO_COLOR（任意非空值）等同于 --color=never，但 --color=always 显式压过。
func resolveColor(mode string, w io.Writer) (bool, error) {
	switch mode {
	case "always":
		return true, nil
	case "never":
		return false, nil
	case "", "auto":
		if os.Getenv("NO_COLOR") != "" {
			return false, nil
		}
		return isTerminal(w), nil
	default:
		return false, fmt.Errorf("--color must be auto|always|never, got %q", mode)
	}
}

// isTerminal 判定 w 是否为字符设备（终端）。不引入第三方依赖：os.File.Stat() + ModeCharDevice
// 在 darwin/linux 上都可用；非 *os.File（如测试里的 bytes.Buffer）一律视为非 TTY。
func isTerminal(w io.Writer) bool {
	f, ok := w.(*os.File)
	if !ok {
		return false
	}
	info, err := f.Stat()
	if err != nil {
		return false
	}
	return info.Mode()&os.ModeCharDevice != 0
}

func ansiCodeFor(status string) string {
	switch status {
	case "warn":
		return ansiYellow
	case "fail":
		return ansiRed
	case "info":
		return ansiCyan
	default:
		return ansiGreen
	}
}

func colorize(useColor bool, code, s string) string {
	if !useColor {
		return s
	}
	return code + s + ansiReset
}
