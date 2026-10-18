// Package file 是内置的文件型检索后端：向量 + BM25 全内存，落盘到 data/。
//
// 为什么默认用它：零外部依赖，clone 下来就能跑通闭环。几万块以内暴力算余弦
// 都是毫秒级，个人知识库和 P0 验证完全够用；规模上来换成 pgvector 后端只改配置。
package file

import (
	"bufio"
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"ragkb/internal/store"
	"ragkb/internal/store/bm25"
	"ragkb/internal/types"
)

const (
	vectorsFile = "vectors.bin"
	chunksFile  = "chunks.jsonl"
	docsFile    = "docs.json"
	magic       = "RAGKBF1\n"
)

// Store 是 store.Backend 的文件实现。
type Store struct {
	dir       string
	dim       int
	normalize bool

	mu      sync.RWMutex
	records []types.Chunk // 与 vectors 的行一一对应
	vectors []float32     // 扁平存储：第 i 行是 vectors[i*dim:(i+1)*dim]
	byID    map[string]int
	byHash  map[string]int
	bm      *bm25.BM25
	docs    map[string]types.Doc
}

// Open 打开（或新建）一个索引目录。
func Open(dir string, dim int, normalize bool) (*Store, error) {
	if dim <= 0 {
		return nil, fmt.Errorf("file: 向量维度必须为正数，收到 %d", dim)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("file: 创建索引目录 %s: %w", dir, err)
	}
	s := &Store{
		dir:       dir,
		dim:       dim,
		normalize: normalize,
		byID:      map[string]int{},
		byHash:    map[string]int{},
		bm:        bm25.New(0, 0),
		docs:      map[string]types.Doc{},
	}
	if err := s.load(); err != nil {
		return nil, err
	}
	return s, nil
}

// Dir 返回索引目录。
func (s *Store) Dir() string { return s.dir }

// ---------------------------------------------------------------- 持久化

func (s *Store) load() error {
	if loaded, err := s.loadSnapshot(); loaded || err != nil {
		return err
	}
	vecPath := filepath.Join(s.dir, vectorsFile)
	chunkPath := filepath.Join(s.dir, chunksFile)

	if _, err := os.Stat(vecPath); err != nil {
		if os.IsNotExist(err) {
			// 全新索引：再试一次只读 chunks.jsonl（兼容只有元数据的残缺目录）
			if err := s.loadChunksOnly(chunkPath); err != nil {
				return err
			}
			return nil
		}
		return fmt.Errorf("file: 检查 %s: %w", vecPath, err)
	}

	raw, err := os.ReadFile(vecPath)
	if err != nil {
		return fmt.Errorf("file: 读取 %s: %w", vecPath, err)
	}
	if len(raw) < 16 || string(raw[:8]) != magic {
		return fmt.Errorf("file: %s 不是本服务的索引文件（magic 不匹配）", vecPath)
	}
	dim := int(binary.LittleEndian.Uint32(raw[8:12]))
	count := int(binary.LittleEndian.Uint32(raw[12:16]))
	if dim != s.dim {
		return fmt.Errorf("file: 索引维度是 %d，配置里是 %d。改了 embedding.dim 或换了嵌入模型后需要重建索引", dim, s.dim)
	}
	need := 16 + count*dim*4
	if len(raw) < need {
		return fmt.Errorf("file: %s 数据不完整（期望 %d 字节，实际 %d 字节）", vecPath, need, len(raw))
	}
	vecs := make([]float32, count*dim)
	for i := 0; i < count*dim; i++ {
		vecs[i] = math.Float32frombits(binary.LittleEndian.Uint32(raw[16+i*4:]))
	}

	chunks, err := readChunks(chunkPath)
	if err != nil {
		return err
	}
	n := count
	if len(chunks) < n {
		// 两个文件不一致时截齐，宁可少几个块，也不要张冠李戴。
		n = len(chunks)
	}
	s.records = chunks[:n]
	s.vectors = vecs[:n*dim]

	if err := s.loadDocs(); err != nil {
		return err
	}
	s.reindex()
	return nil
}

// loadChunksOnly 处理"只有元数据、没有向量文件"的残缺目录：读一遍确认可解析，
// 但不加载任何内容——没有向量就无法检索，等下一次入库重建。
func (s *Store) loadChunksOnly(chunkPath string) error {
	if _, err := readChunks(chunkPath); err != nil {
		return err
	}
	s.records = nil
	s.vectors = nil
	return nil
}

