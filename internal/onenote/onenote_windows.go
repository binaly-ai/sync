//go:build windows

package onenote

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"syscall"
	"time"
	"unsafe"

	"github.com/go-ole/go-ole"
	"github.com/go-ole/go-ole/oleutil"
	"golang.org/x/sys/windows/registry"
)

// exportTimeout bounds one section: OneNote can stall on a damaged file.
const exportTimeout = 2 * time.Minute

// pfPDF is OneNote's PublishFormat for PDF.
const pfPDF = 3

// OneNote's IApplication interface (type library 15.0: OneNote 2013 and later). It is called
// through its function table, not by name (IDispatch): Click-to-Run Office registers the type
// library for 32-bit programs only, and OneNote's own by-name dispatch then fails for a 64-bit
// caller with "Library not registered", whatever is registered on the caller's side. A
// published COM interface never changes, so the slots are fixed.
var iidApplication = ole.NewGUID("{452AC71A-B655-4967-A208-A4CC39DD7949}")

const (
	slotOpenHierarchy = 9  // OpenHierarchy(BSTR path, BSTR relativeTo, BSTR* objectID, CreateFileType)
	slotPublish       = 20 // Publish(BSTR hierarchyID, BSTR targetFile, PublishFormat, BSTR exporterCLSID)
)

// Available reports whether the OneNote desktop app (with its automation interface) is
// installed. The Store-only "OneNote for Windows 10" has none and isn't counted.
func Available() bool {
	k, err := registry.OpenKey(registry.CLASSES_ROOT, `OneNote.Application\CLSID`, registry.QUERY_VALUE)
	if err != nil {
		return false
	}
	k.Close()
	return true
}

// Exporter owns one connection to OneNote, on one thread (OneNote's automation is
// single-threaded), and exports one section at a time.
type Exporter struct {
	workDir string
	reqs    chan request
	quit    chan struct{}
	done    chan struct{}
}

type request struct {
	section, pdf string
	reply        chan error
}

// New starts an exporter that works in workDir, a folder of Sync's own.
func New(workDir string) *Exporter {
	x := &Exporter{workDir: workDir, reqs: make(chan request), quit: make(chan struct{}), done: make(chan struct{})}
	go x.loop()
	return x
}

