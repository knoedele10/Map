package loaders

import "testing"

func TestSparseWorksheetPreservesColumnGaps(t *testing.T) {
	data := []byte(`<worksheet><sheetData><row r="1"><c r="A1" t="inlineStr"><is><t>姓名</t></is></c><c r="C1" t="inlineStr"><is><t>城市</t></is></c></row><row r="2"><c r="C2" t="inlineStr"><is><t>成都</t></is></c></row></sheetData></worksheet>`)
	rows, _, _, err := parseSheet(data, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 || rows[0] != "姓名 |  | 城市" || rows[1] != " |  | 成都" {
		t.Fatalf("lost spreadsheet column positions: %#v", rows)
	}
}
