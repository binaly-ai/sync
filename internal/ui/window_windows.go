package ui

import (
	"encoding/json"
	"errors"
	"runtime"
	"strconv"
	"sync"

	webview2 "github.com/jchv/go-webview2"
	"golang.org/x/sys/windows"
)

// ErrNoWebView means Windows lacks the WebView2 component (only very old Windows 10 builds).
var ErrNoWebView = errors.New("this computer is missing Microsoft Edge WebView2, which Binaly Sync uses for its window")

// Window is the app's one window. Show opens it, or brings it forward if it's open.
type Window struct {
	B        Backend
	DataPath string // WebView2's own files (cache, settings), under Binaly Sync's folder
	OnError  func(error)

	mu sync.Mutex
	wv webview2.WebView
	up bool // open, or opening
}

// Show opens the window on a screen: "status", "settings" or "preview?folder=...".
func (w *Window) Show(screen string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.up {
		if wv := w.wv; wv != nil {
			wv.Dispatch(func() {
				wv.Eval("binalyShow(" + jsString(screen) + ")")
				hwnd := windows.HWND(uintptr(wv.Window()))
				showWindow.Call(uintptr(hwnd), 9) // SW_RESTORE
				setForeground.Call(uintptr(hwnd))
			})
		}
		return
	}
	w.up = true
	go w.run(screen)
}

var (
	user32        = windows.NewLazySystemDLL("user32.dll")
	showWindow    = user32.NewProc("ShowWindow")
	setForeground = user32.NewProc("SetForegroundWindow")
)

func jsString(s string) string { b, _ := json.Marshal(s); return string(b) }

// run owns the window for its lifetime. A Win32 window belongs to the thread that made it,
// so this goroutine stays on one OS thread and runs the window's message loop.
func (w *Window) run(screen string) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	defer func() {
		w.mu.Lock()
		w.wv, w.up = nil, false
		w.mu.Unlock()
	}()

	wv := webview2.NewWithOptions(webview2.WebViewOptions{
		DataPath:  w.DataPath,
		AutoFocus: true,
		WindowOptions: webview2.WindowOptions{
			Title:  "Binaly Sync",
			Width:  940,
			Height: 760,
			IconId: 1, // the program's icon resource (winres)
			Center: true,
		},
	})
	if wv == nil {
		if w.OnError != nil {
			w.OnError(ErrNoWebView)
		}
		return
	}
	// The page calls binalyCall(id, name, body) and gets its answer through _binalyDone. Work
	// runs off the window's thread, so a sign-in or a folder picker doesn't freeze the window.
	_ = wv.Bind("binalyCall", func(id int, name, body string) {
		go func() {
			out, err := Call(w.B, name, []byte(body))
			okay, val := true, any(out)
			if err != nil {
				okay, val = false, err.Error()
			}
			raw, merr := json.Marshal(val)
			if merr != nil {
				okay, raw = false, []byte(jsString(merr.Error()))
			}
			wv.Dispatch(func() {
				wv.Eval("_binalyDone(" + strconv.Itoa(id) + "," + boolJS(okay) + "," + string(raw) + ")")
			})
		}()
	})
	wv.Init("window.binalyStartScreen = " + jsString(screen) + ";")
	wv.SetHtml(Page())

	w.mu.Lock()
	w.wv = wv
	w.mu.Unlock()
	wv.Run()
}

func boolJS(b bool) string {
	if b {
		return "true"
	}
	return "false"
}
