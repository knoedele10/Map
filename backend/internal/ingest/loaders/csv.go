package loaders

import (
	"bytes"
	"encoding/csv"
	"fmt"
	"path/filepath"
	"strings"
)

type csvLoader struct{}

func (csvLoader) Extensions() []string { return []string{".csv", ".tsv"} }

func (csvLoader) Load(name string, data []byte) (*Result, error) {
	data = bytes.TrimPrefix(data, []byte{0xEF, 0xBB, 0xBF})
	ext := strings.ToLower(filepath.Ext(name))
	delim := ','
	if ext == ".tsv" {
		delim = '\t'
	} else if commas, tabs := countDelims(firstLine(data)); commas == 0 && tabs > 0 {
		delim = '\t'
	}

	r := csv.NewReader(bytes.NewReader(data))
	r.Comma = delim
	r.FieldsPerRecord = -1
	r.LazyQuotes = true
	records, err := r.ReadAll()
	if err != nil {
		return nil, fmt.Errorf("csv: parse %s: %w", name, err)
	}

	total := len(records)
	if total > maxTableRows {
		records = records[:maxTableRows]
	}

	var sb strings.Builder
	rows := 0
	for _, rec := range records {
		if allEmpty(rec) {
			continue
		}
		if rows > 0 {
			sb.WriteByte('\n')
		}
		sb.WriteString(strings.Join(rec, " | "))
		rows++
	}
	if total > maxTableRows {
		sb.WriteString(fmt.Sprintf("\n（已截断，共 %d 行）", total))
	}
	return &Result{Parts: []Part{{
		Kind:    kindTable,
		Text:    sb.String(),
		Locator: "CSV 全表",
	}}}, nil
}

func firstLine(data []byte) []byte {
	if i := bytes.IndexByte(data, '\n'); i >= 0 {
		return data[:i]
	}
	return data
}

func countDelims(line []byte) (commas, tabs int) {
	for _, b := range line {
		switch b {
		case ',':
			commas++
		case '\t':
			tabs++
		}
	}
	return commas, tabs
}

func allEmpty(rec []string) bool {
	for _, f := range rec {
		if strings.TrimSpace(f) != "" {
			return false
		}
	}
	return true
}
