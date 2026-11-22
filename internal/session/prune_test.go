package session

import (
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"testing"
	"time"
)

func quietLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// writeMtime creates an empty file with the given mtime so prune
// ordering is deterministic across runs.
func writeMtime(t *testing.T, path string, mtime time.Time) {
	t.Helper()
	if err := os.WriteFile(path, []byte{}, 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
	if err := os.Chtimes(path, mtime, mtime); err != nil {
		t.Fatalf("chtimes %s: %v", path, err)
	}
}

// exists is a tiny helper so test failures report which file path
// disappeared or appeared unexpectedly.
func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func TestPruneDisabledByDefault(t *testing.T) {
	dir := t.TempDir()
	old := filepath.Join(dir, "old.jsonl")
	fresh := filepath.Join(dir, "fresh.jsonl")
	writeMtime(t, old, time.Now().Add(-2*time.Hour))
	writeMtime(t, fresh, time.Now())

	s := &Store{}
	for _, max := range []int{0, -1, -1000} {
		deleted, errs := s.Prune(max, dir, quietLogger())
		if len(deleted) != 0 || len(errs) != 0 {
			t.Errorf("max=%d: expected no-op, got deleted=%v errs=%v", max, deleted, errs)
		}
	}
	if !exists(old) || !exists(fresh) {
		t.Fatalf("files touched despite disabled pruning: old=%v fresh=%v", exists(old), exists(fresh))
	}
}

func TestPruneNoOpWhenUnderLimit(t *testing.T) {
	dir := t.TempDir()
	keep := filepath.Join(dir, "keep.jsonl")
	writeMtime(t, keep, time.Now())
	s, _ := Open(filepath.Join(dir, stateFileName))
	_ = s.SetCurrent(keep)
	_ = s.Update(42, ChatSession{PiSessionFile: keep})

	deleted, errs := s.Prune(10, dir, quietLogger())
	if len(deleted) != 0 || len(errs) != 0 {
		t.Fatalf("expected no deletions, got deleted=%v errs=%v", deleted, errs)
	}
	if !exists(keep) {
		t.Fatal("protected file was deleted")
	}
}

func TestPruneRemovesOldestFirst(t *testing.T) {
	dir := t.TempDir()
	protected := filepath.Join(dir, "active.jsonl")
	chatOne := filepath.Join(dir, "chat-1.jsonl")
	orphan1 := filepath.Join(dir, "orphan1.jsonl")
	orphan2 := filepath.Join(dir, "orphan2.jsonl")
	orphan3 := filepath.Join(dir, "orphan3.jsonl")

	now := time.Now()
	// Protected files get the freshest mtime.
	writeMtime(t, protected, now)
	writeMtime(t, chatOne, now.Add(-1*time.Minute))
	// Orphans are older.
	writeMtime(t, orphan1, now.Add(-3*time.Hour))
	writeMtime(t, orphan2, now.Add(-2*time.Hour))
	writeMtime(t, orphan3, now.Add(-1*time.Hour))

	s, _ := Open(filepath.Join(dir, stateFileName))
	_ = s.SetCurrent(protected)
	_ = s.Update(1, ChatSession{PiSessionFile: chatOne})

	// 5 total (2 protected + 3 orphans). Cap at 3 → keep the 2
	// protected + 1 most-recent orphan (orphan3); delete orphan1 &
	// orphan2.
	deleted, errs := s.Prune(3, dir, quietLogger())
	if len(errs) != 0 {
		t.Fatalf("unexpected errors: %v", errs)
	}
	want := []string{orphan1, orphan2}
	if !equalStringSlices(deleted, want) {
		t.Fatalf("deleted = %v, want %v", deleted, want)
	}
	if !exists(protected) || !exists(chatOne) || !exists(orphan3) {
		t.Fatalf("protected/keep files were touched: active=%v chat=%v orphan3=%v",
			exists(protected), exists(chatOne), exists(orphan3))
	}
	if exists(orphan1) || exists(orphan2) {
		t.Fatalf("orphans not deleted: o1=%v o2=%v", exists(orphan1), exists(orphan2))
	}
}

func TestPruneProtectsAllChatSessions(t *testing.T) {
	dir := t.TempDir()
	a := filepath.Join(dir, "a.jsonl")
	b := filepath.Join(dir, "b.jsonl")
	c := filepath.Join(dir, "c.jsonl")
	orphan := filepath.Join(dir, "orphan.jsonl")

	now := time.Now()
	writeMtime(t, a, now.Add(-1*time.Hour))
	writeMtime(t, b, now.Add(-2*time.Hour))
	writeMtime(t, c, now.Add(-3*time.Hour))
	writeMtime(t, orphan, now.Add(-10*time.Hour))

	s, _ := Open(filepath.Join(dir, stateFileName))
	// Three different chats, three different sessions, none currently active.
	_ = s.Update(1, ChatSession{PiSessionFile: a})
	_ = s.Update(2, ChatSession{PiSessionFile: b})
	_ = s.Update(3, ChatSession{PiSessionFile: c})

	// Cap at 3 -> orphan deleted, all per-chat files kept.
	deleted, _ := s.Prune(3, dir, quietLogger())
	if !equalStringSlices(deleted, []string{orphan}) {
		t.Fatalf("deleted = %v, want only %v", deleted, orphan)
	}
	for _, p := range []string{a, b, c} {
		if !exists(p) {
			t.Errorf("protected file deleted: %s", p)
		}
	}
	if exists(orphan) {
		t.Error("orphan should have been deleted")
	}
}

func TestPruneKeepsStateFileEvenWithTightCap(t *testing.T) {
	dir := t.TempDir()
	statePath := filepath.Join(dir, stateFileName)
	s, err := Open(statePath)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	// One protected session, one orphan, plus the state file itself.
	keep := filepath.Join(dir, "keep.jsonl")
	orphan := filepath.Join(dir, "orphan.jsonl")
	writeMtime(t, keep, time.Now())
	writeMtime(t, orphan, time.Now().Add(-3*time.Hour))
	_ = s.Update(1, ChatSession{PiSessionFile: keep})

	// Cap of 1 forces the orphan out. The state file must survive.
	deleted, _ := s.Prune(1, dir, quietLogger())
	if !equalStringSlices(deleted, []string{orphan}) {
		t.Fatalf("deleted = %v, want [orphan]", deleted)
	}
	if !exists(statePath) {
		t.Fatal("state file was deleted")
	}
	if !exists(keep) {
		t.Fatal("protected session file was deleted")
	}
	if exists(orphan) {
		t.Fatal("orphan not deleted")
	}
}

func TestPruneTolerantToNonSessionFiles(t *testing.T) {
	// Files that don't look like Pi sessions (wrong extension) must
	// be ignored entirely — never counted, never deleted.
	dir := t.TempDir()
	stray := filepath.Join(dir, "random.txt")
	log := filepath.Join(dir, "pi.log")
	writeMtime(t, stray, time.Now().Add(-10*time.Hour))
	writeMtime(t, log, time.Now().Add(-10*time.Hour))

	s := &Store{}
	// Tighter than anything so anything that could be deleted would.
	deleted, errs := s.Prune(0, dir, quietLogger())
	if len(deleted) != 0 || len(errs) != 0 {
		t.Fatalf("unexpected: deleted=%v errs=%v", deleted, errs)
	}
	if !exists(stray) || !exists(log) {
		t.Fatalf("non-session files were touched: stray=%v log=%v", exists(stray), exists(log))
	}
}

func TestPruneMissingDirIsNoOp(t *testing.T) {
	s := &Store{}
	deleted, errs := s.Prune(1, filepath.Join(t.TempDir(), "nope"), quietLogger())
	if len(deleted) != 0 || len(errs) != 0 {
		t.Fatalf("missing dir should be a no-op, got deleted=%v errs=%v", deleted, errs)
	}
}

func TestPruneIgnoresMissingSessionFiles(t *testing.T) {
	// State can reference files that no longer exist (deleted earlier
	// or moved); they must not block pruning or generate errors.
	dir := t.TempDir()
	phantom := filepath.Join(dir, "phantom.jsonl") // never written
	orphan := filepath.Join(dir, "orphan.jsonl")
	writeMtime(t, orphan, time.Now().Add(-2*time.Hour))

	s, _ := Open(filepath.Join(dir, stateFileName))
	_ = s.Update(1, ChatSession{PiSessionFile: phantom})

	// Cap=1 with one protected (missing) + one real orphan → orphan
	// gets deleted, no errors are reported for the missing phantom.
	deleted, errs := s.Prune(1, dir, quietLogger())
	if len(errs) != 0 {
		t.Fatalf("unexpected errors for missing protected file: %v", errs)
	}
	if !equalStringSlices(deleted, []string{orphan}) {
		t.Fatalf("deleted = %v, want %v", deleted, orphan)
	}
	if exists(orphan) {
		t.Fatal("real orphan should have been pruned")
	}
}

func TestPruneCapBelowProtectedCountSkips(t *testing.T) {
	// If maxFiles is below the count of protected files, prune must
	// still NOT delete any of them; it just refuses to act further.
	dir := t.TempDir()
	a := filepath.Join(dir, "a.jsonl")
	b := filepath.Join(dir, "b.jsonl")
	now := time.Now()
	writeMtime(t, a, now.Add(-1*time.Minute))
	writeMtime(t, b, now)

	s, _ := Open(filepath.Join(dir, stateFileName))
	_ = s.SetCurrent(a)
	_ = s.Update(1, ChatSession{PiSessionFile: b})

	deleted, _ := s.Prune(1, dir, quietLogger())
	if len(deleted) != 0 {
		t.Fatalf("protected files must not be deleted; deleted=%v", deleted)
	}
	if !exists(a) || !exists(b) {
		t.Fatal("protected file disappeared")
	}
}

func equalStringSlices(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	ac := append([]string(nil), a...)
	bc := append([]string(nil), b...)
	sort.Strings(ac)
	sort.Strings(bc)
	for i := range ac {
		if ac[i] != bc[i] {
			return false
		}
	}
	return true
}
