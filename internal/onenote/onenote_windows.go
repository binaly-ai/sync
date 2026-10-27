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
	"strings"
	"syscall"
	"time"
	"unsafe"

	"github.com/go-ole/go-ole"
	"github.com/go-ole/go-ole/oleutil"
	"golang.org/x/sys/windows/registry"
)

// exportTimeout bounds one section: OneNote can stall on a damaged file.
const exportTimeout = 2 * time.Minute

// pagesWait bounds the wait for OneNote to load a section's pages after opening it.
const pagesWait = 20 * time.Second

// pfPDF is OneNote's PublishFormat for PDF.
const pfPDF = 3

// OneNote's IApplication interface (type library 15.0: OneNote 2013 and later). It is called
// through its function table, not by name (IDispatch): Click-to-Run Office registers the type
// library for 32-bit programs only, and OneNote's own by-name dispatch then fails for a 64-bit
// caller with "Library not registered", whatever is registered on the caller's side. A
// published COM interface never changes, so the slots are fixed.
var iidApplication = ole.NewGUID("{452AC71A-B655-4967-A208-A4CC39DD7949}")

const (
	slotGetHierarchy   = 7  // GetHierarchy(BSTR startNodeID, HierarchyScope, BSTR* xml, XMLSchema)
	slotOpenHierarchy  = 9  // OpenHierarchy(BSTR path, BSTR relativeTo, BSTR* objectID, CreateFileType)
	slotGetPageContent = 14 // GetPageContent(BSTR pageID, BSTR* xml, PageInfo, XMLSchema)
	slotPublish        = 20 // Publish(BSTR hierarchyID, BSTR targetFile, PublishFormat, BSTR exporterCLSID)
)

const (
	hsPages = 4 // HierarchyScope: down to pages
	piBasic = 0 // PageInfo: the page without its binary data (pictures, ink strokes)
	xs2013  = 2 // XMLSchema 2013, the current one
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
	pages        bool // also read each page's XML
	reply        chan result
}

type result struct {
	hierarchy string
	pages     []RawPage
	err       error
}

// New starts an exporter that works in workDir, a folder of Sync's own.
func New(workDir string) *Exporter {
	x := &Exporter{workDir: workDir, reqs: make(chan request), quit: make(chan struct{}), done: make(chan struct{})}
	go x.loop()
	return x
}

// Export writes section's pages to pdf, which must not exist yet (OneNote won't overwrite).
func (x *Exporter) Export(ctx context.Context, section, pdf string) error {
	_, err := x.do(ctx, request{section: section, pdf: pdf})
	return err
}

// ExportPages exports like Export and also reads every page of the section as OneNote's own
// XML, in the section's page order: titles, dates, authors, typed text, and the words OneNote
// recognized in handwriting and pictures. OneNote lists a just-opened section's pages only
// once it has loaded them, which publishing does, so the PDF is always written too.
func (x *Exporter) ExportPages(ctx context.Context, section, pdf string) ([]RawPage, error) {
	res, err := x.do(ctx, request{section: section, pdf: pdf, pages: true})
	return res.pages, err
}

func (x *Exporter) do(ctx context.Context, r request) (result, error) {
	r.reply = make(chan result, 1)
	select {
	case x.reqs <- r:
	case <-x.quit:
		return result{}, errors.New("exporter closed")
	case <-ctx.Done():
		return result{}, ctx.Err()
	}
	select {
	case res := <-r.reply:
		return res, res.err
	case <-time.After(exportTimeout):
		return result{}, fmt.Errorf("OneNote took over %s to export %s", exportTimeout, filepath.Base(r.section))
	case <-ctx.Done():
		return result{}, ctx.Err()
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
				r.reply <- result{err: fmt.Errorf("starting OneNote automation: %w", initErr)}
				continue
			}
			if app == nil {
				var err error
				if app, err = connect(); err != nil {
					r.reply <- result{err: err}
					continue
				}
			}
			res := x.export(app, r)
			if disconnected(res.err) {
				// OneNote itself went away (closed, crashed): reconnect for the next section.
				release()
			}
			r.reply <- res
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

// export copies the section into a folder of Sync's own, opens the copy, publishes it and,
// when asked, reads its pages.
func (x *Exporter) export(app *ole.IUnknown, r request) result {
	section, pdf := r.section, r.pdf
	fail := func(err error) result { return result{err: err} }
	if err := os.MkdirAll(x.workDir, 0o700); err != nil {
		return fail(err)
	}
	work, err := os.MkdirTemp(x.workDir, "section-")
	if err != nil {
		return fail(err)
	}
	defer os.RemoveAll(work)
	copyPath := filepath.Join(work, "Section.one")
	if err := copyFile(section, copyPath); err != nil {
		return fail(fmt.Errorf("copying %s: %w", filepath.Base(section), err))
	}
	id, err := openHierarchy(app, copyPath)
	if err != nil {
		return fail(fmt.Errorf("OneNote couldn't open %s: %w", filepath.Base(section), err))
	}
	if pdf != "" {
		if err := publish(app, id, pdf); err != nil {
			return fail(fmt.Errorf("OneNote couldn't export %s: %w", filepath.Base(section), err))
		}
		if fi, err := os.Stat(pdf); err != nil || fi.Size() == 0 {
			return fail(fmt.Errorf("OneNote exported nothing for %s", filepath.Base(section)))
		}
	}
	var res result
	if r.pages {
		// OneNote loads a section it has just opened in the background and lists no pages
		// until it has (areAllPagesAvailable="false"). Publishing above loads it; the wait is
		// a safety net, since reading without publishing first never finished loading in
		// testing (2026-10-27).
		deadline := time.Now().Add(pagesWait)
		for {
			if res.hierarchy, err = getHierarchy(app, id); err != nil {
				return fail(fmt.Errorf("OneNote couldn't list the pages of %s: %w", filepath.Base(section), err))
			}
			if !strings.Contains(res.hierarchy, `areAllPagesAvailable="false"`) || time.Now().After(deadline) {
				break
			}
			time.Sleep(250 * time.Millisecond)
		}
		for _, p := range hierarchyPages(res.hierarchy) {
			xml, err := getPageContent(app, p.ID)
			if err != nil {
				return fail(fmt.Errorf("OneNote couldn't read a page of %s: %w", filepath.Base(section), err))
			}
			p.XML = xml
			res.pages = append(res.pages, p)
		}
	}
	return res
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

func getHierarchy(app *ole.IUnknown, id string) (string, error) {
	start := bstr(id)
	defer free(start)
	var out *uint16
	hr, _, _ := syscall.SyscallN(slot(app, slotGetHierarchy), uintptr(unsafe.Pointer(app)),
		uintptr(unsafe.Pointer(start)), hsPages, uintptr(unsafe.Pointer(&out)), xs2013)
	if hr != 0 {
		return "", ole.NewError(hr)
	}
	if out == nil {
		return "", errors.New("no hierarchy returned")
	}
	defer free(out)
	return ole.BstrToString(out), nil
}

func getPageContent(app *ole.IUnknown, pageID string) (string, error) {
	id := bstr(pageID)
	defer free(id)
	var out *uint16
	hr, _, _ := syscall.SyscallN(slot(app, slotGetPageContent), uintptr(unsafe.Pointer(app)),
		uintptr(unsafe.Pointer(id)), uintptr(unsafe.Pointer(&out)), piBasic, xs2013)
	if hr != 0 {
		return "", ole.NewError(hr)
	}
	if out == nil {
		return "", errors.New("no page returned")
	}
	defer free(out)
	return ole.BstrToString(out), nil
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
