package loaders

import (
	"archive/zip"
	"bytes"
	"encoding/base64"
	"fmt"
	"strings"
	"testing"

	"golang.org/x/text/encoding/simplifiedchinese"
	"golang.org/x/text/transform"
)

// 1x1 transparent PNG. Real bytes so http.DetectContentType can sniff it.
const tinyPNGBase64 = "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mP8z8BQDwAEhQGAhKmMIQAAAABJRU5ErkJggg=="

type zipEntry struct {
	name string
	data []byte
}

func tinyPNG(t *testing.T) []byte {
	t.Helper()
	b, err := base64.StdEncoding.DecodeString(tinyPNGBase64)
	if err != nil {
		t.Fatalf("decode tiny png: %v", err)
	}
	if len(b) == 0 {
		t.Fatal("tiny png is empty")
	}
	return b
}

func buildZip(t *testing.T, files []zipEntry) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, f := range files {
		w, err := zw.Create(f.name)
		if err != nil {
			t.Fatalf("zip create %s: %v", f.name, err)
		}
		if _, err := w.Write(f.data); err != nil {
			t.Fatalf("zip write %s: %v", f.name, err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("zip close: %v", err)
	}
	return buf.Bytes()
}

func containsExt(exts []string, want string) bool {
	for _, e := range exts {
		if e == want {
			return true
		}
	}
	return false
}

func TestRegistry(t *testing.T) {
	sup := Supported()
	for _, want := range []string{".docx", ".pdf", ".png"} {
		if !containsExt(sup, want) {
			t.Fatalf("Supported() missing %s; got %v", want, sup)
		}
	}
	if _, ok := For("A.DOCX"); !ok {
		t.Fatal(`For("A.DOCX") should match (case-insensitive)`)
	}
	if _, ok := For("a.exe"); ok {
		t.Fatal(`For("a.exe") should not match`)
	}
}

func TestTextLoaderUTF8(t *testing.T) {
	l, ok := For("note.txt")
	if !ok {
		t.Fatal("no loader registered for .txt")
	}
	res, err := l.Load("note.txt", []byte("乐山大佛 71 米"))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(res.Parts) != 1 || res.Parts[0].Kind != kindText {
		t.Fatalf("parts = %+v", res.Parts)
	}
	if got := res.Parts[0].Text; !strings.Contains(got, "乐山大佛") {
		t.Fatalf("text = %q", got)
	}
}

func TestTextLoaderGB18030(t *testing.T) {
	var buf bytes.Buffer
	w := transform.NewWriter(&buf, simplifiedchinese.GB18030.NewEncoder())
	if _, err := w.Write([]byte("乐山大佛")); err != nil {
		t.Fatalf("gb18030 encode: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("gb18030 close: %v", err)
	}
	if bytes.Equal(buf.Bytes(), []byte("乐山大佛")) {
		t.Fatal("fixture is plain UTF-8; GB18030 path not exercised")
	}
	res, err := (textLoader{}).Load("gb.txt", buf.Bytes())
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if got := res.Parts[0].Text; got != "乐山大佛" {
		t.Fatalf("gb18030 decode = %q", got)
	}
}

func TestCSVLoader(t *testing.T) {
	res, err := (csvLoader{}).Load("t.csv", []byte("name,height\n乐山大佛,71\n"))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(res.Parts) != 1 || res.Parts[0].Kind != kindTable {
		t.Fatalf("parts = %+v", res.Parts)
	}
	txt := res.Parts[0].Text
	if !strings.Contains(txt, "name | height") || !strings.Contains(txt, "乐山大佛 | 71") {
		t.Fatalf("csv text = %q", txt)
	}
}

func TestHTMLLoaderDropsScript(t *testing.T) {
	src := `<html><head><script>var secret = 1;</script><style>p{color:red}</style></head><body><h1>标题</h1><p>正文内容</p></body></html>`
	res, err := (htmlLoader{}).Load("p.html", []byte(src))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	txt := res.Parts[0].Text
	if strings.Contains(txt, "secret") || strings.Contains(txt, "color:red") {
		t.Fatalf("script/style not stripped: %q", txt)
	}
	if !strings.Contains(txt, "正文内容") {
		t.Fatalf("html text = %q", txt)
	}
}

func TestDocxLoader(t *testing.T) {
	docXML := `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<w:document xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main" xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships" xmlns:a="http://schemas.openxmlformats.org/drawingml/2006/main" xmlns:v="urn:schemas-microsoft-com:vml">
<w:body>
<w:p><w:r><w:t>Hello </w:t></w:r><w:r><w:t>RAG</w:t></w:r></w:p>
<w:p><w:r><w:drawing><a:blip r:embed="rId5"/></w:drawing></w:r></w:p>
<w:tbl>
<w:tr><w:tc><w:p><w:r><w:t>名称</w:t></w:r></w:p></w:tc><w:tc><w:p><w:r><w:t>高度</w:t></w:r></w:p></w:tc></w:tr>
<w:tr><w:tc><w:p><w:r><w:t>乐山大佛</w:t></w:r></w:p></w:tc><w:tc><w:p><w:r><w:t>71 米</w:t></w:r></w:p></w:tc></w:tr>
</w:tbl>
<w:p><w:r><w:t>结尾</w:t></w:r></w:p>
</w:body></w:document>`

	relsXML := `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships"><Relationship Id="rId5" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/image" Target="media/img1.png"/></Relationships>`

	data := buildZip(t, []zipEntry{
		{"word/document.xml", []byte(docXML)},
		{"word/_rels/document.xml.rels", []byte(relsXML)},
		{"word/media/img1.png", tinyPNG(t)},
	})
	res, err := (docxLoader{}).Load("sample.docx", data)
	if err != nil {
		t.Fatalf("load: %v", err)
	}

	var texts, tables []string
	images := 0
	for _, p := range res.Parts {
		switch p.Kind {
		case kindText:
			texts = append(texts, p.Text)
		case kindTable:
			tables = append(tables, p.Text)
		case kindImage:
			images++
		}
	}
	if len(texts) != 2 || texts[0] != "Hello RAG" || texts[1] != "结尾" {
		t.Fatalf("texts = %q", texts)
	}
	if len(tables) != 1 {
		t.Fatalf("tables = %q", tables)
	}
	if !strings.Contains(tables[0], "名称 | 高度") || !strings.Contains(tables[0], "乐山大佛 | 71 米") {
		t.Fatalf("table = %q", tables[0])
	}
	if images != 1 || len(res.Assets) != 1 {
		t.Fatalf("images=%d assets=%d parts=%+v", images, len(res.Assets), res.Parts)
	}
	if res.Assets[0].Name != "img_001.png" || res.Assets[0].MIME != "image/png" {
		t.Fatalf("asset = %+v", res.Assets[0])
	}
	if len(res.Parts) < 2 || res.Parts[1].Kind != kindImage {
		t.Fatalf("image not inserted in document order: %+v", res.Parts)
	}
}

func TestXlsxLoader(t *testing.T) {
	wbXML := `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<workbook xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main" xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships"><sheets><sheet name="Sheet1" sheetId="1" r:id="rId1"/></sheets></workbook>`

	relsXML := `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships"><Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/worksheet" Target="worksheets/sheet1.xml"/></Relationships>`

	ssXML := `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<sst xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main" count="3" uniqueCount="3"><si><t>名称</t></si><si><r><t>乐山</t></r><r><t>大佛</t></r></si><si><t>高度</t></si></sst>`

	sheetXML := `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<worksheet xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main"><sheetData><row r="1"><c r="A1" t="s"><v>0</v></c><c r="B1" t="s"><v>2</v></c></row><row r="2"><c r="A2" t="s"><v>1</v></c><c r="B2"><v>71</v></c></row><row r="3"><c r="A3" t="inlineStr"><is><t>备注</t></is></c><c r="B3" t="b"><v>1</v></c></row></sheetData></worksheet>`

	data := buildZip(t, []zipEntry{
		{"xl/workbook.xml", []byte(wbXML)},
		{"xl/_rels/workbook.xml.rels", []byte(relsXML)},
		{"xl/sharedStrings.xml", []byte(ssXML)},
		{"xl/worksheets/sheet1.xml", []byte(sheetXML)},
	})
	res, err := (xlsxLoader{}).Load("sample.xlsx", data)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(res.Parts) != 1 {
		t.Fatalf("parts = %+v", res.Parts)
	}
	p := res.Parts[0]
	if p.Kind != kindTable || p.Page != 1 {
		t.Fatalf("part = %+v", p)
	}
	if p.Locator != "Sheet1!A1:B3" {
		t.Fatalf("locator = %q", p.Locator)
	}
	lines := strings.Split(p.Text, "\n")
	if len(lines) != 3 {
		t.Fatalf("lines = %q", lines)
	}
	if lines[0] != "名称 | 高度" {
		t.Fatalf("row 1 = %q", lines[0])
	}
	if lines[1] != "乐山大佛 | 71" {
		t.Fatalf("shared string runs not concatenated: row 2 = %q", lines[1])
	}
	if lines[2] != "备注 | TRUE" {
		t.Fatalf("row 3 = %q", lines[2])
	}
}

func TestImageLoader(t *testing.T) {
	png := tinyPNG(t)
	res, err := (imageLoader{}).Load("photo.png", png)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(res.Parts) != 1 || res.Parts[0].Kind != kindImage {
		t.Fatalf("parts = %+v", res.Parts)
	}
	if res.Parts[0].Text != "" {
		t.Fatalf("image part text = %q, want empty", res.Parts[0].Text)
	}
	if len(res.Assets) != 1 || res.Parts[0].Asset != res.Assets[0] {
		t.Fatalf("assets = %+v", res.Assets)
	}
	a := res.Assets[0]
	if a.Name != "img_001.png" {
		t.Fatalf("asset name = %q", a.Name)
	}
	if !strings.HasPrefix(a.MIME, "image/") {
		t.Fatalf("asset mime = %q", a.MIME)
	}
	if !bytes.Equal(a.Data, png) {
		t.Fatal("asset bytes were modified")
	}
}

func TestBadZipReturnsErrorWithoutPanic(t *testing.T) {
	cases := []struct {
		name   string
		loader Loader
	}{
		{"bad.docx", docxLoader{}},
		{"bad.xlsx", xlsxLoader{}},
	}
	for _, tc := range cases {
		res, err := tc.loader.Load(tc.name, []byte("definitely not a zip archive"))
		if err == nil {
			t.Fatalf("%s: expected error, got res=%+v", tc.name, res)
		}
	}

	truncated := append([]byte("PK\x03\x04"), bytes.Repeat([]byte{0x00}, 64)...)
	if _, err := (docxLoader{}).Load("trunc.docx", truncated); err == nil {
		t.Fatal("truncated zip: expected error")
	}
	if _, err := (xlsxLoader{}).Load("trunc.xlsx", truncated); err == nil {
		t.Fatal("truncated zip: expected error")
	}
}

// buildTestPDF assembles a minimal one-page PDF with a correct xref table so
// github.com/ledongthuc/pdf can resolve its objects.
func buildTestPDF(t *testing.T) []byte {
	t.Helper()
	var buf bytes.Buffer
	buf.WriteString("%PDF-1.4\n")

	objs := []string{
		"1 0 obj\n<< /Type /Catalog /Pages 2 0 R >>\nendobj\n",
		"2 0 obj\n<< /Type /Pages /Kids [3 0 R] /Count 1 >>\nendobj\n",
		"3 0 obj\n<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] /Resources << /Font << /F1 5 0 R >> >> /Contents 4 0 R >>\nendobj\n",
	}
	content := "BT /F1 24 Tf 100 700 Td (Hello RAG) Tj ET"
	objs = append(objs,
		fmt.Sprintf("4 0 obj\n<< /Length %d >>\nstream\n%s\nendstream\nendobj\n", len(content), content),
		"5 0 obj\n<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>\nendobj\n",
	)

	offsets := make([]int, len(objs)+1)
	for i, o := range objs {
		offsets[i+1] = buf.Len()
		buf.WriteString(o)
	}
	xrefPos := buf.Len()
	buf.WriteString("xref\n0 6\n")
	buf.WriteString("0000000000 65535 f \n")
	for i := 1; i <= len(objs); i++ {
		buf.WriteString(fmt.Sprintf("%010d 00000 n \n", offsets[i]))
	}
	buf.WriteString("trailer\n<< /Size 6 /Root 1 0 R >>\n")
	buf.WriteString(fmt.Sprintf("startxref\n%d\n%%%%EOF\n", xrefPos))
	return buf.Bytes()
}

func TestPDFLoaderMinimal(t *testing.T) {
	data := buildTestPDF(t)
	res, err := (pdfLoader{}).Load("hello.pdf", data)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	found := false
	for _, p := range res.Parts {
		if p.Kind == kindText && strings.Contains(p.Text, "Hello") {
			found = true
			if p.Page != 1 || p.Locator != "第 1 页" {
				t.Fatalf("page metadata = %+v", p)
			}
		}
	}
	if !found {
		t.Fatalf("no text part containing Hello; parts = %+v", res.Parts)
	}
}

func TestPDFLoaderCorruptDoesNotPanic(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("pdf loader panicked: %v", r)
		}
	}()
	res, err := (pdfLoader{}).Load("corrupt.pdf", []byte("%PDF-1.4\nthis is not really a pdf\n%%EOF\n"))
	if err == nil && (res == nil || len(res.Parts) == 0) {
		t.Fatalf("corrupt pdf: expected an error or a partial result, got res=%+v err=%v", res, err)
	}
}

