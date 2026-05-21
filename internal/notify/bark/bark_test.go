package bark

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"

	"github.com/moonfruit/sing-router/internal/notify"
)

// capturingServer 是假 Bark 服务器：记录最近一次收到的表单，按 status 应答。
type capturingServer struct {
	srv    *httptest.Server
	status int

	mu   sync.Mutex
	form url.Values
}

func newCapturingServer() *capturingServer {
	cs := &capturingServer{status: http.StatusOK}
	cs.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		cs.mu.Lock()
		cs.form = r.Form
		cs.mu.Unlock()
		w.WriteHeader(cs.status)
		_, _ = w.Write([]byte(`{"code":` + http.StatusText(cs.status) + `}`))
	}))
	return cs
}

func (cs *capturingServer) close() { cs.srv.Close() }

func (cs *capturingServer) lastForm() url.Values {
	cs.mu.Lock()
	defer cs.mu.Unlock()
	return cs.form
}

func sampleNotification() notify.Notification {
	return notify.Notification{
		Kind:     "apply.ok",
		Title:    "✅ 资源已更新",
		Body:     "sing-box 已用新资源重启",
		Priority: notify.PriorityNormal,
	}
}

func TestNewRequiresKey(t *testing.T) {
	if _, err := New(Config{Key: ""}); err == nil {
		t.Error("New should reject empty key")
	}
}

func TestNewRejectsBadEncryption(t *testing.T) {
	_, err := New(Config{
		Key:        "k",
		Encryption: &EncryptionConfig{Algorithm: "AES256", Mode: "CBC", Key: "tooshort"},
	})
	if err == nil {
		t.Error("New should reject key length mismatch")
	}
}

func TestSendPlaintext(t *testing.T) {
	cs := newCapturingServer()
	defer cs.close()

	ch, err := New(Config{BaseURL: cs.srv.URL, Key: "testkey", Group: "sing-router"})
	if err != nil {
		t.Fatal(err)
	}
	if err := ch.Send(context.Background(), sampleNotification()); err != nil {
		t.Fatalf("Send: %v", err)
	}
	form := cs.lastForm()
	if form.Get("title") != "✅ 资源已更新" {
		t.Errorf("title = %q", form.Get("title"))
	}
	if form.Get("body") != "sing-box 已用新资源重启" {
		t.Errorf("body = %q", form.Get("body"))
	}
	if form.Get("markdown") != "sing-box 已用新资源重启" {
		t.Errorf("markdown = %q", form.Get("markdown"))
	}
	if form.Get("level") != "active" {
		t.Errorf("level = %q, want active", form.Get("level"))
	}
	if form.Get("group") != "sing-router" {
		t.Errorf("group = %q", form.Get("group"))
	}
	if form.Get("id") != "sing-router.apply.ok" {
		t.Errorf("id = %q", form.Get("id"))
	}
	if form.Get("isArchive") != "1" {
		t.Errorf("isArchive = %q", form.Get("isArchive"))
	}
}

func TestSendLevelMapping(t *testing.T) {
	cs := newCapturingServer()
	defer cs.close()
	ch, _ := New(Config{BaseURL: cs.srv.URL, Key: "k"})

	cases := map[notify.Priority]string{
		notify.PriorityLow:      "passive",
		notify.PriorityNormal:   "active",
		notify.PriorityHigh:     "timeSensitive",
		notify.PriorityCritical: "critical",
	}
	for p, want := range cases {
		n := sampleNotification()
		n.Priority = p
		if err := ch.Send(context.Background(), n); err != nil {
			t.Fatalf("Send(%v): %v", p, err)
		}
		if got := cs.lastForm().Get("level"); got != want {
			t.Errorf("priority %v -> level %q, want %q", p, got, want)
		}
	}
}

func TestSendSuppressedCount(t *testing.T) {
	cs := newCapturingServer()
	defer cs.close()
	ch, _ := New(Config{BaseURL: cs.srv.URL, Key: "k"})

	n := sampleNotification()
	n.Fields = map[string]any{"suppressed_count": 4}
	if err := ch.Send(context.Background(), n); err != nil {
		t.Fatal(err)
	}
	if body := cs.lastForm().Get("body"); !strings.Contains(body, "另有 4 次") {
		t.Errorf("body should mention suppressed count: %q", body)
	}
}

func TestSendSubtitleOmittedWhenEmpty(t *testing.T) {
	cs := newCapturingServer()
	defer cs.close()
	ch, _ := New(Config{BaseURL: cs.srv.URL, Key: "k"})

	if err := ch.Send(context.Background(), sampleNotification()); err != nil {
		t.Fatal(err)
	}
	if _, ok := cs.lastForm()["subtitle"]; ok {
		t.Error("subtitle should be omitted when empty")
	}
}

func TestSendNon2xxReturnsError(t *testing.T) {
	cs := newCapturingServer()
	cs.status = http.StatusBadRequest
	defer cs.close()
	ch, _ := New(Config{BaseURL: cs.srv.URL, Key: "k"})

	if err := ch.Send(context.Background(), sampleNotification()); err == nil {
		t.Error("Send should return error on non-2xx response")
	}
}

func TestSendEncrypted(t *testing.T) {
	cs := newCapturingServer()
	defer cs.close()

	encCfg := &EncryptionConfig{Algorithm: "AES256", Mode: "CBC", Key: keyOf(32)}
	ch, err := New(Config{BaseURL: cs.srv.URL, Key: "k", Group: "g", Encryption: encCfg})
	if err != nil {
		t.Fatal(err)
	}
	if err := ch.Send(context.Background(), sampleNotification()); err != nil {
		t.Fatalf("Send: %v", err)
	}
	form := cs.lastForm()
	ciphertext := form.Get("ciphertext")
	iv := form.Get("iv")
	if ciphertext == "" || iv == "" {
		t.Fatalf("encrypted send should carry ciphertext+iv, got ct=%q iv=%q", ciphertext, iv)
	}
	// 明文参数不应出现。
	if form.Get("title") != "" || form.Get("body") != "" {
		t.Error("encrypted send should not leak plaintext params")
	}
	// 解密还原并校验 JSON 内容。
	spec, _ := newCipherSpec(encCfg.Algorithm, encCfg.Mode, []byte(encCfg.Key))
	plain, err := decrypt(spec, ciphertext, iv)
	if err != nil {
		t.Fatalf("decrypt: %v", err)
	}
	var payload map[string]string
	if err := json.Unmarshal(plain, &payload); err != nil {
		t.Fatalf("payload not valid JSON: %v", err)
	}
	if payload["title"] != "✅ 资源已更新" {
		t.Errorf("decrypted title = %q", payload["title"])
	}
	if payload["level"] != "active" {
		t.Errorf("decrypted level = %q", payload["level"])
	}
}
