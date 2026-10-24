//go:build !windows

package ui

import "errors"

// ErrNoWebView: the window is Windows-only for now (macOS comes later).
var ErrNoWebView = errors.New("the Binaly Sync window isn't available on this system yet")

// Window is a stand-in on other systems; Show reports that there's no window.
type Window struct {
	B        Backend
	DataPath string
	OnError  func(error)
}

func (w *Window) Show(string) {
	if w.OnError != nil {
		w.OnError(ErrNoWebView)
	}
}
