package engine

import (
	"archive/zip"
	"bytes"
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/binaly-ai/sync/internal/api"
	"github.com/binaly-ai/sync/internal/onenote"
	"github.com/binaly-ai/sync/internal/pathx"
)

func writeDocx(t *testing.T, path string) {
	t.Helper()
	var b bytes.Buffer
	w := zip.NewWriter(&b)
	for name, body := range map[string]string{
		"docProps/core.xml": `<cp:coreProperties xmlns:cp="cp" xmlns:dc="dc" xmlns:dcterms="dcterms"><dc:title>Bakeout SOP</dc:title><dc:creator>Kate</dc:creator><dcterms:created>2025-03-01T10:00:00Z</dcterms:created></cp:coreProperties>`,
		"word/document.xml": `<w:document xmlns:w="w"><w:body><w:p><w:r><w:t>Heat to 150 C for 48 h</w:t></w:r></w:p></w:body></w:document>`,
	} {
		f, _ := w.Create(name)
		f.Write([]byte(body))
	}
	w.Close()
	os.MkdirAll(filepath.Dir(path), 0o755)
	if err := os.WriteFile(path, b.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
}

func extractsOf(f *fakeServer, name string) []api.ExtractIn {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []api.ExtractIn
	for _, x := range f.extracts {
		if strings.HasSuffix(x.Path, name) {
			out = append(out, x)
		}
	}
	return out
}

func TestReportCarriesMetaAndCreated(t *testing.T) {
	e, fake, dir := setup(t)
	writeDocx(t, filepath.Join(dir, "SOP.docx"))
	write(t, filepath.Join(dir, "notes.txt"), "plain")
	if err := e.Run(context.Background(), Options{}); err != nil {
		t.Fatal(err)
	}
	fr, _ := lastReportOf(fake, "SOP.docx")
	if fr.Meta == nil || fr.Meta.Title != "Bakeout SOP" || fr.Meta.Author != "Kate" || fr.Meta.Created != "2025-03-01T10:00:00Z" {
		t.Fatalf("docx reported with meta %+v", fr.Meta)
	}
	if runtime.GOOS == "windows" && fr.Ctime == "" {
		t.Fatal("no created time on Windows")
	}
	if txt, _ := lastReportOf(fake, "notes.txt"); txt.Meta != nil {
		t.Fatalf("a text file reported meta %+v", txt.Meta)
	}
	// The metadata is read once per version: a full report the next day reuses it.
	got, ok := e.State.CachedMeta(keyOf(filepath.Join(dir, "SOP.docx")), fr.Size, mtimeOf(t, filepath.Join(dir, "SOP.docx")))
	if !ok || !strings.Contains(string(got), "Bakeout SOP") {
		t.Fatalf("metadata not cached: %s", got)
	}
}

func TestExtractSentWhenAsked(t *testing.T) {
	e, fake, dir := setup(t)
	fake.wantExtract = map[string]bool{"notes.txt": true}
	write(t, filepath.Join(dir, "notes.txt"), "Base pressure 2e-10 mbar")
	write(t, filepath.Join(dir, "other.txt"), "not asked for")
	ctx := context.Background()
	if err := e.Run(ctx, Options{}); err != nil {
		t.Fatal(err)
	}
	xs := extractsOf(fake, "notes.txt")
	if len(xs) != 1 {
		t.Fatalf("%d extracts for notes.txt, want 1 (after its upload)", len(xs))
	}
	x := xs[0]
	if x.Kind != "text" || x.SHA256 != sha("Base pressure 2e-10 mbar") || len(x.Pages) != 1 || x.Pages[0].Text != "Base pressure 2e-10 mbar" || x.Pages[0].N != 1 || x.MachineID != "m_test" {
		t.Fatalf("extract %+v", x)
	}
	if len(extractsOf(fake, "other.txt")) != 0 {
		t.Fatal("text sent that the server didn't ask for")
	}

	// Asked again for content already stored ("known"): sent without an upload - but not
	// within a day of the last time, however often the server asks.
	fake.mu.Lock()
	fake.wantExtract["other.txt"] = true
	fake.mu.Unlock()
	if err := e.Run(ctx, Options{Force: true}); err != nil {
		t.Fatal(err)
	}
	if n := len(extractsOf(fake, "other.txt")); n != 1 {
		t.Fatalf("a known file's text was sent %d times, want 1", n)
	}
	if n := len(extractsOf(fake, "notes.txt")); n != 1 {
		t.Fatalf("notes.txt's text was sent again within a day (%d times)", n)
	}
}

func TestUnreadableFileReportsWhy(t *testing.T) {
	e, fake, dir := setup(t)
	fake.wantExtract = map[string]bool{"broken.pdf": true}
	write(t, filepath.Join(dir, "broken.pdf"), "%PDF-1.4 not really")
	if err := e.Run(context.Background(), Options{}); err != nil {
		t.Fatal(err)
	}
	xs := extractsOf(fake, "broken.pdf")
	if len(xs) != 1 || xs[0].Error == "" || len(xs[0].Pages) != 0 || xs[0].Kind != "pdf" {
		t.Fatalf("got %+v, want one extract with the reason and no pages", xs)
	}
}

// fakePagesOneNote also reads pages, like onenote.Exporter.
type fakePagesOneNote struct{ fakeOneNote }

func (f *fakePagesOneNote) ExportPages(ctx context.Context, section, pdf string) ([]onenote.RawPage, error) {
	if err := f.Export(ctx, section, pdf); err != nil {
		return nil, err
	}
	return []onenote.RawPage{{
		ID: "{P1}", Name: "Oct 6 - TSP conditioning", DateTime: "2025-10-06T14:02:00.000Z", LastModified: "2025-10-07T09:10:00.000Z",
		XML: `<one:Page xmlns:one="o"><one:Outline><one:OEChildren><one:OE author="Sivaraj Velusamy"><one:T><![CDATA[Base pressure 2e-10 mbar]]></one:T></one:OE><one:OE><one:InkWord/></one:OE></one:OEChildren></one:Outline></one:Page>`,
	}}, nil
}

func TestOneNoteSectionSendsItsPages(t *testing.T) {
	e, fake, dir := setup(t)
	on := &fakePagesOneNote{}
	e.OneNote, e.Renditions = on, t.TempDir()
	fake.wantExtract = map[string]bool{"Enteries.one": true}
	write(t, filepath.Join(dir, "Notebook", "Enteries.one"), "section v1")
	if err := e.Run(context.Background(), Options{}); err != nil {
		t.Fatal(err)
	}
	fr, _ := lastReportOf(fake, "Enteries.one")
	if fr.Rendition != "pdf" || fr.Meta == nil || fr.Meta.Pages != 1 || fr.Meta.Title != "Enteries" || fr.Meta.Author != "Sivaraj Velusamy" || fr.Meta.Created != "2025-10-06T14:02:00.000Z" {
		t.Fatalf("section reported as %+v meta %+v", fr, fr.Meta)
	}
	xs := extractsOf(fake, "Enteries.one")
	if len(xs) != 1 {
		t.Fatalf("%d extracts, want 1", len(xs))
	}
	x := xs[0]
	p := x.Pages[0]
	if x.Kind != "onenote" || x.SHA256 != fr.SHA256 || p.Title != "Oct 6 - TSP conditioning" || p.Created == "" || p.Author != "Sivaraj Velusamy" || p.Text != "Base pressure 2e-10 mbar" || p.Images != 1 {
		t.Fatalf("extract %+v", x)
	}
	if left, _ := filepath.Glob(filepath.Join(e.Renditions, "*.pages.json")); len(left) != 0 {
		t.Fatalf("pages kept after the server had them: %v", left)
	}
}

func TestOneNoteExportedBeforePagesKeepsItsPDF(t *testing.T) {
	// A section exported by an earlier Sync, with no pages on file: when the server asks for
	// its text, the pages are read again, and the PDF the server holds keeps its hash.
	e, fake, dir := setup(t)
	e.OneNote, e.Renditions = &fakeOneNote{}, t.TempDir()
	write(t, filepath.Join(dir, "Notebook", "Old.one"), "section v1")
	ctx := context.Background()
	if err := e.Run(ctx, Options{}); err != nil {
		t.Fatal(err)
	}
	first, _ := lastReportOf(fake, "Old.one")
	on := &fakePagesOneNote{}
	e.OneNote = on
	fake.mu.Lock()
	fake.wantExtract = map[string]bool{"Old.one": true}
	fake.mu.Unlock()
	if err := e.Run(ctx, Options{Force: true}); err != nil {
		t.Fatal(err)
	}
	again, _ := lastReportOf(fake, "Old.one")
	xs := extractsOf(fake, "Old.one")
	if again.SHA256 != first.SHA256 || len(xs) != 1 || xs[0].SHA256 != first.SHA256 || len(xs[0].Pages) != 1 {
		t.Fatalf("first %s, again %s, extracts %+v", first.SHA256, again.SHA256, xs)
	}
}

func keyOf(p string) string { return pathx.Key(pathx.Canonical(p)) }

func mtimeOf(t *testing.T, p string) int64 {
	fi, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}
	return fi.ModTime().UnixNano()
}
