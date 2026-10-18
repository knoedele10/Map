package ingest

import (
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// FileEntry 记录一个已入库文件的状态，用于增量更新。
type FileEntry struct {
	Fingerprint string            `json:"fingerprint"`
	DocID       string            `json:"doc_id"`
	Chunks      int               `json:"chunks"`
	Size        int64             `json:"size"`
	Assets      []string          `json:"assets,omitempty"`
	Meta        map[string]string `json:"meta,omitempty"`
	UpdatedAt   time.Time         `json:"updated_at"`
}

// Manifest 是 docs 目录的入库台账：内容没变就不重复向量化（省时间也省钱）。
type Manifest struct {
	Files map[string]FileEntry `json:"files"`
}

// LoadManifest 读取台账；文件不存在时返回空台账。
func LoadManifest(path string) (*Manifest, error) {
	m := &Manifest{Files: map[string]FileEntry{}}
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return m, nil
		}
		return nil, fmt.Errorf("读取 manifest %s: %w", path, err)
	}
	if err := json.Unmarshal(raw, m); err != nil {
		return nil, fmt.Errorf("解析 manifest %s: %w", path, err)
	}
	if m.Files == nil {
		m.Files = map[string]FileEntry{}
	}
	return m, nil
}

// Save 原子写入台账。
func (m *Manifest) Save(path string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o644); err != nil {
		return fmt.Errorf("写入 manifest %s: %w", tmp, err)
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("替换 manifest %s: %w", path, err)
	}
	return nil
}

// StableID 由输入内容派生一个稳定的短 ID。
//
// 为什么不用随机 ID：文档 ID 稳定，重新入库时才能覆盖旧记录而不是每次产生新文档；
// 也让"同一份文件在不同机器上得到同一个 doc_id"成立，便于多端同步排查。
func StableID(parts ...string) string {
	h := sha1.New()
	for _, p := range parts {
		h.Write([]byte(p))
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))[:16]
}

// ContentHash 是切块内容的指纹，用于幂等去重。
func ContentHash(text string) string {
	h := sha1.New()
	h.Write([]byte(strings.TrimSpace(text)))
	return hex.EncodeToString(h.Sum(nil))[:16]
}

// FileFingerprint 是文件内容的指纹（增量更新的依据）。
func FileFingerprint(data []byte) string {
	h := sha1.New()
	h.Write(data)
	return hex.EncodeToString(h.Sum(nil))[:16]
}
