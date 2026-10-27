package extract

import (
	"regexp"
	"strings"
	"time"

	"github.com/binaly-ai/sync/internal/api"
	"github.com/ledongthuc/pdf"
)

// pdfPages reads a PDF's text and picture count page by page. A scanned PDF has pictures and
// no text on every page, which is exactly what the server needs to know about it.
func pdfPages(path string) ([]api.ExtractPage, error) {
	f, r, err := pdf.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	n := r.NumPage()
	pages := make([]api.ExtractPage, 0, n)
	for i := 1; i <= n; i++ {
		p := r.Page(i)
		var text string
		if !p.V.IsNull() {
			text, _ = p.GetPlainText(nil) // a page that won't decode keeps its picture count
		}
		pages = append(pages, api.ExtractPage{Text: logicalOrder(tidy(text)), Images: pdfImages(p.Resources(), 0)})
	}
	return pages, nil
}

// pdfImages counts the pictures a page draws, including those inside its form objects.
func pdfImages(res pdf.Value, depth int) int {
	xo := res.Key("XObject")
	n := 0
	for _, k := range xo.Keys() {
		v := xo.Key(k)
		switch v.Key("Subtype").Name() {
		case "Image":
			n++
		case "Form":
			if depth < 3 {
				n += pdfImages(v.Key("Resources"), depth+1)
			}
		}
	}
	return n
}

var doiRe = regexp.MustCompile(`\b10\.\d{4,9}/[-._;()/:A-Za-z0-9]+[A-Za-z0-9]`)

// pdfMeta reads a PDF's document info and, from its first page, a DOI.
func pdfMeta(path string) (*api.FileMeta, error) {
	f, r, err := pdf.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	info := r.Trailer().Key("Info")
	m := &api.FileMeta{
		Title:    clip(info.Key("Title").Text()),
		Author:   clip(info.Key("Author").Text()),
		Created:  pdfDate(info.Key("CreationDate").Text()),
		Modified: pdfDate(info.Key("ModDate").Text()),
		Pages:    r.NumPage(),
	}
	if d := doiRe.FindString(info.Key("Subject").Text() + " " + info.Key("Keywords").Text()); d != "" {
		m.DOI = d
	} else if r.NumPage() > 0 {
		if p := r.Page(1); !p.V.IsNull() {
			if text, err := p.GetPlainText(nil); err == nil {
				m.DOI = doiRe.FindString(text)
			}
		}
	}
	m.DOI = clip(strings.TrimRight(m.DOI, ".,;"))
	return m, nil
}

// pdfDate turns a PDF date ("D:20120727012724+01'00'") into ISO 8601, or "" if it isn't one.
func pdfDate(s string) string {
	s = strings.TrimPrefix(strings.TrimSpace(s), "D:")
	if len(s) < 4 {
		return ""
	}
	s = strings.ReplaceAll(s, "'", "")
	digits := 0
	for digits < len(s) && digits < 14 && s[digits] >= '0' && s[digits] <= '9' {
		digits++
	}
	layouts := map[int]string{4: "2006", 6: "200601", 8: "20060102", 10: "2006010215", 12: "200601021504", 14: "20060102150405"}
	layout, ok := layouts[digits]
	if !ok {
		return ""
	}
	t, err := time.Parse(layout, s[:digits])
	if err != nil {
		return ""
	}
	if zone := s[digits:]; len(zone) >= 5 && (zone[0] == '+' || zone[0] == '-') {
		if z, err := time.Parse("-0700", zone[:5]); err == nil {
			t = time.Date(t.Year(), t.Month(), t.Day(), t.Hour(), t.Minute(), t.Second(), 0, z.Location())
		}
	}
	return t.UTC().Format(time.RFC3339)
}

// tidy trims each line and drops runs of blank lines.
func tidy(s string) string {
	var b strings.Builder
	blank := 0
	for _, line := range strings.Split(strings.ReplaceAll(s, "\r", "\n"), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			blank++
			continue
		}
		if b.Len() > 0 {
			if blank > 0 {
				b.WriteString("\n\n")
			} else {
				b.WriteByte('\n')
			}
		}
		blank = 0
		b.WriteString(line)
	}
	return b.String()
}
