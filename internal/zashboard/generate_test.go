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
	entries := []Entry{{Key: "127.0.0.1", Label: "💻本机", ID: "id-1"}}
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
	// 内层值是紧凑 JSON 字符串（无空格分隔）
	if !strings.Contains(s, `[{\"key\":\"127.0.0.1\",\"label\":\"💻本机\",\"id\":\"id-1\"}]`) {
		t.Fatalf("inner not compact:\n%s", s)
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
	if len(back) != 1 || back[0].Key != "127.0.0.1" {
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

func TestGenerateWritesStaticOnly(t *testing.T) {
	// mac 上 Collect 的命令/文件都缺失 → 仅静态表生效，端到端验证 Generate。
	ui := t.TempDir()
	static := map[string]string{"127.0.0.1": "💻本机"}
	res, err := Generate(context.Background(), ui, static)
	if err != nil {
		t.Fatal(err)
	}
	if res.Skipped || !res.Changed || res.Count != 1 {
		t.Fatalf("unexpected result %#v", res)
	}
	data, err := os.ReadFile(filepath.Join(ui, "zashboard.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "💻本机") {
		t.Fatalf("file missing label:\n%s", data)
	}
}
