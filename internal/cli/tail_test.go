package cli

import (
	"bytes"
	"compress/gzip"
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
}

func appendFile(t *testing.T, path, content string) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0644)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(content); err != nil {
		t.Fatal(err)
	}
	_ = f.Close()
}

func collectFromOffset(t *testing.T, path string, n int) []string {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	if _, err := SeekToLastN(f, n); err != nil {
		t.Fatal(err)
	}
	var out []string
	if err := EmitLines(f, func(b []byte) { out = append(out, string(b)) }); err != nil {
		t.Fatal(err)
	}
	return out
}

func TestSeekToLastNBasic(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "log")
	writeFile(t, path, "a\nb\nc\nd\ne\n")

	got := collectFromOffset(t, path, 2)
	want := []string{"d", "e"}
	if !equalSlice(got, want) {
		t.Fatalf("last 2: got %v want %v", got, want)
	}
}

func TestSeekToLastNFewerThanN(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "log")
	writeFile(t, path, "x\ny\n")
	got := collectFromOffset(t, path, 10)
	if !equalSlice(got, []string{"x", "y"}) {
		t.Fatalf("got %v", got)
	}
}

func TestSeekToLastNEmpty(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "log")
	writeFile(t, path, "")
	got := collectFromOffset(t, path, 5)
	if len(got) != 0 {
		t.Fatalf("got %v", got)
	}
}

func TestSeekToLastNWithoutTrailingNewline(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "log")
	writeFile(t, path, "a\nb\nc")
	got := collectFromOffset(t, path, 1)
	// 末行无 \n 但仍应被 EmitLines 取出。SeekToLastN 应定位到 c 起点。
	if len(got) == 0 || got[len(got)-1] != "c" {
		t.Fatalf("got %v", got)
	}
}

func TestSeekToLastNAll(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "log")
	writeFile(t, path, "a\nb\nc\n")
	f, _ := os.Open(path)
	defer func() { _ = f.Close() }()
	off, err := SeekToLastN(f, 0)
	if err != nil || off != 0 {
		t.Fatalf("all should be 0; got off=%d err=%v", off, err)
	}
}

func TestEmitLinesPreservesLongLines(t *testing.T) {
	long := strings.Repeat("x", 70_000)
	r := bytes.NewReader([]byte(long + "\n" + "short\n"))
	var got []string
	if err := EmitLines(r, func(b []byte) { got = append(got, string(b)) }); err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0] != long || got[1] != "short" {
		t.Fatalf("len=%d g0len=%d g1=%q", len(got), len(got[0]), got[1])
	}
}

func TestFollowFD_AppendsBecomeVisible(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "log")
	writeFile(t, path, "a\n")

	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	off, _ := f.Seek(0, io.SeekEnd)
	_ = f.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	var mu sync.Mutex
	var got []string
	done := make(chan error, 1)
	go func() {
		done <- Follow(ctx, path, off, func(b []byte) {
			mu.Lock()
			got = append(got, string(b))
			mu.Unlock()
		}, FollowConfig{FollowName: false})
	}()

	time.Sleep(100 * time.Millisecond)
	appendFile(t, path, "b\nc\n")
	waitFor(t, &mu, &got, 2, 1500*time.Millisecond)
	cancel()
	<-done

	mu.Lock()
	defer mu.Unlock()
	if !equalSlice(got, []string{"b", "c"}) {
		t.Fatalf("got %v", got)
	}
}

func TestFollowName_HandlesRenameRotate(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "log")
	writeFile(t, path, "1\n")

	f, _ := os.Open(path)
	off, _ := f.Seek(0, io.SeekEnd)
	_ = f.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	var mu sync.Mutex
	var got []string
	done := make(chan error, 1)
	go func() {
		done <- Follow(ctx, path, off, func(b []byte) {
			mu.Lock()
			got = append(got, string(b))
			mu.Unlock()
		}, FollowConfig{FollowName: true})
	}()

	time.Sleep(100 * time.Millisecond)
	appendFile(t, path, "2\n")
	waitFor(t, &mu, &got, 1, 1500*time.Millisecond)

	// rotate by rename: mv log log.1 ; create new log
	if err := os.Rename(path, path+".1"); err != nil {
		t.Fatal(err)
	}
	writeFile(t, path, "3\n")
	waitFor(t, &mu, &got, 2, 2000*time.Millisecond)

	cancel()
	<-done

	mu.Lock()
	defer mu.Unlock()
	if !equalSlice(got, []string{"2", "3"}) {
		t.Fatalf("got %v", got)
	}
}

