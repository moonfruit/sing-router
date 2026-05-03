package log

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func mustParse(t *testing.T, s string) *OrderedEvent {
	t.Helper()
	var raw map[string]json.RawMessage
	if err := json.Unmarshal([]byte(s), &raw); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	e := NewEvent()
	var any map[string]any
	_ = json.Unmarshal([]byte(s), &any)
	keys := orderedKeys(s)
	for _, k := range keys {
		e.Set(k, any[k])
	}
	return e
}

// orderedKeys 仅供测试：从 JSON 文本中按出现顺序提取顶层 key。
func orderedKeys(s string) []string {
	var out []string
	decoder := json.NewDecoder(strings.NewReader(s))
	if _, err := decoder.Token(); err != nil {
		return out
	}
	for decoder.More() {
		tok, err := decoder.Token()
		if err != nil {
			return out
		}
		out = append(out, tok.(string))
		var v json.RawMessage
		_ = decoder.Decode(&v)
	}
	return out
}

func TestPrettyDaemonEvent(t *testing.T) {
	in := `{"@t":"2026-05-02T12:34:56.789+08:00","@l":"Information","@mt":"supervisor: sing-box ready in {ReadyDurationMs}ms","Source":"daemon","Module":"supervisor","ReadyDurationMs":1218}`
	e := mustParse(t, in)
	loc, _ := time.LoadLocation("Asia/Shanghai")
	out := Pretty(e, PrettyOptions{LocalTZ: loc, DisableColor: true})
	want := "2026-05-02 12:34:56.789 INFO  [daemon] supervisor: sing-box ready in 1218ms"
	if out != want {
		t.Fatalf("\nwant %q\ngot  %q", want, out)
	}
}

func TestPrettyShowsDifferentTZ(t *testing.T) {
	in := `{"@t":"2026-05-02T04:34:56.789+00:00","@l":"Information","@mt":"hello","Source":"daemon"}`
	e := mustParse(t, in)
	loc, _ := time.LoadLocation("Asia/Shanghai")
	out := Pretty(e, PrettyOptions{LocalTZ: loc, DisableColor: true})
	if !strings.HasPrefix(out, "+0000 ") {
		t.Fatalf("expected TZ prefix, got %q", out)
	}
}

func TestPrettySingBoxEvent(t *testing.T) {
	in := `{"@t":"2026-05-02T12:34:57.001+08:00","@l":"Information","@mt":"{Module}/{Type}: {Detail}","Source":"sing-box","Module":"router","Type":"default","Detail":"outbound connection to www.example.com:443"}`
	e := mustParse(t, in)
	loc, _ := time.LoadLocation("Asia/Shanghai")
	out := Pretty(e, PrettyOptions{LocalTZ: loc, DisableColor: true})
	want := "2026-05-02 12:34:57.001 INFO  [sing-box] router/default: outbound connection to www.example.com:443"
	if out != want {
		t.Fatalf("\nwant %q\ngot  %q", want, out)
	}
}

func TestPrettyMissingTemplate(t *testing.T) {
	in := `{"@t":"2026-05-02T12:34:56+08:00","@l":"Warning","Source":"daemon"}`
	e := mustParse(t, in)
	loc, _ := time.LoadLocation("Asia/Shanghai")
	out := Pretty(e, PrettyOptions{LocalTZ: loc, DisableColor: true})
	if !strings.Contains(out, "WARN") || !strings.Contains(out, "[daemon]") {
		t.Fatalf("pretty fallback missing: %q", out)
	}
}
