package loaders

import (
	"archive/zip"
	"bytes"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"strings"
)

// docxLoader parses Office Open XML word processing documents (.docx).
//
// It deliberately uses only archive/zip + encoding/xml: a .docx is a zip whose
// word/document.xml holds the body. Paragraph text comes from every w:t run in
// a w:p (concatenated), tables become one "table" part per w:tbl, and images
// referenced from a:blip / v:imagedata become "image" parts in document order.
type docxLoader struct{}

func (docxLoader) Extensions() []string { return []string{".docx"} }

func (docxLoader) Load(name string, data []byte) (res *Result, err error) {
	defer func() {
		if r := recover(); r != nil {
			res = nil
			err = fmt.Errorf("docx: parse %s: %v", name, r)
		}
	}()

	zr, zerr := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if zerr != nil {
		return nil, fmt.Errorf("docx: open zip %s: %w", name, zerr)
	}
	docFile := zipFileByName(zr, "word/document.xml")
	if docFile == nil {
		return nil, fmt.Errorf("docx: %s: missing word/document.xml", name)
	}
	docXML, rerr := readZipEntry(docFile)
	if rerr != nil {
		return nil, fmt.Errorf("docx: read word/document.xml: %w", rerr)
	}

	// r:id -> "word/media/xxx.png", plus the order in which the rels appear.
	rels, relOrder := readOPCRels(zr, "word/document.xml")

	res = &Result{}
	imgSeq := 0
	assetBytes := 0
	var limitErr error

	appendImage := func(relID string, appended bool) {
		if limitErr != nil {
			return
		}
		target, ok := rels[relID]
		if !ok || target == "" {
			return
		}
		f := zipFileByName(zr, target)
		if f == nil {
			return
		}
		b, rerr := readZipEntry(f)
		if rerr != nil {
			limitErr = fmt.Errorf("docx: read %s: %w", target, rerr)
			return
		}
		if assetBytes+len(b) > maxAssetBytes {
			limitErr = fmt.Errorf("docx: assets exceed %d bytes", maxAssetBytes)
			return
		}
		assetBytes += len(b)
		imgSeq++
		ext := strings.ToLower(path.Ext(target))
		if ext == "" {
			ext = ".bin"
		}
		meta := map[string]string{"source": target}
		if appended {
			meta["order"] = "appended"
		}
		a := &Asset{
			Name: fmt.Sprintf("img_%03d%s", imgSeq, ext),
			Data: b,
			MIME: sniffAssetMIME(b, ext),
			Meta: meta,
		}
		res.Parts = append(res.Parts, Part{Kind: kindImage, Asset: a, Locator: "图片"})
		res.Assets = append(res.Assets, a)
	}

	var (
		tableDepth  int
		inText      bool
		inParagraph bool
		inCell      bool
		paraText    strings.Builder
		paraImages  []string
		cellText    strings.Builder
		rowCells    []string
		tableRows   []string
		tableImages []string
		looseImages []string
	)

	addImageRef := func(id string) {
		switch {
		case tableDepth > 0:
			tableImages = append(tableImages, id)
		case inParagraph:
			paraImages = append(paraImages, id)
		default:
			looseImages = append(looseImages, id)
		}
	}

	flushParagraph := func() {
		if !inParagraph {
			return
		}
		if txt := strings.TrimSpace(paraText.String()); txt != "" {
			res.Parts = append(res.Parts, Part{Kind: kindText, Text: txt, Locator: "段落"})
		}
		for _, id := range paraImages {
			appendImage(id, false)
		}
		paraText.Reset()
		paraImages = nil
		inParagraph = false
	}

	flushTable := func() {
		if len(tableRows) > 0 {
			res.Parts = append(res.Parts, Part{
				Kind:    kindTable,
				Text:    strings.Join(tableRows, "\n"),
				Locator: "表格",
			})
		}
		for _, id := range tableImages {
			appendImage(id, false)
		}
		tableRows = nil
		tableImages = nil
		rowCells = nil
	}

	dec := xml.NewDecoder(bytes.NewReader(docXML))
	dec.Strict = false // tolerate undeclared entities / stray markup
	for {
		tok, terr := dec.Token()
		if terr == io.EOF {
			break
		}
		if terr != nil {
			// Keep whatever was parsed; report only when nothing usable came out.
			if len(res.Parts) == 0 {
				return nil, fmt.Errorf("docx: parse document.xml: %w", terr)
			}
			break
		}
		switch t := tok.(type) {
		case xml.StartElement:
			switch t.Name.Local {
			case "tbl":
				tableDepth++
				tableRows = nil
				tableImages = nil
				rowCells = nil
			case "tr":
				if tableDepth > 0 {
					rowCells = nil
				}
			case "tc":
				if tableDepth > 0 {
					cellText.Reset()
					inCell = true
				}
			case "p":
				if tableDepth == 0 {
					inParagraph = true
					paraText.Reset()
					paraImages = nil
				}
			case "t":
				inText = true
			case "blip":
				if id := attrLocal(t, "embed"); id != "" {
					addImageRef(id)
				}
			case "imagedata":
				if id := attrLocal(t, "id"); id != "" {
					addImageRef(id)
				}
			}
		case xml.CharData:
			if inText {
				switch {
				case tableDepth > 0 && inCell:
					cellText.Write(t)
				case inParagraph:
					paraText.Write(t)
				}
			}
		case xml.EndElement:
			switch t.Name.Local {
			case "t":
				inText = false
			case "tc":
				if tableDepth > 0 {
					rowCells = append(rowCells, strings.TrimSpace(cellText.String()))
					cellText.Reset()
					inCell = false
				}
			case "tr":
				if tableDepth > 0 && !allEmpty(rowCells) {
					tableRows = append(tableRows, strings.Join(rowCells, " | "))
				}
				rowCells = nil
			case "tbl":
				if tableDepth > 0 {
					tableDepth--
				}
				if tableDepth == 0 {
					flushTable()
				}
			case "p":
				if tableDepth == 0 {
					flushParagraph()
				}
			}
		}
	}
	flushParagraph()
	for _, id := range looseImages {
		appendImage(id, false)
	}

	// Fallback: no drawing reference was found in document.xml, but the
	// relationship file points at media. Append those in rels order.
	if imgSeq == 0 {
		for _, id := range relOrder {
			target := rels[id]
			if target == "" || !strings.Contains(target, "/media/") {
				continue
			}
			appendImage(id, true)
		}
	}

	if limitErr != nil {
		return res, limitErr
	}
	return res, nil
}

