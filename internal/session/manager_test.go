package session

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestStoreRoundtrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.json")

	s1, err := Open(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	rec := ChatSession{PiSessionFile: "/var/lib/pi/sessions/abc.jsonl", PiSessionID: "abc", LastActivity: time.Now()}
	if err := s1.Update(42, rec); err != nil {
		t.Fatalf("update: %v", err)
	}
	if err := s1.SetCurrent(rec.PiSessionFile); err != nil {
		t.Fatalf("set current: %v", err)
	}

	s2, err := Open(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	got := s2.Get(42)
	if got.PiSessionFile != rec.PiSessionFile || got.PiSessionID != rec.PiSessionID {
		t.Fatalf("roundtrip mismatch: %+v", got)
	}
	if s2.CurrentSessionFile() != rec.PiSessionFile {
		t.Fatalf("current = %q, want %q", s2.CurrentSessionFile(), rec.PiSessionFile)
	}
}

func TestStoreMissingFile(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "does-not-exist.json"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if got := s.Get(1); got.PiSessionFile != "" {
		t.Fatalf("expected zero record, got %+v", got)
	}
}

func TestStoreReset(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	s, _ := Open(path)
	_ = s.Update(7, ChatSession{PiSessionFile: "x"})
	if err := s.Reset(7); err != nil {
		t.Fatalf("reset: %v", err)
	}
	if rec := s.Get(7); rec.PiSessionFile != "" {
		t.Fatalf("record not cleared: %+v", rec)
	}
}

func TestStorePermissions(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	s, _ := Open(path)
	_ = s.Update(1, ChatSession{PiSessionFile: "f"})
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Fatalf("state file permissions = %o, want 600", perm)
	}
}
