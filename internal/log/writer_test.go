package log

import (
	"compress/gzip"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func newTestWriter(t *testing.T, maxSize int64, maxBackups int) (*Writer, string) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sing-router.log")
	w, err := NewWriter(WriterConfig{
		Path:       path,
		MaxSize:    maxSize,
		MaxBackups: maxBackups,
		Gzip:       true,
	})
	if err != nil {
		t.Fatalf("NewWriter: %v", err)
	}
	t.Cleanup(func() { _ = w.Close() })
	return w, path
}

func TestWriterAppendsLines(t *testing.T) {
	w, path := newTestWriter(t, 1024, 3)
	e := NewEvent()
	e.Set("@l", "Information")
	e.Set("Source", "daemon")
	e.Set("@mt", "hello {Name}")
	e.Set("Name", "world")
	if err := w.Write(e); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if err := w.Sync(); err != nil {
		t.Fatalf("Sync: %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if !strings.HasSuffix(string(data), "\n") {
		t.Fatal("expected trailing newline")
	}
	var parsed map[string]any
	if err := json.Unmarshal([]byte(strings.TrimSpace(string(data))), &parsed); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if parsed["@l"] != "Information" {
		t.Fatal("@l mismatch")
	}
}

func TestWriterRotatesAtMaxSize(t *testing.T) {
	w, path := newTestWriter(t, 200, 3)
	big := strings.Repeat("x", 80)
	for i := 0; i < 10; i++ {
		e := NewEvent()
		e.Set("@l", "Information")
		e.Set("@mt", big)
		if err := w.Write(e); err != nil {
			t.Fatalf("write %d: %v", i, err)
		}
	}
	if err := w.Sync(); err != nil {
		t.Fatalf("Sync: %v", err)
	}
	if err := w.WaitGzip(); err != nil {
		t.Fatalf("WaitGzip: %v", err)
	}

	entries, err := os.ReadDir(filepath.Dir(path))
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	var gzCount int
	var sawActive bool
	for _, e := range entries {
		if e.Name() == "sing-router.log" {
			sawActive = true
		}
		if strings.HasSuffix(e.Name(), ".gz") {
			gzCount++
		}
	}
	if !sawActive {
		t.Fatal("active log file missing")
	}
	if gzCount == 0 {
		t.Fatal("expected at least one gzipped backup")
	}
}

func TestWriterPrunesOldBackups(t *testing.T) {
	w, path := newTestWriter(t, 100, 2)
	big := strings.Repeat("y", 60)
	for i := 0; i < 30; i++ {
		e := NewEvent()
		e.Set("@l", "Information")
		e.Set("@mt", big)
		_ = w.Write(e)
	}
	_ = w.Sync()
	_ = w.WaitGzip()

	entries, _ := os.ReadDir(filepath.Dir(path))
	var gzCount int
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".gz") {
			gzCount++
		}
	}
	if gzCount > 2 {
		t.Fatalf("expected at most 2 gz backups (max_backups=2), got %d", gzCount)
	}
}

func TestWriterGzipBackupReadable(t *testing.T) {
	w, path := newTestWriter(t, 80, 3)
	for i := 0; i < 6; i++ {
		e := NewEvent()
		e.Set("@l", "Information")
		e.Set("@mt", strings.Repeat("z", 40))
		_ = w.Write(e)
	}
	_ = w.Sync()
	_ = w.WaitGzip()

	f, err := os.Open(filepath.Join(filepath.Dir(path), "sing-router.log.1.gz"))
	if err != nil {
		t.Fatalf("open backup: %v", err)
	}
	defer func() { _ = f.Close() }()
	gr, err := gzip.NewReader(f)
	if err != nil {
		t.Fatalf("gzip reader: %v", err)
	}
	defer func() { _ = gr.Close() }()
	body, err := io.ReadAll(gr)
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if !strings.Contains(string(body), "@l") {
		t.Fatal("backup content not JSON Lines")
	}
}

func TestWriterReopenOnSIGUSR1Equivalent(t *testing.T) {
	w, path := newTestWriter(t, 1024, 3)
	e := NewEvent()
	e.Set("@l", "Information")
	e.Set("@mt", "first")
	_ = w.Write(e)

	// 模拟外部轮转：重命名 active 文件后调用 Reopen
	rotated := path + ".moved"
	if err := os.Rename(path, rotated); err != nil {
		t.Fatalf("rename: %v", err)
	}
	if err := w.Reopen(); err != nil {
		t.Fatalf("Reopen: %v", err)
	}
	e2 := NewEvent()
	e2.Set("@l", "Information")
	e2.Set("@mt", "after-reopen")
	_ = w.Write(e2)
	_ = w.Sync()

	after, _ := os.ReadFile(path)
	if !strings.Contains(string(after), "after-reopen") {
		t.Fatal("after-reopen content missing in new active file")
	}
	moved, _ := os.ReadFile(rotated)
	if !strings.Contains(string(moved), "first") {
		t.Fatal("rotated file content missing")
	}
}