// Export writes section's pages to pdf, which must not exist yet (OneNote won't overwrite).
func (x *Exporter) Export(ctx context.Context, section, pdf string) error {
	r := request{section: section, pdf: pdf, reply: make(chan error, 1)}
	select {
	case x.reqs <- r:
	case <-x.quit:
		return errors.New("exporter closed")
	case <-ctx.Done():
		return ctx.Err()
	}
	select {
	case err := <-r.reply:
		return err
	case <-time.After(exportTimeout):
		return fmt.Errorf("OneNote took over %s to export %s", exportTimeout, filepath.Base(section))
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Close stops the exporter and lets go of OneNote.
func (x *Exporter) Close() {
	select {
	case <-x.quit:
	default:
		close(x.quit)
	}
	<-x.done
}

func (x *Exporter) loop() {
	defer close(x.done)
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	initErr := ole.CoInitializeEx(0, ole.COINIT_APARTMENTTHREADED)
	if initErr != nil {
		var oe *ole.OleError
		if errors.As(initErr, &oe) && oe.Code() == 1 { // S_FALSE: already initialized on this thread
			initErr = nil
		}
	}
	if initErr == nil {
		defer ole.CoUninitialize()
	}
	var app *ole.IUnknown
	release := func() {
		if app != nil {
			app.Release()
			app = nil
		}
	}
	defer release()
	for {
		select {
		case <-x.quit:
			return
		case r := <-x.reqs:
			if initErr != nil {
				r.reply <- fmt.Errorf("starting OneNote automation: %w", initErr)
				continue
			}
			if app == nil {
				var err error
				if app, err = connect(); err != nil {
					r.reply <- err
					continue
				}
			}
			err := x.export(app, r.section, r.pdf)
			if disconnected(err) {
				// OneNote itself went away (closed, crashed): reconnect for the next section.
				release()
			}
			r.reply <- err
		}
	}
}

func connect() (*ole.IUnknown, error) {
	if !Available() {
		return nil, ErrUnavailable
	}
	unknown, err := oleutil.CreateObject("OneNote.Application")
	if err != nil {
		return nil, fmt.Errorf("starting OneNote: %w", err)
	}
	defer unknown.Release()
	app, err := unknown.QueryInterface(iidApplication)
	if err != nil {
		return nil, fmt.Errorf("connecting to OneNote: %w", err)
	}
	return (*ole.IUnknown)(unsafe.Pointer(app)), nil
}

// export copies the section into a folder of Sync's own, opens the copy and publishes it.
func (x *Exporter) export(app *ole.IUnknown, section, pdf string) error {
	if err := os.MkdirAll(x.workDir, 0o700); err != nil {
		return err
	}
	work, err := os.MkdirTemp(x.workDir, "section-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(work)
	copyPath := filepath.Join(work, "Section.one")
	if err := copyFile(section, copyPath); err != nil {
		return fmt.Errorf("copying %s: %w", filepath.Base(section), err)
	}
	id, err := openHierarchy(app, copyPath)
	if err != nil {
		return fmt.Errorf("OneNote couldn't open %s: %w", filepath.Base(section), err)
	}
	if err := publish(app, id, pdf); err != nil {
		return fmt.Errorf("OneNote couldn't export %s: %w", filepath.Base(section), err)
	}
	if fi, err := os.Stat(pdf); err != nil || fi.Size() == 0 {
		return fmt.Errorf("OneNote exported nothing for %s", filepath.Base(section))
	}
	return nil
}

func openHierarchy(app *ole.IUnknown, path string) (string, error) {
	p, rel := bstr(path), bstr("")
	defer free(p)
	defer free(rel)
	var id *uint16
	hr, _, _ := syscall.SyscallN(slot(app, slotOpenHierarchy), uintptr(unsafe.Pointer(app)),
		uintptr(unsafe.Pointer(p)), uintptr(unsafe.Pointer(rel)), uintptr(unsafe.Pointer(&id)), 0)
	if hr != 0 {
		return "", ole.NewError(hr)
	}
	if id == nil {
		return "", errors.New("no section id returned")
	}
	defer free(id)
	return ole.LpOleStrToString(id), nil
}

func publish(app *ole.IUnknown, id, pdf string) error {
	i, out, exporter := bstr(id), bstr(pdf), bstr("")
	defer free(i)
	defer free(out)
	defer free(exporter)
	hr, _, _ := syscall.SyscallN(slot(app, slotPublish), uintptr(unsafe.Pointer(app)),
		uintptr(unsafe.Pointer(i)), uintptr(unsafe.Pointer(out)), pfPDF, uintptr(unsafe.Pointer(exporter)))
	if hr != 0 {
		return ole.NewError(hr)
	}
	return nil
}

// slot returns the address of method n in a COM object's function table.
func slot(obj *ole.IUnknown, n int) uintptr {
	vtbl := *(**[64]uintptr)(unsafe.Pointer(obj))
	return vtbl[n]
}

func bstr(s string) *uint16 { return (*uint16)(unsafe.Pointer(ole.SysAllocString(s))) }

func free(b *uint16) { ole.SysFreeString((*int16)(unsafe.Pointer(b))) }

func refused(err error) bool {
	var oe *ole.OleError
	return errors.As(err, &oe) && uint32(oe.Code())&0xFFFF0000 == 0x80040000
}

// disconnected: the connection to OneNote failed (it was closed or crashed), as opposed to
// OneNote refusing one section (password-protected, damaged), which leaves it usable.
func disconnected(err error) bool {
	var oe *ole.OleError
	if !errors.As(err, &oe) {
		return false
	}
	switch uint32(oe.Code()) {
	case 0x800706BA, 0x800706BE, 0x80010108, 0x80010012: // RPC server unavailable, call failed, disconnected, server died
		return true
	}
	return false
}

func copyFile(from, to string) error {
	in, err := os.Open(from)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(to)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}
