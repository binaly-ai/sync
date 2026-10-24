//go:build !windows

package main

import (
	"errors"

	"github.com/sqweek/dialog"
)

func pickFolder(title string) (string, error) {
	p, err := dialog.Directory().Title(title).Browse()
	if errors.Is(err, dialog.ErrCancelled) {
		return "", nil
	}
	return p, err
}
