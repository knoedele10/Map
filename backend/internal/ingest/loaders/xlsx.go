package loaders

import (
	"archive/zip"
	"bytes"
	"encoding/xml"
	"fmt"
	"io"
	"strconv"
	"strings"
)

// xlsxLoader parses Office Open XML spreadsheets (.xlsx) with archive/zip +
// encoding/xml only. Each worksheet becomes one "table" part: cells joined by
// " | " inside a row, rows joined by "\n".
type xlsxLoader struct{}

func (xlsxLoader) Extensions() []string { return []string{".xlsx"} }

// xlsxSheet is one <sheet> entry of xl/workbook.xml.
type xlsxSheet struct {
	Name string
	RID  string // r:id, used to find the worksheet part through the rels file
}

func (xlsxLoader) Load(name string, data []byte) (res *Result, err error) {
	defer func() {
		if r := recover(); r != nil {
			res = nil
			err = fmt.Errorf("xlsx: parse %s: %v", name, r)
		}
	}()

	zr, zerr := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if zerr != nil {
		return nil, fmt.Errorf("xlsx: open zip %s: %w", name, zerr)
	}
	wbFile := zipFileByName(zr, "xl/workbook.xml")
	if wbFile == nil {
		return nil, fmt.Errorf("xlsx: %s: missing xl/workbook.xml", name)
	}
	wbData, rerr := readZipEntry(wbFile)
	if rerr != nil {
		return nil, fmt.Errorf("xlsx: read xl/workbook.xml: %w", rerr)
	}
	sheets, serr := parseWorkbookSheets(wbData)
	if serr != nil {
		return nil, fmt.Errorf("xlsx: parse xl/workbook.xml: %w", serr)
	}

	rels, _ := readOPCRels(zr, "xl/workbook.xml")
	shared := parseSharedStrings(zr)

	res = &Result{}
	for idx, sh := range sheets {
		target := ""
		if sh.RID != "" {
			target = rels[sh.RID]
		}
		if target == "" {
			target = fmt.Sprintf("xl/worksheets/sheet%d.xml", idx+1)
		}
		f := zipFileByName(zr, target)
		if f == nil {
			continue
		}
		sheetData, e := readZipEntry(f)
		if e != nil {
			return res, fmt.Errorf("xlsx: read %s: %w", target, e)
		}
		rows, total, rng, perr := parseSheet(sheetData, shared)
		if perr != nil {
			return res, fmt.Errorf("xlsx: parse %s: %w", target, perr)
		}
		if len(rows) == 0 {
			continue
		}
		sheetName := sh.Name
		if sheetName == "" {
			sheetName = fmt.Sprintf("Sheet%d", idx+1)
		}
		text := strings.Join(rows, "\n")
		if total > maxTableRows {
			text += fmt.Sprintf("\n（已截断，共 %d 行）", total)
		}
		loc := sheetName
		if rng != "" {
			loc = sheetName + "!" + rng
		}
		res.Parts = append(res.Parts, Part{
			Kind:    kindTable,
			Text:    text,
			Page:    idx + 1,
			Locator: loc,
		})
	}
	return res, nil
}

// parseWorkbookSheets extracts the ordered <sheet> list from xl/workbook.xml.
func parseWorkbookSheets(data []byte) ([]xlsxSheet, error) {
	dec := xml.NewDecoder(bytes.NewReader(data))
	dec.Strict = false
	var out []xlsxSheet
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return out, err
		}
		se, ok := tok.(xml.StartElement)
		if !ok || se.Name.Local != "sheet" {
			continue
		}
		var sh xlsxSheet
		for _, a := range se.Attr {
			switch a.Name.Local {
			case "name":
				sh.Name = a.Value
			case "id": // r:id
				sh.RID = a.Value
			}
		}
		out = append(out, sh)
	}
	return out, nil
}

// parseSharedStrings reads xl/sharedStrings.xml. A single <si> may be built
// from several <r><t> runs and every run must be concatenated; phonetic runs
// (<rPh>) are ignored.
func parseSharedStrings(zr *zip.Reader) []string {
	f := zipFileByName(zr, "xl/sharedStrings.xml")
	if f == nil {
		return nil
	}
	b, err := readZipEntry(f)
	if err != nil {
		return nil
	}
	dec := xml.NewDecoder(bytes.NewReader(b))
	dec.Strict = false

	var (
		out  []string
		sb   strings.Builder
		inSI bool
		inT  bool
		rph  int
	)
	for {
		tok, err := dec.Token()
		if err != nil {
			break
		}
		switch t := tok.(type) {
		case xml.StartElement:
			switch t.Name.Local {
			case "si":
				inSI = true
				sb.Reset()
			case "rPh":
				if inSI {
					rph++
				}
			case "t":
				if inSI && rph == 0 {
					inT = true
				}
			}
		case xml.CharData:
			if inSI && inT {
				sb.Write(t)
			}
		case xml.EndElement:
			switch t.Name.Local {
			case "t":
				inT = false
			case "rPh":
				if rph > 0 {
					rph--
				}
			case "si":
				out = append(out, sb.String())
				inSI = false
			}
		}
	}
	return out
}

