//go:build windows

package onenote

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// Needs the OneNote desktop app and a sample section: BINALY_SYNC_ONENOTE_SAMPLE=<path to a .one>.
func TestExportSample(t *testing.T) {
	sample := os.Getenv("BINALY_SYNC_ONENOTE_SAMPLE")
	if sample == "" || !Available() {
		t.Skip("set BINALY_SYNC_ONENOTE_SAMPLE and install OneNote to run")
	}
	before, _ := os.ReadFile(sample)
	dir := t.TempDir()
	x := New(filepath.Join(dir, "work"))
	defer x.Close()
	for i := 0; i < 3; i++ {
		pdf := filepath.Join(dir, "out"+string(rune('a'+i))+".pdf")
		start := time.Now()
		if err := x.Export(context.Background(), sample, pdf); err != nil {
			t.Fatalf("export %d: %v", i, err)
		}
		fi, _ := os.Stat(pdf)
		head := make([]byte, 5)
		f, _ := os.Open(pdf)
		f.Read(head)
		f.Close()
		if string(head) != "%PDF-" {
			t.Fatalf("export %d isn't a PDF: %q", i, head)
		}
		t.Logf("export %d: %d bytes in %s", i, fi.Size(), time.Since(start))
	}
	after, _ := os.ReadFile(sample)
	if string(before) != string(after) {
		t.Fatal("the original section changed")
	}
	if left, _ := os.ReadDir(filepath.Join(dir, "work")); len(left) != 0 {
		t.Fatalf("working copies left behind: %d", len(left))
	}
}

// Reads a sample section's pages (BINALY_SYNC_ONENOTE_SAMPLE): OneNote lists them only once
// the section is loaded, which the export does.
func TestExportPagesSample(t *testing.T) {
	sample := os.Getenv("BINALY_SYNC_ONENOTE_SAMPLE")
	if sample == "" || !Available() {
		t.Skip("set BINALY_SYNC_ONENOTE_SAMPLE and install OneNote to run")
	}
	dir := t.TempDir()
	x := New(filepath.Join(dir, "work"))
	defer x.Close()
	pages, err := x.ExportPages(context.Background(), sample, filepath.Join(dir, "out.pdf"))
	if err != nil {
		t.Fatal(err)
	}
	if len(pages) == 0 {
		t.Fatal("no pages")
	}
	for i, raw := range pages {
		p := ParsePage(raw)
		if p.Title == "" || p.Created == "" || raw.XML == "" {
			t.Fatalf("page %d: %+v", i, p)
		}
		t.Logf("page %d %q created %s, %d characters, %d pictures unread, authors %v", i+1, p.Title, p.Created, len(p.Text), p.Images, p.Authors)
	}
}

func TestExportMissingSection(t *testing.T) {
	if !Available() {
		t.Skip("OneNote isn't installed")
	}
	dir := t.TempDir()
	x := New(filepath.Join(dir, "work"))
	defer x.Close()
	if err := x.Export(context.Background(), filepath.Join(dir, "nope.one"), filepath.Join(dir, "x.pdf")); err == nil {
		t.Fatal("a missing section exported")
	}
}
