//go:build !windows

package creds

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"

	"github.com/binaly-ai/sync/internal/config"
)

// System returns a file-backed store. Development only: Windows uses Credential Manager, and
// macOS will use the Keychain when that build exists.
func System() Store { return &fileStore{path: filepath.Join(config.Dir(), "credentials.json")} }

type fileStore struct {
	mu   sync.Mutex
	path string
}

func (f *fileStore) load() (map[string]string, error) {
	m := map[string]string{}
	raw, err := os.ReadFile(f.path)
	if errors.Is(err, os.ErrNotExist) {
		return m, nil
	}
	if err != nil {
		return nil, err
	}
	return m, json.Unmarshal(raw, &m)
}

func (f *fileStore) save(m map[string]string) error {
	raw, _ := json.Marshal(m)
	return os.WriteFile(f.path, raw, 0o600)
}

func (f *fileStore) Get(key string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	m, err := f.load()
	if err != nil {
		return "", err
	}
	if v, ok := m[prefix+key]; ok {
		return v, nil
	}
	return "", ErrNotFound
}

func (f *fileStore) Set(key, secret string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	m, err := f.load()
	if err != nil {
		return err
	}
	m[prefix+key] = secret
	return f.save(m)
}

func (f *fileStore) Delete(key string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	m, err := f.load()
	if err != nil {
		return err
	}
	delete(m, prefix+key)
	return f.save(m)
}
