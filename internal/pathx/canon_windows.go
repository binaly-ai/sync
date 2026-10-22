//go:build windows

package pathx

import (
	"path/filepath"
	"strings"
	"syscall"
	"unsafe"
)

var (
	mpr                  = syscall.NewLazyDLL("mpr.dll")
	procWNetGetConnection = mpr.NewProc("WNetGetConnectionW")
)

// remoteOf returns the network path a mapped drive letter points at ("Z:" -> "\\nas\lab"),
// or "" when the letter is a local disk or not mapped.
func remoteOf(drive string) string {
	local, err := syscall.UTF16PtrFromString(drive)
	if err != nil {
		return ""
	}
	buf := make([]uint16, 1024)
	n := uint32(len(buf))
	r, _, _ := procWNetGetConnection.Call(uintptr(unsafe.Pointer(local)), uintptr(unsafe.Pointer(&buf[0])), uintptr(unsafe.Pointer(&n)))
	if r != 0 {
		return ""
	}
	return syscall.UTF16ToString(buf)
}

// Canonical turns a path into the form Sync reports: absolute, and on a mapped drive, the
// network path behind it. Two machines that map one share to different letters then report
// the same paths. Local disks keep their drive letter.
func Canonical(p string) string {
	abs, err := filepath.Abs(p)
	if err != nil {
		abs = p
	}
	if len(abs) >= 2 && abs[1] == ':' {
		if remote := remoteOf(strings.ToUpper(abs[:2])); remote != "" {
			return strings.TrimRight(remote, `\`) + abs[2:]
		}
	}
	return abs
}

// DriveMap lists every mapped drive letter and its network path, for the server to show paths
// the way this machine's user knows them.
func DriveMap() map[string]string {
	out := map[string]string{}
	for c := 'A'; c <= 'Z'; c++ {
		d := string(c) + ":"
		if r := remoteOf(d); r != "" {
			out[d] = r
		}
	}
	return out
}
