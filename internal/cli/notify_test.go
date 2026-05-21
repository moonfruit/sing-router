package cli

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/moonfruit/sing-router/internal/config"
)

func barkServer(status int) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(status)
	}))
}

func TestRunNotifyTestSuccess(t *testing.T) {
	srv := barkServer(http.StatusOK)
	defer srv.Close()

	cfg := config.NotifyConfig{
		Enabled: true,
		Bark: []config.BarkConfig{
			{Name: "phone", Enabled: true, BaseURL: srv.URL, Key: "k"},
		},
	}
	var buf bytes.Buffer
	if err := runNotifyTest(&buf, cfg, ""); err != nil {
		t.Fatalf("runNotifyTest: %v", err)
	}
	if !strings.Contains(buf.String(), "OK") {
		t.Errorf("output should report OK: %q", buf.String())
	}
}

func TestRunNotifyTestReportsFailure(t *testing.T) {
	srv := barkServer(http.StatusBadRequest)
	defer srv.Close()

	cfg := config.NotifyConfig{
		Enabled: true,
		Bark: []config.BarkConfig{
			{Name: "phone", Enabled: true, BaseURL: srv.URL, Key: "k"},
		},
	}
	var buf bytes.Buffer
	err := runNotifyTest(&buf, cfg, "")
	if err == nil {
		t.Fatal("runNotifyTest should fail when channel returns non-2xx")
	}
	if !strings.Contains(buf.String(), "FAIL") {
		t.Errorf("output should report FAIL: %q", buf.String())
	}
}

func TestRunNotifyTestChannelFilter(t *testing.T) {
	srv := barkServer(http.StatusOK)
	defer srv.Close()

	cfg := config.NotifyConfig{
		Enabled: true,
		Bark: []config.BarkConfig{
			{Name: "phone", Enabled: true, BaseURL: srv.URL, Key: "k1"},
			{Name: "ipad", Enabled: true, BaseURL: srv.URL, Key: "k2"},
		},
	}
	var buf bytes.Buffer
	if err := runNotifyTest(&buf, cfg, "ipad"); err != nil {
		t.Fatalf("runNotifyTest: %v", err)
	}
	out := buf.String()
	if strings.Contains(out, "phone") {
		t.Errorf("filter should exclude phone: %q", out)
	}
	if !strings.Contains(out, "ipad") {
		t.Errorf("filter should include ipad: %q", out)
	}
}

func TestRunNotifyTestNoChannel(t *testing.T) {
	cfg := config.NotifyConfig{Enabled: true}
	var buf bytes.Buffer
	if err := runNotifyTest(&buf, cfg, ""); err == nil {
		t.Error("runNotifyTest should error when no channel configured")
	}
}

func TestCheckNotifyDisabled(t *testing.T) {
	checks := checkNotify(&config.DaemonConfig{Notify: config.NotifyConfig{Enabled: false}})
	if len(checks) != 1 || checks[0].Status != "info" {
		t.Errorf("disabled notify should yield one info check, got %+v", checks)
	}
}

func TestCheckNotifyGoodChannel(t *testing.T) {
	cfg := &config.DaemonConfig{Notify: config.NotifyConfig{
		Enabled:     true,
		MinPriority: "normal",
		Bark: []config.BarkConfig{
			{Name: "phone", Enabled: true, BaseURL: "https://api.day.app", Key: "k"},
		},
	}}
	for _, c := range checkNotify(cfg) {
		if c.Status == "fail" {
			t.Errorf("good config should not fail: %+v", c)
		}
	}
}

func TestCheckNotifyDetectsBadChannel(t *testing.T) {
	cases := []struct {
		name string
		bark config.BarkConfig
	}{
		{"empty key", config.BarkConfig{Name: "a", Enabled: true}},
		{"bad base_url", config.BarkConfig{Name: "b", Enabled: true, Key: "k", BaseURL: "not a url"}},
		{
			"bad encryption key length",
			config.BarkConfig{
				Name: "c", Enabled: true, Key: "k",
				Encryption: &config.BarkEncryptionConfig{Algorithm: "AES256", Mode: "CBC", Key: "short"},
			},
		},
	}
	for _, tc := range cases {
		cfg := &config.DaemonConfig{Notify: config.NotifyConfig{
			Enabled: true, MinPriority: "low",
			Bark: []config.BarkConfig{tc.bark},
		}}
		hasFail := false
		for _, c := range checkNotify(cfg) {
			if c.Status == "fail" {
				hasFail = true
			}
		}
		if !hasFail {
			t.Errorf("%s: expected a fail check", tc.name)
		}
	}
}

func TestCheckNotifyNoEnabledChannel(t *testing.T) {
	cfg := &config.DaemonConfig{Notify: config.NotifyConfig{
		Enabled:     true,
		MinPriority: "low",
		Bark:        []config.BarkConfig{{Name: "phone", Enabled: false, Key: "k"}},
	}}
	hasWarn := false
	for _, c := range checkNotify(cfg) {
		if c.Status == "warn" {
			hasWarn = true
		}
	}
	if !hasWarn {
		t.Error("enabled notify with no enabled channel should warn")
	}
}
