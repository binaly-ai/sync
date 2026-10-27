package extract

import (
	"archive/zip"
	"encoding/xml"
	"io"
	"path"
	"sort"
	"strconv"
	"strings"

	"github.com/binaly-ai/sync/internal/api"
)

// Office documents (docx, pptx, xlsx) are zip files of XML parts. Only the parts that hold
// text and document properties are read.

// maxPartBytes bounds one XML part read into memory.
const maxPartBytes = 64 << 20

type ooxml struct {
	zr    *zip.ReadCloser
	parts map[string]*zip.File
}

func openOOXML(p string) (*ooxml, error) {
	zr, err := zip.OpenReader(p)
	if err != nil {
		return nil, err
	}
	o := &ooxml{zr: zr, parts: map[string]*zip.File{}}
	for _, f := range zr.File {
		o.parts[strings.TrimPrefix(f.Name, "/")] = f
	}
	return o, nil
}

func (o *ooxml) Close() { o.zr.Close() }

// open returns a part's XML decoder, or nil when the part isn't there.
func (o *ooxml) open(name string) (*xml.Decoder, io.Closer) {
	f := o.parts[strings.TrimPrefix(name, "/")]
	if f == nil {
		return nil, nil
	}
	rc, err := f.Open()
	if err != nil {
		return nil, nil
	}
	d := xml.NewDecoder(io.LimitReader(rc, maxPartBytes))
	d.Strict = false
	return d, rc
}

// rels maps a part's relationship ids to the parts they point at.
func (o *ooxml) rels(part string) map[string]string {
	dir, file := path.Split(part)
	d, c := o.open(dir + "_rels/" + file + ".rels")
	out := map[string]string{}
	if d == nil {
		return out
	}
	defer c.Close()
	for {
		tok, err := d.Token()
		if err != nil {
			return out
		}
		if se, ok := tok.(xml.StartElement); ok && se.Name.Local == "Relationship" {
			target := attrOf(se, "Target")
			if attrOf(se, "TargetMode") == "External" || target == "" {
				continue
			}
			if strings.HasPrefix(target, "/") {
				out[attrOf(se, "Id")] = strings.TrimPrefix(target, "/")
			} else {
				out[attrOf(se, "Id")] = path.Clean(dir + target)
			}
		}
	}
}

func attrOf(se xml.StartElement, name string) string {
	for _, a := range se.Attr {
		if a.Name.Local == name {
			return a.Value
		}
	}
	return ""
}

// lines accumulates text a paragraph at a time, with table rows joined by " | ".
type lines struct {
	b     strings.Builder
	para  strings.Builder
	cells []string
	depth int // table nesting
}

func (l *lines) text(s string) { l.para.WriteString(s) }

func (l *lines) endPara() {
	s := strings.TrimSpace(l.para.String())
	l.para.Reset()
	if s == "" {
		return
	}
	if l.depth > 0 && len(l.cells) > 0 {
		c := &l.cells[len(l.cells)-1]
		*c = strings.TrimSpace(*c + " " + s)
		return
	}
	l.b.WriteString(s)
	l.b.WriteByte('\n')
}

func (l *lines) startCell() { l.cells = append(l.cells, "") }

func (l *lines) endRow() {
	var filled []string
	for _, c := range l.cells {
		if c != "" {
			filled = append(filled, c)
		}
	}
	l.cells = l.cells[:0]
	if len(filled) > 0 {
		l.b.WriteString(strings.Join(filled, " | "))
		l.b.WriteByte('\n')
	}
}

func (l *lines) take() string {
	l.endPara()
	s := strings.TrimSpace(l.b.String())
	l.b.Reset()
	return s
}

// docxPages splits a Word document where Word last laid out a page break, so the page numbers
// match what a reader of the document sees.
func docxPages(p string) ([]api.ExtractPage, error) {
	o, err := openOOXML(p)
	if err != nil {
		return nil, err
	}
	defer o.Close()
	d, c := o.open("word/document.xml")
	if d == nil {
		return nil, io.ErrUnexpectedEOF
	}
	defer c.Close()
	var pages []api.ExtractPage
	var l lines
	images := 0
	flush := func() {
		pages = append(pages, api.ExtractPage{Text: l.take(), Images: images})
		images = 0
	}
	inText := false
	for {
		tok, err := d.Token()
		if err != nil {
			break
		}
		switch t := tok.(type) {
		case xml.StartElement:
			switch t.Name.Local {
			case "t":
				inText = true
			case "tab":
				l.text("\t")
			case "br":
				if attrOf(t, "type") == "page" {
					flush()
				} else {
					l.text("\n")
				}
			case "lastRenderedPageBreak":
				flush()
			case "drawing", "pict":
				images++
			case "tbl":
				l.endPara()
				l.depth++
			case "tc":
				l.startCell()
			}
		case xml.EndElement:
			switch t.Name.Local {
			case "t":
				inText = false
			case "p":
				l.endPara()
			case "tr":
				l.endRow()
			case "tbl":
				l.depth--
			}
		case xml.CharData:
			if inText {
				l.text(string(t))
			}
		}
	}
	flush()
	return dropEmptyTail(pages), nil
}

