package ingest

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"ragkb/internal/config"
	"ragkb/internal/store/file"
	"ragkb/internal/types"
)

type localEmbedder struct{}

func (localEmbedder) Embed(_ context.Context, texts []string) ([][]float32, error) {
	v := make([][]float32, len(texts))
	for i := range texts {
		v[i] = []float32{1, 0, 0, 0}
	}
	return v, nil
}
func (localEmbedder) Dim() int                   { return 4 }
func (localEmbedder) Model() string              { return "test" }
func (localEmbedder) Provider() string           { return "test" }
func (localEmbedder) Ping(context.Context) error { return nil }

func TestIncrementalUploadPreservesOtherDocumentsAndUnchangedChunks(t *testing.T) {
	ctx := context.Background()
	cfg := config.Default()
	root := t.TempDir()
	cfg.Storage.DocsDir = filepath.Join(root, "docs")
	cfg.Storage.DataDir = filepath.Join(root, "data")
	cfg.Storage.AssetsDir = filepath.Join(root, "assets")
	cfg.Chunking.Size, cfg.Chunking.Overlap, cfg.Chunking.MinChars = 30, 0, 1
	if err := os.MkdirAll(cfg.Storage.DocsDir, 0755); err != nil {
		t.Fatal(err)
	}
	st, err := file.Open(cfg.Storage.DataDir, 4, true)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	p, err := NewPipeline(cfg, st, localEmbedder{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	write := func(name, text string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(cfg.Storage.DocsDir, name), []byte(text), 0644); err != nil {
			t.Fatal(err)
		}
	}
	run := func(opts RunOptions) {
		t.Helper()
		stats, err := p.Run(ctx, opts, nil)
		if err != nil || stats.FilesFailed != 0 {
			t.Fatalf("stats=%+v err=%v", stats, err)
		}
	}
	write("a.txt", strings.Repeat("甲", 30)+strings.Repeat("乙", 30))
	write("b.txt", "另一份独立资料，用于检查上传时不会删除其他文档。")
	run(RunOptions{})
	write("a.txt", strings.Repeat("甲", 30)+strings.Repeat("丙", 30))
	run(RunOptions{Paths: []string{"a.txt"}})
	docs, _ := st.ListDocs(ctx)
	if len(docs) != 2 {
		t.Fatalf("定向入库误删其他文档: %d", len(docs))
	}
	docID := StableID("doc", "a.txt")
	c, ok, _ := st.GetChunk(ctx, StableID(docID, "c", "0"))
	if !ok || !strings.Contains(c.Text, "甲") {
		t.Fatal("更新后丢失未变化的块")
	}
	write("c.txt", strings.Repeat("甲", 30)+strings.Repeat("丙", 30))
	run(RunOptions{Paths: []string{"c.txt"}})
	if err := st.DeleteDoc(ctx, docID); err != nil {
		t.Fatal(err)
	}
	copyID := StableID("doc", "c.txt")
	if _, ok, _ := st.GetChunk(ctx, StableID(copyID, "c", "0")); !ok {
		t.Fatal("同内容文档没有独立的索引块")
	}
	if _, ok, _ := st.GetChunk(ctx, StableID(docID, "c", "1")); ok {
		t.Fatal("删除后残留索引块")
	}
	if hits, _ := st.SparseSearch(ctx, "独立资料", 4, types.Filter{}); len(hits) == 0 {
		t.Fatal("其他文档无法检索")
	}
}

func TestEmptyDirectoryRemovesStaleIndex(t *testing.T) {
	ctx := context.Background()
	cfg := config.Default()
	root := t.TempDir()
	cfg.Storage.DocsDir, cfg.Storage.DataDir, cfg.Storage.AssetsDir = filepath.Join(root, "docs"), filepath.Join(root, "data"), filepath.Join(root, "assets")
	os.MkdirAll(cfg.Storage.DocsDir, 0755)
	path := filepath.Join(cfg.Storage.DocsDir, "one.txt")
	os.WriteFile(path, []byte("这是文档内容。"), 0644)
	st, err := file.Open(cfg.Storage.DataDir, 4, true)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	p, _ := NewPipeline(cfg, st, localEmbedder{}, nil)
	if _, err := p.Run(ctx, RunOptions{}, nil); err != nil {
		t.Fatal(err)
	}
	os.Remove(path)
	if _, err := p.Run(ctx, RunOptions{}, nil); err != nil {
		t.Fatal(err)
	}
	stats, _ := st.Stats(ctx)
	if stats.Docs != 0 || stats.Chunks != 0 {
		t.Fatalf("空目录仍残留索引: %+v", stats)
	}
}
