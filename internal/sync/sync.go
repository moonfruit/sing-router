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
	"crypto/sha256"
	"encoding/hex"
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
	// StagingPath 仅在 sing-box 走 staging 路径(UpdateAll)且 Changed=true 时设置：
	// 解压后未 rename 的 .new 文件绝对路径,由 Applier 负责 backup+rename;CLI 同步
	// 路径(UpdateSingBox)在内部直接 rename 并清空此字段。
	StagingPath string
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

// UpdateSingBox 拉新版 sing-box 并立即安装到 bin/sing-box(同步路径,供 CLI
// `update` 子命令使用)。内部走 staging + sha256 闸门:解压到 bin/sing-box.new,
// 若与现行二进制内容一致则丢弃 staging,Changed=false(屏蔽 etag 假信号)。
//
// changed=true 表示二进制被实际替换;version 是从 gitee 拿到的版本号。
func (u *Updater) UpdateSingBox(ctx context.Context) (changed bool, version string, err error) {
	staging, ch, ver, err := u.updateSingBoxCommon(ctx)
	if err != nil {
		return false, ver, err
	}
	if !ch {
		return false, ver, nil
	}
	binTarget := filepath.Join(u.rundir, "bin", "sing-box")
	if err := os.Rename(staging, binTarget); err != nil {
		_ = os.Remove(staging)
		return false, ver, fmt.Errorf("install sing-box: %w", err)
	}
	return true, ver, nil
}

// UpdateSingBoxStaging 与 UpdateSingBox 一致,但**不**把 staging rename 到正式
// 路径,由调用方(通常是 daemon.Applier:备份+原子提交+失败 revert)决定何时提交。
// Changed=true 时 staging 文件位于 bin/sing-box.new;Changed=false 时 staging
// 已被清理(可能本来就不存在,或与现行内容一致被丢弃)。
func (u *Updater) UpdateSingBoxStaging(ctx context.Context) (stagingPath string, changed bool, version string, err error) {
	return u.updateSingBoxCommon(ctx)
}

// updateSingBoxCommon 下载 tarball 并解压到 bin/sing-box.new,然后用 sha256 比对
// 现行二进制。一致则删 staging 返 Changed=false;不一致则保留 staging 返 true。
// tarball 与现行二进制都未变化时跳过解压(并顺手清掉残留 staging)。
func (u *Updater) updateSingBoxCommon(ctx context.Context) (stagingPath string, changed bool, version string, err error) {
	gc := u.cfg.Gitee.SingBox
	version, err = u.gitee.Version(ctx, gc.Ref, gc.VersionPath)
	if err != nil {
		return "", false, "", fmt.Errorf("resolve sing-box version: %w", err)
	}
	if version == "" {
		return "", false, "", errors.New("gitee returned empty sing-box version")
	}
	tarPath := strings.ReplaceAll(gc.TarballPathTemplate, "{version}", version)
	tarballTarget := filepath.Join(u.rundir, "var", "sing-box.tar.gz")
	binTarget := filepath.Join(u.rundir, "bin", "sing-box")
	stagingPath = binTarget + ".new"

	tarChanged, err := u.gitee.DownloadRaw(ctx, gc.Ref, tarPath, tarballTarget)
	if err != nil {
		return "", false, version, fmt.Errorf("download sing-box tarball: %w", err)
	}
	binMissing := !fileExists(binTarget)
	if !tarChanged && !binMissing {
		// 上游 etag 未变 + 现行二进制还在 → 直接判定无变化,顺手清掉残留 staging。
		_ = os.Remove(stagingPath)
		return stagingPath, false, version, nil
	}
	if err := extractSingBoxBinary(tarballTarget, stagingPath); err != nil {
		return "", false, version, fmt.Errorf("extract sing-box: %w", err)
	}
	if !binMissing {
		same, err := sameFileSHA256(stagingPath, binTarget)
		if err != nil {
			_ = os.Remove(stagingPath)
			return "", false, version, fmt.Errorf("compare sing-box sha256: %w", err)
		}
		if same {
			_ = os.Remove(stagingPath)
			return stagingPath, false, version, nil
		}
	}
	return stagingPath, true, version, nil
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
//
// sing-box 走 staging 路径:解压到 bin/sing-box.new,真正的 rename + backup 由
// 调用方(daemon Applier 或不开 auto_apply 时由 sync_loop 顺手 rename)做决定。
func (u *Updater) UpdateAll(ctx context.Context) AllResult {
	var r AllResult
	var wg stdsync.WaitGroup
	wg.Add(3)
	go func() {
		defer wg.Done()
		staging, c, v, err := u.UpdateSingBoxStaging(ctx)
		r.SingBox = ItemResult{Changed: c, Version: v, Err: err}
		if c {
			r.SingBox.StagingPath = staging
		}
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

// CommitSingBoxStaging 把 bin/sing-box.new 原子 rename 到 bin/sing-box。供
// auto_apply 关闭的 sync 路径在无需备份/重启时直接落盘。staging 不存在则 no-op。
func (u *Updater) CommitSingBoxStaging() error {
	binTarget := filepath.Join(u.rundir, "bin", "sing-box")
	staging := binTarget + ".new"
	if !fileExists(staging) {
		return nil
	}
	if err := os.Rename(staging, binTarget); err != nil {
		return fmt.Errorf("install sing-box: %w", err)
	}
	return nil
}

func fileExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

// extractSingBoxBinary 从 tarball（.tar.gz）中提取名为 sing-box 的可执行文件,
// 写入 outPath(0o755);不做 rename / sha256 比对,这些由调用方决定。tarball
// 中的二进制可能位于子目录(如 sing-box-1.13.5-linux-arm64-musl/sing-box),
// 按 basename 匹配。
func extractSingBoxBinary(tarball, outPath string) error {
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

	if err := os.MkdirAll(filepath.Dir(outPath), 0o755); err != nil {
		return err
	}
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
		out, err := os.OpenFile(outPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o755)
		if err != nil {
			return err
		}
		if _, err := io.Copy(out, tr); err != nil {
			_ = out.Close()
			_ = os.Remove(outPath)
			return err
		}
		if err := out.Close(); err != nil {
			_ = os.Remove(outPath)
			return err
		}
		if err := os.Chmod(outPath, 0o755); err != nil {
			_ = os.Remove(outPath)
			return err
		}
		return nil
	}
	return errors.New("sing-box binary not found in tarball")
}

// sameFileSHA256 报告两个文件的 sha256 是否一致;任一打开失败返回 err。
func sameFileSHA256(a, b string) (bool, error) {
	ah, err := fileSHA256(a)
	if err != nil {
		return false, err
	}
	bh, err := fileSHA256(b)
	if err != nil {
		return false, err
	}
	return ah == bh, nil
}

// fileSHA256 计算文件内容 sha256,以小写 hex 返回。
func fileSHA256(p string) (string, error) {
	f, err := os.Open(p)
	if err != nil {
		return "", err
	}
	defer func() { _ = f.Close() }()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
