package notify

import (
	"strings"
	"testing"

	"github.com/moonfruit/sing2seq/clef"
)

func eventOf(eventID string, kv ...any) *clef.Event {
	ev := clef.NewEvent()
	ev.Set("EventID", eventID)
	for i := 0; i+1 < len(kv); i += 2 {
		ev.Set(kv[i].(string), kv[i+1])
	}
	return ev
}

// TestCatalogAllKindsRender 守住目录里每个 Kind 都能渲染出非空标题与正文，
// 且 Translate 回填的 Priority 与目录一致。
func TestCatalogAllKindsRender(t *testing.T) {
	for kind, entry := range catalog {
		n, ok := Translate(eventOf(kind))
		if !ok {
			t.Fatalf("%s: Translate returned ok=false", kind)
		}
		if n.Kind != kind {
			t.Errorf("%s: Kind = %q", kind, n.Kind)
		}
		if n.Title == "" {
			t.Errorf("%s: empty Title", kind)
		}
		if n.Body == "" {
			t.Errorf("%s: empty Body", kind)
		}
		if n.Priority != entry.Priority {
			t.Errorf("%s: Priority = %v, want %v", kind, n.Priority, entry.Priority)
		}
	}
}

// TestCatalogExcludes 守住刻意排除的 EventID 不会被翻译成通知（self-loop /
// 用户要求 / 噪音）。
func TestCatalogExcludes(t *testing.T) {
	excluded := []string{
		"apply.noop",
		"sync.item.updated",
		"sync.item.unchanged",
		"apply.failed",
		"supervisor.startup.ok",
		"supervisor.shutdown.ok",
		"supervisor.restart.ok",
		"panic.recovered",
		"notify.send.failed",
		"notify.queue.overflow",
		"seq.enabled",
		"gitee.disabled",
	}
	for _, id := range excluded {
		ev := eventOf(id)
		if IsCatalogued(ev) {
			t.Errorf("%s should not be catalogued", id)
		}
		if _, ok := Translate(ev); ok {
			t.Errorf("%s should not translate", id)
		}
	}
}

func TestTranslateApplyOK(t *testing.T) {
	n, ok := Translate(eventOf("apply.ok", "Bin", true, "Zoo", true, "Rule", false, "CN", false))
	if !ok {
		t.Fatal("apply.ok should translate")
	}
	if n.Priority != PriorityNormal {
		t.Errorf("Priority = %v, want Normal", n.Priority)
	}
	if !strings.Contains(n.Body, "sing-box 二进制") {
		t.Errorf("body missing bin item: %q", n.Body)
	}
	if !strings.Contains(n.Body, "zoo 配置") {
		t.Errorf("body missing zoo item: %q", n.Body)
	}
	if strings.Contains(n.Body, "rule-set 规则集") {
		t.Errorf("body should not list unchanged rule-set: %q", n.Body)
	}
	if strings.Contains(n.Body, "cn.txt") {
		t.Errorf("body should not list unchanged cn: %q", n.Body)
	}
}

func TestTranslateChildCrashed(t *testing.T) {
	n, ok := Translate(eventOf("supervisor.child.crashed", "CrashCount", 3, "BackoffMs", 16000))
	if !ok {
		t.Fatal("should translate")
	}
	if n.Priority != PriorityHigh {
		t.Errorf("Priority = %v, want High", n.Priority)
	}
	if !strings.Contains(n.Body, "第 3 次") {
		t.Errorf("body missing crash count: %q", n.Body)
	}
	if !strings.Contains(n.Body, "16s") {
		t.Errorf("body missing backoff: %q", n.Body)
	}
}

func TestTranslateAppendsErr(t *testing.T) {
	n, _ := Translate(eventOf("apply.check.failed", "Err", "bad json at line 5"))
	if !strings.Contains(n.Body, "bad json at line 5") {
		t.Errorf("body missing err detail: %q", n.Body)
	}
	// 无 Err 字段时不应留下空的 "错误：" 尾巴
	n2, _ := Translate(eventOf("apply.check.failed"))
	if strings.Contains(n2.Body, "错误：") {
		t.Errorf("body should not have empty err suffix: %q", n2.Body)
	}
}

func TestTranslateUncataloguedReturnsFalse(t *testing.T) {
	if _, ok := Translate(eventOf("some.random.event")); ok {
		t.Error("uncatalogued event should return ok=false")
	}
}

func TestEventFieldsExcludesMeta(t *testing.T) {
	ev := eventOf("apply.ok", "Zoo", true)
	ev.Set("@l", "Information")
	ev.Set("Source", "daemon")
	n, _ := Translate(ev)
	if _, ok := n.Fields["@l"]; ok {
		t.Error("Fields should exclude @l")
	}
	if _, ok := n.Fields["Source"]; ok {
		t.Error("Fields should exclude Source")
	}
	if _, ok := n.Fields["EventID"]; ok {
		t.Error("Fields should exclude EventID")
	}
	if v, ok := n.Fields["Zoo"]; !ok || v != true {
		t.Errorf("Fields should keep Zoo: %v", n.Fields)
	}
}
