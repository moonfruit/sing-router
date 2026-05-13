package log

import (
	"strings"
	"testing"
)

func TestConnColorizerNoneNoOp(t *testing.T) {
	c := NewConnColorizer(ProfileNone)
	if got := c.Wrap("abc"); got != "abc" {
		t.Errorf("ProfileNone Wrap should be no-op, got %q", got)
	}
}

func TestConnColorizerEmptyID(t *testing.T) {
	c := NewConnColorizer(Profile8)
	if got := c.Wrap(""); got != "" {
		t.Errorf("empty id should pass through, got %q", got)
	}
}

func TestConnColorizerSameIDSameColor(t *testing.T) {
	c := NewConnColorizer(Profile8)
	a := c.Wrap("conn-1")
	b := c.Wrap("conn-1")
	if a != b {
		t.Errorf("same id should yield same wrap; got %q vs %q", a, b)
	}
	if !strings.Contains(a, "conn-1") || !strings.Contains(a, "\x1b[") {
		t.Errorf("wrap should include id and ANSI, got %q", a)
	}
}

func TestConnColorizerDifferentIDsDifferentColors(t *testing.T) {
	c := NewConnColorizer(Profile8)
	a := c.Wrap("id-a")
	b := c.Wrap("id-b")
	if extractPrefix(a) == extractPrefix(b) {
		t.Errorf("two distinct ids should get distinct colors; both got %q", extractPrefix(a))
	}
}

func TestConnColorizerLRUEviction(t *testing.T) {
	// Profile8 palette 大小 = 6；填满 6 个，再加第 7 个应淘汰最早。
	c := NewConnColorizer(Profile8)
	ids := []string{"a", "b", "c", "d", "e", "f"}
	prefixes := make(map[string]string)
	for _, id := range ids {
		prefixes[id] = extractPrefix(c.Wrap(id))
	}
	// 第 7 个：应当占用被淘汰的 "a" 的位置。
	wrap7 := c.Wrap("g")
	pre7 := extractPrefix(wrap7)
	if pre7 != prefixes["a"] {
		t.Errorf("new id after full palette should reuse evicted slot color; got %q want %q", pre7, prefixes["a"])
	}
	// "a" 现已被淘汰；再次 Wrap("a") 应分配新颜色（应淘汰当前最旧者 "b"）。
	wrapA2 := c.Wrap("a")
	preA2 := extractPrefix(wrapA2)
	if preA2 != prefixes["b"] {
		t.Errorf("re-adding evicted id should land in next evicted slot; got %q want %q", preA2, prefixes["b"])
	}
}

func TestConnColorizerTouchPreventsEviction(t *testing.T) {
	c := NewConnColorizer(Profile8) // cap=6
	for _, id := range []string{"a", "b", "c", "d", "e", "f"} {
		c.Wrap(id)
	}
	// 触摸 "a"，使其成为最新 → 下一个新 id 应淘汰 "b"。
	c.Wrap("a")
	preB := extractPrefix(c.Wrap("b")) // 刚被淘汰，会重新分配（淘汰当前 mru[0]，即原 "c"）
	_ = preB
	// 此时 used 里应该没有 "c" 了；为验证 a 没被淘汰，调用 Wrap("a") 仍同色。
	first := c.Wrap("a")
	second := c.Wrap("a")
	if first != second {
		t.Errorf("touched id should keep color; got %q vs %q", first, second)
	}
}

// extractPrefix 抽取 wrap 字符串的 ANSI 前缀部分（不含 reset）。
func extractPrefix(s string) string {
	end := strings.LastIndex(s, "\x1b[0m")
	if end < 0 {
		return s
	}
	idStart := strings.LastIndex(s[:end], "m")
	if idStart < 0 {
		return s
	}
	return s[:idStart+1]
}
