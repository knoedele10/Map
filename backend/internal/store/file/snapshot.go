package file

import (
	"compress/gzip"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"ragkb/internal/types"
)

// One atomic snapshot keeps documents, chunks and vectors in the same generation.
// Existing three-file indexes remain readable and are upgraded on the next Save.
type indexSnapshot struct {
	Version int                  `json:"version"`
	Dim     int                  `json:"dim"`
	Records []types.Chunk        `json:"chunks"`
	Vectors []float32            `json:"vectors"`
	Docs    map[string]types.Doc `json:"docs"`
}

func (s *Store) loadSnapshot() (bool, error) {
	f, err := os.Open(filepath.Join(s.dir, "index.snapshot.gz"))
	if os.IsNotExist(err) {
		return false, nil
	}
	if err != nil {
		return true, err
	}
	defer f.Close()
	reader, err := gzip.NewReader(f)
	if err != nil {
		return true, err
	}
	defer reader.Close()
	var snap indexSnapshot
	if err := json.NewDecoder(reader).Decode(&snap); err != nil {
		return true, fmt.Errorf("file: 索引快照损坏: %w", err)
	}
	if snap.Version != 1 || snap.Dim != s.dim || len(snap.Vectors) != len(snap.Records)*s.dim {
		return true, fmt.Errorf("file: 索引快照版本或维度不匹配，请检查配置")
	}
	s.records, s.vectors = snap.Records, snap.Vectors
	if snap.Docs != nil {
		s.docs = snap.Docs
	}
	s.reindex()
	return true, nil
}

func (s *Store) saveSnapshotLocked() error {
	if err := os.MkdirAll(s.dir, 0755); err != nil {
		return err
	}
	f, err := os.CreateTemp(s.dir, ".index-snapshot-*")
	if err != nil {
		return err
	}
	name := f.Name()
	defer os.Remove(name)
	writer := gzip.NewWriter(f)
	err = json.NewEncoder(writer).Encode(indexSnapshot{Version: 1, Dim: s.dim, Records: s.records, Vectors: s.vectors, Docs: s.docs})
	if closeErr := writer.Close(); err == nil {
		err = closeErr
	}
	if err == nil {
		err = f.Sync()
	}
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	return os.Rename(name, filepath.Join(s.dir, "index.snapshot.gz"))
}
