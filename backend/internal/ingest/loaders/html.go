package loaders

import (
	"html"
	"regexp"
	"strings"
)

type htmlLoader struct{}

func (htmlLoader) Extensions() []string { return []string{".html", ".htm"} }

var (
	reHTMLDrop  = regexp.MustCompile("(?is)<script\\b[^>]*>.*?</\\s*script\\s*>|<style\\b[^>]*>.*?</\\s*style\\s*>")
	reHTMLBreak = regexp.MustCompile("(?i)</?(?:p|div|br|li|tr|h[1-6]|table|ul|ol|section|article|header|footer|blockquote|pre)\\b[^>]*>")
	reHTMLTag   = regexp.MustCompile("(?s)<[^>]*>")
)

func (htmlLoader) Load(name string, data []byte) (*Result, error) {
	s := reHTMLDrop.ReplaceAllString(string(data), " ")
	s = reHTMLBreak.ReplaceAllString(s, "\n")
	s = reHTMLTag.ReplaceAllString(s, "")
	s = html.UnescapeString(s)

	lines := strings.Split(s, "\n")
	out := make([]string, 0, len(lines))
	for _, ln := range lines {
		ln = strings.Join(strings.Fields(ln), " ")
		if ln != "" {
			out = append(out, ln)
		}
	}
	return &Result{Parts: []Part{{
		Kind:    kindText,
		Text:    strings.Join(out, "\n"),
		Locator: "全文",
	}}}, nil
}
