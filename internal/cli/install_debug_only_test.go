package cli

import (
	"bytes"
	"strings"
	"testing"
)

func runInstallDryRun(t *testing.T, extra ...string) string {
	t.Helper()
	rundir := t.TempDir()
	cmd := newInstallCmd()
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetErr(&buf)
	args := append([]string{"-D", rundir, "--firmware=koolshare", "--dry-run"}, extra...)
	cmd.SetArgs(args)
	if err := cmd.Execute(); err != nil {
		t.Fatalf("install execute: %v\noutput: %s", err, buf.String())
	}
	return buf.String()
}

func TestInstall_DebugOnlySkipsInitdAndFirmware(t *testing.T) {
	out := runInstallDryRun(t, "--debug-only")

	mustNotContain(t, out, "write /opt/etc/init.d/S99sing-router")
	mustNotContain(t, out, "install firmware hooks")
	mustContain(t, out, "skipped /opt/etc/init.d/S99sing-router (--debug-only)")
	mustContain(t, out, "skipped firmware hook installation (--debug-only)")
	mustContain(t, out, "Debug seed complete")
	mustContain(t, out, "sing-router daemon -D")
}

func TestInstall_DebugOnlyAutoStartIgnored(t *testing.T) {
	out := runInstallDryRun(t, "--debug-only", "--start=true")

	mustNotContain(t, out, "start init.d service")
	mustContain(t, out, "ignoring --start because of --debug-only")
}

func TestInstall_NoDebugOnlyStillWritesInitd(t *testing.T) {
	out := runInstallDryRun(t, "--skip-firmware-hooks")

	mustContain(t, out, "write /opt/etc/init.d/S99sing-router")
	mustNotContain(t, out, "Debug seed complete")
	mustContain(t, out, "Next steps:")
}

func mustContain(t *testing.T, haystack, needle string) {
	t.Helper()
	if !strings.Contains(haystack, needle) {
		t.Errorf("expected output to contain %q, got:\n%s", needle, haystack)
	}
}

func mustNotContain(t *testing.T, haystack, needle string) {
	t.Helper()
	if strings.Contains(haystack, needle) {
		t.Errorf("expected output to NOT contain %q, got:\n%s", needle, haystack)
	}
}
