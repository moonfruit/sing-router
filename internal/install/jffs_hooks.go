package install

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"strings"
)

const (
	beginMarker = "# BEGIN %s (managed by `sing-router install`; do not edit)"
	endMarker   = "# END %s"
)

// InjectHook 在 path 文件中放置/更新 `# BEGIN <name>` ... `# END <name>` 块。
// 文件不存在则创建，含 shebang，权限 0755。已存在的块整段替换，块外内容不动。
func InjectHook(path, name, payload string) error {
	block := renderBlock(name, payload)
	data, err := os.ReadFile(path)
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			return err
		}
		// 新文件
		fresh := []byte("#!/bin/sh\n\n" + block + "\n")
		return os.WriteFile(path, fresh, 0o755)
	}
	s := string(data)
	begin := fmt.Sprintf(beginMarker, name)
	end := fmt.Sprintf(endMarker, name)
	if i, j := blockIndices(s, begin, end); i >= 0 && j > i {
		// 替换：保留 [0,i) 与 [j+lenEnd+1,len(s))，中间换成 block
		before := trimTrailingNewline(s[:i])
		after := s[j+len(end):]
		return writeExec(path, []byte(before+"\n"+block+after))
	}
	// 追加
	var buf bytes.Buffer
	buf.WriteString(s)
	if !bytes.HasSuffix(buf.Bytes(), []byte("\n")) {
		buf.WriteByte('\n')
	}
	buf.WriteString(block + "\n")
	return writeExec(path, buf.Bytes())
}

// writeExec 把 data 写入 path 并强制 0755 模式；用于 jffs hook 文件。
func writeExec(path string, data []byte) error {
	if err := os.WriteFile(path, data, 0o755); err != nil {
		return err
	}
	return os.Chmod(path, 0o755)
}

// RemoveHook 把 path 文件中我们的 BEGIN/END 块整段删除。
// 文件不存在或无块时为 no-op。
func RemoveHook(path, name string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	s := string(data)
	begin := fmt.Sprintf(beginMarker, name)
	end := fmt.Sprintf(endMarker, name)
	i, j := blockIndices(s, begin, end)
	if i < 0 || j <= i {
		return nil
	}
	before := trimTrailingNewline(s[:i])
	after := s[j+len(end):]
	if !strings.HasPrefix(after, "\n") {
		after = "\n" + after
	}
	joined := before + after
	return os.WriteFile(path, []byte(joined), 0o755)
}

func renderBlock(name, payload string) string {
	return fmt.Sprintf(beginMarker+"\n%s\n"+endMarker, name, payload, name)
}

// blockIndices 返回 BEGIN 行起始字节与 END 行起始字节；找不到返回 (-1, -1)。
func blockIndices(s, begin, end string) (int, int) {
	i := indexOfLine(s, begin)
	if i < 0 {
		return -1, -1
	}
	j := indexOfLine(s[i:], end)
	if j < 0 {
		return -1, -1
	}
	return i, i + j
}

// indexOfLine 找以 prefix 整行开头的位置（行首匹配），-1 如不存在。
func indexOfLine(s, prefix string) int {
	if strings.HasPrefix(s, prefix) {
		return 0
	}
	idx := strings.Index(s, "\n"+prefix)
	if idx < 0 {
		return -1
	}
	return idx + 1
}

func trimTrailingNewline(s string) string {
	for len(s) > 0 && (s[len(s)-1] == '\n' || s[len(s)-1] == '\r') {
		s = s[:len(s)-1]
	}
	return s
}
