//go:build windows

package scan

import (
	"path/filepath"
	"strings"
	"syscall"
	"unsafe"
)

var procGetVolumeInformation = syscall.NewLazyDLL("kernel32.dll").NewProc("GetVolumeInformationW")

// CloudDrive names the cloud service behind a path's drive, or "" for an ordinary disk or
// share. Google Drive for desktop mounts a virtual drive whose files carry no placeholder
// flag - each looks ordinary and downloads in full when read - so the only reliable signal
// is the volume itself: it reports the label "Google Drive" (measured: file system "FAT32").
func CloudDrive(path string) string {
	vol := filepath.VolumeName(path)
	if vol == "" || strings.HasPrefix(vol, `\\`) {
		return ""
	}
	root, err := syscall.UTF16PtrFromString(vol + `\`)
	if err != nil {
		return ""
	}
	label := make([]uint16, 261)
	fsName := make([]uint16, 261)
	var serial, maxComp, flags uint32
	r, _, _ := procGetVolumeInformation.Call(
		uintptr(unsafe.Pointer(root)),
		uintptr(unsafe.Pointer(&label[0])), uintptr(len(label)),
		uintptr(unsafe.Pointer(&serial)), uintptr(unsafe.Pointer(&maxComp)), uintptr(unsafe.Pointer(&flags)),
		uintptr(unsafe.Pointer(&fsName[0])), uintptr(len(fsName)),
	)
	if r == 0 {
		return ""
	}
	if strings.EqualFold(syscall.UTF16ToString(label), "Google Drive") {
		return "Google Drive"
	}
	return ""
}
