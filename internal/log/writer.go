package log

import (
	"bufio"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"sync"

	"github.com/moonfruit/sing2seq/clef"
)

// WriterConfig 配置 Writer 的轮转行为。
type WriterConfig struct {
	Path       string // active 文件绝对路径
	MaxSize    int64  // 字节；> 0 触发大小轮转，<= 0 表示不轮转
	MaxBackups int    // 保留的旧文件数量；超出按 .N 编号删除最旧
	Gzip       bool   // 是否在轮转后异步把 .1 压成 .1.gz
}

// Writer 写 CLEF JSON Lines 到 active 文件，按大小阈值轮转，可选 gzip。
// 并发安全。
type Writer struct {
	cfg WriterConfig

	mu   sync.Mutex
	f    *os.File
	bw   *bufio.Writer
	size int64

	gzipWg sync.WaitGroup
}

// NewWriter 打开（或创建）active 文件。父目录必须已存在。
func NewWriter(cfg WriterConfig) (*Writer, error) {
	if cfg.Path == "" {
		return nil, fmt.Errorf("Writer.Path is required")
	}
	w := &Writer{cfg: cfg}
	if err := w.openActive(); err != nil {
		return nil, err
	}
	return w, nil
}

func (w *Writer) openActive() error {
	f, err := os.OpenFile(w.cfg.Path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		return err
	}
	info, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return err
	}
	w.f = f
	w.bw = bufio.NewWriter(f)
	w.size = info.Size()
	return nil
}

// Write 序列化事件并追加一行；必要时触发轮转。
func (w *Writer) Write(e *clef.Event) error {
	data, err := json.Marshal(e)
	if err != nil {
		return err
	}
	data = append(data, '\n')

	w.mu.Lock()
	defer w.mu.Unlock()

	if w.cfg.MaxSize > 0 && w.size+int64(len(data)) > w.cfg.MaxSize && w.size > 0 {
		if err := w.rotateLocked(); err != nil {
			return err
		}
	}
	n, err := w.bw.Write(data)
	w.size += int64(n)
	return err
}

// Sync 刷新缓冲到磁盘。
func (w *Writer) Sync() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if err := w.bw.Flush(); err != nil {
		return err
	}
	return w.f.Sync()
}

// Reopen 关闭当前 active 文件并重新打开同一路径；用于 logrotate copytruncate
// 的反向场景或 SIGUSR1 处理。
func (w *Writer) Reopen() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if err := w.bw.Flush(); err != nil {
		return err
	}
	if err := w.f.Close(); err != nil {
		return err
	}
	return w.openActive()
}

// Close 刷新并关闭 active 文件，等待所有异步 gzip 完成。
func (w *Writer) Close() error {
	w.mu.Lock()
	var firstErr error
	if w.bw != nil {
		if err := w.bw.Flush(); err != nil {
			firstErr = err
		}
	}
	if w.f != nil {
		if err := w.f.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	w.bw = nil
	w.f = nil
	w.mu.Unlock()
	w.gzipWg.Wait()
	return firstErr
}

// WaitGzip 阻塞直到所有未完成的 gzip 后台任务结束（仅供测试使用）。
func (w *Writer) WaitGzip() error {
	w.gzipWg.Wait()
	return nil
}

func (w *Writer) rotateLocked() error {
	if err := w.bw.Flush(); err != nil {
		return err
	}
	if err := w.f.Close(); err != nil {
		return err
	}

	// 顺延：.N → .N+1 (倒序，避免覆盖)
	backups := listBackups(w.cfg.Path)
	sort.Sort(sort.Reverse(byIndex(backups)))
	for _, b := range backups {
		next := backupNameAt(w.cfg.Path, b.idx+1, b.gz)
		_ = os.Rename(b.path, next)
	}

	// active → .1
	if err := os.Rename(w.cfg.Path, w.cfg.Path+".1"); err != nil {
		return fmt.Errorf("rename active: %w", err)
	}

	// 异步 gzip
	if w.cfg.Gzip {
		w.gzipWg.Add(1)
		go w.gzipBackground(w.cfg.Path + ".1")
	}

	// 修剪：删除超过 MaxBackups 的旧文件
	pruneBackups(w.cfg.Path, w.cfg.MaxBackups)

	if err := w.openActive(); err != nil {
		return err
	}
	w.size = 0
	return nil
}

func (w *Writer) gzipBackground(src string) {
	defer w.gzipWg.Done()
	in, err := os.Open(src)
	if err != nil {
		return
	}
	defer func() { _ = in.Close() }()
	out, err := os.OpenFile(src+".gz", os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0644)
	if err != nil {
		return
	}
	defer func() { _ = out.Close() }()
	gw := gzip.NewWriter(out)
	if _, err := io.Copy(gw, in); err != nil {
		return
	}
	if err := gw.Close(); err != nil {
		return
	}
	_ = os.Remove(src)
	pruneBackups(w.cfg.Path, w.cfg.MaxBackups)
}

type backup struct {
	idx  int
	gz   bool
	path string
}

type byIndex []backup

func (b byIndex) Len() int           { return len(b) }
func (b byIndex) Less(i, j int) bool { return b[i].idx < b[j].idx }
func (b byIndex) Swap(i, j int)      { b[i], b[j] = b[j], b[i] }

func listBackups(active string) []backup {
	dir := filepath.Dir(active)
	base := filepath.Base(active)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var out []backup
	for _, e := range entries {
		name := e.Name()
		if name == base || !startsWith(name, base+".") {
			continue
		}
		rest := name[len(base)+1:]
		gz := false
		if hasSuffix(rest, ".gz") {
			rest = rest[:len(rest)-3]
			gz = true
		}
		idx := atoi(rest)
		if idx <= 0 {
			continue
		}
		out = append(out, backup{idx: idx, gz: gz, path: filepath.Join(dir, name)})
	}
	return out
}

func backupNameAt(active string, idx int, gz bool) string {
	name := fmt.Sprintf("%s.%d", active, idx)
	if gz {
		name += ".gz"
	}
	return name
}

func pruneBackups(active string, maxBackups int) {
	if maxBackups <= 0 {
		return
	}
	backups := listBackups(active)
	sort.Sort(byIndex(backups))
	excess := len(backups) - maxBackups
	if excess <= 0 {
		return
	}
	// 删除 idx 最大的（最旧的）—— 编号越大越旧（rotation 顺延后旧文件 idx 增长）。
	sort.Sort(sort.Reverse(byIndex(backups)))
	for i := 0; i < excess; i++ {
		_ = os.Remove(backups[i].path)
	}
}

// 小工具：避免重复导入造成的循环（writer.go 内部使用）
func startsWith(s, prefix string) bool {
	return len(s) >= len(prefix) && s[:len(prefix)] == prefix
}
func hasSuffix(s, suffix string) bool {
	return len(s) >= len(suffix) && s[len(s)-len(suffix):] == suffix
}
func atoi(s string) int {
	if s == "" {
		return 0
	}
	n := 0
	for _, c := range s {
		if c < '0' || c > '9' {
			return 0
		}
		n = n*10 + int(c-'0')
	}
	return n
}
