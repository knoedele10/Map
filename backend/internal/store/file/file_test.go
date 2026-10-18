package file

import (
	"context"
	"path/filepath"
	"testing"

	"ragkb/internal/types"
)

func testChunks() []types.Chunk {
	return []types.Chunk{
		{ID: "c1", DocID: "d1", Source: "a.md", Index: 0, Text: "年假 5 天，需提前申请", Hash: "h1",
			Modality: types.ModalityText, Dynasty: "唐", Abilities: []string{"历史"}},
		{ID: "c2", DocID: "d1", Source: "a.md", Index: 1, Text: "病假需要医院证明", Hash: "h2",
			Modality: types.ModalityText},
		{ID: "c3", DocID: "d2", Source: "b.md", Index: 0, Text: "珍珠港事件 1941 年", Hash: "h3",
			Modality: types.ModalityTable, Place: "夏威夷"},
	}
}

func vec(v ...float32) []float32 { return v }

func TestAddSearchAndPersist(t *testing.T) {
	dir := t.TempDir()
	ctx := context.Background()
	s, err := Open(dir, 4, true)
	if err != nil {
		t.Fatalf("打开失败: %v", err)
	}
	chunks := testChunks()
	vecs := [][]float32{vec(1, 0, 0, 0), vec(0, 1, 0, 0), vec(0, 0, 1, 0)}
	if err := s.Add(ctx, chunks, vecs); err != nil {
		t.Fatalf("写入失败: %v", err)
	}
	if err := s.UpsertDoc(ctx, types.Doc{ID: "d1", Source: "a.md", Title: "a", Status: "indexed", Chunks: 2}); err != nil {
		t.Fatalf("登记文档失败: %v", err)
	}
	if err := s.Save(); err != nil {
		t.Fatalf("落盘失败: %v", err)
	}

	// 稠密检索：查询向量与 c1 同向，应排第一
	hits, err := s.DenseSearch(ctx, vec(1, 0, 0, 0), 3, types.Filter{})
	if err != nil {
		t.Fatalf("稠密检索失败: %v", err)
	}
	if len(hits) != 3 {
		t.Fatalf("应返回 3 条，实际 %d", len(hits))
	}
	if hits[0].ChunkID != "c1" {
		t.Errorf("首位应是 c1，实际 %s（分数 %v）", hits[0].ChunkID, hits[0].Score)
	}

	// 稀疏检索：中文 bigram 应能命中"年假"
	sparse, err := s.SparseSearch(ctx, "年假", 3, types.Filter{})
	if err != nil {
		t.Fatalf("稀疏检索失败: %v", err)
	}
	if len(sparse) == 0 || sparse[0].ChunkID != "c1" {
		t.Errorf("BM25 应把 c1 排第一，实际 %+v", sparse)
	}

	// 去重
	ok, err := s.HasHash(ctx, "h1")
	if err != nil || !ok {
		t.Errorf("HasHash(h1) 应为 true，实际 %v %v", ok, err)
	}
	if ok, _ := s.HasHash(ctx, "nope"); ok {
		t.Error("不存在的 hash 应为 false")
	}

	// 关闭后重新打开：数据必须还在（这是持久化最关键的性质）
	if err := s.Close(); err != nil {
		t.Fatalf("关闭失败: %v", err)
	}
	reopened, err := Open(dir, 4, true)
	if err != nil {
		t.Fatalf("重新打开失败: %v", err)
	}
	defer reopened.Close()

	st, err := reopened.Stats(ctx)
	if err != nil {
		t.Fatalf("统计失败: %v", err)
	}
	if st.Chunks != 3 {
		t.Errorf("重开后应有 3 块，实际 %d", st.Chunks)
	}
	if st.Docs != 1 {
		t.Errorf("重开后应有 1 个文档，实际 %d", st.Docs)
	}
	if chunk, ok, err := reopened.GetChunk(ctx, "c2"); err != nil || !ok {
		t.Errorf("重开后应能取到 c2: ok=%v err=%v", ok, err)
	} else if chunk.Text == "" {
		t.Error("重开后取到的块内容为空")
	}
	hits2, err := reopened.DenseSearch(ctx, vec(1, 0, 0, 0), 1, types.Filter{})
	if err != nil || len(hits2) == 0 || hits2[0].ChunkID != "c1" {
		t.Errorf("重开后稠密检索结果不一致: %+v %v", hits2, err)
	}
}

