//go:build windows

package main

import (
	"os"
	"syscall"
)

// attachConsole lets the command-line half print to the terminal it was started from. The
// executable is built as a Windows GUI program (so the tray app opens no console window), and
// a GUI program has no console of its own.
func attachConsole() {
	const attachParentProcess = ^uint32(0) // (DWORD)-1
	k := syscall.NewLazyDLL("kernel32.dll")
	if r, _, _ := k.NewProc("AttachConsole").Call(uintptr(attachParentProcess)); r == 0 {
		return // started without a terminal, or output is already redirected
	}
	if out, err := os.OpenFile("CONOUT$", os.O_WRONLY, 0); err == nil {
		if fi, _ := os.Stdout.Stat(); fi == nil || fi.Mode()&os.ModeCharDevice != 0 || os.Stdout.Fd() == 0 {
			os.Stdout = out
		}
		if fi, _ := os.Stderr.Stat(); fi == nil || fi.Mode()&os.ModeCharDevice != 0 || os.Stderr.Fd() == 0 {
			os.Stderr = out
		}
	}
}
