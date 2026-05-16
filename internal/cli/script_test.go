package cli

import (
	"path/filepath"
	"strings"
	"testing"
)

// loadScript 必须在读到 .tmpl 资产时渲染模板，否则 `sing-router script koolshare/N99 | sh`
// 这种调试链路会拿到原样模板，BINARY guard 直接跳过——等于 Codex P2 的回归。
func TestLoadScript_RendersKoolshareTemplate(t *testing.T) {
	data, err := loadScript("koolshare/N99")
	if err != nil {
		t.Fatalf("loadScript: %v", err)
	}
	s := string(data)
	if strings.Contains(s, "{{") || strings.Contains(s, "}}") {
		t.Fatalf("rendered koolshare/N99 still contains template syntax:\n%s", s)
	}
	if !strings.Contains(s, `BINARY="`) {
		t.Fatalf("rendered output missing BINARY= line:\n%s", s)
	}
	// 渲染应当烧进 self 路径（测试可执行体）；用 filepath.IsAbs 守住"必须绝对路径"。
	for _, line := range strings.Split(s, "\n") {
		l := strings.TrimSpace(line)
		if !strings.HasPrefix(l, `BINARY="`) {
			continue
		}
		val := strings.TrimSuffix(strings.TrimPrefix(l, `BINARY="`), `"`)
		if !filepath.IsAbs(val) {
			t.Errorf("BINARY=%q is not absolute", val)
		}
	}
}

// 非 .tmpl 资产保持原样返回，避免对裸 shell 脚本做无谓的模板解析（哪天脚本里
// 出现 `{{` 字面量会立刻 parse fail）。
func TestLoadScript_NonTemplatePassthrough(t *testing.T) {
	data, err := loadScript("startup")
	if err != nil {
		t.Fatalf("loadScript: %v", err)
	}
	if !strings.HasPrefix(string(data), "#!/usr/bin/env bash") {
		t.Errorf("startup asset content unexpected:\n%s", data)
	}
}

func TestLoadScript_UnknownNameErrors(t *testing.T) {
	if _, err := loadScript("nonexistent"); err == nil {
		t.Fatal("expected error for unknown script name")
	} else if !strings.Contains(err.Error(), "unknown script") {
		t.Errorf("error should mention 'unknown script', got: %v", err)
	}
}
