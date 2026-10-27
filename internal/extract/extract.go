// Package extract reads what a file says, mechanically: its text page by page, how many
// pictures each page holds, and the metadata its format embeds (title, authors, dates, DOI).
//
// It exists because the model reading the lab's files skimmed long ones, never saw a file's
// dates, and missed whatever a note left out (2026-10-27). Sync already has every file on disk
// and reads each one to hash it, so the exact part happens here, once, and the server keeps it
// searchable. A page with pictures and little text is flagged rather than guessed at: that is
// handwriting or a sketch, which only a model looking at the page can read.
//
// Everything here is best-effort. A file this package can't read yields an error the caller
// reports, never a failed run.
package extract

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/binaly-ai/sync/internal/api"
)

const (
	// MaxPageChars caps one page's text (the server's limit).
	MaxPageChars = 100_000
	// MaxBodyBytes caps one extract's request body; pages past it lose their text.
	MaxBodyBytes = 16 << 20
	// bodyBudget leaves room in MaxBodyBytes for JSON escaping and the page fields.
	bodyBudget = 12 << 20
	// timeout bounds one file: a damaged PDF can make a parser spin.
	timeout = 2 * time.Minute
	// maxFileBytes: larger files aren't read for text (a scan of a whole book).
	maxFileBytes = 1 << 30
)

// Doc is one file's text.
type Doc struct {
	Kind      string
	Pages     []api.ExtractPage
	Truncated bool
}

// Kind returns the extract kind for a file name, or "" when this package doesn't read that
// format. OneNote sections are read through the OneNote app (package onenote), not here.
func Kind(name string) string {
	switch strings.ToLower(filepath.Ext(name)) {
	case ".pdf":
		return "pdf"
	case ".docx", ".docm":
		return "docx"
	case ".pptx", ".pptm":
		return "pptx"
	case ".xlsx", ".xlsm":
		return "xlsx"
	case ".txt", ".md", ".csv", ".tsv", ".log":
		return "text"
	}
	return ""
}

// ErrUnsupported means the file's format isn't one this package reads.
var ErrUnsupported = errors.New("not a format Binaly Sync reads text from")

// File reads a file's text. It gives up after a while, and recovers from a parser that panics
// on a damaged file.
func File(ctx context.Context, path string, size int64) (*Doc, error) {
	kind := Kind(path)
	if kind == "" {
		return nil, ErrUnsupported
	}
	if size > maxFileBytes {
		return nil, fmt.Errorf("over %d MB, not read for text", maxFileBytes>>20)
	}
	type result struct {
		pages []api.ExtractPage
		err   error
	}
	done := make(chan result, 1)
	go func() {
		var r result
		defer func() {
			if p := recover(); p != nil {
				r = result{err: fmt.Errorf("couldn't read the file: %v", p)}
			}
			done <- r
		}()
		switch kind {
		case "pdf":
			r.pages, r.err = pdfPages(path)
		case "docx":
			r.pages, r.err = docxPages(path)
		case "pptx":
			r.pages, r.err = pptxPages(path)
		case "xlsx":
			r.pages, r.err = xlsxPages(path)
		case "text":
			r.pages, r.err = textPages(path)
		}
	}()
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	select {
	case r := <-done:
		if r.err != nil {
			return nil, r.err
		}
		d := &Doc{Kind: kind, Pages: r.pages}
		Finish(d)
		return d, nil
	case <-ctx.Done():
		// The parser goroutine is left to finish on its own; it holds only its own memory.
		return nil, fmt.Errorf("reading its text took over %s", timeout)
	}
}

// Finish numbers the pages, caps their text, fills in character counts and hashes, and
// drops text past the body budget, marking the doc truncated.
func Finish(d *Doc) {
	total := 0
	for i := range d.Pages {
		p := &d.Pages[i]
		p.N = i + 1
		p.Text = strings.TrimSpace(strings.ToValidUTF8(p.Text, ""))
		if len(p.Text) > MaxPageChars {
			p.Text = cut(p.Text, MaxPageChars)
			d.Truncated = true
		}
		if p.Hash == "" {
			p.Hash = pageHash(p.Text, p.Images)
		}
		if total+len(p.Text) > bodyBudget {
			p.Text = ""
			d.Truncated = true
		}
		total += len(p.Text)
		p.Chars = utf8.RuneCountInString(p.Text)
	}
}

func pageHash(text string, images int) string {
	sum := sha256.Sum256([]byte(fmt.Sprintf("%s\x00%d", text, images)))
	return hex.EncodeToString(sum[:])
}

// cut shortens s to at most n bytes without splitting a character.
func cut(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}

// clip bounds a metadata string (the server keeps 512 characters).
func clip(s string) string {
	s = strings.TrimSpace(strings.ToValidUTF8(s, ""))
	return cut(s, 512)
}
