//go:build !windows

package watch

import "errors"

// start isn't available here yet: the regular walk is the only way changes are noticed.
func start(f Folder, changed, lost func()) (func(), error) {
	return nil, errors.New("watching folders isn't supported on this system yet")
}
