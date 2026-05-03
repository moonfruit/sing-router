package install

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/moonfruit/sing-router/assets"
)

// WriteInitd 把内嵌 init.d 模板写到 path，并把模板里的 rundir 占位换成
// 实际值，最后 chmod 0755。
func WriteInitd(path, rundir string) error {
	raw, err := assets.ReadFile("initd/S99sing-router")
	if err != nil {
		return err
	}
	rendered := strings.ReplaceAll(string(raw), "/opt/home/sing-router", rundir)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(path, []byte(rendered), 0o755); err != nil {
		return err
	}
	return os.Chmod(path, 0o755)
}
