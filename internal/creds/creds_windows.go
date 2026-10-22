//go:build windows

package creds

import (
	"errors"

	"github.com/danieljoos/wincred"
)

// System returns the Windows Credential Manager store.
func System() Store { return winStore{} }

type winStore struct{}

func (winStore) Get(key string) (string, error) {
	c, err := wincred.GetGenericCredential(prefix + key)
	if err != nil {
		if errors.Is(err, wincred.ErrElementNotFound) {
			return "", ErrNotFound
		}
		return "", err
	}
	return string(c.CredentialBlob), nil
}

func (winStore) Set(key, secret string) error {
	c := wincred.NewGenericCredential(prefix + key)
	c.CredentialBlob = []byte(secret)
	c.Persist = wincred.PersistLocalMachine
	c.Comment = "Binaly Sync sign-in. Remove it by signing out of that workspace in Binaly Sync."
	return c.Write()
}

func (winStore) Delete(key string) error {
	c, err := wincred.GetGenericCredential(prefix + key)
	if err != nil {
		if errors.Is(err, wincred.ErrElementNotFound) {
			return nil
		}
		return err
	}
	return c.Delete()
}
