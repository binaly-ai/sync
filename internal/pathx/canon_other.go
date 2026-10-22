//go:build !windows

package pathx

import "path/filepath"

// Canonical returns the absolute path. Drive mapping is a Windows concept.
func Canonical(p string) string {
	abs, err := filepath.Abs(p)
	if err != nil {
		return p
	}
	return abs
}

// DriveMap is empty off Windows.
func DriveMap() map[string]string { return map[string]string{} }
