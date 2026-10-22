//go:build windows

// Package install puts Binaly Sync where it belongs for the current user - no administrator
// rights, nothing machine-wide - and turns start-at-logon on or off.
package install

import (
	"io"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/windows/registry"
)

const runKey = `Software\Microsoft\Windows\CurrentVersion\Run`
const valueName = "Binaly Sync"

// Dir is %LOCALAPPDATA%\Programs\Binaly.
func Dir() string {
	return filepath.Join(os.Getenv("LOCALAPPDATA"), "Programs", "Binaly")
}

// Target is the installed executable's path.
func Target() string { return filepath.Join(Dir(), "binaly-sync.exe") }

// IsInstalled reports whether the running executable is the installed copy.
func IsInstalled() bool {
	self, err := os.Executable()
	if err != nil {
		return false
	}
	return strings.EqualFold(filepath.Clean(self), filepath.Clean(Target()))
}

// Install copies the running executable into place and returns the installed path.
func Install() (string, error) {
	self, err := os.Executable()
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(Dir(), 0o755); err != nil {
		return "", err
	}
	in, err := os.Open(self)
	if err != nil {
		return "", err
	}
	defer in.Close()
	tmp := Target() + ".new"
	out, err := os.Create(tmp)
	if err != nil {
		return "", err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return "", err
	}
	out.Close()
	// Replacing a running exe fails on Windows; move the old one aside first.
	if _, err := os.Stat(Target()); err == nil {
		_ = os.Remove(Target() + ".old")
		if err := os.Rename(Target(), Target()+".old"); err != nil {
			return "", err
		}
	}
	return Target(), os.Rename(tmp, Target())
}

// SetStartAtLogon adds or removes the per-user Run entry.
func SetStartAtLogon(on bool, exe string) error {
	k, _, err := registry.CreateKey(registry.CURRENT_USER, runKey, registry.SET_VALUE|registry.QUERY_VALUE)
	if err != nil {
		return err
	}
	defer k.Close()
	if !on {
		err := k.DeleteValue(valueName)
		if err == registry.ErrNotExist {
			return nil
		}
		return err
	}
	return k.SetStringValue(valueName, `"`+exe+`"`)
}