func TestXlsxTruncation(t *testing.T) {
	var sb strings.Builder
	sb.WriteString(`<?xml version="1.0" encoding="UTF-8" standalone="yes"?><worksheet xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main"><sheetData>`)
	total := maxTableRows + 25
	for i := 1; i <= total; i++ {
		fmt.Fprintf(&sb, `<row r="%d"><c r="A%d"><v>%d</v></c></row>`, i, i, i)
	}
	sb.WriteString(`</sheetData></worksheet>`)

	wbXML := `<?xml version="1.0" encoding="UTF-8" standalone="yes"?><workbook xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main" xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships"><sheets><sheet name="S" sheetId="1" r:id="rId1"/></sheets></workbook>`
	relsXML := `<?xml version="1.0" encoding="UTF-8" standalone="yes"?><Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships"><Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/worksheet" Target="worksheets/sheet1.xml"/></Relationships>`

	data := buildZip(t, []zipEntry{
		{"xl/workbook.xml", []byte(wbXML)},
		{"xl/_rels/workbook.xml.rels", []byte(relsXML)},
		{"xl/worksheets/sheet1.xml", []byte(sb.String())},
	})
	res, err := (xlsxLoader{}).Load("big.xlsx", data)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(res.Parts) != 1 {
		t.Fatalf("parts = %d", len(res.Parts))
	}
	lines := strings.Split(res.Parts[0].Text, "\n")
	if len(lines) != maxTableRows+1 {
		t.Fatalf("emitted %d lines, want %d data rows + 1 note", len(lines), maxTableRows)
	}
	wantNote := fmt.Sprintf("（已截断，共 %d 行）", total)
	if lines[len(lines)-1] != wantNote {
		t.Fatalf("truncation note = %q, want %q", lines[len(lines)-1], wantNote)
	}
}

