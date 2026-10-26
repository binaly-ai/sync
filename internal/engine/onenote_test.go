package engine

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/binaly-ai/sync/internal/api"
	"github.com/binaly-ai/sync/internal/onenote"
)

// fakeOneNote stands in for the OneNote app: its "PDF" names the section's bytes, so a changed
// section gives a different PDF.
type fakeOneNote struct {
	mu    sync.Mutex
	calls int
	fail  map[string]bool // base names to fail
}

func (f *fakeOneNote) Export(ctx context.Context, section, pdf string) error {
	f.mu.Lock()
	f.calls++
	fail := f.fail[filepath.Base(section)]
	f.mu.Unlock()
	if fail {
		return errors.New("OneNote is busy")
	}
	b, err := os.ReadFile(section)
	if err != nil {
		return err
	}
	if _, err := os.Stat(pdf); err == nil {
		return errors.New("target exists") // OneNote won't overwrite
	}
	return os.WriteFile(pdf, append([]byte("%PDF-1.7 export of "), b...), 0o600)
}

func (f *fakeOneNote) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

func lastReportOf(f *fakeServer, name string) (api.FileReport, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i := len(f.reports) - 1; i >= 0; i-- {
		for _, fr := range f.reports[i].Files {
			if strings.HasSuffix(fr.Path, name) {
				return fr, true
			}
		}
	}
	return api.FileReport{}, false
}

func TestOneNoteSectionIsSentAsItsPDF(t *testing.T) {
	e, fake, dir := setup(t)
	on := &fakeOneNote{}
	e.OneNote, e.Renditions = on, t.TempDir()
	section := filepath.Join(dir, "Notebook", "Cu runs.one")
	write(t, section, "section v1")
	write(t, filepath.Join(dir, "Notebook", "Open Notebook.onetoc2"), "toc")

	if err := e.Run(context.Background(), Options{}); err != nil {
		t.Fatal(err)
	}
	fr, ok := lastReportOf(fake, "Cu runs.one")
	pdf := "%PDF-1.7 export of section v1"
	if !ok || fr.Rendition != "pdf" || fr.SHA256 != sha(pdf) || fr.Size != int64(len(pdf)) {
		t.Fatalf("section reported as %+v, want its PDF", fr)
	}
	if !strings.HasSuffix(fr.Path, ".one") {
		t.Fatalf("the report names %s, not the section", fr.Path)
	}
	fake.mu.Lock()
	stored := string(fake.stored[sha(pdf)])
	fake.mu.Unlock()
	if stored != pdf {
		t.Fatalf("server stored %q, want the PDF", stored)
	}
	if left, _ := os.ReadDir(e.Renditions); len(left) != 0 {
		t.Fatalf("%d PDF(s) left after the upload", len(left))
	}
	if toc, ok := lastReportOf(fake, "Open Notebook.onetoc2"); !ok || toc.Rendition != "" {
		t.Fatalf("the table of contents should be reported as it is: %+v", toc)
	}

	// A full run with nothing changed reports the same PDF without exporting again.
	calls := on.count()
	if err := e.Run(context.Background(), Options{Force: true}); err != nil {
		t.Fatal(err)
	}
	if on.count() != calls {
		t.Fatalf("an unchanged section was exported again (%d -> %d)", calls, on.count())
	}
	if fr, _ := lastReportOf(fake, "Cu runs.one"); fr.SHA256 != sha(pdf) || fr.Rendition != "pdf" {
		t.Fatalf("a full run reported %+v", fr)
	}

	// A changed section is exported again and sent as its new PDF.
	write(t, section, "section v2, longer")
	if err := e.Run(context.Background(), Options{}); err != nil {
		t.Fatal(err)
	}
	pdf2 := "%PDF-1.7 export of section v2, longer"
	if fr, _ := lastReportOf(fake, "Cu runs.one"); fr.SHA256 != sha(pdf2) {
		t.Fatalf("after a change the section was reported as %+v", fr)
	}
	if on.count() != calls+1 {
		t.Fatalf("a changed section should be exported once more: %d -> %d", calls, on.count())
	}
}

func TestOneNoteExportFailureFallsBack(t *testing.T) {
	e, fake, dir := setup(t)
	on := &fakeOneNote{fail: map[string]bool{"Locked.one": true}}
	e.OneNote, e.Renditions = on, t.TempDir()
	write(t, filepath.Join(dir, "Locked.one"), "locked section")
	if err := e.Run(context.Background(), Options{}); err != nil {
		t.Fatal(err)
	}
	fr, ok := lastReportOf(fake, "Locked.one")
	if !ok || fr.Rendition != "" || fr.SHA256 != sha("locked section") {
		t.Fatalf("a section OneNote couldn't export should be reported as it is: %+v", fr)
	}
	// Not a refusal by OneNote itself, so it's tried again next run.
	calls := on.count()
	if err := e.Run(context.Background(), Options{Force: true}); err != nil {
		t.Fatal(err)
	}
	if on.count() != calls+1 {
		t.Fatalf("a passing failure should be retried: %d -> %d", calls, on.count())
	}
}

func TestNoOneNoteShowsANotice(t *testing.T) {
	e, fake, dir := setup(t)
	write(t, filepath.Join(dir, "A.one"), "a")
	write(t, filepath.Join(dir, "B.one"), "b")
	if err := e.Run(context.Background(), Options{}); err != nil {
		t.Fatal(err)
	}
	if n := e.Snapshot().Notice; !strings.Contains(n, "2 OneNote sections") || !strings.Contains(n, "install the OneNote desktop app") {
		t.Fatalf("notice is %q", n)
	}
	if fr, _ := lastReportOf(fake, "A.one"); fr.Rendition != "" {
		t.Fatalf("without OneNote a section is reported as it is: %+v", fr)
	}
}

// The real OneNote app, end to end through the engine: BINALY_SYNC_ONENOTE_SAMPLE=<a .one>.
func TestRealOneNoteThroughTheEngine(t *testing.T) {
	sample := os.Getenv("BINALY_SYNC_ONENOTE_SAMPLE")
	if sample == "" || !onenote.Available() {
		t.Skip("set BINALY_SYNC_ONENOTE_SAMPLE and install OneNote to run")
	}
	e, fake, dir := setup(t)
	x := onenote.New(filepath.Join(t.TempDir(), "work"))
	defer x.Close()
	e.OneNote, e.Renditions = x, t.TempDir()
	b, err := os.ReadFile(sample)
	if err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(dir, "Lab notebook", "Section.one"), string(b))
	if err := e.Run(context.Background(), Options{}); err != nil {
		t.Fatal(err)
	}
	fr, ok := lastReportOf(fake, "Section.one")
	if !ok || fr.Rendition != "pdf" {
		t.Fatalf("reported %+v", fr)
	}
	fake.mu.Lock()
	stored := fake.stored[fr.SHA256]
	fake.mu.Unlock()
	if !strings.HasPrefix(string(stored), "%PDF-") || int64(len(stored)) != fr.Size {
		t.Fatalf("server holds %d bytes starting %q", len(stored), stored[:min(8, len(stored))])
	}
	t.Logf("sent a %d-byte PDF for a %d-byte section", fr.Size, len(b))
}
