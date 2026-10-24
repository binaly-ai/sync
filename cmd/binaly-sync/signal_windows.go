package main

import (
	"context"

	"golang.org/x/sys/windows"
)

// showEvent lets a second start (the Start menu, or the downloaded exe run again) ask the
// running Binaly Sync to show its window. It's a named event in this Windows session only;
// no port, nothing another user of the computer can reach.
const showEvent = `Local\BinalySyncShowWindow`

// signalShow asks the running instance to show its window. False if none is listening.
func signalShow() bool {
	name, _ := windows.UTF16PtrFromString(showEvent)
	h, err := windows.OpenEvent(windows.EVENT_MODIFY_STATE, false, name)
	if err != nil {
		return false
	}
	defer windows.CloseHandle(h)
	return windows.SetEvent(h) == nil
}

// listenShow calls show whenever another start signals, until ctx ends.
func listenShow(ctx context.Context, show func()) {
	name, _ := windows.UTF16PtrFromString(showEvent)
	h, err := windows.CreateEvent(nil, 0, 0, name) // auto-reset
	if err != nil {
		return
	}
	defer windows.CloseHandle(h)
	for ctx.Err() == nil {
		if r, _ := windows.WaitForSingleObject(h, 1000); r == windows.WAIT_OBJECT_0 {
			show()
		}
	}
}