// pptxPages gives one page per slide, in the presentation's order, with the slide's speaker
// notes after its text.
func pptxPages(p string) ([]api.ExtractPage, error) {
	o, err := openOOXML(p)
	if err != nil {
		return nil, err
	}
	defer o.Close()
	rels := o.rels("ppt/presentation.xml")
	var order []string
	if d, c := o.open("ppt/presentation.xml"); d != nil {
		for {
			tok, err := d.Token()
			if err != nil {
				break
			}
			if se, ok := tok.(xml.StartElement); ok && se.Name.Local == "sldId" {
				for _, a := range se.Attr {
					if a.Name.Local == "id" && a.Name.Space != "" { // r:id, not the numeric id
						if target := rels[a.Value]; target != "" {
							order = append(order, target)
						}
					}
				}
			}
		}
		c.Close()
	}
	if len(order) == 0 {
		order = numbered(o, "ppt/slides/slide")
	}
	pages := make([]api.ExtractPage, 0, len(order))
	for _, slide := range order {
		text, images := drawingText(o, slide)
		for _, target := range o.rels(slide) {
			if strings.Contains(target, "notesSlides/") {
				if notes, _ := drawingText(o, target); notes != "" {
					text = strings.TrimSpace(text + "\n[Speaker notes]\n" + notes)
				}
			}
		}
		pages = append(pages, api.ExtractPage{Text: text, Images: images})
	}
	return pages, nil
}

// drawingText reads the text and picture count of a DrawingML part (a slide, a notes page).
func drawingText(o *ooxml, part string) (string, int) {
	d, c := o.open(part)
	if d == nil {
		return "", 0
	}
	defer c.Close()
	var l lines
	images := 0
	inText := false
	for {
		tok, err := d.Token()
		if err != nil {
			break
		}
		switch t := tok.(type) {
		case xml.StartElement:
			switch t.Name.Local {
			case "t":
				inText = true
			case "pic":
				images++
			case "tbl":
				l.endPara()
				l.depth++
			case "tc":
				l.startCell()
			}
		case xml.EndElement:
			switch t.Name.Local {
			case "t":
				inText = false
			case "p":
				l.endPara()
			case "tr":
				l.endRow()
			case "tbl":
				l.depth--
			}
		case xml.CharData:
			if inText {
				l.text(string(t))
			}
		}
	}
	return l.take(), images
}

// xlsxPages gives one page per sheet, titled with the sheet's name, one line per row.
func xlsxPages(p string) ([]api.ExtractPage, error) {
	o, err := openOOXML(p)
	if err != nil {
		return nil, err
	}
	defer o.Close()
	shared := sharedStrings(o)
	rels := o.rels("xl/workbook.xml")
	type sheet struct{ name, part string }
	var sheets []sheet
	if d, c := o.open("xl/workbook.xml"); d != nil {
		for {
			tok, err := d.Token()
			if err != nil {
				break
			}
			if se, ok := tok.(xml.StartElement); ok && se.Name.Local == "sheet" {
				for _, a := range se.Attr {
					if a.Name.Local == "id" && a.Name.Space != "" {
						if target := rels[a.Value]; target != "" {
							sheets = append(sheets, sheet{attrOf(se, "name"), target})
						}
					}
				}
			}
		}
		c.Close()
	}
	if len(sheets) == 0 {
		for _, part := range numbered(o, "xl/worksheets/sheet") {
			sheets = append(sheets, sheet{"", part})
		}
	}
	var pages []api.ExtractPage
	for _, s := range sheets {
		pages = append(pages, api.ExtractPage{Title: clip(s.name), Text: sheetText(o, s.part, shared), Images: len(drawingsOf(o, s.part))})
	}
	return pages, nil
}

func drawingsOf(o *ooxml, part string) []string {
	var out []string
	for _, target := range o.rels(part) {
		if strings.Contains(target, "drawings/") || strings.Contains(target, "charts/") {
			out = append(out, target)
		}
	}
	return out
}

