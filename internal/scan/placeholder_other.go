//go:build !windows

package scan

import "io/fs"

func onlineOnly(fs.FileInfo) bool { return false }
