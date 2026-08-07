package install

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"text/template"
	"time"

	"github.com/moonfruit/sing-router/assets"
)

// TemplateVars 是首次渲染 daemon.toml 时由 install 命令注入的变量集合。
// 当前覆盖 [install] / [gitee] 节，其他节如未来需要按参数定制再扩展即可。
type TemplateVars struct {
	DownloadSingBox bool
	DownloadCNList  bool
	AutoStart       bool
	Firmware        string // "koolshare" | "merlin"
	GiteeToken      string // [gitee].token；空字符串 → 渲染为 token = ""，与历史行为一致
}

// followBinaryFixedSeeds 是"跟随二进制刷新"里路径固定的那部分。
// var/rules/ 下的 rule_set 兜底不写在这里——见 followBinarySeeds()。
var followBinaryFixedSeeds = []string{
	"var/cn.txt",
	"var/cn.txt.etag",
}

// followBinarySeeds 返回"跟随二进制刷新"的全部路径（embed 与 rundir 同名）。
// 这些都是数据资源（cn.txt / rule_set 内嵌兜底），用 writeIfNewer：rundir 文件
// 比当前 binary mtime 旧时覆盖；用户跑了 sing-router update 之后 rundir 文件
// 更新过 → 比 binary 还新 → 不覆盖，保留下载下来的最新版本。
//
// var/rules/ 同样按内嵌目录枚举而不是手工列举：清单曾与 Makefile 的 RULE_SETS
// 漂移（内嵌了用不上的 lan.srs，却缺了 DefaultRequiredRuleSets 需要的 doh.srs），
// 结果无 token 的机器上 EnsureRequiredRuleSets 写出 local 条目指向不存在的文件，
// sing-box 直接 `parse rule-set: no such file` 拒绝启动。
func followBinarySeeds() ([]string, error) {
	entries, err := fs.ReadDir(assets.FS(), "var/rules")
	if err != nil {
		return nil, fmt.Errorf("list embedded var/rules: %w", err)
	}
	out := make([]string, 0, len(entries)+len(followBinaryFixedSeeds))
	out = append(out, followBinaryFixedSeeds...)
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		out = append(out, "var/rules/"+e.Name())
	}
	sort.Strings(out)
	return out, nil
}

// EmbeddedConfigFragments 列出内嵌 config/ 下的全部文件（"config/xxx" 形式，
// 已排序）。这里刻意不写死清单——历史上手工维护的白名单曾漏掉新增 fragment，
// 导致 install 后 config 缺文件、sing-box 因引用不到 rule_set 直接拒绝启动。
// assets/config 与 $RUNDIR/config 是一一对应关系，全量拷贝即正确行为。
func EmbeddedConfigFragments() ([]string, error) {
	entries, err := fs.ReadDir(assets.FS(), "config")
	if err != nil {
		return nil, fmt.Errorf("list embedded config: %w", err)
	}
	out := make([]string, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		out = append(out, "config/"+e.Name())
	}
	sort.Strings(out)
	return out, nil
}

// SeedDefaults 把内嵌资源拷到 rundir：
//   - config/*.json + daemon.toml 走 writeDefaultAndSeed：xxx 仅在不存在时落
//     盘（保护用户编辑，同时首装让 daemon 能直接起）；只有当 xxx 已存在且与
//     新内容不一致时才写 xxx.default 供用户 diff/合并，等价或缺失时不产生
//     冗余 .default 文件
//   - var/cn.txt + var/rules/*.srs（含 etag）走 writeIfNewer（跟随二进制 mtime；
//     用户 sing-router update 后的下载内容不会被覆盖）
func SeedDefaults(rundir string, vars TemplateVars) error {
	plainFiles, err := EmbeddedConfigFragments()
	if err != nil {
		return err
	}
	for _, src := range plainFiles {
		if err := writeDefaultAndSeed(rundir, src, func() ([]byte, error) {
			return assets.ReadFile(src)
		}); err != nil {
			return err
		}
	}

	dataSeeds, err := followBinarySeeds()
	if err != nil {
		return err
	}
	binMtime := binaryMtime()
	for _, src := range dataSeeds {
		if err := writeIfNewer(rundir, src, binMtime, func() ([]byte, error) {
			return assets.ReadFile(src)
		}); err != nil {
			return err
		}
	}

	return writeDefaultAndSeed(rundir, "daemon.toml", func() ([]byte, error) {
		return renderDaemonToml(vars)
	})
}

// writeDefaultAndSeed 是 RUNDIR 配置文件的安装语义：
//   - dst 不存在：写 dst（首装让 daemon 直接能起），不产生 .default
//   - dst 已存在且内容与 produce() 一致：什么也不做（无需 diff 基准）
//   - dst 已存在且内容不同：保留 dst（用户编辑），把新内容覆盖到 dst+".default"
//     供用户 diff/合并
//
// 仅 produce 一次，避免对 embed 数据重复 ReadFile。
func writeDefaultAndSeed(rundir, dst string, produce func() ([]byte, error)) error {
	data, err := produce()
	if err != nil {
		return err
	}
	full := filepath.Join(rundir, dst)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		return err
	}
	existing, err := os.ReadFile(full)
	switch {
	case errors.Is(err, os.ErrNotExist):
		return os.WriteFile(full, data, 0o644)
	case err != nil:
		return err
	}
	if bytes.Equal(existing, data) {
		return nil
	}
	return os.WriteFile(full+".default", data, 0o644)
}

// writeIfNewer 若目标文件 mtime 早于 cmpMtime 才覆盖；不存在则照常写。
// cmpMtime.IsZero() 时只在文件缺失时写入（无法比较即不动）。
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
