package loaders

import (
	"bytes"
	"fmt"
	"strings"

	"github.com/ledongthuc/pdf"
)

// pdfLoader extracts the plain text of each page. The upstream library panics
// on some malformed files, so every entry point is guarded by recover and a
// partial Result is returned together with the error.
type pdfLoader struct{}

func (pdfLoader) Extensions() []string { return []string{".pdf"} }

func (pdfLoader) Load(name string, data []byte) (res *Result, err error) {
	res = &Result{}
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("pdf: %s: recovered from panic: %v", name, r)
		}
	}()

	r, oerr := pdf.NewReader(bytes.NewReader(data), int64(len(data)))
	if oerr != nil {
		return nil, fmt.Errorf("pdf: open %s: %w", name, oerr)
	}
	pages := r.NumPage()
	if pages < 0 {
		pages = 0
	}
	var firstErr error
	for i := 1; i <= pages; i++ {
		if perr := loadPDFPage(res, r, i); perr != nil && firstErr == nil {
			firstErr = perr
		}
	}
	if firstErr != nil {
		return res, fmt.Errorf("pdf: %s: %w", name, firstErr)
	}
	return res, nil
}

// loadPDFPage parses page i in isolation so that one bad page cannot abort the
// rest of the document.
func loadPDFPage(res *Result, r *pdf.Reader, i int) (err error) {
	defer func() {
		if rec := recover(); rec != nil {
			err = fmt.Errorf("page %d: recovered from panic: %v", i, rec)
		}
	}()
	p := r.Page(i)
	if p.V.IsNull() {
		return nil
	}
	txt, gerr := p.GetPlainText(nil)
	if gerr != nil {
		return fmt.Errorf("page %d: %w", i, gerr)
	}
	txt = strings.TrimSpace(txt)
	if txt == "" {
		return nil
	}
	res.Parts = append(res.Parts, Part{
		Kind:    kindText,
		Text:    txt,
		Page:    i,
		Locator: fmt.Sprintf("第 %d 页", i),
	})
	return nil
}
