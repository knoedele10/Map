// Package store 是检索存储层。
//
// 这里刻意分成两层：
//   - Backend：各存储实现（内置文件索引 / Postgres+pgvector）只需要实现两个原始检索原语；
//   - Hybrid：融合、过滤、归一化等**共享**逻辑，只写一遍，换后端不改变检索行为。
//
// 这样做的现实理由：起步用内置文件索引零依赖跑通闭环，规模上来后换 pgvector
// 只改配置，不需要动 rag / api 任何一行代码。
package store

import (
	"context"
	"fmt"
	"math"
	"sort"

	"ragkb/internal/types"
)

// Candidate 是一次原始检索的命中（只有 ID 和分数）。
type Candidate struct {
	ChunkID string
	Score   float64
}

// Fusion 指定多路召回如何合并。
const (
	FusionRRF      = "rrf"      // Reciprocal Rank Fusion：只依赖名次，天然免疫量纲差异
	FusionWeighted = "weighted" // 加权：需要把两路分数归一化后按 alpha 混合
)

// SearchQuery 是一次混合检索的完整输入。
type SearchQuery struct {
	Vector     []float32 // 稠密查询向量；为 nil 时退化为纯稀疏检索
	Text       string    // 原始查询文本，用于 BM25
	TopK       int       // 最终返回条数
	CandidateK int       // 每路召回条数
	Alpha      float64   // weighted 融合权重：1 = 纯向量，0 = 纯 BM25
	Fusion     string    // rrf | weighted
	RRFK       int       // RRF 平滑常数，默认 60
	Filter     types.Filter
	MinScore   float64
}

// Backend 是一个可插拔的检索存储。实现者只需保证两个原始检索原语的语义：
// 返回**按分数降序**的候选，并且已经应用了 Filter。
type Backend interface {
	// Add 幂等写入：同一 (doc_id, index) 或相同内容 hash 不应产生重复块。
	Add(ctx context.Context, chunks []types.Chunk, vectors [][]float32) error
	// RemoveDoc 删除一个文档的全部块，返回删除条数。
	RemoveDoc(ctx context.Context, docID string) (int, error)
	// RemoveBySource 按来源相对路径删除（重新入库前清旧块）。
	RemoveBySource(ctx context.Context, source string) (int, error)

	DenseSearch(ctx context.Context, vector []float32, k int, f types.Filter) ([]Candidate, error)
	SparseSearch(ctx context.Context, text string, k int, f types.Filter) ([]Candidate, error)

	GetChunk(ctx context.Context, id string) (types.Chunk, bool, error)
	HasHash(ctx context.Context, hash string) (bool, error)

	UpsertDoc(ctx context.Context, doc types.Doc) error
	DeleteDoc(ctx context.Context, docID string) error
	GetDoc(ctx context.Context, id string) (types.Doc, bool, error)
	ListDocs(ctx context.Context) ([]types.Doc, error)
	Stats(ctx context.Context) (types.Stats, error)

	Close() error
}

// Persister 是可选的持久化能力。内置文件后端需要显式落盘；数据库后端实现了
// 事务写入，不需要这个接口。上层统一用 SaveIfNeeded 调用，不必关心后端类型。
type Persister interface {
	Save() error
}

// DocumentReplacer commits a fully prepared document in one storage operation.
// Readers must observe either the previous document or the complete new version.
type DocumentReplacer interface {
	ReplaceDocument(context.Context, types.Doc, []types.Chunk, [][]float32) (int, error)
}

// SaveIfNeeded 在后端支持持久化时落盘。入库流水线在整批处理完后调用一次，
// 避免每个文件都重写整个索引（那是 O(n²)）。
func SaveIfNeeded(b Backend) error {
	if p, ok := b.(Persister); ok {
		return p.Save()
	}
	return nil
}

