//go:build windows

package scan

import (
	"os"
	"syscall"
	"time"
)

// created returns a file's creation time, if the file system keeps one.
func created(fi os.FileInfo) (time.Time, bool) {
	if d, ok := fi.Sys().(*syscall.Win32FileAttributeData); ok && d.CreationTime.Nanoseconds() > 0 {
		return time.Unix(0, d.CreationTime.Nanoseconds()), true
	}
	return time.Time{}, false
}
