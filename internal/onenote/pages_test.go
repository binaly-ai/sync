package onenote

import (
	"strings"
	"testing"
)

const hierarchy = `<?xml version="1.0"?>
<one:Section xmlns:one="http://schemas.microsoft.com/office/onenote/2013/onenote" name="Section" ID="{S}">
<one:Page ID="{P1}" name="Oct 6 - TSP conditioning" dateTime="2025-10-06T14:02:00.000Z" lastModifiedTime="2025-10-07T09:10:00.000Z" pageLevel="1"/>
<one:Page ID="{P2}" name="Oct 20 - XPS" dateTime="2025-10-20T10:00:00.000Z" lastModifiedTime="2025-10-20T18:00:00.000Z" pageLevel="2"/>
</one:Section>`

// The shape of a real page, as OneNote 2016 returned it for the test sections (2026-10-27),
// with the parts those samples didn't have: handwriting OneNote recognized, and a drawing it
// didn't.
const page = `<?xml version="1.0"?>
<one:Page xmlns:one="http://schemas.microsoft.com/office/onenote/2013/onenote" ID="{P1}" name="Oct 6 - TSP conditioning" dateTime="2025-10-06T14:02:00.000Z" lastModifiedTime="2025-10-07T09:10:00.000Z" pageLevel="1">
<one:Title><one:OE author="Sivaraj Velusamy" lastModifiedBy="Sivaraj Velusamy"><one:T><![CDATA[Oct 6 - TSP conditioning]]></one:T></one:OE></one:Title>
<one:Outline><one:OEChildren>
 <one:OE author="Sivaraj Velusamy" creationTime="2025-10-06T14:05:00.000Z"><one:T><![CDATA[<span style='font-weight:bold'>Base</span> pressure 2e-10 mbar &amp; stable]]></one:T></one:OE>
 <one:OE author="Oliver Monti"><one:T><![CDATA[Check the TSP filament]]></one:T></one:OE>
 <one:OE author="Sivaraj Velusamy"><one:InkWord recognizedText="degassed"/><one:InkWord/></one:OE>
 <one:OE><one:Table><one:Row><one:Cell><one:OEChildren><one:OE><one:T><![CDATA[Sample]]></one:T></one:OE></one:OEChildren></one:Cell><one:Cell><one:OEChildren><one:OE><one:T><![CDATA[]]></one:T></one:OE></one:OEChildren></one:Cell><one:Cell><one:OEChildren><one:OE><one:T><![CDATA[Ag(111)]]></one:T></one:OE></one:OEChildren></one:Cell></one:Row></one:Table></one:OE>
 <one:OE><one:InsertedFile pathSource="C:\x\run.xlsx" preferredName="run 14.xlsx"/></one:OE>
</one:OEChildren></one:Outline>
<one:Image><one:OCRData><one:OCRText><![CDATA[LEED 3.05 A]]></one:OCRText></one:OCRData></one:Image>
<one:Image/>
<one:InkDrawing/>
</one:Page>`

func TestHierarchyPagesInOrder(t *testing.T) {
	ps := hierarchyPages(hierarchy)
	if len(ps) != 2 || ps[0].ID != "{P1}" || ps[1].Name != "Oct 20 - XPS" || ps[1].Level != "2" || ps[0].DateTime != "2025-10-06T14:02:00.000Z" {
		t.Fatalf("got %+v", ps)
	}
}

func TestParsePage(t *testing.T) {
	p := ParsePage(RawPage{Name: "Oct 6 - TSP conditioning", DateTime: "2025-10-06T14:02:00.000Z", LastModified: "2025-10-07T09:10:00.000Z", XML: page})
	want := strings.Join([]string{
		"Base pressure 2e-10 mbar & stable",
		"Check the TSP filament",
		"degassed",
		"Sample | Ag(111)",
		"[Attached file: run 14.xlsx]",
		"[Picture text: LEED 3.05 A]",
	}, "\n")
	if p.Text != want {
		t.Fatalf("text:\n%s\nwant:\n%s", p.Text, want)
	}
	// One unrecognized ink word, one picture with no text, one drawing; the picture OneNote
	// read is described by its words.
	if p.Images != 3 {
		t.Fatalf("images %d, want 3", p.Images)
	}
	if len(p.Authors) != 2 || p.Authors[0] != "Sivaraj Velusamy" || p.Authors[1] != "Oliver Monti" {
		t.Fatalf("authors %v", p.Authors)
	}
	if len(p.Attachments) != 1 || p.Title != "Oct 6 - TSP conditioning" || p.Created == "" || len(p.Hash) != 64 {
		t.Fatalf("page %+v", p)
	}
	again := ParsePage(RawPage{Name: p.Title, DateTime: p.Created, LastModified: "2025-10-08T00:00:00.000Z", XML: page})
	if again.Hash == p.Hash {
		t.Fatal("a page modified later kept its hash")
	}
}

func TestParsePageNeverFails(t *testing.T) {
	if p := ParsePage(RawPage{Name: "x", XML: "<not xml"}); p.Title != "x" || p.Text != "" {
		t.Fatalf("got %+v", p)
	}
}