// Hybrid 把稠密与稀疏两路召回融合成最终结果。这是整个检索层的核心。
//
// 为什么默认用 RRF 而不是加权求和：BM25 的分数是无界的（可能 30，也可能 3），
// 余弦相似度是 0~1，直接加权需要先归一化，而归一化方式一变排名就变。
// RRF 只看名次，两路量纲不同也不影响，是混合检索里最稳的默认选择。
func Hybrid(ctx context.Context, b Backend, q SearchQuery) ([]types.Hit, error) {
	if q.TopK <= 0 {
		q.TopK = 4
	}
	if q.CandidateK < q.TopK {
		q.CandidateK = q.TopK * 5
	}
	if q.Fusion == "" {
		q.Fusion = FusionRRF
	}
	if q.RRFK <= 0 {
		q.RRFK = 60
	}

	type acc struct {
		denseRank, sparseRank int
		dense, sparse         float64
		rrf                   float64
	}
	merged := map[string]*acc{}
	touch := func(id string) *acc {
		a, ok := merged[id]
		if !ok {
			a = &acc{denseRank: -1, sparseRank: -1}
			merged[id] = a
		}
		return a
	}

	if len(q.Vector) > 0 {
		cands, err := b.DenseSearch(ctx, q.Vector, q.CandidateK, q.Filter)
		if err != nil {
			return nil, fmt.Errorf("稠密检索失败: %w", err)
		}
		for i, c := range cands {
			a := touch(c.ChunkID)
			a.denseRank, a.dense = i, c.Score
			a.rrf += 1.0 / float64(q.RRFK+i+1)
		}
	}
	if q.Text != "" {
		cands, err := b.SparseSearch(ctx, q.Text, q.CandidateK, q.Filter)
		if err != nil {
			return nil, fmt.Errorf("稀疏检索失败: %w", err)
		}
		for i, c := range cands {
			a := touch(c.ChunkID)
			a.sparseRank, a.sparse = i, c.Score
			a.rrf += 1.0 / float64(q.RRFK+i+1)
		}
	}

	// weighted 融合：把两路各自归一化到 0~1 再按 alpha 混合。
	var maxDense, maxSparse float64
	if q.Fusion == FusionWeighted {
		for _, a := range merged {
			maxDense = math.Max(maxDense, a.dense)
			maxSparse = math.Max(maxSparse, a.sparse)
		}
	}

	hits := make([]types.Hit, 0, len(merged))
	for id, a := range merged {
		chunk, ok, err := b.GetChunk(ctx, id)
		if err != nil {
			return nil, fmt.Errorf("读取命中块 %s 失败: %w", id, err)
		}
		if !ok {
			continue // 索引与元数据不一致时跳过，不因此让整次检索失败
		}
		if !q.Filter.Match(chunk) {
			continue // 后端没实现过滤时的兜底，保证语义一致
		}

		var score float64
		switch q.Fusion {
		case FusionWeighted:
			d, s := 0.0, 0.0
			if maxDense > 0 {
				d = a.dense / maxDense
			}
			if maxSparse > 0 {
				s = a.sparse / maxSparse
			}
			score = q.Alpha*d + (1-q.Alpha)*s
		default:
			// RRF 原始分很小（~0.016 起），除以本次最大值归一化到 0~1，方便前端展示。
			score = a.rrf
		}

		from := "both"
		switch {
		case a.denseRank >= 0 && a.sparseRank < 0:
			from = "dense"
		case a.denseRank < 0 && a.sparseRank >= 0:
			from = "sparse"
		}
		hits = append(hits, types.Hit{
			Chunk:  chunk,
			Score:  score,
			Dense:  a.dense,
			Sparse: a.sparse,
			From:   from,
		})
	}

	if q.Fusion != FusionWeighted {
		var max float64
		for _, h := range hits {
			max = math.Max(max, h.Score)
		}
		if max > 0 {
			for i := range hits {
				hits[i].Score /= max
			}
		}
	}

	sort.SliceStable(hits, func(i, j int) bool {
		if hits[i].Score != hits[j].Score {
			return hits[i].Score > hits[j].Score
		}
		return hits[i].Chunk.ID < hits[j].Chunk.ID
	})

	if q.MinScore > 0 {
		kept := hits[:0]
		for _, h := range hits {
			if h.Score >= q.MinScore {
				kept = append(kept, h)
			}
		}
		hits = kept
	}
	if len(hits) > q.TopK {
		hits = hits[:q.TopK]
	}
	for i := range hits {
		hits[i].Rank = i + 1
	}
	return hits, nil
}
