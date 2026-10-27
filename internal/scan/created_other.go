//go:build !windows

package scan

import (
	"os"
	"time"
)

// created returns a file's creation time; off Windows it isn't read.
func created(fi os.FileInfo) (time.Time, bool) { return time.Time{}, false }
