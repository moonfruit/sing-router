package firmware

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
)

const koolshareHookRel = "koolshare/init.d/N99sing-router.sh"
const koolshareAssetPath = "firmware/koolshare/N99sing-router.sh"

type koolshare struct {
	base   string // 默认 "/"
	assets fs.FS
}

func (k *koolshare) Kind() Kind { return KindKoolshare }

func (k *koolshare) InstallHooks(_ string) error {
	script, err := fs.ReadFile(k.assets, koolshareAssetPath)
	if err != nil {
		return err
	}
	target := filepath.Join(k.base, koolshareHookRel)
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return err
	}
	return atomicWriteExec(target, script, 0o755)
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

// atomicWriteExec writes data to path via tmp+rename and chmods to mode.
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
