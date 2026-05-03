package log

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestEmitterFormatsAndWritesJSONL(t *testing.T) {
	dir := t.TempDir()
	w, err := NewWriter(WriterConfig{
		Path:       filepath.Join(dir, "sing-router.log"),
		MaxSize:    0,
		MaxBackups: 0,
	})
	if err != nil {
		t.Fatalf("NewWriter: %v", err)
	}
	defer func() { _ = w.Close() }()

	bus := NewBus(8)
	defer bus.Close()

	em := NewEmitter(EmitterConfig{
		Source:   "daemon",
		MinLevel: LevelInfo,
		Writer:   w,
		Bus:      bus,
	})

	em.Info("supervisor", "supervisor.boot.started", "starting daemon at {Rundir}", map[string]any{"Rundir": "/opt/home/sing-router"})

	// bus 应该至少投递一次
	var mu sync.Mutex
	delivered := 0
	bus.Subscribe(SubscriberFunc{
		MatchFn:   func(*OrderedEvent) bool { return true },
		DeliverFn: func(*OrderedEvent) { mu.Lock(); delivered++; mu.Unlock() },
	})
	em.Warn("zoo", "zoo.preprocess.dropped_field", "dropped field {Field}", map[string]any{"Field": "experimental"})

	if err := w.Sync(); err != nil {
		t.Fatalf("Sync: %v", err)
	}
	data, _ := os.ReadFile(filepath.Join(dir, "sing-router.log"))
	lines := strings.Split(strings.TrimRight(string(data), "\n"), "\n")
	if len(lines) < 2 {
		t.Fatalf("want at least 2 lines, got %d", len(lines))
	}

	var first map[string]any
	if err := json.Unmarshal([]byte(lines[0]), &first); err != nil {
		t.Fatalf("invalid json: %v", err)
	}
	if first["@l"] != "Information" || first["Source"] != "daemon" || first["EventID"] != "supervisor.boot.started" {
		t.Fatalf("unexpected fields: %v", first)
	}
}

func TestEmitterDropsBelowMinLevel(t *testing.T) {
	dir := t.TempDir()
	w, _ := NewWriter(WriterConfig{Path: filepath.Join(dir, "x.log")})
	defer func() { _ = w.Close() }()

	em := NewEmitter(EmitterConfig{
		Source:   "daemon",
		MinLevel: LevelWarn,
		Writer:   w,
		Bus:      NewBus(4),
	})
	em.Info("supervisor", "noop", "msg", nil)
	em.Debug("supervisor", "noop", "msg", nil)
	em.Warn("supervisor", "kept", "msg", nil)

	_ = w.Sync()
	data, _ := os.ReadFile(filepath.Join(dir, "x.log"))
	if !strings.Contains(string(data), "kept") {
		t.Fatal("Warn missing")
	}
	if strings.Contains(string(data), "noop") {
		t.Fatal("Info/Debug should be filtered out")
	}
}