func readChunks(path string) ([]types.Chunk, error) {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("file: 读取 %s: %w", path, err)
	}
	defer f.Close()

	var out []types.Chunk
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var c types.Chunk
		if err := json.Unmarshal([]byte(line), &c); err != nil {
			return nil, fmt.Errorf("file: 解析 %s 的某一行失败: %w", path, err)
		}
		out = append(out, c)
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("file: 读取 %s: %w", path, err)
	}
	return out, nil
}

func (s *Store) loadDocs() error {
	path := filepath.Join(s.dir, docsFile)
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("file: 读取 %s: %w", path, err)
	}
	var parsed struct {
		Docs map[string]types.Doc `json:"docs"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return fmt.Errorf("file: 解析 %s: %w", path, err)
	}
	if parsed.Docs != nil {
		s.docs = parsed.Docs
	}
	return nil
}

// reindex 重建内存索引（byID / byHash / BM25）。只在加载或批量删除后调用。
func (s *Store) reindex() {
	s.byID = make(map[string]int, len(s.records))
	s.byHash = make(map[string]int, len(s.records))
	s.bm = bm25.New(0, 0)
	for i, c := range s.records {
		s.byID[c.ID] = i
		if c.Hash != "" {
			s.byHash[c.Hash] = i
		}
		s.bm.Add(c.ID, c.Text)
	}
}

// Save 原子落盘：先写临时文件再 rename，避免写到一半崩溃留下半截索引。
func (s *Store) Save() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.saveLocked()
}

func (s *Store) saveLocked() error { return s.saveSnapshotLocked() }

func writeFileAtomic(path string, data []byte) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return fmt.Errorf("file: 写入 %s: %w", tmp, err)
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("file: 替换 %s: %w", path, err)
	}
	return nil
}

// ---------------------------------------------------------------- 写入

// Add 幂等写入：内容 hash 已存在、或同一 (doc_id, index) 已存在的块会被跳过。
func (s *Store) Add(ctx context.Context, chunks []types.Chunk, vectors [][]float32) error {
	if len(chunks) != len(vectors) {
		return fmt.Errorf("file: 块数(%d)与向量数(%d)不一致", len(chunks), len(vectors))
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.addLocked(chunks, vectors)
}

func (s *Store) addLocked(chunks []types.Chunk, vectors [][]float32) error {
	for i := range chunks {
		if len(vectors[i]) != s.dim {
			return fmt.Errorf("file: 向量维度不符")
		}
	}
	for i, c := range chunks {
		if len(vectors[i]) != s.dim {
			return fmt.Errorf("file: 块 %s 的向量是 %d 维，索引是 %d 维", c.ID, len(vectors[i]), s.dim)
		}
		if c.Hash != "" {
			if _, dup := s.byHash[c.Hash]; dup {
				continue
			}
		}
		if c.ID == "" {
			c.ID = types.NewID()
		}
		if _, dup := s.byID[c.ID]; dup {
			continue
		}
		if c.CreatedAt.IsZero() {
			c.CreatedAt = nowUTC()
		}
		vec := vectors[i]
		if s.normalize {
			vec = normalize(vec)
		}
		s.byID[c.ID] = len(s.records)
		if c.Hash != "" {
			s.byHash[c.Hash] = len(s.records)
		}
		s.records = append(s.records, c)
		s.vectors = append(s.vectors, vec...)
		s.bm.Add(c.ID, c.Text)
	}
	return nil
}

func (s *Store) RemoveDoc(ctx context.Context, docID string) (int, error) {
	n := s.removeWhere(func(c types.Chunk) bool { return c.DocID == docID })
	if n > 0 {
		s.mu.Lock()
		delete(s.docs, docID)
		s.mu.Unlock()
	}
	return n, nil
}

func (s *Store) RemoveBySource(ctx context.Context, source string) (int, error) {
	return s.removeWhere(func(c types.Chunk) bool { return c.Source == source }), nil
}

func (s *Store) removeWhere(drop func(types.Chunk) bool) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.removeWhereLocked(drop)
}

func (s *Store) removeWhereLocked(drop func(types.Chunk) bool) int {

	keptRecords := make([]types.Chunk, 0, len(s.records))
	keptVectors := make([]float32, 0, len(s.vectors))
	removed := 0
	for i, c := range s.records {
		if drop(c) {
			removed++
			s.bm.Remove(c.ID)
			delete(s.byID, c.ID)
			if c.Hash != "" {
				delete(s.byHash, c.Hash)
			}
			continue
		}
		keptRecords = append(keptRecords, c)
		keptVectors = append(keptVectors, s.vectors[i*s.dim:(i+1)*s.dim]...)
	}
	if removed == 0 {
		return 0
	}
	s.records = keptRecords
	s.vectors = keptVectors
	// 位置索引必须重建，因为切片下标全变了。
	s.byID = make(map[string]int, len(keptRecords))
	for i, c := range keptRecords {
		s.byID[c.ID] = i
	}
	return removed
}

// ---------------------------------------------------------------- 检索

// DenseSearch 暴力算余弦相似度。
//
// 复杂度 O(n·dim)：10 万块 × 1024 维约几十毫秒，个人知识库完全够用。
// 真到千万级再上 HNSW / IVF（或者直接切 pgvector 后端）。
func (s *Store) DenseSearch(ctx context.Context, vector []float32, k int, f types.Filter) ([]store.Candidate, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if k <= 0 || len(s.records) == 0 {
		return nil, nil
	}
	if len(vector) != s.dim {
		return nil, fmt.Errorf("file: 查询向量是 %d 维，索引是 %d 维", len(vector), s.dim)
	}
	q := vector
	if s.normalize {
		q = normalize(vector)
	}

	type scored struct {
		idx int
		val float64
	}
	all := make([]scored, 0, len(s.records))
	for i, c := range s.records {
		if !f.Match(c) {
			continue
		}
		v := s.vectors[i*s.dim : (i+1)*s.dim]
		var sc float64
		if s.normalize {
			sc = dot(q, v) // 两边都已归一化，点积即余弦
		} else {
			sc = cosine(q, v)
		}
		all = append(all, scored{idx: i, val: sc})
	}
	sort.Slice(all, func(a, b int) bool {
		if all[a].val != all[b].val {
			return all[a].val > all[b].val
		}
		return s.records[all[a].idx].ID < s.records[all[b].idx].ID
	})
	if len(all) > k {
		all = all[:k]
	}
	out := make([]store.Candidate, 0, len(all))
	for _, sc := range all {
		out = append(out, store.Candidate{ChunkID: s.records[sc.idx].ID, Score: sc.val})
	}
	return out, nil
}

// SparseSearch 用 BM25 做关键词召回。中文靠 bigram 分词，所以「锦官城」这类
// 别名要靠别名表扩写查询（见 rag 层的 query expansion）。
func (s *Store) SparseSearch(ctx context.Context, text string, k int, f types.Filter) ([]store.Candidate, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if k <= 0 || strings.TrimSpace(text) == "" {
		return nil, nil
	}
	// 多取一些再过滤，避免过滤把结果削得太少。
	scored := s.bm.Search(text, k*4)
	out := make([]store.Candidate, 0, k)
	for _, sc := range scored {
		idx, ok := s.byID[sc.ID]
		if !ok {
			continue
		}
		if !f.Match(s.records[idx]) {
			continue
		}
		out = append(out, store.Candidate{ChunkID: sc.ID, Score: sc.Score})
		if len(out) >= k {
			break
		}
	}
	return out, nil
}

func (s *Store) GetChunk(ctx context.Context, id string) (types.Chunk, bool, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	idx, ok := s.byID[id]
	if !ok {
		return types.Chunk{}, false, nil
	}
	return s.records[idx], true, nil
}

func (s *Store) HasHash(ctx context.Context, hash string) (bool, error) {
	if hash == "" {
		return false, nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	_, ok := s.byHash[hash]
	return ok, nil
}

// ---------------------------------------------------------------- 文档登记

func (s *Store) UpsertDoc(ctx context.Context, doc types.Doc) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if doc.ID == "" {
		return fmt.Errorf("file: 文档 ID 不能为空")
	}
	s.docs[doc.ID] = doc
	return nil
}

func (s *Store) DeleteDoc(ctx context.Context, docID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.removeWhereLocked(func(c types.Chunk) bool { return c.DocID == docID })
	delete(s.docs, docID)
	return nil
}

func (s *Store) ReplaceDocument(ctx context.Context, doc types.Doc, chunks []types.Chunk, vectors [][]float32) (int, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	if doc.ID == "" || len(chunks) != len(vectors) {
		return 0, fmt.Errorf("file: 文档或向量数量非法")
	}
	for i, c := range chunks {
		if c.DocID != doc.ID || len(vectors[i]) != s.dim {
			return 0, fmt.Errorf("file: 块归属或向量维度非法")
		}
		for _, x := range vectors[i] {
			if math.IsNaN(float64(x)) || math.IsInf(float64(x), 0) {
				return 0, fmt.Errorf("file: 非有限向量")
			}
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	n := s.removeWhereLocked(func(c types.Chunk) bool { return c.DocID == doc.ID })
	if err := s.addLocked(chunks, vectors); err != nil {
		return n, err
	}
	s.docs[doc.ID] = doc
	return n, nil
}

func (s *Store) GetDoc(ctx context.Context, id string) (types.Doc, bool, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	d, ok := s.docs[id]
	return d, ok, nil
}

func (s *Store) ListDocs(ctx context.Context) ([]types.Doc, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]types.Doc, 0, len(s.docs))
	for _, d := range s.docs {
		out = append(out, d)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Source < out[j].Source })
	return out, nil
}

// ---------------------------------------------------------------- 统计

func (s *Store) Stats(ctx context.Context) (types.Stats, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	stats := types.Stats{
		Backend:    "file",
		Chunks:     len(s.records),
		Docs:       len(s.docs),
		Dim:        s.dim,
		Modalities: map[string]int{},
		DataDir:    s.dir,
	}
	sets := map[string]map[string]struct{}{
		"dynasty": {}, "place": {}, "topic": {}, "source_type": {}, "credibility": {}, "abilities": {},
	}
	assets := map[string]struct{}{}
	for _, c := range s.records {
		stats.Modalities[string(c.Modality)]++
		if c.Modality == types.ModalityImage && c.AssetURL != "" {
			assets[c.AssetURL] = struct{}{}
		}
		addFacet(sets["dynasty"], c.Dynasty)
		addFacet(sets["place"], c.Place)
		addFacet(sets["topic"], c.Topic)
		addFacet(sets["source_type"], c.SourceType)
		addFacet(sets["credibility"], c.Credibility)
		for _, a := range c.Abilities {
			addFacet(sets["abilities"], a)
		}
	}
	stats.Assets = len(assets)
	stats.Facets = types.Facets{
		Dynasty:     sortedKeys(sets["dynasty"]),
		Place:       sortedKeys(sets["place"]),
		Topic:       sortedKeys(sets["topic"]),
		SourceType:  sortedKeys(sets["source_type"]),
		Credibility: sortedKeys(sets["credibility"]),
		Abilities:   sortedKeys(sets["abilities"]),
	}
	return stats, nil
}

func (s *Store) Close() error { return s.Save() }

// ---------------------------------------------------------------- 向量工具

func normalize(v []float32) []float32 {
	var sum float64
	for _, x := range v {
		sum += float64(x) * float64(x)
	}
	if sum == 0 {
		return v
	}
	n := math.Sqrt(sum)
	out := make([]float32, len(v))
	for i, x := range v {
		out[i] = float32(float64(x) / n)
	}
	return out
}

func dot(a, b []float32) float64 {
	var sum float64
	for i := range a {
		sum += float64(a[i]) * float64(b[i])
	}
	return sum
}

func cosine(a, b []float32) float64 {
	var na, nb float64
	for i := range a {
		na += float64(a[i]) * float64(a[i])
		nb += float64(b[i]) * float64(b[i])
	}
	if na == 0 || nb == 0 {
		return 0
	}
	return dot(a, b) / (math.Sqrt(na) * math.Sqrt(nb))
}

func addFacet(set map[string]struct{}, v string) {
	if v = strings.TrimSpace(v); v != "" {
		set[v] = struct{}{}
	}
}

func sortedKeys(set map[string]struct{}) []string {
	out := make([]string, 0, len(set))
	for k := range set {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func nowUTC() time.Time { return time.Now().UTC() }
