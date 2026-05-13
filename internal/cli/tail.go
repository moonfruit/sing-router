package cli

import (
	"bufio"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/fsnotify/fsnotify"
)

// LineEmit 接收一行（不含末尾换行符）的字节切片。
// 调用方不应保留切片引用；如果需要持有应自行复制。
type LineEmit func([]byte)

// SeekToLastN 把 f 的读指针定位到最后 n 行的开头（不含分隔符的下一字节）。
// n <= 0 视为「全部」，定位到 0。文件不足 n 行时也定位到 0。
// 通过反向 4KB 块扫描计 \n 数实现，O(N+块) 内存。
func SeekToLastN(f *os.File, n int) (int64, error) {
	if n <= 0 {
		_, err := f.Seek(0, io.SeekStart)
		return 0, err
	}
	stat, err := f.Stat()
	if err != nil {
		return 0, err
	}
	size := stat.Size()
	if size == 0 {
		return 0, nil
	}

	const block = 4096
	var (
		offset = size
		buf    = make([]byte, block)
		count  int
	)
	// 末尾若以 \n 结尾，把它视为「行尾」而不是新一行：从倒数第二行起开始计数。
	if last := size - 1; last >= 0 {
		var oneByte [1]byte
		if _, err := f.ReadAt(oneByte[:], last); err == nil && oneByte[0] == '\n' {
			offset = last
		}
	}
	for offset > 0 {
		readSize := int64(block)
		if offset < readSize {
			readSize = offset
		}
		offset -= readSize
		if _, err := f.ReadAt(buf[:readSize], offset); err != nil {
			return 0, err
		}
		for i := readSize - 1; i >= 0; i-- {
			if buf[i] == '\n' {
				count++
				if count == n {
					start := offset + i + 1
					if _, err := f.Seek(start, io.SeekStart); err != nil {
						return 0, err
					}
					return start, nil
				}
			}
		}
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return 0, err
	}
	return 0, nil
}

// EmitLines 从 r 读取按 \n 分隔的行直到 EOF；每行（不含 \n）交 emit。
// 返回最后一次 read 的非 EOF 错误（EOF 视为正常结束）。
func EmitLines(r io.Reader, emit LineEmit) error {
	br := bufio.NewReader(r)
	for {
		line, err := br.ReadBytes('\n')
		if len(line) > 0 {
			if line[len(line)-1] == '\n' {
				line = line[:len(line)-1]
			}
			emit(line)
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return err
		}
	}
}

// FollowConfig 控制 follow 行为。
type FollowConfig struct {
	// FollowName 为 true → 类似 tail -F：watch 父目录，文件被 rotate（rename/create）
	// 或 truncate 时自动重接；文件长期消失也不退出。
	// 为 false → 类似 tail -f：仅 watch 自身 fd，rotate/删除即结束。
	FollowName bool
	// PollFallback 在 fsnotify 不可用（如不支持 inotify）时启用 polling 间隔。
	// 为 0 时取默认 250ms。
	PollFallback time.Duration
}

// Follow 在 path 当前内容已被消费（startOffset 表示当前读指针偏移）后持续追读，
// 直到 ctx 取消。emit 在每次发现新行时被调用。
// 实现优先用 fsnotify；watcher 创建失败则降级到轮询。
func Follow(ctx context.Context, path string, startOffset int64, emit LineEmit, cfg FollowConfig) error {
	if cfg.PollFallback <= 0 {
		cfg.PollFallback = 250 * time.Millisecond
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return err
	}

	f, err := os.Open(abs)
	if err != nil && !cfg.FollowName {
		return err
	}
	state := &followState{
		path:   abs,
		offset: startOffset,
		f:      f,
		emit:   emit,
	}
	defer state.close()

	w, werr := fsnotify.NewWatcher()
	if werr != nil {
		return state.pollLoop(ctx, cfg)
	}
	defer func() { _ = w.Close() }()

	if cfg.FollowName {
		if err := w.Add(filepath.Dir(abs)); err != nil {
			return state.pollLoop(ctx, cfg)
		}
	}
	if state.f != nil {
		_ = w.Add(abs) // 文件不存在时 (FollowName) 跳过
	}

	// 进入循环前先把残余字节消费一次（防止 startOffset 与 EOF 之间已有数据）。
	state.drain()

	// 兜底 ticker：定期 drain 以处理 fsnotify 漏事件（如 macOS kqueue 对 truncate
	// 的事件合并）。频率与 PollFallback 一致，开销极小。
	ticker := time.NewTicker(cfg.PollFallback)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return nil
		case ev, ok := <-w.Events:
			if !ok {
				return nil
			}
			if filepath.Clean(ev.Name) != abs {
				continue
			}
			if cfg.FollowName {
				if ev.Op&(fsnotify.Create) != 0 {
					state.reopen()
					_ = w.Add(abs)
				}
				if ev.Op&(fsnotify.Remove|fsnotify.Rename) != 0 {
					state.closeFD()
					continue
				}
			} else {
				if ev.Op&(fsnotify.Remove|fsnotify.Rename) != 0 {
					state.drain()
					return nil
				}
			}
			if ev.Op&fsnotify.Write != 0 {
				state.drain()
			}
		case err, ok := <-w.Errors:
			if !ok {
				return nil
			}
			if err != nil {
				return err
			}
		case <-ticker.C:
			// 兜底：开/关文件等情况漏事件时也能触发一次读
			if state.f == nil && cfg.FollowName {
				state.reopen()
				if state.f != nil {
					_ = w.Add(abs)
				}
			}
			state.drain()
		}
	}
}

type followState struct {
	path     string
	offset   int64
	lastSize int64
	f        *os.File
	emit     LineEmit
}

func (s *followState) close() {
	s.closeFD()
}

func (s *followState) closeFD() {
	if s.f != nil {
		_ = s.f.Close()
		s.f = nil
	}
}

func (s *followState) reopen() {
	s.closeFD()
	f, err := os.Open(s.path)
	if err != nil {
		return
	}
	s.f = f
	s.offset = 0
	s.lastSize = 0
}

func (s *followState) drain() {
	if s.f == nil {
		return
	}
	stat, err := s.f.Stat()
	if err != nil {
		return
	}
	size := stat.Size()
	// truncate 检测：当前 size < offset，或 size 比上一次更小（写少了 → 必然是截断）。
	if size < s.offset || size < s.lastSize {
		if _, err := s.f.Seek(0, io.SeekStart); err != nil {
			return
		}
		s.offset = 0
	}
	s.lastSize = size
	if size == s.offset {
		return
	}
	if _, err := s.f.Seek(s.offset, io.SeekStart); err != nil {
		return
	}
	br := bufio.NewReader(s.f)
	for {
		line, err := br.ReadBytes('\n')
		if len(line) > 0 {
			if line[len(line)-1] == '\n' {
				s.offset += int64(len(line))
				s.emit(line[:len(line)-1])
			} else {
				// 残行（写到一半）：不消费，等下次事件 size 增长后从同一 offset 重读。
				return
			}
		}
		if err != nil {
			return
		}
	}
}

func (s *followState) pollLoop(ctx context.Context, cfg FollowConfig) error {
	t := time.NewTicker(cfg.PollFallback)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-t.C:
			if s.f == nil && cfg.FollowName {
				s.reopen()
			}
			s.drain()
		}
	}
}
