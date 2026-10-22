// Package creds keeps sign-in secrets out of every file Binaly Sync writes. On Windows they
// live in Credential Manager, under "Binaly Sync: <account key>", protected by the user's
// Windows login. Elsewhere (development only) they fall back to a 0600 file.
package creds

// Store keeps one secret per account key.
type Store interface {
	Get(key string) (string, error)
	Set(key, secret string) error
	Delete(key string) error
}

const prefix = "Binaly Sync: "

// Memory is an in-memory Store, for tests.
type Memory map[string]string

func (m Memory) Get(key string) (string, error) {
	if v, ok := m[key]; ok {
		return v, nil
	}
	return "", ErrNotFound
}
func (m Memory) Set(key, secret string) error { m[key] = secret; return nil }
func (m Memory) Delete(key string) error      { delete(m, key); return nil }

type notFound struct{}

func (notFound) Error() string { return "no saved sign-in" }

// ErrNotFound means there is no secret for that account: it needs signing in.
var ErrNotFound error = notFound{}
