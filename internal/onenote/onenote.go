// Package onenote turns OneNote sections (.one files) into PDFs with the OneNote desktop app on
// this computer. Nothing reads the .one format well except OneNote itself: an open-source
// reader got titles and some typed text out of sample sections, lost the page layout and the
// handwriting, and crashed on two of three files (2026-10-26). OneNote's own export keeps all
// of it, so where the app is installed Binaly Sync sends that PDF in place of the section.
//
// The section is never opened where it lives: it is copied into Sync's own folder and the copy
// is opened, so nothing in the lab's folders is touched, and nothing is added to the user's
// notebook list.
package onenote

import "errors"

// ErrUnavailable means the OneNote desktop app isn't installed on this computer.
var ErrUnavailable = errors.New("the OneNote desktop app isn't installed on this computer")

// IsSection reports whether a file name is a OneNote section.
func IsSection(name string) bool {
	n := len(name)
	return n > 4 && (name[n-4:] == ".one" || name[n-4:] == ".ONE" || equalFold(name[n-4:], ".one"))
}

func equalFold(a, b string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := 0; i < len(a); i++ {
		x, y := a[i], b[i]
		if 'A' <= x && x <= 'Z' {
			x += 'a' - 'A'
		}
		if 'A' <= y && y <= 'Z' {
			y += 'a' - 'A'
		}
		if x != y {
			return false
		}
	}
	return true
}

// Refused reports whether OneNote itself declined to export a section (password-protected,
// damaged, an unsupported version), as opposed to OneNote being unavailable or closed. Only a
// refusal is worth remembering until the section changes.
func Refused(err error) bool { return refused(err) }
