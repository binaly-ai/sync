package extract

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/binaly-ai/sync/internal/api"
)

// minimalPDF builds a one-page PDF with a line of text, one picture and document info, with
// a correct cross-reference table.
func minimalPDF(text string) []byte {
	content := fmt.Sprintf("BT /F1 12 Tf 72 720 Td (%s) Tj ET q 10 0 0 10 72 600 cm /Im1 Do Q", text)
	objs := []string{
		"<< /Type /Catalog /Pages 2 0 R >>",
		"<< /Type /Pages /Kids [3 0 R] /Count 1 >>",
		"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] /Contents 4 0 R /Resources << /Font << /F1 5 0 R >> /XObject << /Im1 6 0 R >> >> >>",
		fmt.Sprintf("<< /Length %d >>\nstream\n%s\nendstream", len(content), content),
		"<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>",
		"<< /Type /XObject /Subtype /Image /Width 1 /Height 1 /ColorSpace /DeviceGray /BitsPerComponent 8 /Length 1 >>\nstream\n\x80\nendstream",
		"<< /Title (Spin texture of Cu\\(110\\)) /Author (A. Scientist) /CreationDate (D:20240102030405Z) >>",
	}
	var b bytes.Buffer
	b.WriteString("%PDF-1.4\n")
	offsets := make([]int, len(objs))
	for i, o := range objs {
		offsets[i] = b.Len()
		fmt.Fprintf(&b, "%d 0 obj\n%s\nendobj\n", i+1, o)
	}
	xref := b.Len()
	fmt.Fprintf(&b, "xref\n0 %d\n0000000000 65535 f \n", len(objs)+1)
	for _, off := range offsets {
		fmt.Fprintf(&b, "%010d 00000 n \n", off)
	}
	fmt.Fprintf(&b, "trailer\n<< /Size %d /Root 1 0 R /Info 7 0 R >>\nstartxref\n%d\n%%%%EOF\n", len(objs)+1, xref)
	return b.Bytes()
}