func TestFilterIsApplied(t *testing.T) {
	ctx := context.Background()
	s, err := Open(t.TempDir(), 4, true)
	if err != nil {
		t.Fatalf("打开失败: %v", err)
	}
	defer s.Close()
	if err := s.Add(ctx, testChunks(), [][]float32{vec(1, 0, 0, 0), vec(0, 1, 0, 0), vec(0, 0, 1, 0)}); err != nil {
		t.Fatalf("写入失败: %v", err)
	}

	// abilities 门禁：未标注的块（c2/c3）始终可检索，标注了"历史"的 c1 需要解锁
	locked, err := s.DenseSearch(ctx, vec(1, 0, 0, 0), 10, types.Filter{Abilities: []string{"三国"}})
	if err != nil {
		t.Fatalf("检索失败: %v", err)
	}
	for _, h := range locked {
		if h.ChunkID == "c1" {
			t.Error("abilities=三国 时不应返回标注了 历史 的 c1")
		}
	}
	unlocked, _ := s.DenseSearch(ctx, vec(1, 0, 0, 0), 10, types.Filter{Abilities: []string{"历史"}})
	found := false
	for _, h := range unlocked {
		if h.ChunkID == "c1" {
			found = true
		}
	}
	if !found {
		t.Error("abilities=历史 时应返回 c1")
	}

	// 模态过滤
	tables, _ := s.DenseSearch(ctx, vec(0, 0, 1, 0), 10, types.Filter{Modalities: []types.Modality{types.ModalityTable}})
	if len(tables) != 1 || tables[0].ChunkID != "c3" {
		t.Errorf("按模态过滤应只返回 c3，实际 %+v", tables)
	}

	// 朝代过滤
	tang, _ := s.DenseSearch(ctx, vec(1, 0, 0, 0), 10, types.Filter{Dynasty: []string{"唐"}})
	if len(tang) != 1 || tang[0].ChunkID != "c1" {
		t.Errorf("按朝代过滤应只返回 c1，实际 %+v", tang)
	}
}

func TestRemoveAndStats(t *testing.T) {
	ctx := context.Background()
	s, err := Open(t.TempDir(), 4, true)
	if err != nil {
		t.Fatalf("打开失败: %v", err)
	}
	defer s.Close()
	if err := s.Add(ctx, testChunks(), [][]float32{vec(1, 0, 0, 0), vec(0, 1, 0, 0), vec(0, 0, 1, 0)}); err != nil {
		t.Fatalf("写入失败: %v", err)
	}

	if n, err := s.RemoveBySource(ctx, "a.md"); err != nil || n != 2 {
		t.Errorf("按来源删除应删 2 块，实际 %d %v", n, err)
	}
	st, _ := s.Stats(ctx)
	if st.Chunks != 1 {
		t.Errorf("删除后应剩 1 块，实际 %d", st.Chunks)
	}
	if len(st.Facets.Place) == 0 {
		t.Error("facets 应包含地点 夏威夷")
	}

	if n, err := s.RemoveDoc(ctx, "d2"); err != nil || n != 1 {
		t.Errorf("按文档删除应删 1 块，实际 %d %v", n, err)
	}
	if st, _ := s.Stats(ctx); st.Chunks != 0 {
		t.Errorf("全部删除后应为 0 块，实际 %d", st.Chunks)
	}
}

func TestDedupByHashAndID(t *testing.T) {
	ctx := context.Background()
	s, err := Open(t.TempDir(), 4, true)
	if err != nil {
		t.Fatalf("打开失败: %v", err)
	}
	defer s.Close()
	chunks := testChunks()
	vecs := [][]float32{vec(1, 0, 0, 0), vec(0, 1, 0, 0), vec(0, 0, 1, 0)}
	if err := s.Add(ctx, chunks, vecs); err != nil {
		t.Fatalf("首次写入失败: %v", err)
	}
	// 重复写入同样的内容：hash 与 ID 都重复，不应产生新块
	if err := s.Add(ctx, chunks, vecs); err != nil {
		t.Fatalf("重复写入失败: %v", err)
	}
	if st, _ := s.Stats(ctx); st.Chunks != 3 {
		t.Errorf("重复写入后仍应是 3 块，实际 %d", st.Chunks)
	}
}

func TestDimensionMismatchRejected(t *testing.T) {
	ctx := context.Background()
	s, err := Open(t.TempDir(), 4, true)
	if err != nil {
		t.Fatalf("打开失败: %v", err)
	}
	defer s.Close()
	// 维度不对必须报错，而不是默默写入坏数据
	err = s.Add(ctx, []types.Chunk{{ID: "x", Text: "t", Hash: "hx"}}, [][]float32{vec(1, 2)})
	if err == nil {
		t.Error("维度不匹配时应返回错误")
	}
	// 块数与向量数不一致也必须报错
	err = s.Add(ctx, []types.Chunk{{ID: "y", Text: "t", Hash: "hy"}}, [][]float32{})
	if err == nil {
		t.Error("块数与向量数不一致时应返回错误")
	}
}

func TestDir(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir, 4, true)
	if err != nil {
		t.Fatalf("打开失败: %v", err)
	}
	defer s.Close()
	if filepath.Clean(s.Dir()) != filepath.Clean(dir) {
		t.Errorf("Dir() 应返回 %s，实际 %s", dir, s.Dir())
	}
}
