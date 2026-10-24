package main

import (
	"os"
	"os/exec"
	"strconv"
	"syscall"

	"golang.org/x/sys/windows"
)

// stopOthers ends every other running Binaly Sync, so its executable can be replaced or deleted.
func stopOthers() {
	kill := exec.Command("taskkill", "/F", "/FI", "IMAGENAME eq binaly-sync.exe", "/FI", "PID ne "+strconv.Itoa(os.Getpid()))
	kill.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: windows.CREATE_NO_WINDOW}
	_ = kill.Run()
}
