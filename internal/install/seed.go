package install

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"text/template"

	"github.com/moonfruit/sing-router/assets"
)

// TemplateVars 是首次渲染 daemon.toml 时由 install 命令注入的变量集合。
// 当前仅 [install] 节的字段，其他节如未来需要按参数定制再扩展即可。
type TemplateVars struct {
	DownloadSingBox   bool
	DownloadCNList    bool
	DownloadZashboard bool
	AutoStart         bool
	Firmware          string // "koolshare" | "merlin"
}

// SeedDefaults 把内嵌的 config.d/*.json 复制到 rundir，并用 vars 渲染
// 内嵌 daemon.toml 模板写入 daemon.toml。
//
// 仅当目标不存在时才写。已存在的文件保持不动（保留用户编辑）——这意味着
// 首次 install 之后，daemon.toml 完全由用户维护，再次运行 install 不会重写。
func SeedDefaults(rundir string, vars TemplateVars) error {
	plainFiles := map[string]string{
		"config.d.default/clash.json":       "config.d/clash.json",
		"config.d.default/dns.json":         "config.d/dns.json",
		"config.d.default/inbounds.json":    "config.d/inbounds.json",
		"config.d.default/log.json":         "config.d/log.json",
		"config.d.default/cache.json":       "config.d/cache.json",
		"config.d.default/certificate.json": "config.d/certificate.json",
		"config.d.default/http.json":        "config.d/http.json",
		"config.d.default/outbounds.json":   "config.d/outbounds.json",
		"config.d.default/zoo.json":         "config.d/zoo.json",
	}
	for src, dst := range plainFiles {
		if err := writeIfMissing(rundir, dst, func() ([]byte, error) {
			return assets.ReadFile(src)
		}); err != nil {
			return err
		}
	}
	if err := writeIfMissing(rundir, "var/cn.txt", func() ([]byte, error) {
		return assets.ReadFile("cn.txt")
	}); err != nil {
		return err
	}
	return writeIfMissing(rundir, "daemon.toml", func() ([]byte, error) {
		return renderDaemonToml(vars)
	})
}

func writeIfMissing(rundir, dst string, produce func() ([]byte, error)) error {
	full := filepath.Join(rundir, dst)
	if _, err := os.Stat(full); err == nil {
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	data, err := produce()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		return err
	}
	return os.WriteFile(full, data, 0o644)
}

func renderDaemonToml(vars TemplateVars) ([]byte, error) {
	raw, err := assets.ReadFile("daemon.toml.tmpl")
	if err != nil {
		return nil, err
	}
	tmpl, err := template.New("daemon.toml").Parse(string(raw))
	if err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, vars); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}
