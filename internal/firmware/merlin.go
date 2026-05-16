package firmware

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/moonfruit/sing-router/internal/install"
)

type merlin struct {
	base   string
	assets fs.FS
	nvram  nvramReader
}

func (m *merlin) Kind() Kind { return KindMerlin }

// rundir is unused: the merlin BEGIN/END block bakes the binary absolute path
// via {{.Binary}} render. binary 必须是 sing-router 在路由器上的绝对路径
// （默认 /opt/sbin/sing-router）—— Merlin 触发 nat-start 时 PATH 不含 /opt/sbin。
func (m *merlin) InstallHooks(_, binary string) error {
	// Read both snippet payloads first so a missing asset never leaves the system half-installed.
	natPayload, err := readSnippetPayload(m.assets, "firmware/merlin/nat-start.snippet", binary)
	if err != nil {
		return err
	}
	svcPayload, err := readSnippetPayload(m.assets, "firmware/merlin/services-start.snippet", binary)
	if err != nil {
		return err
	}
	natPath := filepath.Join(m.base, "jffs/scripts/nat-start")
	svcPath := filepath.Join(m.base, "jffs/scripts/services-start")
	if err := os.MkdirAll(filepath.Dir(natPath), 0o755); err != nil {
		return err
	}
	if err := install.InjectHook(natPath, "sing-router", natPayload); err != nil {
		return err
	}
	return install.InjectHook(svcPath, "sing-router", svcPayload)
}

func (m *merlin) RemoveHooks() error {
	for _, name := range []string{"nat-start", "services-start"} {
		path := filepath.Join(m.base, "jffs/scripts", name)
		if err := install.RemoveHook(path, "sing-router"); err != nil {
			return err
		}
	}
	return nil
}

func (m *merlin) VerifyHooks() []HookCheck {
	jffsVal, _ := m.nvram.Get("jffs2_scripts")
	checks := []HookCheck{
		{
			Type:     "nvram",
			Path:     "jffs2_scripts",
			Required: true,
			Present:  jffsVal == "1",
			Note:     "Merlin custom scripts must be enabled or hooks won't fire",
		},
	}
	for _, name := range []string{"nat-start", "services-start"} {
		path := filepath.Join(m.base, "jffs/scripts", name)
		data, err := readFile(path)
		present := err == nil && strings.Contains(string(data), "# BEGIN sing-router")
		checks = append(checks, HookCheck{
			Type:     "file",
			Path:     path,
			Required: true,
			Present:  present,
			Note:     "Merlin " + name + " hook must contain # BEGIN sing-router block",
		})
	}
	return checks
}

// readSnippetPayload extracts the lines between # BEGIN/# END markers in an
// embedded snippet, then renders any {{.Binary}} placeholders.
func readSnippetPayload(a fs.FS, name, binary string) (string, error) {
	raw, err := fs.ReadFile(a, name)
	if err != nil {
		return "", err
	}
	var inside bool
	var out []string
	for _, l := range strings.Split(string(raw), "\n") {
		if strings.HasPrefix(l, "# BEGIN") {
			inside = true
			continue
		}
		if strings.HasPrefix(l, "# END") {
			inside = false
			continue
		}
		if inside {
			out = append(out, l)
		}
	}
	rendered, err := RenderHookTemplate(name, []byte(strings.Join(out, "\n")), binary)
	if err != nil {
		return "", err
	}
	return string(rendered), nil
}

// readFile is the os.ReadFile seam used by VerifyHooks; replaceable in tests to inject read failures.
var readFile = os.ReadFile
