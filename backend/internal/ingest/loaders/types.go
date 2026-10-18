// Package loaders turns raw document bytes into an ordered list of text,
// table and image parts, without touching the filesystem.
package loaders

// Asset is a binary resource (an image) extracted from a document. A vision
// stage later fills in a caption for it.
type Asset struct {
	Name string            // suggested file name, e.g. "img_001.png"
	Data []byte            // raw bytes
	MIME string            // e.g. "image/png"
	Meta map[string]string // optional: page / origin ...
}

// Part is one ordered fragment of a parsed document.
type Part struct {
	Kind    string // "text" | "table" | "image"
	Text    string // text/table content; placeholder caption for images
	Asset   *Asset // non-nil only when Kind == "image"
	Page    int    // 1-based page / sheet number, 0 when not applicable
	Locator string // human readable position, e.g. "第 3 页", "Sheet1!A1:D20"
}

// Result is the outcome of loading one document.
type Result struct {
	Parts  []Part
	Assets []*Asset // every Asset pointer that appears in Parts
}

// Loader parses one family of file extensions.
type Loader interface {
	Extensions() []string
	Load(name string, data []byte) (*Result, error)
}

const (
	kindText  = "text"
	kindTable = "table"
	kindImage = "image"
)

const (
	// maxEntrySize caps one decompressed zip entry.
	maxEntrySize = 64 << 20
	// maxAssetBytes caps the sum of all extracted assets.
	maxAssetBytes = 64 << 20
	// maxTableRows caps rows emitted for a single sheet / CSV file.
	maxTableRows = 5000
)