// parseSheet walks one worksheet part and returns the non-empty rows as text,
// the total non-empty row count before truncation, and the used range
// (e.g. "A1:B3").
func parseSheet(data []byte, shared []string) (rows []string, total int, rng string, err error) {
	dec := xml.NewDecoder(bytes.NewReader(data))
	dec.Strict = false

	var (
		rowCells   map[int]string
		curRow     int
		lastRow    int
		minRow     int
		maxRow     int
		minCol     int
		maxCol     int
		haveRange  bool
		seqCellCol int
	)

	var (
		inCell   bool
		inV      bool
		inT      bool
		rph      int
		cellCol  int
		cellType string
		valV     strings.Builder
		valInl   strings.Builder
	)

	flushRow := func() {
		if len(rowCells) == 0 {
			rowCells = nil
			return
		}
		lastColumn := -1
		for c := range rowCells {
			if c > lastColumn {
				lastColumn = c
			}
		}
		parts := make([]string, lastColumn+1)
		nonEmpty := false
		for c, v := range rowCells {
			parts[c] = v
			if strings.TrimSpace(v) != "" {
				nonEmpty = true
			}
		}
		if nonEmpty {
			total++
			if total <= maxTableRows {
				rows = append(rows, strings.Join(parts, " | "))
			}
		}
		rowCells = nil
	}

	noteRange := func(row, col int) {
		if !haveRange {
			minRow, maxRow, minCol, maxCol = row, row, col, col
			haveRange = true
			return
		}
		if row < minRow {
			minRow = row
		}
		if row > maxRow {
			maxRow = row
		}
		if col < minCol {
			minCol = col
		}
		if col > maxCol {
			maxCol = col
		}
	}

	for {
		tok, terr := dec.Token()
		if terr == io.EOF {
			break
		}
		if terr != nil {
			if len(rows) == 0 {
				return nil, 0, "", terr
			}
			break
		}
		switch t := tok.(type) {
		case xml.StartElement:
			switch t.Name.Local {
			case "row":
				rowCells = map[int]string{}
				curRow = 0
				if v := attrLocal(t, "r"); v != "" {
					curRow, _ = strconv.Atoi(v)
				}
				if curRow <= 0 {
					curRow = lastRow + 1
				}
				seqCellCol = 0
			case "c":
				inCell = true
				cellCol = -1
				cellType = ""
				valV.Reset()
				valInl.Reset()
				inV, inT, rph = false, false, 0
				for _, a := range t.Attr {
					switch a.Name.Local {
					case "r":
						if col, _ := parseCellRef(a.Value); col >= 0 {
							cellCol = col
						}
					case "t":
						cellType = a.Value
					}
				}
			case "v":
				if inCell {
					inV = true
				}
			case "t":
				if inCell {
					inT = true
				}
			case "rPh":
				if inCell {
					rph++
				}
			}
		case xml.CharData:
			if !inCell {
				continue
			}
			switch {
			case inV:
				valV.Write(t)
			case inT && rph == 0:
				valInl.Write(t)
			}
		case xml.EndElement:
			switch t.Name.Local {
			case "v":
				inV = false
			case "t":
				inT = false
			case "rPh":
				if rph > 0 {
					rph--
				}
			case "c":
				if !inCell {
					continue
				}
				inCell = false
				val := cellValue(cellType, valV.String(), valInl.String(), shared)
				if cellCol < 0 {
					cellCol = seqCellCol
					for c := range rowCells {
						if c >= cellCol {
							cellCol = c + 1
						}
					}
				}
				seqCellCol = cellCol + 1
				rowCells[cellCol] = val
				if strings.TrimSpace(val) != "" {
					noteRange(curRow, cellCol)
				}
			case "row":
				flushRow()
				lastRow = curRow
			}
		}
	}
	flushRow()

	if haveRange {
		rng = fmt.Sprintf("%s%d:%s%d", colLetters(minCol), minRow, colLetters(maxCol), maxRow)
	}
	return rows, total, rng, nil
}

// cellValue resolves one <c> element according to its t attribute.
func cellValue(cellType, raw, inline string, shared []string) string {
	switch cellType {
	case "inlineStr":
		return inline
	case "s":
		idx, err := strconv.Atoi(strings.TrimSpace(raw))
		if err != nil || idx < 0 || idx >= len(shared) {
			return ""
		}
		return shared[idx]
	case "b":
		switch strings.TrimSpace(raw) {
		case "1":
			return "TRUE"
		case "0":
			return "FALSE"
		default:
			return strings.TrimSpace(raw)
		}
	default: // "str", "e", "n", or absent (number)
		return strings.TrimSpace(raw)
	}
}

// parseCellRef splits "B3" into a 0-based column index and a 1-based row.
func parseCellRef(ref string) (col, row int) {
	col, row = -1, 0
	if ref == "" {
		return
	}
	i := 0
	letters := 0
	acc := 0
	for ; i < len(ref); i++ {
		c := ref[i]
		if c >= 'a' && c <= 'z' {
			c -= 'a' - 'A'
		}
		if c < 'A' || c > 'Z' {
			break
		}
		acc = acc*26 + int(c-'A'+1)
		letters++
	}
	if letters == 0 {
		return -1, 0
	}
	col = acc - 1 // 1-based letters -> 0-based index
	row, _ = strconv.Atoi(ref[i:])
	return col, row
}

// colLetters converts a 0-based column index to its spreadsheet letters.
func colLetters(idx int) string {
	if idx < 0 {
		return "A"
	}
	var b [8]byte
	i := len(b)
	for idx >= 0 && i > 0 {
		i--
		b[i] = byte('A' + idx%26)
		idx = idx/26 - 1
	}
	return string(b[i:])
}
