package state

import (
	"path/filepath"
	"testing"
	"time"
)

func TestLoadEmpty(t *testing.T) {
	dir := t.TempDir()
	s, err := Load(filepath.Join(dir, "state.json"))
	if err != nil {
		t.Fatalf("Load empty: %v", err)
	}
	if s.LastBootAt != "" {
		t.Fatalf("LastBootAt should be empty: %s", s.LastBootAt)
	}
	if s.RestartCount != 0 {
		t.Fatal("RestartCount should be 0")
	}
}

func TestSaveLoadRoundtrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.json")
	s := &State{
		LastBootAt:            time.Now().UTC().Format(time.RFC3339),
		RestartCount:          3,
		LastZooLoadedAt:       "2026-05-02T12:00:00+08:00",
		LastIptablesAppliedAt: "2026-05-02T12:34:56+08:00",
	}
	if err := s.Save(path); err != nil {
		t.Fatalf("Save: %v", err)
	}
	loaded, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if loaded.RestartCount != 3 {
		t.Fatalf("RestartCount: %d", loaded.RestartCount)
	}
	if loaded.LastZooLoadedAt != s.LastZooLoadedAt {
		t.Fatal("LastZooLoadedAt mismatch")
	}
}

func TestSaveAtomic(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.json")
	s := &State{RestartCount: 1}
	for i := 0; i < 5; i++ {
		s.RestartCount = i
		if err := s.Save(path); err != nil {
			t.Fatal(err)
		}
		// 中途不应该有 .tmp 残留
		if exists(filepath.Join(dir, "state.json.tmp")) {
			t.Fatal("tmp file should not survive after Save")
		}
	}
}

func exists(p string) bool {
	_, err := osStat(p)
	return err == nil
}
