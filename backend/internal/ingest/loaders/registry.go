package loaders

import (
	"path/filepath"
	"sort"
	"strings"
)

// registry maps a lower-case, dot-prefixed extension to its loader.
var registry = map[string]Loader{}

func register(l Loader) {
	for _, ext := range l.Extensions() {
		registry[strings.ToLower(ext)] = l
	}
}

func init() {
	register(textLoader{})
	register(docxLoader{})
	register(xlsxLoader{})
	register(pdfLoader{})
	register(htmlLoader{})
	register(csvLoader{})
	register(imageLoader{})
}

// For returns the loader for a file name's extension (case-insensitive).
func For(name string) (Loader, bool) {
	ext := strings.ToLower(filepath.Ext(name))
	if ext == "" {
		return nil, false
	}
	l, ok := registry[ext]
	return l, ok
}

// Supported returns every known extension (dot-prefixed, lower-case, sorted).
func Supported() []string {
	out := make([]string, 0, len(registry))
	for ext := range registry {
		out = append(out, ext)
	}
	sort.Strings(out)
	return out
}
