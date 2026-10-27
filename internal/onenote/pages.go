package onenote

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/xml"
	"html"
	"sort"
	"strconv"
	"strings"
)

// RawPage is one page of a section as OneNote describes it: its place in the hierarchy and,
// once read, its content as OneNote's own XML (schema 2013).
type RawPage struct {
	ID           string
	Name         string
	DateTime     string // created
	LastModified string
	Level        string // 1 for a page, 2 or 3 for a subpage
	XML          string
}

// hierarchyPages lists the pages in a GetHierarchy result, in the section's order.
func hierarchyPages(hierarchy string) []RawPage {
	var pages []RawPage
	d := xml.NewDecoder(strings.NewReader(hierarchy))
	for {
		tok, err := d.Token()
		if err != nil {
			return pages
		}
		se, ok := tok.(xml.StartElement)
		if !ok || se.Name.Local != "Page" {
			continue
		}
		var p RawPage
		for _, a := range se.Attr {
			switch a.Name.Local {
			case "ID":
				p.ID = a.Value
			case "name":
				p.Name = a.Value
			case "dateTime":
				p.DateTime = a.Value
			case "lastModifiedTime":
				p.LastModified = a.Value
			case "pageLevel":
				p.Level = a.Value
			}
		}
		if p.ID != "" {
			pages = append(pages, p)
		}
	}
}

// Page is what Binaly gets from one OneNote page: its title, dates and authors, its text in
// reading order (typed text, table rows, the words OneNote recognized in handwriting and in
// pictures), and how many pictures or drawings it holds that no text describes. Those are what
// a person (or Claude, looking at the PDF) still has to read.
type Page struct {
	Title    string
	Created  string
	Modified string
	// Authors, most active first: the people OneNote credits with the page's paragraphs.
	Authors     []string
	Text        string
	Images      int
	Attachments []string // files inserted on the page, by name; the PDF shows only an icon
	Hash        string   // changes when the page's content or its modified time does
}

// ParsePage reads a RawPage's XML. It never fails: what it can't read is left out.
func ParsePage(p RawPage) Page {
	out := Page{Title: p.Name, Created: p.DateTime, Modified: p.LastModified}
	var b strings.Builder
	line := func(s string) {
		s = strings.TrimSpace(s)
		if s != "" {
			b.WriteString(s)
			b.WriteByte('\n')
		}
	}
	authors := map[string]int{}
	var cells []string // the current table row
	inCell, inTitle := 0, 0
	d := xml.NewDecoder(strings.NewReader(p.XML))
	d.Strict = false
	var field string // the element whose character data comes next: T or OCRText
	read := false    // the current OCRText has been counted
	for {
		tok, err := d.Token()
		if err != nil {
			break
		}
		switch t := tok.(type) {
		case xml.StartElement:
			switch t.Name.Local {
			case "OE":
				for _, a := range t.Attr {
					if (a.Name.Local == "author" || a.Name.Local == "lastModifiedBy") && a.Value != "" {
						authors[a.Value]++
					}
				}
			case "Title":
				inTitle++
			case "Cell":
				inCell++
				cells = append(cells, "")
			case "T", "OCRText":
				field = t.Name.Local
				read = false
			case "Image":
				out.Images++
			case "InkDrawing":
				out.Images++ // a drawing; recognized handwriting arrives as InkWord instead
			case "InkWord":
				if w := attr(t, "recognizedText"); w != "" {
					line(w)
				} else {
					out.Images++
				}
			case "InsertedFile":
				if n := attr(t, "preferredName"); n != "" {
					out.Attachments = append(out.Attachments, n)
					line("[Attached file: " + n + "]")
				}
			}
		case xml.EndElement:
			switch t.Name.Local {
			case "Title":
				inTitle--
			case "Cell":
				inCell--
			case "Row":
				var filled []string
				for _, c := range cells {
					if c != "" {
						filled = append(filled, c)
					}
				}
				line(strings.Join(filled, " | "))
				cells = cells[:0]
			case "T", "OCRText":
				field = ""
			}
		case xml.CharData:
			if field == "" {
				continue
			}
			s := strings.TrimSpace(htmlText(string(t)))
			switch {
			case s == "":
			case field == "OCRText":
				if !read {
					out.Images-- // a picture whose words OneNote read is described by them
					read = true
				}
				line("[Picture text: " + strings.Join(strings.Fields(s), " ") + "]")
			case inTitle > 0 && s == out.Title:
				// The page title repeats the hierarchy's name; it heads the page already.
			case inCell > 0 && len(cells) > 0:
				cells[len(cells)-1] = strings.TrimSpace(cells[len(cells)-1] + " " + s)
			default:
				line(s)
			}
		}
	}
	if out.Images < 0 {
		out.Images = 0
	}
	out.Text = strings.TrimSpace(b.String())
	out.Authors = byCount(authors)
	sum := sha256.Sum256([]byte(out.Title + "\x00" + out.Created + "\x00" + out.Modified + "\x00" + out.Text +
		"\x00" + strconv.Itoa(out.Images)))
	out.Hash = hex.EncodeToString(sum[:])
	return out
}

func attr(se xml.StartElement, name string) string {
	for _, a := range se.Attr {
		if a.Name.Local == name {
			return a.Value
		}
	}
	return ""
}

// htmlText turns the HTML fragment OneNote keeps in a text run (spans, links, entities) into
// plain text.
func htmlText(s string) string {
	if !strings.ContainsAny(s, "<&") {
		return s
	}
	var b strings.Builder
	in := false
	for _, r := range s {
		switch {
		case r == '<':
			in = true
		case r == '>':
			in = false
		case !in:
			b.WriteRune(r)
		}
	}
	return html.UnescapeString(b.String())
}

func byCount(m map[string]int) []string {
	names := make([]string, 0, len(m))
	for n := range m {
		names = append(names, n)
	}
	sort.Slice(names, func(i, j int) bool {
		if m[names[i]] != m[names[j]] {
			return m[names[i]] > m[names[j]]
		}
		return names[i] < names[j]
	})
	return names
}
