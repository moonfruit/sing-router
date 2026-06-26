package zashboard

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"os"
	"path/filepath"
)

// configKey 是 zashboard web 导入时识别的配置键。
const configKey = "config/source-ip-label-list"

// Result 汇报一次生成的结果。
type Result struct {
	Skipped  bool     // ui_dir 不存在 → 跳过
	Changed  bool     // 内容相对旧文件有变化（触发写盘）
	Count    int      // entries 数量
	Warnings []string // 采集降级告警
}

// Generate 采集本地数据并把 zashboard.json 写入 uiDir。
// uiDir 不存在 → Skipped（不报错）。任一数据源缺失 → 降级 + warning。
func Generate(ctx context.Context, uiDir string, static map[string]string) (Result, error) {
	if fi, err := os.Stat(uiDir); err != nil || !fi.IsDir() {
		return Result{Skipped: true}, nil
	}
	raw, warns := Collect(ctx)
	entries := BuildEntries(raw, static)
	content, err := renderPayload(entries)
	if err != nil {
		return Result{Warnings: warns}, err
	}
	changed, err := writeIfChanged(filepath.Join(uiDir, "zashboard.json"), content)
	if err != nil {
		return Result{Warnings: warns}, err
	}
	return Result{Changed: changed, Count: len(entries), Warnings: warns}, nil
}

// renderPayload 产出与 Python 逐字节一致的字节：
// 内层 entries 紧凑 JSON 字符串、外层对象 2 空格缩进，均关闭 HTML 转义（保留 emoji 原样）。
func renderPayload(entries []Entry) ([]byte, error) {
	if entries == nil {
		entries = []Entry{}
	}
	inner, err := marshalNoEscape(entries, "")
	if err != nil {
		return nil, err
	}
	return marshalNoEscape(map[string]string{configKey: string(inner)}, "  ")
}

// marshalNoEscape: SetEscapeHTML(false) 保留 emoji/中文与 <>& 原样；indent 为空即紧凑。
// Encoder.Encode 会追加换行，去掉以匹配 json.Marshal 风格。
func marshalNoEscape(v any, indent string) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if indent != "" {
		enc.SetIndent("", indent)
	}
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return bytes.TrimRight(buf.Bytes(), "\n"), nil
}

// writeIfChanged: 内容 sha256 与现有文件相同则跳过；否则原子写（.tmp + rename）。
func writeIfChanged(path string, content []byte) (bool, error) {
	if old, err := os.ReadFile(path); err == nil {
		if sha256.Sum256(old) == sha256.Sum256(content) {
			return false, nil
		}
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, content, 0o644); err != nil {
		return false, err
	}
	if err := os.Rename(tmp, path); err != nil {
		return false, err
	}
	return true, nil
}
