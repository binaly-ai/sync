// Package config holds Binaly Sync's local, non-secret settings: the install id, the signed-in
// accounts (which server, which workspace, which member - never a token), and the user's
// preferences. Tokens live in the operating system's credential store (package creds).
//
// One install can be signed in to several workspaces at once (docs: "Several workspaces").
// An account is keyed by server origin + user + workspace, so a future on-premise server or a
// staging server fits without changes.
package config

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"time"
)

// DefaultServer is where Binaly Sync talks to unless told otherwise.
const DefaultServer = "https://mcp.binaly.ai"

// Account is one sign-in: one member in one workspace on one server.
type Account struct {
	Server        string `json:"server"`
	ClientID      string `json:"clientId"`
	UserID        string `json:"userId,omitempty"`
	Email         string `json:"email,omitempty"`
	WorkspaceID   string `json:"workspaceId"`
	WorkspaceName string `json:"workspaceName"`
	// MachineID is what this workspace calls this install (each workspace has its own).
	MachineID string `json:"machineId,omitempty"`
	// Static is true for a machine token pasted with `login --token` (no refresh).
	Static bool `json:"static,omitempty"`
}

// Key identifies the account, and names its credential in the credential store.
func (a Account) Key() string {
	user := a.UserID
	if user == "" {
		user = "token"
	}
	return a.Server + "|" + user + "|" + a.WorkspaceID
}

// Settings are the user's preferences, shared by every workspace.
type Settings struct {
	StartAtLogon bool `json:"startAtLogon"`
	// Updates: "auto" or "notify".
	Updates string `json:"updates"`
	// BandwidthKBps limits uploads; 0 means no limit.
	BandwidthKBps int `json:"bandwidthKBps"`
	// PausedUntil, RFC 3339, empty when running, or PausedIndefinitely ("until I resume").
	PausedUntil string `json:"pausedUntil,omitempty"`
}

// PausedIndefinitely is PausedUntil for a pause that lasts until the person resumes.
const PausedIndefinitely = "until-resumed"

// Paused reports whether syncing is paused at now, and until when (zero for "until I resume").
func (s Settings) Paused(now time.Time) (bool, time.Time) {
	if s.PausedUntil == PausedIndefinitely {
		return true, time.Time{}
	}
	if t, err := time.Parse(time.RFC3339, s.PausedUntil); err == nil && now.Before(t) {
		return true, t
	}
	return false, time.Time{}
}

// File is everything stored in config.json.
type File struct {
	InstallID string    `json:"installId"`
	Accounts  []Account `json:"accounts"`
	Settings  Settings  `json:"settings"`
}

// Store reads and writes config.json under Dir().
type Store struct {
	mu   sync.Mutex
	path string
	data File
}

// Dir is where Binaly Sync keeps its local files: %LOCALAPPDATA%\Binaly\Sync on Windows.
// BINALY_SYNC_HOME overrides it (tests, and running two installs side by side).
func Dir() string {
	if d := os.Getenv("BINALY_SYNC_HOME"); d != "" {
		return d
	}
	if runtime.GOOS == "windows" {
		if d := os.Getenv("LOCALAPPDATA"); d != "" {
			return filepath.Join(d, "Binaly", "Sync")
		}
	}
	if d, err := os.UserConfigDir(); err == nil {
		return filepath.Join(d, "binaly-sync")
	}
	return ".binaly-sync"
}

// Open loads config.json, creating it (with a fresh install id) on first run.
func Open() (*Store, error) {
	dir := Dir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	s := &Store{path: filepath.Join(dir, "config.json")}
	raw, err := os.ReadFile(s.path)
	switch {
	case err == nil:
		if err := json.Unmarshal(raw, &s.data); err != nil {
			return nil, err
		}
	case errors.Is(err, os.ErrNotExist):
		s.data.Settings = Settings{StartAtLogon: true, Updates: "auto"}
	default:
		return nil, err
	}
	if s.data.InstallID == "" {
		b := make([]byte, 16)
		if _, err := rand.Read(b); err != nil {
			return nil, err
		}
		s.data.InstallID = "inst-" + hex.EncodeToString(b)
		if err := s.saveLocked(); err != nil {
			return nil, err
		}
	}
	return s, nil
}

func (s *Store) saveLocked() error {
	raw, err := json.MarshalIndent(s.data, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}

// InstallID is this install's random id.
func (s *Store) InstallID() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.data.InstallID
}

// Accounts returns a copy of the signed-in accounts.
func (s *Store) Accounts() []Account {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Account(nil), s.data.Accounts...)
}

// PutAccount adds or replaces an account (matched by Key).
func (s *Store) PutAccount(a Account) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, x := range s.data.Accounts {
		if x.Key() == a.Key() {
			s.data.Accounts[i] = a
			return s.saveLocked()
		}
	}
	s.data.Accounts = append(s.data.Accounts, a)
	return s.saveLocked()
}

// RemoveAccount forgets an account.
func (s *Store) RemoveAccount(key string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := s.data.Accounts[:0]
	for _, x := range s.data.Accounts {
		if x.Key() != key {
			out = append(out, x)
		}
	}
	s.data.Accounts = out
	return s.saveLocked()
}

// Settings returns the current preferences.
func (s *Store) Settings() Settings {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.data.Settings
}

// UpdateSettings changes preferences.
func (s *Store) UpdateSettings(f func(*Settings)) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	f(&s.data.Settings)
	return s.saveLocked()
}
