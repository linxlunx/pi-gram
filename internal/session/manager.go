// Package session persists the association between Telegram chats and
// Pi sessions. The initial deployment has a single Pi process with one
// active session, but the store is keyed by chat ID so multiple chats
// (each with its own Pi session file) can be supported later by
// switching the active Pi session when serving a different chat.
package session

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// ChatSession records the Pi session associated with a Telegram chat.
type ChatSession struct {
	PiSessionFile string    `json:"pi_session_file,omitempty"`
	PiSessionID   string    `json:"pi_session_id,omitempty"`
	LastActivity  time.Time `json:"last_activity,omitempty"`
}

type data struct {
	Version            int                   `json:"version"`
	CurrentSessionFile string                `json:"current_session_file,omitempty"`
	Chats              map[int64]ChatSession `json:"chats"`
}

// Store persists session metadata in a small JSON file. Writes are
// atomic (write to a temp file, then rename).
type Store struct {
	path string
	mu   sync.Mutex
	d    data
}

// Open loads the store from path, creating an empty store if the file
// does not exist yet.
func Open(path string) (*Store, error) {
	s := &Store{path: path, d: data{Version: 1, Chats: map[int64]ChatSession{}}}
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return s, nil
		}
		return nil, fmt.Errorf("read session state: %w", err)
	}
	if err := json.Unmarshal(raw, &s.d); err != nil {
		return nil, fmt.Errorf("parse session state %s: %w", path, err)
	}
	if s.d.Chats == nil {
		s.d.Chats = map[int64]ChatSession{}
	}
	if s.d.Version == 0 {
		s.d.Version = 1
	}
	return s, nil
}

// Get returns the session record for a chat (zero value if unknown).
func (s *Store) Get(chatID int64) ChatSession {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.d.Chats[chatID]
}

// Update stores the session record for a chat.
func (s *Store) Update(chatID int64, cs ChatSession) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.d.Chats[chatID] = cs
	return s.saveLocked()
}

// Reset clears the stored session for a chat (used by /new).
func (s *Store) Reset(chatID int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.d.Chats, chatID)
	return s.saveLocked()
}

// SetCurrent records the Pi session file the process is currently
// using. It is restored after a Pi process restart.
func (s *Store) SetCurrent(sessionFile string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.d.CurrentSessionFile == sessionFile {
		return nil
	}
	s.d.CurrentSessionFile = sessionFile
	return s.saveLocked()
}

// CurrentSessionFile returns the last known active Pi session file.
func (s *Store) CurrentSessionFile() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.d.CurrentSessionFile
}

func (s *Store) saveLocked() error {
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return fmt.Errorf("create state directory: %w", err)
	}
	raw, err := json.MarshalIndent(s.d, "", "  ")
	if err != nil {
		return fmt.Errorf("encode session state: %w", err)
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return fmt.Errorf("write session state: %w", err)
	}
	if err := os.Rename(tmp, s.path); err != nil {
		return fmt.Errorf("rename session state: %w", err)
	}
	return nil
}