// attrLocal returns the value of the first attribute with the given local name.
// Namespaces are ignored on purpose: docx prefixes vary but local names do not.
func attrLocal(el xml.StartElement, local string) string {
	for _, a := range el.Attr {
		if a.Name.Local == local {
			return a.Value
		}
	}
	return ""
}

// zipFileByName returns the zip entry with the exact (slash-normalised) name.
func zipFileByName(zr *zip.Reader, name string) *zip.File {
	want := strings.ReplaceAll(name, "\\", "/")
	for _, f := range zr.File {
		if strings.ReplaceAll(f.Name, "\\", "/") == want {
			return f
		}
	}
	return nil
}

// readZipEntry reads one decompressed entry, enforcing maxEntrySize.
func readZipEntry(f *zip.File) ([]byte, error) {
	if f == nil {
		return nil, fmt.Errorf("loaders: nil zip entry")
	}
	if f.UncompressedSize64 > maxEntrySize {
		return nil, fmt.Errorf("loaders: zip entry %s too large (%d bytes)", f.Name, f.UncompressedSize64)
	}
	rc, err := f.Open()
	if err != nil {
		return nil, fmt.Errorf("loaders: open %s: %w", f.Name, err)
	}
	defer rc.Close()
	b, err := io.ReadAll(io.LimitReader(rc, maxEntrySize+1))
	if err != nil {
		return nil, fmt.Errorf("loaders: read %s: %w", f.Name, err)
	}
	if len(b) > maxEntrySize {
		return nil, fmt.Errorf("loaders: zip entry %s exceeds %d bytes", f.Name, maxEntrySize)
	}
	return b, nil
}

// opcRelationship is one <Relationship> from an OPC .rels part.
type opcRelationship struct {
	ID     string `xml:"Id,attr"`
	Type   string `xml:"Type,attr"`
	Target string `xml:"Target,attr"`
	Mode   string `xml:"TargetMode,attr"`
}

type opcRelationships struct {
	Rels []opcRelationship `xml:"Relationship"`
}

// readOPCRels reads the .rels part belonging to partPath (e.g.
// "word/document.xml" -> "word/_rels/document.xml.rels") and returns a
// relationship-id -> zip-path map plus the ids in file order.
func readOPCRels(zr *zip.Reader, partPath string) (map[string]string, []string) {
	out := map[string]string{}
	dir := path.Dir(partPath)
	relPath := path.Join(dir, "_rels", path.Base(partPath)+".rels")
	f := zipFileByName(zr, relPath)
	if f == nil {
		return out, nil
	}
	b, err := readZipEntry(f)
	if err != nil {
		return out, nil
	}
	var rels opcRelationships
	if err := xml.Unmarshal(b, &rels); err != nil {
		return out, nil
	}
	order := make([]string, 0, len(rels.Rels))
	for _, r := range rels.Rels {
		if r.ID == "" || r.Mode == "External" {
			continue
		}
		out[r.ID] = resolveZipPath(dir, r.Target)
		order = append(order, r.ID)
	}
	return out, order
}

// resolveZipPath resolves an OPC relationship target against the directory of
// the source part, returning a zip-entry name (forward slashes, cleaned).
func resolveZipPath(baseDir, target string) string {
	t := strings.TrimSpace(target)
	if t == "" {
		return ""
	}
	if u, err := url.PathUnescape(t); err == nil {
		t = u
	}
	if strings.HasPrefix(t, "/") {
		return path.Clean(strings.TrimPrefix(t, "/"))
	}
	return path.Clean(path.Join(baseDir, t))
}

// sniffAssetMIME sniffs binary content, falling back to the file extension
// when http.DetectContentType cannot identify an image.
func sniffAssetMIME(data []byte, ext string) string {
	if len(data) > 0 {
		if m := http.DetectContentType(data); strings.HasPrefix(m, "image/") {
			return m
		}
	}
	return mimeByExt(ext)
}

func mimeByExt(ext string) string {
	switch strings.ToLower(ext) {
	case ".png":
		return "image/png"
	case ".jpg", ".jpeg", ".jpe":
		return "image/jpeg"
	case ".gif":
		return "image/gif"
	case ".webp":
		return "image/webp"
	case ".bmp":
		return "image/bmp"
	case ".tif", ".tiff":
		return "image/tiff"
	case ".svg":
		return "image/svg+xml"
	case ".emf":
		return "image/emf"
	case ".wmf":
		return "image/wmf"
	default:
		return "application/octet-stream"
	}
}
