//go:build !windows

package main

import "errors"

func cmdUninstall([]string) error {
	return errors.New("uninstall is only needed on Windows; elsewhere, delete the binary")
}
