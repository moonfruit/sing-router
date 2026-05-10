package install

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"text/template"
	"time"

	"github.com/moonfruit/sing-router/assets"
)

// TemplateVars 是首次渲染 daemon.toml 时由 install 命令注入的变量集合。
// 当前覆盖 [install] / [gitee] 节，其他节如未来需要按参数定制再扩展即可。
type TemplateVars struct {
	DownloadSingBox   bool
	DownloadCNList    bool
	DownloadZashboard bool
	AutoStart         bool
	Firmware          string // "koolshare" | "merlin"
	GiteeToken        string // [gitee].token；空字符串 → 渲染为 token = ""，与历史行为一致
}

// followBinarySeeds 是"跟随二进制刷新"的路径集——每对 src(embed) → dst(rundir)。
// 这些都是数据资源（cn.txt / rule_set 内嵌兜底），用 writeIfNewer：rundir 文件
// 比当前 binary mtime 旧时覆盖；用户跑了 sing-router update 之后 rundir 文件
// 更新过 → 比 binary 还新 → 不覆盖，保留下载下来的最新版本。
var followBinarySeeds = map[string]string{
	"var/cn.txt":              "var/cn.txt",
	"var/cn.txt.etag":         "var/cn.txt.etag",
	"rules/geoip-cn.srs":      "var/rules/geoip-cn.srs",
	"rules/geoip-cn.srs.etag": "var/rules/geoip-cn.srs.etag",
	"rules/geosites-cn.srs":   "var/rules/geosites-cn.srs",
	"rules/geosites-cn.srs.etag": "var/rules/geosites-cn.srs.etag",
	"rules/lan.srs":           "var/rules/lan.srs",
	"rules/lan.srs.etag":      "var/rules/lan.srs.etag",
	"rules/fakeip-bypass.srs":      "var/rules/fakeip-bypass.srs",
	"rules/fakeip-bypass.srs.etag": "var/rules/fakeip-bypass.srs.etag",
}

// SeedDefaults 把内嵌资源拷到 rundir：
//   - config.d/*.json + daemon.toml 走 writeIfMissing（保护用户编辑）
//   - var/cn.txt + var/rules/*.srs（含 etag）走 writeIfNewer（跟随二进制 mtime；
//     用户 sing-router update 后的下载内容不会被覆盖）
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

	binMtime := binaryMtime()
	for src, dst := range followBinarySeeds {
		if err := writeIfNewer(rundir, dst, binMtime, func() ([]byte, error) {
			return assets.ReadFile(src)
		}); err != nil {
			return err
		}
	}

	return writeIfMissing(rundir, "daemon.toml", func() ([]byte, error) {
		return renderDaemonToml(vars)
	})
}

// writeIfMissing 仅当目标不存在时写入；已存在保留不动（用户编辑保护）。
func writeIfMissing(rundir, dst string, produce func() ([]byte, error)) error {
	full := filepath.Join(rundir, dst)
	if _, err := os.Stat(full); err == nil {
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return doWrite(full, produce)
}

// writeIfNewer 与 writeIfMissing 类似，但若目标文件 mtime 早于 cmpMtime 也覆盖。
// cmpMtime.IsZero() 时退化为 writeIfMissing 语义（无法比较即不动）。
func writeIfNewer(rundir, dst string, cmpMtime time.Time, produce func() ([]byte, error)) error {
	full := filepath.Join(rundir, dst)
	info, err := os.Stat(full)
	switch {
	case err == nil:
		// 已存在：仅当 binary 更新时才重写
		if cmpMtime.IsZero() || !info.ModTime().Before(cmpMtime) {
			return nil
		}
	case errors.Is(err, os.ErrNotExist):
		// 不存在：照常写
	default:
		return err
	}
	return doWrite(full, produce)
}

func doWrite(full string, produce func() ([]byte, error)) error {
	data, err := produce()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		return err
	}
	return os.WriteFile(full, data, 0o644)
}

// binaryMtime 返回当前可执行文件的 mtime，作为内嵌资源"时间戳"的代理：
// embed.FS 不暴露真实 mtime，但 binary 是和资源同时构建出来的，足以表达
// "embed 资源是否比 rundir 老"。失败时返回零值——writeIfNewer 会退化为 noop。
func binaryMtime() time.Time {
	exe, err := os.Executable()
	if err != nil {
		return time.Time{}
	}
	info, err := os.Stat(exe)
	if err != nil {
		return time.Time{}
	}
	return info.ModTime()
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
