package main

import (
	"errors"
	"runtime"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

// pickFolder shows Windows' standard folder picker: the Explorer-style window with an address
// bar (a path can be pasted there), Quick access, network locations and search. It's the
// IFileOpenDialog COM object in folder mode, called directly rather than through a library.
// Returns "" if the person cancels.
func pickFolder(title string) (string, error) {
	runtime.LockOSThread() // COM objects belong to the thread that made them
	defer runtime.UnlockOSThread()
	if err := windows.CoInitializeEx(0, windows.COINIT_APARTMENTTHREADED|windows.COINIT_DISABLE_OLE1DDE); err != nil {
		// S_FALSE (already initialized on this thread) arrives as a non-nil "error" too.
		if !errors.Is(err, syscall.Errno(1)) {
			return "", err
		}
	}
	defer windows.CoUninitialize()

	var dlg *comObject
	if hr := coCreateInstance(&clsidFileOpenDialog, &iidFileOpenDialog, &dlg); hr != 0 {
		return "", hresult(hr)
	}
	defer dlg.call(vtRelease)

	var opts uint32
	dlg.call(vtGetOptions, uintptr(unsafe.Pointer(&opts)))
	opts |= fosPickFolders | fosForceFileSystem | fosPathMustExist
	if hr := dlg.call(vtSetOptions, uintptr(opts)); hr != 0 {
		return "", hresult(hr)
	}
	if t, err := windows.UTF16PtrFromString(title); err == nil {
		dlg.call(vtSetTitle, uintptr(unsafe.Pointer(t)))
	}
	hr := dlg.call(vtShow, 0)
	if hr == errCancelled {
		return "", nil
	}
	if hr != 0 {
		return "", hresult(hr)
	}
	var item *comObject
	if hr := dlg.call(vtGetResult, uintptr(unsafe.Pointer(&item))); hr != 0 {
		return "", hresult(hr)
	}
	defer item.call(vtRelease)
	var name *uint16
	if hr := item.call(vtGetDisplayName, sigdnFileSysPath, uintptr(unsafe.Pointer(&name))); hr != 0 {
		return "", hresult(hr)
	}
	defer windows.CoTaskMemFree(unsafe.Pointer(name))
	return windows.UTF16PtrToString(name), nil
}

// comObject is a COM interface pointer: its first word points at the method table.
type comObject struct{ vtbl *[32]uintptr }

func (o *comObject) call(method int, args ...uintptr) uintptr {
	r, _, _ := syscall.SyscallN(o.vtbl[method], append([]uintptr{uintptr(unsafe.Pointer(o))}, args...)...)
	return r
}

// Method slots. IFileOpenDialog extends IFileDialog, which extends IModalWindow (Show), which
// extends IUnknown; IShellItem extends IUnknown.
const (
	vtRelease        = 2
	vtShow           = 3
	vtSetOptions     = 9
	vtGetOptions     = 10
	vtSetTitle       = 17
	vtGetResult      = 20
	vtGetDisplayName = 5 // IShellItem
)

const (
	fosPickFolders     = 0x20
	fosForceFileSystem = 0x40
	fosPathMustExist   = 0x800
	sigdnFileSysPath   = 0x80058000
	errCancelled       = 0x800704C7 // HRESULT_FROM_WIN32(ERROR_CANCELLED)
)

var (
	clsidFileOpenDialog = windows.GUID{Data1: 0xDC1C5A9C, Data2: 0xE88A, Data3: 0x4DDE, Data4: [8]byte{0xA5, 0xA1, 0x60, 0xF8, 0x2A, 0x20, 0xAE, 0xF7}}
	iidFileOpenDialog   = windows.GUID{Data1: 0xD57C7288, Data2: 0xD4AD, Data3: 0x4768, Data4: [8]byte{0xBE, 0x02, 0x9D, 0x96, 0x95, 0x32, 0xD9, 0x60}}

	procCoCreateInstance = windows.NewLazySystemDLL("ole32.dll").NewProc("CoCreateInstance")
)

func coCreateInstance(clsid, iid *windows.GUID, out **comObject) uintptr {
	const clsctxInprocServer = 1
	r, _, _ := procCoCreateInstance.Call(uintptr(unsafe.Pointer(clsid)), 0, clsctxInprocServer, uintptr(unsafe.Pointer(iid)), uintptr(unsafe.Pointer(out)))
	return r
}

type hresult uintptr

func (h hresult) Error() string {
	return "the folder picker failed (" + windows.Errno(h&0xFFFF).Error() + ")"
}
