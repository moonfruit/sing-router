package install

import (
	"errors"
	"os"
	"path/filepath"

	"github.com/moonfruit/sing-router/assets"
)

// SeedDefaults 把内嵌的 daemon.toml 与 config.d/*.json 写入 rundir，
// 仅当目标不存在时才写。已存在的文件保持不动（保留用户编辑）。
func SeedDefaults(rundir string) error {
	seedFiles := map[string]string{
		"config.d.default/clash.json":       "config.d/clash.json",
		"config.d.default/dns.json":         "config.d/dns.json",
		"config.d.default/inbounds.json":    "config.d/inbounds.json",
		"config.d.default/log.json":         "config.d/log.json",
		"config.d.default/cache.json":       "config.d/cache.json",
		"config.d.default/certificate.json": "config.d/certificate.json",
		"config.d.default/http.json":        "config.d/http.json",
		"config.d.default/outbounds.json":   "config.d/outbounds.json",
		"daemon.toml.default":               "daemon.toml",
	}
	for src, dst := range seedFiles {
		full := filepath.Join(rundir, dst)
		if _, err := os.Stat(full); err == nil {
			continue
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
		data, err := assets.ReadFile(src)
		if err != nil {
			return err
		}
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(full, data, 0o644); err != nil {
			return err
		}
	}
	return nil
}
