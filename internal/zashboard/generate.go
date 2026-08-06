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

// SettingsFileName 是落到 ui_dir 的文件名——zashboard web 端「导入配置」读它。
// 名字带 -settings 后缀是为了跟 zashboard 自身的构建产物区分开：ui_dir 归
// sing-box 所有，混在一堆 UI 资源里的 zashboard.json 看不出是谁写的。
const SettingsFileName = "zashboard-settings.json"

// legacyFileName 是 SettingsFileName 之前用的名字。见到就清理：留着既会被
// zashboard 当成第二份配置，也会单独占住 ui_dir（见 externalUIInstalled）。
const legacyFileName = "zashboard.json"

// SkipReason 说明本轮为什么没生成。
type SkipReason string

const (
	// SkipUIDirAbsent：ui_dir 压根不存在（daemon.toml 未配 ui_dir，或还没装 UI）。
	SkipUIDirAbsent SkipReason = "ui_dir absent"
	// SkipUINotInstalled：ui_dir 在，但除了我们自己的文件之外空空如也 → external
	// UI 还没下载成功，此刻写文件会把 sing-box 的下载判据搅浑（见 Generate 注释）。
	SkipUINotInstalled SkipReason = "external UI not installed yet"
)

// Result 汇报一次生成的结果。
type Result struct {
	Skipped    bool       // 未生成（原因见 SkipReason）
	SkipReason SkipReason // Skipped 时非空
	Changed    bool       // 内容相对旧文件有变化（触发写盘）
	Count      int        // entries 数量
	Warnings   []string   // 采集降级告警
}

// Generate 采集本地数据并把 SettingsFileName 写入 uiDir。
// uiDir 不存在或 external UI 尚未装好 → Skipped（不报错）。
// 任一数据源缺失 → 降级 + warning。
//
// 与 sing-box 共用 ui_dir 的两条约束（reF1nd 版 clashapi 行为，已核对源码）：
//
//  1. sing-box 只在 ui_dir **为空**时才首次下载 external UI
//     （checkAndDownloadExternalUI：`len(entries) == 0 || update`）。我们的文件
//     会让目录"看起来非空"——UI 下载一次失败后就再没有第二次机会，面板永久 404。
//     所以 UI 装好之前一律不写，并顺手清掉可能残留的旧文件。
//  2. external_ui_update_interval 到点且上游 **确有更新**（非 304）时，sing-box 会
//     removeAllInDirectory(ui_dir) 再解压——我们的文件必然被删。这里不做对抗，
//     靠每轮 sync 重新生成自愈；空窗最长一个 sync interval。
func Generate(ctx context.Context, uiDir string, static map[string]string) (Result, error) {
	if fi, err := os.Stat(uiDir); err != nil || !fi.IsDir() {
		return Result{Skipped: true, SkipReason: SkipUIDirAbsent}, nil
	}
	installed, err := externalUIInstalled(uiDir)
	if err != nil {
		return Result{}, err
	}
	if !installed {
		removeOwnFiles(uiDir)
		return Result{Skipped: true, SkipReason: SkipUINotInstalled}, nil
	}
	raw, warns := Collect(ctx)
	entries := BuildEntries(raw, static)
	content, err := renderPayload(entries)
	if err != nil {
		return Result{Warnings: warns}, err
	}
	changed, err := writeIfChanged(filepath.Join(uiDir, SettingsFileName), content)
	if err != nil {
		return Result{Warnings: warns}, err
	}
	_ = os.Remove(filepath.Join(uiDir, legacyFileName))
	return Result{Changed: changed, Count: len(entries), Warnings: warns}, nil
}

// ownFileNames 是本包在 ui_dir 里写过的全部文件名（含历史名与原子写中间态）。
// 判断"UI 是否已安装"时要把它们排除，否则我们自己写的文件会让判据永远成立。
func ownFileNames() []string {
	return []string{
		SettingsFileName, SettingsFileName + ".tmp",
		legacyFileName, legacyFileName + ".tmp",
	}
}

// externalUIInstalled 判断 uiDir 里是否已有 external UI 的内容——即除本包自己
// 写的文件之外还有别的条目。
func externalUIInstalled(uiDir string) (bool, error) {
	entries, err := os.ReadDir(uiDir)
	if err != nil {
		return false, err
	}
	own := make(map[string]bool, 4)
	for _, n := range ownFileNames() {
		own[n] = true
	}
	for _, e := range entries {
		if !own[e.Name()] {
			return true, nil
		}
	}
	return false, nil
}

// removeOwnFiles 清掉本包写过的文件。UI 没装好时调用：留着它们会让 sing-box
// 永远认为 ui_dir 非空，从而再也不去下载 UI。
func removeOwnFiles(uiDir string) {
	for _, n := range ownFileNames() {
		_ = os.Remove(filepath.Join(uiDir, n))
	}
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
	// 去掉 Encoder.Encode 追加的尾换行：与 Python 脚本 print(text) / stdout 路径逐字节一致
	// （注意 Python 的 -o 文件路径会写 text+"\n"，本实现刻意不带尾换行，勿"修"回去）。
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
	defer os.Remove(tmp)
	if err := os.Rename(tmp, path); err != nil {
		return false, err
	}
	return true, nil
}
