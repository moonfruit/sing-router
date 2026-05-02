package log

import (
	"encoding/json"
	"testing"
)

func TestOrderedEventPreservesInsertionOrder(t *testing.T) {
	e := NewEvent()
	e.Set("@t", "2026-05-02T12:00:00+08:00")
	e.Set("@l", "Information")
	e.Set("Source", "daemon")
	e.Set("EventID", "supervisor.boot.ready")

	out, err := json.Marshal(e)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	want := `{"@t":"2026-05-02T12:00:00+08:00","@l":"Information","Source":"daemon","EventID":"supervisor.boot.ready"}`
	if string(out) != want {
		t.Fatalf("\nwant %s\ngot  %s", want, out)
	}
}

func TestOrderedEventOverwriteKeepsPosition(t *testing.T) {
	e := NewEvent()
	e.Set("a", 1)
	e.Set("b", 2)
	e.Set("a", 99)

	out, _ := json.Marshal(e)
	want := `{"a":99,"b":2}`
	if string(out) != want {
		t.Fatalf("want %s, got %s", want, out)
	}
}

func TestOrderedEventGetMissing(t *testing.T) {
	e := NewEvent()
	if _, ok := e.Get("missing"); ok {
		t.Fatal("missing key reported as present")
	}
}

func TestOrderedEventNestedEvent(t *testing.T) {
	inner := NewEvent()
	inner.Set("kind", "fatal")
	outer := NewEvent()
	outer.Set("Source", "daemon")
	outer.Set("Error", inner)

	out, _ := json.Marshal(outer)
	want := `{"Source":"daemon","Error":{"kind":"fatal"}}`
	if string(out) != want {
		t.Fatalf("want %s, got %s", want, out)
	}
}

func TestOrderedEventKeysReturnsCopy(t *testing.T) {
	e := NewEvent()
	e.Set("x", 1)
	e.Set("y", 2)

	ks := e.Keys()
	if len(ks) != 2 || ks[0] != "x" || ks[1] != "y" {
		t.Fatalf("wrong keys: %v", ks)
	}
	// 修改副本不影响原始顺序
	ks[0] = "mutated"
	if e.Keys()[0] != "x" {
		t.Fatal("Keys() returned internal slice, not a defensive copy")
	}
}
