package zashboard

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRenderPayloadFormat(t *testing.T) {
	entries := []Entry{
		{Key: "127.0.0.1", Label: "💻本机", ID: "id-1"},
		{Key: "10.0.0.5", Label: "A<b>&c", ID: "id-2"},
	}
	got, err := renderPayload(entries)
	if err != nil {
		t.Fatal(err)
	}
	s := string(got)
	// 外层 2 空格缩进 + 单一键
	if !strings.Contains(s, "{\n  \"config/source-ip-label-list\": ") {
		t.Fatalf("outer indent/key wrong:\n%s", s)
	}
	// emoji 原样 UTF-8（不转义为 \uXXXX）
	if !strings.Contains(s, "💻本机") {
		t.Fatalf("emoji escaped:\n%s", s)
	}
	// 内层值是紧凑 JSON 字符串（无空格分隔）；检查第一条目的紧凑形式
	if !strings.Contains(s, `{\"key\":\"127.0.0.1\",\"label\":\"💻本机\",\"id\":\"id-1\"}`) {
		t.Fatalf("inner not compact:\n%s", s)
	}
	// SetEscapeHTML(false): <>& 必须原样，不得转义为 < / > / &
	if !strings.Contains(s, "A<b>&c") {
		t.Fatalf("HTML chars escaped:\n%s", s)
	}
	if strings.Contains(s, `\u003c`) || strings.Contains(s, `\u003e`) || strings.Contains(s, `\u0026`) {
		t.Fatalf("HTML escaping not disabled:\n%s", s)
	}
	// 尾换行必须被去掉
	if strings.HasSuffix(s, "\n") {
		t.Fatalf("trailing newline not trimmed:\n%q", s)
	}
	// 可被解析回来：外层 map 取值再解析内层数组
	var outer map[string]string
	if err := json.Unmarshal(got, &outer); err != nil {
		t.Fatalf("outer parse: %v", err)
	}
	var back []Entry
	if err := json.Unmarshal([]byte(outer["config/source-ip-label-list"]), &back); err != nil {
		t.Fatalf("inner parse: %v", err)
	}
	if len(back) != 2 || back[0].Key != "127.0.0.1" {
		t.Fatalf("roundtrip mismatch: %#v", back)
	}
}

func TestWriteIfChangedGate(t *testing.T) {
	path := filepath.Join(t.TempDir(), "zashboard.json")
	content := []byte("hello")

	changed, err := writeIfChanged(path, content)
	if err != nil || !changed {
		t.Fatalf("first write changed=%v err=%v", changed, err)
	}
	changed, err = writeIfChanged(path, content) // 内容相同 → 不重写
	if err != nil || changed {
		t.Fatalf("second write changed=%v err=%v (want false)", changed, err)
	}
	changed, err = writeIfChanged(path, []byte("world")) // 内容变化 → 重写
	if err != nil || !changed {
		t.Fatalf("third write changed=%v err=%v (want true)", changed, err)
	}
}

func TestGenerateSkipWhenUIDirAbsent(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "no-such-ui")
	res, err := Generate(context.Background(), missing, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Skipped {
		t.Fatalf("want Skipped, got %#v", res)
	}
}

// fakeInstalledUI 造一个"external UI 已装好"的 ui_dir：只要有一个不属于本包的
// 文件，Generate 就认为 sing-box 已经把 UI 解压过了。
func fakeInstalledUI(t *testing.T) string {
	t.Helper()
	ui := t.TempDir()
	if err := os.WriteFile(filepath.Join(ui, "index.html"), []byte("<html></html>"), 0o644); err != nil {
		t.Fatal(err)
	}
	return ui
}

func TestGenerateWritesStaticOnly(t *testing.T) {
	// mac 上 Collect 的命令/文件都缺失 → 仅静态表生效，端到端验证 Generate。
	ui := fakeInstalledUI(t)
	static := map[string]string{"127.0.0.1": "💻本机"}
	res, err := Generate(context.Background(), ui, static)
	if err != nil {
		t.Fatal(err)
	}
	if res.Skipped || !res.Changed || res.Count != 1 {
		t.Fatalf("unexpected result %#v", res)
	}
	data, err := os.ReadFile(filepath.Join(ui, SettingsFileName))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "💻本机") {
		t.Fatalf("file missing label:\n%s", data)
	}
}

// 回归守护：external UI 还没装好时绝不能写文件。
//
// reF1nd 版 clashapi 的 checkAndDownloadExternalUI 只在 `len(entries) == 0` 时
// 首次下载 UI。我们的文件会让 ui_dir 恒非空——UI 下载失败过一次（无网 / 代理没起来）
// 之后就再也不会重试，面板永久 404。所以这里既要跳过，还要把残留清掉。
func TestGenerateSkipsAndCleansWhenUINotInstalled(t *testing.T) {
	ui := t.TempDir()
	// 模拟上一版留下的两种残留
	for _, n := range []string{SettingsFileName, legacyFileName} {
		if err := os.WriteFile(filepath.Join(ui, n), []byte("{}"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	res, err := Generate(context.Background(), ui, map[string]string{"127.0.0.1": "💻本机"})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Skipped || res.SkipReason != SkipUINotInstalled {
		t.Fatalf("want skip(%s), got %#v", SkipUINotInstalled, res)
	}
	entries, err := os.ReadDir(ui)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("ui_dir should be left empty so sing-box retries the UI download, got %v", entries)
	}
}

// UI 装好后，旧文件名要被清掉，只留 SettingsFileName——否则 zashboard 里会
// 出现两份来源不明的配置。
func TestGenerateRemovesLegacyFile(t *testing.T) {
	ui := fakeInstalledUI(t)
	legacy := filepath.Join(ui, legacyFileName)
	if err := os.WriteFile(legacy, []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Generate(context.Background(), ui, map[string]string{"127.0.0.1": "💻本机"}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(legacy); !os.IsNotExist(err) {
		t.Fatalf("legacy %s should be removed; stat err=%v", legacyFileName, err)
	}
	if _, err := os.Stat(filepath.Join(ui, SettingsFileName)); err != nil {
		t.Fatalf("%s missing: %v", SettingsFileName, err)
	}
}
