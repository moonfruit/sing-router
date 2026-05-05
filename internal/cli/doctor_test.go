package cli

import (
	"testing"

	"github.com/moonfruit/sing-router/internal/firmware"
)

func TestDoctorHookCheck_PresentRequiredPasses(t *testing.T) {
	got := doctorHookCheck(firmware.HookCheck{Type: "file", Path: "/x", Required: true, Present: true, Note: "n"})
	if got.Status != "pass" {
		t.Fatalf("status=%q want pass", got.Status)
	}
}

func TestDoctorHookCheck_AbsentRequiredFails(t *testing.T) {
	got := doctorHookCheck(firmware.HookCheck{Type: "file", Path: "/x", Required: true, Present: false})
	if got.Status != "fail" {
		t.Fatalf("status=%q want fail", got.Status)
	}
}

func TestDoctorHookCheck_AbsentOptionalWarns(t *testing.T) {
	got := doctorHookCheck(firmware.HookCheck{Type: "file", Path: "/x", Required: false, Present: false})
	if got.Status != "warn" {
		t.Fatalf("status=%q want warn", got.Status)
	}
}

func TestDoctorHookCheck_NameIncludesKindPrefix(t *testing.T) {
	got := doctorHookCheck(firmware.HookCheck{Type: "nvram", Path: "jffs2_scripts", Required: true, Present: true})
	if got.Name != "nvram: jffs2_scripts" {
		t.Fatalf("name=%q want %q", got.Name, "nvram: jffs2_scripts")
	}
}