func TestDocxLoaderVMLImage(t *testing.T) {
	docXML := `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<w:document xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main" xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships" xmlns:v="urn:schemas-microsoft-com:vml"><w:body><w:p><w:r><w:t>前</w:t></w:r><w:r><w:pict><v:shape><v:imagedata r:id="rId7"/></v:shape></w:pict></w:r><w:r><w:t>后</w:t></w:r></w:p></w:body></w:document>`
	relsXML := `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships"><Relationship Id="rId7" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/image" Target="media/img2.png"/></Relationships>`
	data := buildZip(t, []zipEntry{
		{"word/document.xml", []byte(docXML)},
		{"word/_rels/document.xml.rels", []byte(relsXML)},
		{"word/media/img2.png", tinyPNG(t)},
	})
	res, err := (docxLoader{}).Load("vml.docx", data)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(res.Assets) != 1 || res.Assets[0].Name != "img_001.png" {
		t.Fatalf("assets = %+v", res.Assets)
	}
	if len(res.Parts) != 2 || res.Parts[0].Kind != kindText || res.Parts[1].Kind != kindImage {
		t.Fatalf("parts = %+v", res.Parts)
	}
	if res.Parts[0].Text != "前后" {
		t.Fatalf("paragraph text = %q", res.Parts[0].Text)
	}
}
