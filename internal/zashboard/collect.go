package zashboard

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// 数据源文件路径（ASUS Merlin / Koolshare 标准位置）。
const (
	arpPath    = "/proc/net/arp"
	leasesPath = "/var/lib/misc/dnsmasq.leases"
)

// Collect 本地采集 4 个数据源。任一失败 → 对应字段置空 + append warning，不返回 error。
func Collect(ctx context.Context) (RawData, []string) {
	var raw RawData
	var warns []string

	if out, err := runCmd(ctx, "nvram", "get", "custom_clientlist"); err != nil {
		warns = append(warns, fmt.Sprintf("nvram custom_clientlist: %v", err))
	} else {
		raw.Clients = out
	}

	if b, err := os.ReadFile(arpPath); err != nil {
		warns = append(warns, fmt.Sprintf("read %s: %v", arpPath, err))
	} else {
		raw.ARP = string(b)
	}

	if b, err := os.ReadFile(leasesPath); err != nil {
		warns = append(warns, fmt.Sprintf("read %s: %v", leasesPath, err))
	} else {
		raw.Leases = string(b)
	}

	if out, err := runCmd(ctx, "ip", "-6", "neigh", "show"); err != nil {
		warns = append(warns, fmt.Sprintf("ip -6 neigh: %v", err))
	} else {
		raw.Neigh = out
	}

	return raw, warns
}

func runCmd(ctx context.Context, name string, args ...string) (string, error) {
	var stdout, stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		if msg := strings.TrimSpace(stderr.String()); msg != "" {
			return "", fmt.Errorf("%w: %s", err, msg)
		}
		return "", err
	}
	return stdout.String(), nil
}
