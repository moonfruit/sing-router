package log

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestEmitterStackWritesAndPublishes(t *testing.T) {
	dir := t.TempDir()
	w, err := NewWriter(WriterConfig{Path: filepath.Join(dir, "test.log")})
	if err != nil {
		t.Fatalf("NewWriter: %v", err)
	}

	stack := NewEmitterStack(StackConfig{
		Source:   "daemon",
		MinLevel: LevelInfo,
		Writer:   w,
	})

	stack.Emitter.Info("supervisor", "boot", "starting at {Path}", map[string]any{"Path": "/opt/x"})

	// Bus.Close drains, so by the time Close returns the Writer has received the event.
	if err := stack.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	data, _ := os.ReadFile(filepath.Join(dir, "test.log"))
	lines := strings.Split(strings.TrimRight(string(data), "\n"), "\n")
	if len(lines) < 1 || lines[0] == "" {
		t.Fatalf("no lines written: %q", string(data))
	}
	var ev map[string]any
	if err := json.Unmarshal([]byte(lines[0]), &ev); err != nil {
		t.Fatalf("invalid json: %v", err)
	}
	if ev["@l"] != "Information" || ev["Source"] != "daemon" || ev["EventID"] != "boot" {
		t.Fatalf("unexpected fields: %v", ev)
	}
}
