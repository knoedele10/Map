package loaders

import (
	"fmt"
	"path/filepath"
	"strings"
)

// imageLoader turns a standalone raster image into a single "image" part. The
// original bytes are preserved untouched so the caller can store them as an
// asset and a vision stage can caption them later.
type imageLoader struct{}

func (imageLoader) Extensions() []string {
	return []string{".png", ".jpg", ".jpeg", ".webp", ".gif", ".bmp", ".tif", ".tiff"}
}

func (imageLoader) Load(name string, data []byte) (*Result, error) {
	if len(data) == 0 {
		return nil, fmt.Errorf("image: %s: empty file", name)
	}
	if len(data) > maxAssetBytes {
		return nil, fmt.Errorf("image: %s: %d bytes exceeds %d", name, len(data), maxAssetBytes)
	}
	ext := strings.ToLower(filepath.Ext(name))
	if ext == "" {
		ext = ".bin"
	}
	a := &Asset{
		Name: "img_001" + ext,
		Data: data,
		MIME: sniffAssetMIME(data, ext),
	}
	return &Result{
		Parts:  []Part{{Kind: kindImage, Asset: a, Locator: "原图"}},
		Assets: []*Asset{a},
	}, nil
}
