// Package install 实施 install/uninstall/doctor 的具体动作。
package install

import (
	"os"
	"path/filepath"
)

// EnsureLayout 创建 $RUNDIR 及其全部固定子目录。
// 注意：不创建 ui/，留给 sing-box clash_api 首次下载时自行创建。
func EnsureLayout(rundir string) error {
	for _, sub := range []string{"config.d", "bin", "var", "run", "log"} {
		if err := os.MkdirAll(filepath.Join(rundir, sub), 0o755); err != nil {
			return err
		}
	}
	return nil
}