func zipFile(t *testing.T, path string, parts map[string]string) {
	t.Helper()
	var b bytes.Buffer
	w := zip.NewWriter(&b)
	for name, body := range parts {
		f, _ := w.Create(name)
		f.Write([]byte(body))
	}
	w.Close()
	if err := os.WriteFile(path, b.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
}

const core = `<?xml version="1.0"?><cp:coreProperties xmlns:cp="http://schemas.openxmlformats.org/package/2006/metadata/core-properties" xmlns:dc="http://purl.org/dc/elements/1.1/" xmlns:dcterms="http://purl.org/dc/terms/"><dc:title>Growth log</dc:title><dc:creator>Sivaraj</dc:creator><cp:lastModifiedBy>Oliver</cp:lastModifiedBy><dcterms:created>2025-10-06T14:02:00Z</dcterms:created><dcterms:modified>2025-10-07T09:10:00Z</dcterms:modified></cp:coreProperties>`

func doc(t *testing.T, name string, size int64) *Doc {
	t.Helper()
	d, err := File(context.Background(), name, size)
	if err != nil {
		t.Fatalf("%s: %v", filepath.Base(name), err)
	}
	return d
}

func TestPDF(t *testing.T) {
	p := filepath.Join(t.TempDir(), "paper.pdf")
	raw := minimalPDF("Hello copper doi 10.1234/abcd.5678.")
	os.WriteFile(p, raw, 0o644)
	d := doc(t, p, int64(len(raw)))
	if d.Kind != "pdf" || len(d.Pages) != 1 {
		t.Fatalf("got %+v", d)
	}
	pg := d.Pages[0]
	if !strings.Contains(pg.Text, "Hello copper") || pg.Images != 1 || pg.N != 1 || pg.Chars == 0 || len(pg.Hash) != 64 {
		t.Fatalf("page %+v", pg)
	}
	m, err := ReadMeta(p)
	if err != nil || m == nil {
		t.Fatalf("meta: %v %v", m, err)
	}
	if m.Title != "Spin texture of Cu(110)" || m.Author != "A. Scientist" || m.Created != "2024-01-02T03:04:05Z" || m.Pages != 1 || m.DOI != "10.1234/abcd.5678" {
		t.Fatalf("meta %+v", *m)
	}
}

func TestDamagedPDFFailsCleanly(t *testing.T) {
	p := filepath.Join(t.TempDir(), "broken.pdf")
	os.WriteFile(p, []byte("%PDF-1.4\nthis is not a pdf"), 0o644)
	if _, err := File(context.Background(), p, 30); err == nil {
		t.Fatal("a damaged PDF gave text")
	}
	if m, _ := ReadMeta(p); m != nil {
		t.Fatalf("a damaged PDF gave metadata %+v", *m)
	}
}

func TestDOCXPagesFollowWordsPageBreaks(t *testing.T) {
	p := filepath.Join(t.TempDir(), "protocol.docx")
	zipFile(t, p, map[string]string{
		"docProps/core.xml": core,
		"docProps/app.xml":  `<Properties><Pages>2</Pages></Properties>`,
		"word/document.xml": `<w:document xmlns:w="w"><w:body>
<w:p><w:r><w:t>Step 1: anneal at 400 C</w:t></w:r></w:p>
<w:tbl><w:tr><w:tc><w:p><w:r><w:t>Sample</w:t></w:r></w:p></w:tc><w:tc><w:p><w:r><w:t>T (K)</w:t></w:r></w:p></w:tc></w:tr>
<w:tr><w:tc><w:p><w:r><w:t>DV10</w:t></w:r></w:p></w:tc><w:tc><w:p><w:r><w:t>675</w:t></w:r></w:p></w:tc></w:tr></w:tbl>
<w:p><w:r><w:lastRenderedPageBreak/><w:t>Step 2: image</w:t></w:r><w:r><w:drawing/></w:r></w:p>
</w:body></w:document>`,
	})
	d := doc(t, p, 1000)
	if len(d.Pages) != 2 {
		t.Fatalf("pages %+v", d.Pages)
	}
	if want := "Step 1: anneal at 400 C\nSample | T (K)\nDV10 | 675"; d.Pages[0].Text != want {
		t.Fatalf("page 1 %q, want %q", d.Pages[0].Text, want)
	}
	if d.Pages[1].Text != "Step 2: image" || d.Pages[1].Images != 1 {
		t.Fatalf("page 2 %+v", d.Pages[1])
	}
	m, _ := ReadMeta(p)
	if m == nil || m.Title != "Growth log" || m.Author != "Sivaraj" || m.LastModifiedBy != "Oliver" || m.Created != "2025-10-06T14:02:00Z" || m.Pages != 2 {
		t.Fatalf("meta %+v", m)
	}
}

func TestPPTXSlidesInPresentationOrderWithNotes(t *testing.T) {
	p := filepath.Join(t.TempDir(), "group meeting.pptx")
	zipFile(t, p, map[string]string{
		"ppt/presentation.xml":             `<p:presentation xmlns:p="p" xmlns:r="r"><p:sldIdLst><p:sldId id="256" r:id="rId3"/><p:sldId id="257" r:id="rId2"/></p:sldIdLst></p:presentation>`,
		"ppt/_rels/presentation.xml.rels":  `<Relationships><Relationship Id="rId2" Target="slides/slide1.xml"/><Relationship Id="rId3" Target="slides/slide2.xml"/></Relationships>`,
		"ppt/slides/slide1.xml":            `<p:sld xmlns:p="p" xmlns:a="a"><a:p><a:r><a:t>Results</a:t></a:r></a:p><p:pic/></p:sld>`,
		"ppt/slides/slide2.xml":            `<p:sld xmlns:p="p" xmlns:a="a"><a:p><a:r><a:t>Title </a:t></a:r><a:r><a:t>slide</a:t></a:r></a:p></p:sld>`,
		"ppt/slides/_rels/slide2.xml.rels": `<Relationships><Relationship Id="rId1" Target="../notesSlides/notesSlide1.xml"/></Relationships>`,
		"ppt/notesSlides/notesSlide1.xml":  `<p:notes xmlns:p="p" xmlns:a="a"><a:p><a:r><a:t>Mention the bakeout</a:t></a:r></a:p></p:notes>`,
	})
	d := doc(t, p, 1000)
	if len(d.Pages) != 2 || d.Pages[0].Text != "Title slide\n[Speaker notes]\nMention the bakeout" || d.Pages[1].Text != "Results" || d.Pages[1].Images != 1 {
		t.Fatalf("slides %+v", d.Pages)
	}
}

func TestXLSXOnePagePerSheet(t *testing.T) {
	p := filepath.Join(t.TempDir(), "samples.xlsx")
	zipFile(t, p, map[string]string{
		"xl/workbook.xml":            `<workbook xmlns:r="r"><sheets><sheet name="Growth" r:id="rId1"/><sheet name="Empty" r:id="rId2"/></sheets></workbook>`,
		"xl/_rels/workbook.xml.rels": `<Relationships><Relationship Id="rId1" Target="worksheets/sheet1.xml"/><Relationship Id="rId2" Target="worksheets/sheet2.xml"/></Relationships>`,
		"xl/sharedStrings.xml":       `<sst><si><t>Sample</t></si><si><t>DV10</t></si></sst>`,
		"xl/worksheets/sheet1.xml":   `<worksheet><sheetData><row><c t="s"><v>0</v></c><c t="inlineStr"><is><t>Temp</t></is></c></row><row><c t="s"><v>1</v></c><c><v>675</v></c></row></sheetData></worksheet>`,
		"xl/worksheets/sheet2.xml":   `<worksheet><sheetData/></worksheet>`,
	})
	d := doc(t, p, 1000)
	if len(d.Pages) != 2 || d.Pages[0].Title != "Growth" || d.Pages[0].Text != "Sample | Temp\nDV10 | 675" || d.Pages[1].Title != "Empty" {
		t.Fatalf("sheets %+v", d.Pages)
	}
}

func TestTextAndWindows1252(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "run.txt")
	os.WriteFile(p, []byte("Temperature 25\xb0C \x96 stable\r\n"), 0o644)
	d := doc(t, p, 30)
	if len(d.Pages) != 1 || d.Pages[0].Text != "Temperature 25°C – stable" {
		t.Fatalf("got %q", d.Pages[0].Text)
	}
	long := filepath.Join(dir, "long.log")
	os.WriteFile(long, []byte(strings.Repeat("0123456789abcdefghi\n", 7000)), 0o644)
	if d := doc(t, long, 140000); len(d.Pages) != 2 || d.Truncated {
		t.Fatalf("a 140,000-character log gave %d pages (truncated %v), want 2", len(d.Pages), d.Truncated)
	}
}

