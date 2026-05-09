// Package sync 把"从 gitee/公网拉取资源 → 安装到 rundir"的全过程编排起来，
// 是 CLI `update` 命令与 daemon 后台同步 loop 的共享实现。
//
// 三类资源：
//   - sing-box：拉 version.txt 决定版本 → 拉 tarball → 解压 → 替换 bin/sing-box
//   - zoo：拉 gitee config.json → 写 var/zoo.raw.json（daemon Preprocess 接管）
//   - cn-list：直接走公网 cdn 拉到 var/cn.txt
//
// 所有写盘操作通过 httpx 包的原子写完成；任何一项失败都不影响其他项（UpdateAll）。
package sync

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	stdsync "sync"

	"github.com/moonfruit/sing-router/internal/config"
	"github.com/moonfruit/sing-router/internal/gitee"
	"github.com/moonfruit/sing-router/internal/httpx"
)

// Updater 持有同步所需的所有状态。线程安全：方法间无共享可变状态，可并发调用。
type Updater struct {
	cfg    *config.DaemonConfig
	rundir string
	gitee  *gitee.Client
}

// NewUpdater 用已加载的 daemon 配置 + rundir 构造 Updater。
func NewUpdater(cfg *config.DaemonConfig, rundir string) *Updater {
	gc := gitee.NewClient(cfg.Gitee)
	gc.Retries = cfg.Download.HTTPRetries
	return &Updater{cfg: cfg, rundir: rundir, gitee: gc}
}

// ItemResult 是单项资源同步的结果。Err != nil 时其他字段无意义。
type ItemResult struct {
	Changed bool
	Version string // 仅 sing-box 用
	Err     error
}

// AllResult 是 UpdateAll 的结果。
type AllResult struct {
	SingBox ItemResult
	Zoo     ItemResult
	CNList  ItemResult
}

// HasError 报告是否任一项失败。
func (r AllResult) HasError() bool {
	return r.SingBox.Err != nil || r.Zoo.Err != nil || r.CNList.Err != nil
}

// UpdateSingBox 拉新版 sing-box 并安装到 bin/sing-box。
//
// 流程：
//  1. gitee.Version() 拿最新版本号 V
//  2. gitee.DownloadRaw() 把 tarball 拉到 var/sing-box.tar.gz（etag 旁路）
//  3. 若 changed=true 或 bin/sing-box 不存在，则解压并原子替换二进制
//
// changed=true 表示二进制被实际替换；version 是 step 1 拿到的版本号。
func (u *Updater) UpdateSingBox(ctx context.Context) (changed bool, version string, err error) {
	gc := u.cfg.Gitee.SingBox
	version, err = u.gitee.Version(ctx, gc.Ref, gc.VersionPath)
	if err != nil {
		return false, "", fmt.Errorf("resolve sing-box version: %w", err)
	}
	if version == "" {
		return false, "", errors.New("gitee returned empty sing-box version")
	}
	tarPath := strings.ReplaceAll(gc.TarballPathTemplate, "{version}", version)
	tarballTarget := filepath.Join(u.rundir, "var", "sing-box.tar.gz")
	binTarget := filepath.Join(u.rundir, "bin", "sing-box")

	tarChanged, err := u.gitee.DownloadRaw(ctx, gc.Ref, tarPath, tarballTarget)
	if err != nil {
		return false, version, fmt.Errorf("download sing-box tarball: %w", err)
	}
	binMissing := !fileExists(binTarget)
	if !tarChanged && !binMissing {
		return false, version, nil
	}
	if err := extractSingBoxBinary(tarballTarget, binTarget); err != nil {
		return false, version, fmt.Errorf("extract sing-box: %w", err)
	}
	return true, version, nil
}

// UpdateZoo 把 gitee 上的 config.json 拉到 var/zoo.raw.json，由 daemon 的 Preprocess
// 自动接管（不在此处直接调 Preprocess，避免 sync 包反向依赖 daemon）。
func (u *Updater) UpdateZoo(ctx context.Context) (changed bool, err error) {
	zc := u.cfg.Gitee.Zoo
	target := filepath.Join(u.rundir, "var", "zoo.raw.json")
	changed, err = u.gitee.DownloadRaw(ctx, zc.Ref, zc.Path, target)
	if err != nil {
		return false, fmt.Errorf("download zoo (%s): %w", zc.Path, err)
	}
	return changed, nil
}

// UpdateCNList 直接从 [download].cn_list_url（公网，不需要 token）拉 cn.txt 到
// var/cn.txt，复用 httpx 的 etag 增量。
func (u *Updater) UpdateCNList(ctx context.Context) (changed bool, err error) {
	url := u.cfg.Download.CNListURL
	if url == "" {
		return false, errors.New("download.cn_list_url is empty")
	}
	target := filepath.Join(u.rundir, "var", "cn.txt")
	changed, err = httpx.Download(ctx, url, target, httpx.Options{
		Retries: u.cfg.Download.HTTPRetries,
	})
	if err != nil {
		return false, fmt.Errorf("download cn.txt: %w", err)
	}
	return changed, nil
}

// UpdateAll 并发更新三类资源；任一失败不影响其他两个，最后通过 AllResult 聚合。
func (u *Updater) UpdateAll(ctx context.Context) AllResult {
	var r AllResult
	var wg stdsync.WaitGroup
	wg.Add(3)
	go func() {
		defer wg.Done()
		c, v, err := u.UpdateSingBox(ctx)
		r.SingBox = ItemResult{Changed: c, Version: v, Err: err}
	}()
	go func() {
		defer wg.Done()
		c, err := u.UpdateZoo(ctx)
		r.Zoo = ItemResult{Changed: c, Err: err}
	}()
	go func() {
		defer wg.Done()
		c, err := u.UpdateCNList(ctx)
		r.CNList = ItemResult{Changed: c, Err: err}
	}()
	wg.Wait()
	return r
}

func fileExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

// extractSingBoxBinary 从 tarball（.tar.gz）中提取名为 sing-box 的可执行文件，
// 原子替换 binTarget。tarball 中的二进制可能位于子目录（如
// sing-box-1.13.5-linux-arm64-musl/sing-box），按 basename 匹配即可。
func extractSingBoxBinary(tarball, binTarget string) error {
	f, err := os.Open(tarball)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return err
	}
	defer func() { _ = gz.Close() }()
	tr := tar.NewReader(gz)

	if err := os.MkdirAll(filepath.Dir(binTarget), 0o755); err != nil {
		return err
	}
	tmp := binTarget + ".new"
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
		if hdr.Typeflag != tar.TypeReg {
			continue
		}
		if filepath.Base(hdr.Name) != "sing-box" {
			continue
		}
		out, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o755)
		if err != nil {
			return err
		}
		if _, err := io.Copy(out, tr); err != nil {
			_ = out.Close()
			_ = os.Remove(tmp)
			return err
		}
		if err := out.Close(); err != nil {
			_ = os.Remove(tmp)
			return err
		}
		if err := os.Chmod(tmp, 0o755); err != nil {
			_ = os.Remove(tmp)
			return err
		}
		if err := os.Rename(tmp, binTarget); err != nil {
			_ = os.Remove(tmp)
			return err
		}
		return nil
	}
	return errors.New("sing-box binary not found in tarball")
}
