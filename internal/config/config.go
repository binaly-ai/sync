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
	// CheckMinutes is how often every folder is looked through in full, the safety net under
	// watching for changes; 0 means DefaultCheckMinutes.
	CheckMinutes int `json:"checkMinutes,omitempty"`
	// PausedUntil, RFC 3339, empty when running, or PausedIndefinitely ("until I resume").
	PausedUntil string `json:"pausedUntil,omitempty"`
}

// DefaultCheckMinutes, and the limits a person can set it to.
const (
	DefaultCheckMinutes = 15
	MinCheckMinutes     = 1
	MaxCheckMinutes     = 24 * 60
)

// CheckEvery is how long to wait between full looks through the folders.
func (s Settings) CheckEvery() time.Duration {
	return time.Duration(ClampCheckMinutes(s.CheckMinutes)) * time.Minute
}

// ClampCheckMinutes keeps a check interval within the limits; 0 (never set) is the default.
func ClampCheckMinutes(m int) int {
	switch {
	case m <= 0:
		return DefaultCheckMinutes
	case m < MinCheckMinutes:
		return MinCheckMinutes
	case m > MaxCheckMinutes:
		return MaxCheckMinutes
	}
	return m
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
	// Wizard is the first-launch wizard's state: "pending" on a new install until the wizard is
	// finished or skipped, then "done". Installs from before the wizard have "" and never see it.
	Wizard string `json:"wizard,omitempty"`
	// SetupPending lists workspaces (account keys) signed in since, whose AI assistant and
	// processing steps haven't been finished or skipped yet.
	SetupPending []string `json:"setupPending,omitempty"`
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
		s.data.Settings = Settings{StartAtLogon: true, Updates: "auto", CheckMinutes: DefaultCheckMinutes}
		s.data.Wizard = "pending"
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
	s.data.SetupPending = without(s.data.SetupPending, key)
	return s.saveLocked()
}

// WizardPending reports whether the first-launch wizard should open: a new install that hasn't
// finished or skipped it.
func (s *Store) WizardPending() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.data.Wizard == "pending"
}

// SetupPending returns the workspaces whose AI assistant and processing steps are still to do.
func (s *Store) SetupPending() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.data.SetupPending...)
}

// MarkSetupPending records a newly signed-in workspace, so the window walks through connecting
// the AI assistant and setting up processing for it.
func (s *Store) MarkSetupPending(key string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, k := range s.data.SetupPending {
		if k == key {
			return nil
		}
	}
	s.data.SetupPending = append(s.data.SetupPending, key)
	return s.saveLocked()
}

// FinishWizard records that the wizard was finished or skipped, for this install and for one
// workspace (all of them when key is "").
func (s *Store) FinishWizard(key string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.data.Wizard == "pending" {
		s.data.Wizard = "done"
	}
	if key == "" {
		s.data.SetupPending = nil
	} else {
		s.data.SetupPending = without(s.data.SetupPending, key)
	}
	return s.saveLocked()
}

func without(list []string, key string) []string {
	var out []string
	for _, k := range list {
		if k != key {
			out = append(out, k)
		}
	}
	return out
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