func TestFinishCapsPagesAndBody(t *testing.T) {
	big := strings.Repeat("é", MaxPageChars) // two bytes each: over the byte cap
	d := &Doc{Kind: "text"}
	for i := 0; i < 200; i++ {
		d.Pages = append(d.Pages, api.ExtractPage{Text: big})
	}
	Finish(d)
	if !d.Truncated {
		t.Fatal("not marked truncated")
	}
	total := 0
	for i, p := range d.Pages {
		if len(p.Text) > MaxPageChars || p.N != i+1 || len(p.Hash) != 64 {
			t.Fatalf("page %d: %d bytes, n=%d", i, len(p.Text), p.N)
		}
		total += len(p.Text)
	}
	if total > bodyBudget {
		t.Fatalf("%d bytes of text, over the budget", total)
	}
}

func TestVisualOrderHebrewIsTurnedBack(t *testing.T) {
	// "חיבור וחיסור עד 20" (addition and subtraction up to 20), as a visual-order PDF stores it.
	visual := "20 דע רוסיחו רוביח\nםירפסמה תרכה ןוכנ םוקמב\nםיעבטה םירפסמה ןומיס"
	got := logicalOrder(visual)
	if !strings.HasPrefix(got, "חיבור וחיסור עד 20") {
		t.Fatalf("got %q", got)
	}
	logical := "הכרת המספרים במקום נכון\nסימון המספרים הטבעיים"
	if logicalOrder(logical) != logical {
		t.Fatal("logical-order Hebrew was reversed")
	}
	if logicalOrder("Cu(110) at 400 C") != "Cu(110) at 400 C" {
		t.Fatal("Latin text changed")
	}
}

