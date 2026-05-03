package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
)

type doctorCheck struct {
	Name   string `json:"name"`
	Status string `json:"status"` // pass | warn | fail
	Detail string `json:"detail,omitempty"`
}

func newDoctorCmd() *cobra.Command {
	var (
		rundir string
		asJSON bool
	)
	cmd := &cobra.Command{
		Use:   "doctor",
		Short: "Read-only health check of all sing-router files and runtime expectations",
		RunE: func(cmd *cobra.Command, args []string) error {
			if rundir == "" {
				rundir = "/opt/home/sing-router"
			}
			checks := runDoctorChecks(rundir)
			return printDoctor(cmd.OutOrStdout(), checks, asJSON)
		},
	}
	cmd.Flags().StringVarP(&rundir, "rundir", "D", "", "Runtime root directory")
	cmd.Flags().BoolVar(&asJSON, "json", false, "JSON output")
	return cmd
}

func runDoctorChecks(rundir string) []doctorCheck {
	var out []doctorCheck

	fileExists := func(path string) bool {
		info, err := os.Stat(path)
		return err == nil && !info.IsDir()
	}

	out = append(out, checkExistsExec("/opt/sbin/sing-router"))
	out = append(out, checkDirExists(rundir, "rundir"))
	for _, sub := range []string{"config.d", "bin", "var", "run", "log"} {
		out = append(out, checkDirExists(filepath.Join(rundir, sub), "rundir/"+sub))
	}
	out = append(out, checkExistsExec(filepath.Join(rundir, "bin", "sing-box")))
	for _, c := range []string{"clash.json", "dns.json", "inbounds.json", "log.json"} {
		out = append(out, checkExistsAs(filepath.Join(rundir, "config.d", c), "config.d/"+c, "fail"))
	}
	out = append(out, checkExistsAs(filepath.Join(rundir, "config.d", "zoo.json"), "config.d/zoo.json", "warn"))
	out = append(out, checkExistsAs(filepath.Join(rundir, "var", "cn.txt"), "var/cn.txt", "warn"))
	out = append(out, checkExistsExec("/opt/etc/init.d/S99sing-router"))
	out = append(out, checkJffsHook("/jffs/scripts/nat-start"))
	out = append(out, checkJffsHook("/jffs/scripts/services-start"))

	// dns.json inet4_range 与 routing FAKEIP 一致性检查（spec 6.4 hint）
	dnsPath := filepath.Join(rundir, "config.d", "dns.json")
	if fileExists(dnsPath) {
		data, _ := os.ReadFile(dnsPath)
		if strings.Contains(string(data), `"inet4_range": "22.0.0.0/8"`) {
			out = append(out, doctorCheck{Name: "dns.json inet4_range", Status: "warn", Detail: "still 22.0.0.0/8; daemon expects 28.0.0.0/8"})
		} else {
			out = append(out, doctorCheck{Name: "dns.json inet4_range", Status: "pass"})
		}
	}
	// log.timestamp = true（vendored sing2seq parser 硬依赖）
	logPath := filepath.Join(rundir, "config.d", "log.json")
	if fileExists(logPath) {
		data, _ := os.ReadFile(logPath)
		if strings.Contains(string(data), `"timestamp": true`) {
			out = append(out, doctorCheck{Name: "log.json timestamp", Status: "pass"})
		} else {
			out = append(out, doctorCheck{Name: "log.json timestamp", Status: "warn", Detail: "must be true; otherwise sing-box log parsing degrades"})
		}
	}
	return out
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

func checkJffsHook(path string) doctorCheck {
	data, err := os.ReadFile(path)
	if err != nil {
		return doctorCheck{Name: path, Status: "fail", Detail: err.Error()}
	}
	if !strings.Contains(string(data), "BEGIN sing-router") {
		return doctorCheck{Name: path, Status: "fail", Detail: "BEGIN sing-router block missing"}
	}
	return doctorCheck{Name: path, Status: "pass"}
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
		}
		if c.Detail == "" {
			fmt.Fprintf(w, "  %s  %s\n", marker, c.Name)
		} else {
			fmt.Fprintf(w, "  %s  %s — %s\n", marker, c.Name, c.Detail)
		}
	}
	return nil
}
