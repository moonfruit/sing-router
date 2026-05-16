package firmware

import (
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
)

func newTestMerlin(t *testing.T, nv nvramReader) *merlin {
	t.Helper()
	a := fstest.MapFS{
		"firmware/merlin/nat-start.snippet": &fstest.MapFile{
			Data: []byte("# BEGIN sing-router (managed by `sing-router install`; do not edit)\n[ -x \"{{.Binary}}\" ] && \"{{.Binary}}\" reapply-rules >/dev/null 2>&1 &\n# END sing-router\n"),
		},
		"firmware/merlin/services-start.snippet": &fstest.MapFile{
			Data: []byte("# BEGIN sing-router (managed by `sing-router install`; do not edit)\n/opt/etc/init.d/S99sing-router start &\n# END sing-router\n"),
		},
	}
	return &merlin{base: t.TempDir(), assets: a, nvram: nv}
}

const testMerlinBinary = "/opt/sbin/sing-router"

func TestMerlinKind(t *testing.T) {
	m := newTestMerlin(t, fakeNvram{})
	if m.Kind() != KindMerlin {
		t.Fatalf("Kind=%q want %q", m.Kind(), KindMerlin)
	}
}

func TestMerlinInstallHooksInjectsBlocks(t *testing.T) {
	m := newTestMerlin(t, fakeNvram{})
	if err := m.InstallHooks("", testMerlinBinary); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"nat-start", "services-start"} {
		path := filepath.Join(m.base, "jffs/scripts", name)
		data := readFileT(t, path)
		if !strings.Contains(data, "# BEGIN sing-router") {
			t.Errorf("%s missing BEGIN marker:\n%s", name, data)
		}
		if !strings.Contains(data, "# END sing-router") {
			t.Errorf("%s missing END marker:\n%s", name, data)
		}
	}
}

// 守护方案 1 的关键不变量：Merlin nat-start 也必须用绝对路径调用 sing-router，
// 同样的 PATH 限制（/sbin:/usr/sbin:/bin:/usr/bin，不含 /opt/sbin）。
func TestMerlinNatStartRendersAbsoluteBinaryPath(t *testing.T) {
	m := newTestMerlin(t, fakeNvram{})
	if err := m.InstallHooks("", "/custom/path/sing-router"); err != nil {
		t.Fatal(err)
	}
	data := readFileT(t, filepath.Join(m.base, "jffs/scripts", "nat-start"))
	if !strings.Contains(data, "/custom/path/sing-router") {
		t.Fatalf("nat-start missing requested binary path:\n%s", data)
	}
	if strings.Contains(data, "{{") || strings.Contains(data, "}}") {
		t.Fatalf("nat-start still contains unrendered template syntax:\n%s", data)
	}
	if strings.Contains(data, "which sing-router") {
		t.Fatalf("nat-start must not use `which sing-router` PATH lookup:\n%s", data)
	}
}

func TestMerlinInstallHooksIdempotent(t *testing.T) {
	m := newTestMerlin(t, fakeNvram{})
	if err := m.InstallHooks("", testMerlinBinary); err != nil {
		t.Fatal(err)
	}
	if err := m.InstallHooks("", testMerlinBinary); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"nat-start", "services-start"} {
		path := filepath.Join(m.base, "jffs/scripts", name)
		data := readFileT(t, path)
		if strings.Count(data, "# BEGIN sing-router") != 1 {
			t.Errorf("%s should have exactly one BEGIN block, got:\n%s", name, data)
		}
	}
}

func TestMerlinRemoveHooksWhenAbsent(t *testing.T) {
	m := newTestMerlin(t, fakeNvram{})
	if err := m.RemoveHooks(); err != nil {
		t.Fatalf("RemoveHooks on absent hooks should be nil, got %v", err)
	}
}

func TestMerlinRemoveHooks(t *testing.T) {
	m := newTestMerlin(t, fakeNvram{})
	_ = m.InstallHooks("", testMerlinBinary)
	if err := m.RemoveHooks(); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"nat-start", "services-start"} {
		path := filepath.Join(m.base, "jffs/scripts", name)
		data := readFileT(t, path)
		if strings.Contains(data, "BEGIN sing-router") {
			t.Errorf("%s still has block after Remove:\n%s", name, data)
		}
	}
}

func TestMerlinVerifyHooks(t *testing.T) {
	m := newTestMerlin(t, fakeNvram{"jffs2_scripts": "1"})
	checks := m.VerifyHooks()
	if len(checks) != 3 {
		t.Fatalf("want 3 checks, got %d", len(checks))
	}
	if checks[0].Type != "nvram" || checks[0].Path != "jffs2_scripts" {
		t.Errorf("first check should be nvram[jffs2_scripts], got %+v", checks[0])
	}
	if !checks[0].Present {
		t.Errorf("jffs2_scripts=1 should report Present=true, got %+v", checks[0])
	}

	// hooks not installed -> file checks Present=false
	if checks[1].Present || checks[2].Present {
		t.Errorf("uninstalled hooks should be Present=false")
	}

	_ = m.InstallHooks("", testMerlinBinary)
	checks = m.VerifyHooks()
	if !checks[1].Present || !checks[2].Present {
		t.Errorf("installed hooks should be Present=true")
	}
}

func TestMerlinVerifyHooksDisabledScripts(t *testing.T) {
	m := newTestMerlin(t, fakeNvram{"jffs2_scripts": "0"})
	checks := m.VerifyHooks()
	if checks[0].Present {
		t.Errorf("jffs2_scripts=0 should report Present=false, got %+v", checks[0])
	}
}

// helpers
func readFileT(t *testing.T, path string) string {
	t.Helper()
	b, err := readFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(b)
}

var _ Target = (*merlin)(nil)