func sharedStrings(o *ooxml) []string {
	d, c := o.open("xl/sharedStrings.xml")
	if d == nil {
		return nil
	}
	defer c.Close()
	var out []string
	var cur strings.Builder
	inT := false
	for {
		tok, err := d.Token()
		if err != nil {
			return out
		}
		switch t := tok.(type) {
		case xml.StartElement:
			if t.Name.Local == "t" {
				inT = true
			}
		case xml.EndElement:
			switch t.Name.Local {
			case "t":
				inT = false
			case "si":
				out = append(out, cur.String())
				cur.Reset()
			}
		case xml.CharData:
			if inT {
				cur.Write(t)
			}
		}
	}
}

// sheetText reads a worksheet's cell values, a row per line. It stops at MaxPageChars: the
// rest of a large sheet is data the server stores, not text to search.
func sheetText(o *ooxml, part string, shared []string) string {
	d, c := o.open(part)
	if d == nil {
		return ""
	}
	defer c.Close()
	var b strings.Builder
	var row []string
	var cellType string
	var val strings.Builder
	inV := false
	for b.Len() < MaxPageChars {
		tok, err := d.Token()
		if err != nil {
			break
		}
		switch t := tok.(type) {
		case xml.StartElement:
			switch t.Name.Local {
			case "c":
				cellType = attrOf(t, "t")
				val.Reset()
			case "v", "t":
				inV = true
			}
		case xml.EndElement:
			switch t.Name.Local {
			case "v", "t":
				inV = false
			case "c":
				v := strings.TrimSpace(val.String())
				if cellType == "s" {
					if i, err := strconv.Atoi(v); err == nil && i >= 0 && i < len(shared) {
						v = strings.TrimSpace(shared[i])
					}
				}
				if v != "" {
					row = append(row, v)
				}
			case "row":
				if len(row) > 0 {
					b.WriteString(strings.Join(row, " | "))
					b.WriteByte('\n')
				}
				row = row[:0]
			}
		case xml.CharData:
			if inV {
				val.Write(t)
			}
		}
	}
	return strings.TrimSpace(b.String())
}

// numbered lists parts named prefix1.xml, prefix2.xml ... in numeric order: the fallback when
// a document's own list of parts can't be read.
func numbered(o *ooxml, prefix string) []string {
	type part struct {
		n    int
		name string
	}
	var ps []part
	for name := range o.parts {
		if strings.HasPrefix(name, prefix) && strings.HasSuffix(name, ".xml") {
			if n, err := strconv.Atoi(strings.TrimSuffix(strings.TrimPrefix(name, prefix), ".xml")); err == nil {
				ps = append(ps, part{n, name})
			}
		}
	}
	sort.Slice(ps, func(i, j int) bool { return ps[i].n < ps[j].n })
	out := make([]string, len(ps))
	for i, p := range ps {
		out[i] = p.name
	}
	return out
}

func dropEmptyTail(pages []api.ExtractPage) []api.ExtractPage {
	for len(pages) > 1 && pages[len(pages)-1].Text == "" && pages[len(pages)-1].Images == 0 {
		pages = pages[:len(pages)-1]
	}
	return pages
}

// officeMeta reads an Office document's properties: docProps/core.xml for title, authors and
// dates, docProps/app.xml for the page or slide count.
func officeMeta(p string) (*api.FileMeta, error) {
	o, err := openOOXML(p)
	if err != nil {
		return nil, err
	}
	defer o.Close()
	m := &api.FileMeta{}
	if d, c := o.open("docProps/core.xml"); d != nil {
		var field string
		for {
			tok, err := d.Token()
			if err != nil {
				break
			}
			switch t := tok.(type) {
			case xml.StartElement:
				field = t.Name.Local
			case xml.EndElement:
				field = ""
			case xml.CharData:
				v := clip(string(t))
				switch field {
				case "title":
					m.Title = v
				case "creator":
					m.Author = v
				case "lastModifiedBy":
					m.LastModifiedBy = v
				case "created":
					m.Created = v
				case "modified":
					m.Modified = v
				}
			}
		}
		c.Close()
	}
	if d, c := o.open("docProps/app.xml"); d != nil {
		var field string
		for {
			tok, err := d.Token()
			if err != nil {
				break
			}
			switch t := tok.(type) {
			case xml.StartElement:
				field = t.Name.Local
			case xml.EndElement:
				field = ""
			case xml.CharData:
				if field == "Pages" || field == "Slides" {
					if n, err := strconv.Atoi(strings.TrimSpace(string(t))); err == nil {
						m.Pages = n
					}
				}
			}
		}
		c.Close()
	}
	return m, nil
}