func TestPDFDates(t *testing.T) {
	for in, want := range map[string]string{
		"D:20120727012724+01'00'": "2012-07-27T00:27:24Z",
		"D:20240102":              "2024-01-02T00:00:00Z",
		"D:2024":                  "2024-01-01T00:00:00Z",
		"yesterday":               "",
		"":                        "",
	} {
		if got := pdfDate(in); got != want {
			t.Errorf("pdfDate(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestJPEGCaptureTime(t *testing.T) {
	// A JPEG whose APP1 segment holds a little-endian TIFF: IFD0 points to the EXIF IFD, which
	// has DateTimeOriginal.
	date := "2025:10:06 14:02:33\x00"
	var tiff bytes.Buffer
	le := binary.LittleEndian
	tiff.WriteString("II")
	binary.Write(&tiff, le, uint16(42))
	binary.Write(&tiff, le, uint32(8)) // IFD0 at 8
	binary.Write(&tiff, le, uint16(1))
	binary.Write(&tiff, le, uint16(0x8769))
	binary.Write(&tiff, le, uint16(4))
	binary.Write(&tiff, le, uint32(1))
	binary.Write(&tiff, le, uint32(26)) // EXIF IFD at 26
	binary.Write(&tiff, le, uint32(0))
	binary.Write(&tiff, le, uint16(1)) // EXIF IFD: one entry
	binary.Write(&tiff, le, uint16(0x9003))
	binary.Write(&tiff, le, uint16(2))
	binary.Write(&tiff, le, uint32(len(date)))
	binary.Write(&tiff, le, uint32(44)) // value at 44
	binary.Write(&tiff, le, uint32(0))
	tiff.WriteString(date)
	var jpg bytes.Buffer
	jpg.Write([]byte{0xFF, 0xD8, 0xFF, 0xE1})
	binary.Write(&jpg, binary.BigEndian, uint16(2+6+tiff.Len()))
	jpg.WriteString("Exif\x00\x00")
	jpg.Write(tiff.Bytes())
	jpg.Write([]byte{0xFF, 0xDA, 0, 2, 0xFF, 0xD9})
	p := filepath.Join(t.TempDir(), "stm.jpg")
	os.WriteFile(p, jpg.Bytes(), 0o644)
	m, err := ReadMeta(p)
	if err != nil || m == nil || m.Captured != "2025-10-06T14:02:33" {
		t.Fatalf("meta %+v %v", m, err)
	}
	plain := filepath.Join(t.TempDir(), "plain.jpg")
	os.WriteFile(plain, []byte{0xFF, 0xD8, 0xFF, 0xDA, 0, 2, 0xFF, 0xD9}, 0o644)
	if m, err := ReadMeta(plain); m != nil || err != nil {
		t.Fatalf("a JPEG without EXIF gave %+v %v", m, err)
	}
}

func TestUnsupported(t *testing.T) {
	if Kind("x.opj") != "" || Kind("a.PDF") != "pdf" || Kind("b.Docx") != "docx" {
		t.Fatal("kinds")
	}
	if _, err := File(context.Background(), "x.opj", 1); err != ErrUnsupported {
		t.Fatalf("got %v", err)
	}
}
