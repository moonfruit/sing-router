package daemon

import (
	"bufio"
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/moonfruit/sing2seq/clef"
)

func writeFakeLog(t *testing.T, lines []string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "sing-router.log")
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestAPILogsTailReturnsNDJSON(t *testing.T) {
	lines := []string{
		`{"@t":"2026-05-09T00:00:01Z","@l":"Information","Source":"daemon","EventID":"supervisor.boot.started"}`,
		`{"@t":"2026-05-09T00:00:02Z","@l":"Information","Source":"sing-box","EventID":"router.connect"}`,
		`{"@t":"2026-05-09T00:00:03Z","@l":"Warning","Source":"daemon","EventID":"shell.teardown.failed"}`,
	}
	path := writeFakeLog(t, lines)
	mux := NewMux(APIDeps{LogFile: path})
	ts := httptest.NewServer(mux)
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/api/v1/logs?n=10")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != 200 {
		t.Fatalf("status %d", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "application/x-ndjson") {
		t.Fatalf("ct %q", ct)
	}
	var got []string
	sc := bufio.NewScanner(resp.Body)
	for sc.Scan() {
		got = append(got, sc.Text())
	}
	if len(got) != 3 {
		t.Fatalf("want 3 lines, got %d: %v", len(got), got)
	}
	for i, want := range lines {
		if got[i] != want {
			t.Fatalf("line %d:\n want %s\n got  %s", i, want, got[i])
		}
	}
}

func TestAPILogsFilters(t *testing.T) {
	lines := []string{
		`{"@l":"Verbose","Source":"daemon","EventID":"trace.x"}`,
		`{"@l":"Information","Source":"daemon","EventID":"supervisor.boot.started"}`,
		`{"@l":"Information","Source":"sing-box","EventID":"router.connect"}`,
		`{"@l":"Warning","Source":"daemon","EventID":"shell.teardown.failed"}`,
		`{"@l":"Error","Source":"sing-box","EventID":"router.crash"}`,
	}
	path := writeFakeLog(t, lines)
	mux := NewMux(APIDeps{LogFile: path})
	ts := httptest.NewServer(mux)
	defer ts.Close()

	cases := []struct {
		name  string
		query string
		want  int
	}{
		{"source=daemon", "source=daemon", 3},
		{"source=sing-box", "source=sing-box", 2},
		{"level=warn", "level=warn", 2},
		{"event_id prefix", "event_id=router.", 2},
		{"combined", "source=sing-box&level=error", 1},
		{"n trims newest", "n=2", 2},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			resp, err := http.Get(ts.URL + "/api/v1/logs?" + c.query)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = resp.Body.Close() }()
			if resp.StatusCode != 200 {
				t.Fatalf("status %d", resp.StatusCode)
			}
			var got int
			sc := bufio.NewScanner(resp.Body)
			for sc.Scan() {
				if strings.TrimSpace(sc.Text()) != "" {
					got++
				}
			}
			if got != c.want {
				t.Fatalf("want %d lines, got %d", c.want, got)
			}
		})
	}
}

func TestAPILogsMissingFileIsEmpty(t *testing.T) {
	mux := NewMux(APIDeps{LogFile: filepath.Join(t.TempDir(), "missing.log")})
	ts := httptest.NewServer(mux)
	defer ts.Close()
	resp, err := http.Get(ts.URL + "/api/v1/logs?n=10")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != 200 {
		t.Fatalf("status %d", resp.StatusCode)
	}
}

func TestAPILogsInvalidQuery(t *testing.T) {
	mux := NewMux(APIDeps{})
	ts := httptest.NewServer(mux)
	defer ts.Close()
	resp, err := http.Get(ts.URL + "/api/v1/logs?source=bogus")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != 400 {
		t.Fatalf("status %d", resp.StatusCode)
	}
}

func TestAPILogsFollowStreamsBusEvents(t *testing.T) {
	bus := clef.NewBus(64)
	defer bus.Close()
	em := clef.NewEmitter(clef.EmitterConfig{Source: "daemon", Bus: bus, MinLevel: clef.LevelTrace})

	hist := []string{
		`{"@l":"Information","Source":"daemon","EventID":"supervisor.boot.started"}`,
	}
	path := writeFakeLog(t, hist)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	mux := NewMux(APIDeps{Bus: bus, LogFile: path, Ctx: ctx})
	ts := httptest.NewServer(mux)
	defer ts.Close()

	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, ts.URL+"/api/v1/logs?follow=true&n=10", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/event-stream") {
		t.Fatalf("ct %q", ct)
	}

	sc := bufio.NewScanner(resp.Body)
	// 等连接建立后再 publish，避免事件丢在 subscribe 之前。
	histDone := make(chan struct{})
	gotLines := make(chan string, 8)
	go func() {
		seenHist := false
		for sc.Scan() {
			line := sc.Text()
			if line == "" {
				if !seenHist {
					seenHist = true
					close(histDone)
				}
				continue
			}
			if strings.HasPrefix(line, "data: ") {
				gotLines <- strings.TrimPrefix(line, "data: ")
			}
		}
	}()

	select {
	case <-histDone:
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for history flush")
	}
	// 历史那条
	select {
	case got := <-gotLines:
		if !strings.Contains(got, "supervisor.boot.started") {
			t.Fatalf("want history line, got %s", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for history line")
	}

	em.Info("supervisor", "supervisor.live.event", "live", nil)

	select {
	case got := <-gotLines:
		if !strings.Contains(got, "supervisor.live.event") {
			t.Fatalf("want live event, got %s", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for live event")
	}

	cancel() // 关 client 端 → handler 退出
}
