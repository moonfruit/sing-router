package firmware

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"text/template"
)

const koolshareHookRel = "koolshare/init.d/N99sing-router.sh"
const koolshareAssetPath = "firmware/koolshare/N99sing-router.sh.tmpl"

type koolshare struct {
	base   string // 默认 "/"
	assets fs.FS
}

func (k *koolshare) Kind() Kind { return KindKoolshare }

// rundir is unused: the koolshare hook只依赖渲染进来的 binary 绝对路径，
// 不需要知道 rundir。binary 必须是绝对路径（默认 /opt/sbin/sing-router）。
func (k *koolshare) InstallHooks(_, binary string) error {
	raw, err := fs.ReadFile(k.assets, koolshareAssetPath)
	if err != nil {
		return err
	}
	rendered, err := RenderHookTemplate(koolshareAssetPath, raw, binary)
	if err != nil {
		return err
	}
	target := filepath.Join(k.base, koolshareHookRel)
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return err
	}
	return atomicWriteExec(target, rendered, 0o755)
}

func (k *koolshare) RemoveHooks() error {
	target := filepath.Join(k.base, koolshareHookRel)
	if err := os.Remove(target); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

func (k *koolshare) VerifyHooks() []HookCheck {
	target := filepath.Join(k.base, koolshareHookRel)
	info, err := os.Stat(target)
	present := err == nil && !info.IsDir() && info.Mode()&0o111 != 0
	return []HookCheck{{
		Type:     "file",
		Path:     target,
		Required: true,
		Present:  present,
		Note:     "koolshare nat-start hook (replays iptables on WAN/firewall restart)",
	}}
}

// atomicWriteExec writes data to path via tmp+rename and forces the mode
// bits on disk. Chmod is explicit because os.WriteFile applies its mode
// argument through the process umask; Chmod bypasses umask and guarantees
// the target lands with the exact bits requested (matters for 0o755 hooks
// on routers where the daemon's umask isn't well-defined).
func atomicWriteExec(path string, data []byte, mode os.FileMode) error {
	tmp := path + ".new"
	if err := os.WriteFile(tmp, data, mode); err != nil {
		return err
	}
	if err := os.Chmod(tmp, mode); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, path)
}

// RenderHookTemplate executes a text/template against payload, binding
// {{.Binary}} to binary. Exported so `sing-router script` / `/api/v1/script/`
// can render embedded .tmpl hook payloads before printing them (otherwise
// pipelines like `sing-router script koolshare/N99 | sh -s start_nat` get
// an unrendered template and the BINARY guard fails).
func RenderHookTemplate(name string, payload []byte, binary string) ([]byte, error) {
	tmpl, err := template.New(name).Option("missingkey=error").Parse(string(payload))
	if err != nil {
		return nil, fmt.Errorf("parse %s template: %w", name, err)
	}
	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, map[string]string{"Binary": binary}); err != nil {
		return nil, fmt.Errorf("render %s template: %w", name, err)
	}
	return buf.Bytes(), nil
}
