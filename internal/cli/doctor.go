package cli

import (
	"encoding/json"
	"errors"
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
	forceRouting bool // --check-routing
	skipRouting  bool // --skip-routing
	rulesOnly    bool // --rules-only
}

type doctorCheck struct {
	Name   string `json:"name"`
	Status string `json:"status"` // pass | warn | fail | info
	Detail string `json:"detail,omitempty"`
}

func newDoctorCmd() *cobra.Command {
	var (
		rundir string
		asJSON bool
		opts   doctorOpts
	)
	cmd := &cobra.Command{
		Use:   "doctor",
		Short: "Read-only health check of all sing-router files and runtime expectations",
		RunE: func(cmd *cobra.Command, args []string) error {
			if opts.forceRouting && opts.skipRouting {
				return errors.New("--check-routing and --skip-routing are mutually exclusive")
			}
			if rundir == "" {
				rundir = "/opt/home/sing-router"
			}
			checks := runDoctorChecks(rundir, opts)
			return printDoctor(cmd.OutOrStdout(), checks, asJSON)
		},
	}
	cmd.Flags().StringVarP(&rundir, "rundir", "D", "", "Runtime root directory")
	cmd.Flags().BoolVar(&asJSON, "json", false, "JSON output")
	cmd.Flags().BoolVar(&opts.forceRouting, "check-routing", false, "Force runtime ip/iptables checks even if sing-box appears down")
	cmd.Flags().BoolVar(&opts.skipRouting, "skip-routing", false, "Skip runtime ip/iptables checks entirely")
	cmd.Flags().BoolVar(&opts.rulesOnly, "rules-only", false, "Only run runtime ip/iptables checks (skip file/dir/firmware checks)")
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

// runRoutingChecks 在 Linux 上按 flag 与 TUN 状态决定是否实际跑路由/iptables 检查。
func runRoutingChecks(cfg *config.DaemonConfig, opts doctorOpts) []doctorCheck {
	if opts.skipRouting {
		return []doctorCheck{{Name: "routing checks", Status: "info", Detail: "skipped (--skip-routing)"}}
	}
	if runtime.GOOS != "linux" {
		return []doctorCheck{{Name: "routing checks", Status: "info", Detail: "skipped on " + runtime.GOOS}}
	}
	r := config.LoadRouting(cfg)
	if !opts.forceRouting && !tunExists(r.Tun) {
		return []doctorCheck{{
			Name:   "routing checks",
			Status: "info",
			Detail: "skipped (TUN " + r.Tun + " absent; sing-box not running; use --check-routing to force)",
		}}
	}
	return checkRouting(r)
}

func doctorHookCheck(hc firmware.HookCheck) doctorCheck {
	prefix := hc.Type + ":"
	name := prefix + " " + hc.Path
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

func printDoctor(w io.Writer, checks []doctorCheck, asJSON bool) error {
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
		if c.Detail == "" {
			fmt.Fprintf(w, "  %s  %s\n", marker, c.Name)
		} else {
			fmt.Fprintf(w, "  %s  %s — %s\n", marker, c.Name, c.Detail)
		}
	}
	return nil
}