func TestFollowName_HandlesTruncate(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "log")
	writeFile(t, path, "old\n")

	f, _ := os.Open(path)
	off, _ := f.Seek(0, io.SeekEnd)
	_ = f.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	var mu sync.Mutex
	var got []string
	done := make(chan error, 1)
	go func() {
		done <- Follow(ctx, path, off, func(b []byte) {
			mu.Lock()
			got = append(got, string(b))
			mu.Unlock()
		}, FollowConfig{FollowName: true})
	}()
	time.Sleep(100 * time.Millisecond)

	// truncate then write new content；中间留出时间让 fsnotify 事件分离，
	// 模拟真实 logrotate copytruncate 场景（truncate 与下次 daemon write 之间有间隔）。
	if err := os.Truncate(path, 0); err != nil {
		t.Fatal(err)
	}
	// 留出窗口让 ticker 至少跑两次 stat 抓到 size=0 中间状态（避免 truncate→write
	// 在 macOS kqueue 下事件合并导致看不到 0）。
	time.Sleep(700 * time.Millisecond)
	appendFile(t, path, "fresh\n")
	waitFor(t, &mu, &got, 1, 2000*time.Millisecond)

	cancel()
	<-done

	mu.Lock()
	defer mu.Unlock()
	if len(got) == 0 || got[len(got)-1] != "fresh" {
		t.Fatalf("got %v", got)
	}
}

func collectEmitLastN(t *testing.T, content string, n int) []string {
	t.Helper()
	var out []string
	if err := EmitLastN(strings.NewReader(content), n, func(b []byte) {
		out = append(out, string(b))
	}); err != nil {
		t.Fatal(err)
	}
	return out
}

func TestEmitLastN(t *testing.T) {
	const five = "a\nb\nc\nd\ne\n"
	cases := []struct {
		name    string
		content string
		n       int
		want    []string
	}{
		{"fewer than total", five, 2, []string{"d", "e"}},
		{"equal to total", five, 5, []string{"a", "b", "c", "d", "e"}},
		{"more than total", five, 10, []string{"a", "b", "c", "d", "e"}},
		{"n zero means all", five, 0, []string{"a", "b", "c", "d", "e"}},
		{"n negative means all", five, -1, []string{"a", "b", "c", "d", "e"}},
		{"empty input", "", 3, nil},
		{"no trailing newline", "a\nb\nc", 2, []string{"b", "c"}},
		{"single line no newline", "solo", 1, []string{"solo"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := collectEmitLastN(t, tc.content, tc.n)
			if !equalSlice(got, tc.want) {
				t.Fatalf("got %v want %v", got, tc.want)
			}
		})
	}
}

func TestEmitLastN_GzipStream(t *testing.T) {
	var buf bytes.Buffer
	gw := gzip.NewWriter(&buf)
	if _, err := gw.Write([]byte("one\ntwo\nthree\nfour\n")); err != nil {
		t.Fatal(err)
	}
	if err := gw.Close(); err != nil {
		t.Fatal(err)
	}

	gr, err := gzip.NewReader(&buf)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = gr.Close() }()

	var got []string
	if err := EmitLastN(gr, 2, func(b []byte) { got = append(got, string(b)) }); err != nil {
		t.Fatal(err)
	}
	if !equalSlice(got, []string{"three", "four"}) {
		t.Fatalf("got %v", got)
	}
}

// waitFor 等待 got 至少有 wantLen 个元素，超时则失败。
func waitFor(t *testing.T, mu *sync.Mutex, got *[]string, wantLen int, deadline time.Duration) {
	t.Helper()
	end := time.Now().Add(deadline)
	for time.Now().Before(end) {
		mu.Lock()
		n := len(*got)
		mu.Unlock()
		if n >= wantLen {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	mu.Lock()
	defer mu.Unlock()
	t.Fatalf("timeout waiting for %d entries; got %v", wantLen, *got)
}

func equalSlice(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
