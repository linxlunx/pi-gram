package session

import (
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// piSessionExt is the file-extension heuristic used to recognise Pi
// session files inside the session directory. The gateway never
// enumerates the directory at runtime otherwise; Pi owns those files.
// Operators using a Pi version with a different naming convention
// can rename or symlink sessions to keep this filter working.
const piSessionExt = ".jsonl"

// stateFileName is the basename the gateway writes for its own
// state. It must never be deleted even if it happens to live in the
// same directory as Pi session files.
const stateFileName = "pi-telegram-state.json"

// Prune deletes the oldest Pi session files in dir until at most
// maxFiles Pi session files remain (active and per-chat protected
// files included). The state file is never considered. Files outside
// the directory and subdirectories are not touched by definition.
//
// "Active" = the file referenced by SetCurrent.
// "Per-chat protected" = the file referenced by chats[id].pi_session_file
// for every known chat. Those files hold the conversation history
// that the gateway can switch Pi back into.
//
// maxFiles <= 0 disables pruning and immediately returns (nil, nil).
// Errors are collected per file rather than aborting, so a single
// failure (e.g. a permission error) does not block the rest.
//
// Returns the list of paths that were deleted (sorted oldest mtime
// first) and any errors that prevented deletion.
func (s *Store) Prune(maxFiles int, dir string, logger *slog.Logger) (deleted []string, errs []error) {
	if maxFiles <= 0 {
		return nil, nil
	}
	if logger == nil {
		logger = slog.Default()
	}
	if dir == "" {
		return nil, nil
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, []error{err}
	}

	protected := s.protectedSessionFiles()

	type candidate struct {
		path  string
		mtime time.Time
	}
	var cands []candidate
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if name == stateFileName {
			continue
		}
		// Only consider files that look like Pi session files. This is
		// the documented on-disk format; other content in the
		// directory (logs, backups, etc.) belongs elsewhere.
		if !strings.HasSuffix(name, piSessionExt) {
			continue
		}
		full := filepath.Join(dir, name)
		if _, ok := protected[full]; ok {
			continue
		}
		info, err := os.Stat(full)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			errs = append(errs, err)
			continue
		}
		cands = append(cands, candidate{path: full, mtime: info.ModTime()})
	}

	sort.Slice(cands, func(i, j int) bool {
		return cands[i].mtime.Before(cands[j].mtime)
	})

	// Each protected path counts toward the cap even if the file is
	// missing on disk right now; the next prune re-evaluates.
	total := len(protected) + len(cands)
	for _, c := range cands {
		if total <= maxFiles {
			break
		}
		if err := os.Remove(c.path); err != nil {
			if errors.Is(err, os.ErrNotExist) {
				// Race with another prune run; nothing to do.
				total--
				continue
			}
			errs = append(errs, err)
			continue
		}
		deleted = append(deleted, c.path)
		logger.Info("pruned old pi session file",
			"path", c.path,
			"mtime", c.mtime.Format(time.RFC3339))
		total--
	}
	return deleted, errs
}

// protectedSessionFiles returns the absolute paths of Pi session
// files the gateway must not delete: the currently active session
// and the most-recent session of every chat. Paths are cleaned so
// equality checks against filepath.Join output are stable.
func (s *Store) protectedSessionFiles() map[string]struct{} {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make(map[string]struct{})
	add := func(p string) {
		if p == "" {
			return
		}
		out[filepath.Clean(p)] = struct{}{}
	}
	add(s.d.CurrentSessionFile)
	for _, cs := range s.d.Chats {
		add(cs.PiSessionFile)
	}
	return out
}
