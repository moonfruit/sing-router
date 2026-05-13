package cli

import (
	"bytes"
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

func TestDoctorHookCheck_FileTypeOmitsPrefix(t *testing.T) {
	got := doctorHookCheck(firmware.HookCheck{Type: "file", Path: "/koolshare/init.d/N99sing-router.sh", Required: true, Present: true})
	if got.Name != "/koolshare/init.d/N99sing-router.sh" {
		t.Fatalf("name=%q want path without 'file:' prefix", got.Name)
	}
}

func TestResolveColor(t *testing.T) {
	t.Setenv("NO_COLOR", "")
	buf := &bytes.Buffer{}

	cases := []struct {
		name    string
		mode    string
		noColor string
		want    bool
		wantErr bool
	}{
		{name: "auto on non-tty disables color", mode: "auto", want: false},
		{name: "empty mode treated as auto", mode: "", want: false},
		{name: "always forces on", mode: "always", want: true},
		{name: "never forces off", mode: "never", want: false},
		{name: "NO_COLOR disables auto", mode: "auto", noColor: "1", want: false},
		{name: "always overrides NO_COLOR", mode: "always", noColor: "1", want: true},
		{name: "invalid mode errors", mode: "rainbow", wantErr: true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Setenv("NO_COLOR", c.noColor)
			got, err := resolveColor(c.mode, buf)
			if c.wantErr {
				if err == nil {
					t.Fatalf("expected error for mode=%q", c.mode)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != c.want {
				t.Fatalf("got=%v want=%v", got, c.want)
			}
		})
	}
}

func TestPrintDoctor_ColorWrapsMarker(t *testing.T) {
	checks := []doctorCheck{
		{Name: "x", Status: "pass"},
		{Name: "y", Status: "fail", Detail: "boom"},
	}

	plain := &bytes.Buffer{}
	if err := printDoctor(plain, checks, false, false); err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(plain.Bytes(), []byte("\x1b[")) {
		t.Fatalf("plain output must not contain ANSI: %q", plain.String())
	}

	colored := &bytes.Buffer{}
	if err := printDoctor(colored, checks, false, true); err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(colored.Bytes(), []byte(ansiGreen+"PASS"+ansiReset)) {
		t.Fatalf("colored output missing green PASS: %q", colored.String())
	}
	if !bytes.Contains(colored.Bytes(), []byte(ansiRed+"FAIL"+ansiReset)) {
		t.Fatalf("colored output missing red FAIL: %q", colored.String())
	}

	jsonBuf := &bytes.Buffer{}
	if err := printDoctor(jsonBuf, checks, true, true); err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(jsonBuf.Bytes(), []byte("\x1b[")) {
		t.Fatalf("json output must never contain ANSI even when useColor=true: %q", jsonBuf.String())
	}
}
