package watch

import (
	"os"
	"path/filepath"
	"sync"
	"unsafe"

	"golang.org/x/sys/windows"
)

// Created, deleted, renamed, resized or written. Not attributes or last access: backup and
// antivirus software touch those constantly.
const mask = windows.FILE_NOTIFY_CHANGE_FILE_NAME | windows.FILE_NOTIFY_CHANGE_DIR_NAME |
	windows.FILE_NOTIFY_CHANGE_SIZE | windows.FILE_NOTIFY_CHANGE_LAST_WRITE

// 64 KB is the most a network share will return in one notification.
const bufSize = 64 << 10

// start watches one folder and everything under it with ReadDirectoryChangesW. changed runs
// after each relevant change; lost runs once if watching breaks (the folder went away). The
// returned stop never waits, so it is safe to call while holding the Watcher's lock.
func start(f Folder, changed, lost func()) (func(), error) {
	p, err := windows.UTF16PtrFromString(f.Path)
	if err != nil {
		return nil, err
	}
	h, err := windows.CreateFile(p, windows.FILE_LIST_DIRECTORY,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE, nil,
		windows.OPEN_EXISTING, windows.FILE_FLAG_BACKUP_SEMANTICS|windows.FILE_FLAG_OVERLAPPED, 0)
	if err != nil {
		return nil, err
	}
	ready, err := windows.CreateEvent(nil, 1, 0, nil)
	if err != nil {
		windows.CloseHandle(h)
		return nil, err
	}
	quit, err := windows.CreateEvent(nil, 1, 0, nil)
	if err != nil {
		windows.CloseHandle(ready)
		windows.CloseHandle(h)
		return nil, err
	}
	var mu sync.Mutex
	exited := false
	stop := func() {
		mu.Lock()
		if !exited {
			windows.SetEvent(quit)
		}
		mu.Unlock()
	}
	go func() {
		stopped := false
		defer func() {
			mu.Lock()
			exited = true
			windows.CloseHandle(h)
			windows.CloseHandle(ready)
			windows.CloseHandle(quit)
			mu.Unlock()
			if !stopped {
				lost()
			}
		}()
		buf := make([]byte, bufSize)
		ov := new(windows.Overlapped)
		for {
			*ov = windows.Overlapped{HEvent: ready}
			windows.ResetEvent(ready)
			if err := windows.ReadDirectoryChanges(h, &buf[0], bufSize, true, mask, nil, ov, 0); err != nil {
				return
			}
			ev, err := windows.WaitForMultipleObjects([]windows.Handle{ready, quit}, false, windows.INFINITE)
			if err != nil || ev != windows.WAIT_OBJECT_0 {
				// Stopping: cancel the read, and wait for it to end before buf goes away.
				windows.CancelIoEx(h, ov)
				var n uint32
				_ = windows.GetOverlappedResult(h, ov, &n, true)
				stopped = err == nil && ev == windows.WAIT_OBJECT_0+1
				return
			}
			var n uint32
			if err := windows.GetOverlappedResult(h, ov, &n, false); err != nil {
				return
			}
			// Nothing returned means more changed than the buffer could list: something did.
			if n == 0 || relevant(f, buf[:n]) {
				changed()
			}
		}
	}()
	return stop, nil
}

// relevant reports whether any change in a notification is one a walk would pick up.
func relevant(f Folder, b []byte) bool {
	for off := 0; off+12 <= len(b); {
		info := (*windows.FileNotifyInformation)(unsafe.Pointer(&b[off]))
		name := windows.UTF16ToString(unsafe.Slice(&info.FileName, info.FileNameLength/2))
		if !ignored(f, info.Action, name) {
			return true
		}
		if info.NextEntryOffset == 0 {
			return false
		}
		off += int(info.NextEntryOffset)
	}
	return false
}

func ignored(f Folder, action uint32, name string) bool {
	if f.Ignore != nil && f.Ignore(name) {
		return true
	}
	// A folder is "modified" whenever something inside it is created or deleted - even
	// Office's ~$ lock file when a document is merely opened. The change itself is reported
	// separately, so the folder's own entry adds nothing.
	if action == windows.FILE_ACTION_MODIFIED {
		if fi, err := os.Stat(filepath.Join(f.Path, name)); err == nil && fi.IsDir() {
			return true
		}
	}
	return false
}
