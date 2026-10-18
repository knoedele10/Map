package loaders

import (
	"bytes"
	"fmt"
	"io"
	"strings"
	"unicode/utf8"

	"golang.org/x/text/encoding/simplifiedchinese"
	"golang.org/x/text/transform"
)

type textLoader struct{}

func (textLoader) Extensions() []string {
	return []string{".txt", ".md", ".markdown", ".log", ".json", ".yaml", ".yml"}
}

func (textLoader) Load(name string, data []byte) (*Result, error) {
	return &Result{Parts: []Part{{
		Kind:    kindText,
		Text:    decodeTextBytes(data),
		Locator: "全文",
	}}}, nil
}

// decodeTextBytes decodes document bytes as UTF-8 (BOM aware), then GB18030,
// then UTF-8 with replacement characters as a last resort.
func decodeTextBytes(data []byte) string {
	if bytes.HasPrefix(data, []byte{0xEF, 0xBB, 0xBF}) {
		return string(data[3:])
	}
	if utf8.Valid(data) {
		return string(data)
	}
	if s, err := decodeGB18030(data); err == nil {
		return s
	}
	return strings.ToValidUTF8(string(data), "\uFFFD")
}

func decodeGB18030(data []byte) (string, error) {
	r := transform.NewReader(bytes.NewReader(data), simplifiedchinese.GB18030.NewDecoder())
	out, err := io.ReadAll(r)
	if err != nil {
		return "", fmt.Errorf("text: gb18030 decode: %w", err)
	}
	return string(out), nil
}
