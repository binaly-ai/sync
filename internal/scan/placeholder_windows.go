//go:build windows

package scan

import (
	"io/fs"
	"syscall"
)

// File attributes that mark a cloud placeholder: OneDrive, Dropbox and other Cloud Files API
// providers set RECALL_ON_DATA_ACCESS on files whose contents aren't on this disk.
const (
	attrOffline            = 0x00001000
	attrRecallOnOpen       = 0x00040000
	attrRecallOnDataAccess = 0x00400000
)

func onlineOnly(fi fs.FileInfo) bool {
	d, ok := fi.Sys().(*syscall.Win32FileAttributeData)
	if !ok {
		return false
	}
	return d.FileAttributes&(attrOffline|attrRecallOnOpen|attrRecallOnDataAccess) != 0
}
