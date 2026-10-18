// Package pgvector 是 store.Backend 的 PostgreSQL + pgvector 实现。
//
// # 稀疏检索的取舍（如实说明）
//
// PostgreSQL 没有中文分词器，所以本后端提供的是**近似 BM25 的稀疏召回**；
// **精确的 Okapi BM25（有 IDF、有文档长度归一化、有可调的 k1/b）由内置文件后端
// internal/store/bm25 提供**，两者不可互换、分数不可比。
//
// 具体做法：
//   - 写入时用 bm25.Tokenize 把 text **预分词**（汉字 unigram + bigram，非汉字词
//     整段小写），空格连接后存进 rag_chunks.text_tokens；
//   - 检索时对查询做**同样的**预分词，用
//     to_tsvector('simple', text_tokens) @@ to_tsquery('simple', $1) 匹配，
//     用 ts_rank_cd 排序。
//
// 为什么不是任务书里写的 plainto_tsquery：plainto_tsquery 对多个词是**隐式 AND**，
// 而预分词会把一个 n 字的中文查询展开成 n 个 unigram + n-1 个 bigram；AND 之后
// 等于要求"查询里每个字、每个相邻字对都出现"，召回率极低，近似 BM25 会退化成
// "整句必须命中"。这里改用同样安全的 to_tsquery：$1 是 Go 侧从 token 拼出来的
// OR 表达式（token 只可能是字母/数字/汉字，且它是**参数值**不是 SQL 文本），
// 配合 ts_rank_cd，方向与 BM25 的"命中词越多分越高"一致。
//
// ts_rank_cd 不是 BM25：没有 IDF、没有文档长度归一化，也没有 k1/b。它只提供
// 方向性的排序信号，绝对值没有意义（store.Hybrid 默认用 RRF，只看名次，正好不敏感）。
//
// # 维度
//
// deploy/schema.sql 里 rag_chunks.embedding 写死 vector(1024)（bge-m3 的输出维度）。
// Open(ctx, dsn, dim) 会读 pg_attribute 拿到真实列类型来校验 dim，不一致直接报错，
// 避免"写到一半才失败"。
//
// # 连接池
//
// 用 github.com/jackc/pgx/v5/pgxpool（pgx 官方连接池），默认上限 defaultMaxConns。
package pgvector

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"ragkb/internal/store"
	"ragkb/internal/store/bm25"
	"ragkb/internal/types"
)

// BackendName 是 Stats.Backend 里的后端标识。
const BackendName = "pgvector"

// defaultMaxConns 是连接池默认上限。
const defaultMaxConns = 8

// Store 是 pgvector 存储后端，实现 store.Backend。
//
// Store 可并发使用：每个方法各从 pgxpool 借一条连接，连接不跨方法共享。
type Store struct {
	pool *pgxpool.Pool
	dim  int
}

// 编译期断言：Store 必须实现 store.Backend。
var _ store.Backend = (*Store)(nil)

// Open 连接数据库并校验索引维度。
//
// dim 必须与 deploy/schema.sql 里 rag_chunks.embedding 的 vector(N) 一致；
// 表还不存在或维度不一致都会返回错误（提示先执行 schema.sql / 改维度）。
func Open(ctx context.Context, dsn string, dim int, maxConns ...int) (*Store, error) {
	if strings.TrimSpace(dsn) == "" {
		return nil, errors.New("pgvector: dsn 不能为空")
	}
	if dim <= 0 {
		return nil, fmt.Errorf("pgvector: dim 必须为正数，得到 %d", dim)
	}
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return nil, fmt.Errorf("pgvector: 解析 dsn 失败: %w", err)
	}
	cfg.MaxConns = defaultMaxConns
	if len(maxConns) > 0 && maxConns[0] > 0 {
		cfg.MaxConns = int32(maxConns[0])
	}

	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("pgvector: 创建连接池失败: %w", err)
	}

	s := &Store{pool: pool, dim: dim}
	if err := s.pool.Ping(ctx); err != nil {
		s.pool.Close()
		return nil, fmt.Errorf("pgvector: 连接数据库失败: %w", err)
	}
	actual, err := s.embeddingDim(ctx)
	if err != nil {
		s.pool.Close()
		return nil, err
	}
	if actual != dim {
		s.pool.Close()
		return nil, fmt.Errorf(
			"pgvector: rag_chunks.embedding 是 vector(%d)，与 Open 传入的 dim=%d 不一致"+
				"（换维度要同时改 deploy/schema.sql 与配置）", actual, dim)
	}
	return s, nil
}

// Dim 返回索引维度。
func (s *Store) Dim() int { return s.dim }

// Close 关闭连接池。pgxpool.Pool.Close 没有返回值，所以这里恒返回 nil。
func (s *Store) Close() error {
	s.pool.Close()
	return nil
}

// ---------------------------------------------------------------------------
// 连接与维度
// ---------------------------------------------------------------------------

// withConn 从连接池借一条连接执行 fn，结束后归还。
func (s *Store) withConn(ctx context.Context, fn func(*pgx.Conn) error) error {
	c, err := s.pool.Acquire(ctx)
	if err != nil {
		return err
	}
	defer c.Release()
	return fn(c.Conn())
}

const embeddingTypeSQL = `SELECT format_type(a.atttypid, a.atttypmod)
FROM pg_attribute a
WHERE a.attrelid = 'rag_chunks'::regclass
  AND a.attname = 'embedding'
  AND NOT a.attisdropped`

// embeddingDim 读出 rag_chunks.embedding 的真实维度（如 "vector(1024)" -> 1024）。
func (s *Store) embeddingDim(ctx context.Context) (int, error) {
	var typ string
	err := s.withConn(ctx, func(c *pgx.Conn) error {
		return c.QueryRow(ctx, embeddingTypeSQL).Scan(&typ)
	})
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.Is(err, pgx.ErrNoRows) || (errors.As(err, &pgErr) && pgErr.Code == "42P01") {
			return 0, fmt.Errorf("pgvector: 表 rag_chunks 不存在，请先执行 deploy/schema.sql: %w", err)
		}
		return 0, fmt.Errorf("pgvector: 读取 rag_chunks.embedding 列类型失败: %w", err)
	}
	return parseVectorDim(typ)
}

var vectorDimRe = regexp.MustCompile(`^vector\((\d+)\)$`)

// parseVectorDim 从 format_type 的结果里解析维度，如 "vector(1024)" -> 1024。
func parseVectorDim(typ string) (int, error) {
	m := vectorDimRe.FindStringSubmatch(strings.TrimSpace(typ))
	if m == nil {
		return 0, fmt.Errorf("pgvector: rag_chunks.embedding 的列类型是 %q，期望 vector(N)（见 deploy/schema.sql）", typ)
	}
	n, err := strconv.Atoi(m[1])
	if err != nil || n <= 0 {
		return 0, fmt.Errorf("pgvector: 无法解析向量维度 %q", typ)
	}
	return n, nil
}

// ---------------------------------------------------------------------------
// 向量与分词辅助
// ---------------------------------------------------------------------------

// vectorLiteral 把向量编码成 pgvector 的文本表示 "[1,2,3]"。
//
// pgx 的类型表里没有 vector（本仓库没引 pgvector-go），但 pgx 对未注册的 OID
// 一律按文本格式发送（Map.FormatCodeForOID 的兜底是 TextFormatCode），
// 而 Go string 在文本格式下原样写出，所以 $1::vector 可以直接用字符串参数。
func vectorLiteral(v []float32) (string, error) {
	if len(v) == 0 {
		return "", errors.New("空向量")
	}
	var b strings.Builder
	b.Grow(len(v)*8 + 2)
	b.WriteByte('[')
	for i, x := range v {
		f := float64(x)
		if math.IsNaN(f) || math.IsInf(f, 0) {
			return "", fmt.Errorf("第 %d 维不是有限数: %v", i, x)
		}
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(strconv.FormatFloat(f, 'g', -1, 32))
	}
	b.WriteByte(']')
	return b.String(), nil
}

// normalizeL2 返回向量的 L2 归一化**副本**（不修改入参）。
//
// 与 llm 侧 embedding.normalize 保持一致：查询向量先归一化再送进 pgvector。
// 余弦距离对缩放不敏感，归一化不改变排序，只是让"服务端怎么算"和"数据库怎么算"
// 用的是同一份输入。零向量原样返回：pgvector 对零向量算余弦会得到 NaN，
// 但那属于调用方传了坏数据，这里不静默替换。
func normalizeL2(v []float32) []float32 {
	out := make([]float32, len(v))
	var sum float64
	for _, x := range v {
		sum += float64(x) * float64(x)
	}
	if sum == 0 {
		copy(out, v)
		return out
	}
	inv := 1 / math.Sqrt(sum)
	for i, x := range v {
		out[i] = float32(float64(x) * inv)
	}
	return out
}

// tokensOf 把文本预分词成空格连接的 token 串，写进 rag_chunks.text_tokens。
func tokensOf(text string) string {
	return strings.Join(bm25.Tokenize(text), " ")
}

// tsQueryFromTokens 把预分词结果拼成 to_tsquery 用的 OR 表达式。
//
// 安全：token 只可能来自 bm25.Tokenize（字母/数字/汉字），这里仍然做一次白名单
// 校验并给每个 token 加单引号；而且表达式本身是**参数值**，不是 SQL 文本，
// 所以即使校验漏了也不会变成 SQL 注入。
func tsQueryFromTokens(tokens []string) string {
	seen := make(map[string]struct{}, len(tokens))
	parts := make([]string, 0, len(tokens))
	for _, t := range tokens {
		if !isSafeToken(t) {
			continue
		}
		key := strings.ToLower(t)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		parts = append(parts, "'"+key+"'")
	}
	return strings.Join(parts, " | ")
}

// longTokens 取出长度 ≥2 个字的 token（中文 bigram 与非汉字词）。
//
// 用途见 SparseSearch：多字查询要求至少命中一个长 token，用来挡掉
// "只共享一个汉字"造成的误召回。
func longTokens(tokens []string) []string {
	out := make([]string, 0, len(tokens))
	for _, t := range tokens {
		if len([]rune(t)) >= 2 {
			out = append(out, t)
		}
	}
	return out
}

// isSafeToken 只放行字母、数字、汉字（即 bm25.Tokenize 的输出范围）。
func isSafeToken(t string) bool {
	if t == "" {
		return false
	}
	for _, r := range t {
		if !unicode.IsLetter(r) && !unicode.IsDigit(r) {
			return false
		}
	}
	return true
}

// nilIfEmpty 让"空数组"在往返后回到 nil，保证 GetChunk / GetDoc 的结果与写入的
// Chunk / Doc 可直接用 reflect.DeepEqual 比较。
func nilIfEmpty(v []string) []string {
	if len(v) == 0 {
		return nil
	}
	return v
}

// ---------------------------------------------------------------------------
// 写入
// ---------------------------------------------------------------------------

const insertChunkSQL = `INSERT INTO rag_chunks (
	id, doc_id, source, idx, text, text_tokens, modality, asset_url, locator,
	dynasty, place, topic, source_type, credibility,
	abilities, tags, hash, meta, embedding, created_at
) VALUES (
	$1, $2, $3, $4, $5, $6, $7, $8, $9,
	$10, $11, $12, $13, $14,
	$15, $16, $17, $18::jsonb, $19::vector, $20
)
ON CONFLICT DO NOTHING`

const insertDocSkeletonSQL = `INSERT INTO rag_docs (id, source, modality, status, updated_at)
VALUES ($1, $2, $3, 'pending', now())
ON CONFLICT (id) DO NOTHING`

// Add 幂等写入块与向量。
//
// 幂等靠三处唯一约束 + ON CONFLICT DO NOTHING：
//   - rag_chunks_pkey (id)
//   - rag_chunks_doc_idx_uniq (doc_id, idx)
//   - rag_chunks_hash_uniq_idx (hash) WHERE hash <> ”
//
// 任一冲突都静默跳过：不会产生重复行，也不会因为一条重复就让整批失败。
// 注意 DO NOTHING 意味着"同 key 不同内容"不会覆盖 —— 重新入库改了内容，
// 要先 RemoveBySource / RemoveDoc 清掉旧块（这正是 backend.go 里那两个方法的用途）。
//
// vectors 可以是 nil（只写块不写向量，此时 embedding 为 NULL，不会被稠密检索命中）；
// 非 nil 时长度必须与 chunks 一致。
func (s *Store) Add(ctx context.Context, chunks []types.Chunk, vectors [][]float32) error {
	if len(chunks) == 0 {
		return nil
	}
	if len(vectors) != 0 && len(vectors) != len(chunks) {
		return fmt.Errorf("pgvector: chunks(%d) 与 vectors(%d) 数量不一致", len(chunks), len(vectors))
	}
	for i := range chunks {
		if chunks[i].ID == "" {
			return fmt.Errorf("pgvector: 第 %d 个块缺少 id", i)
		}
		if len(vectors) > 0 && len(vectors[i]) != s.dim {
			return fmt.Errorf("pgvector: 块 %s 的向量维度 %d 与索引维度 %d 不一致",
				chunks[i].ID, len(vectors[i]), s.dim)
		}
	}

	// 先把参数全部编好，坏数据在拿连接之前就报错。
	argSets := make([][]any, len(chunks))
	for i := range chunks {
		args, err := s.chunkArgs(chunks[i], vectors, i)
		if err != nil {
			return err
		}
		argSets[i] = args
	}

	return s.withConn(ctx, func(c *pgx.Conn) error {
		if err := ensureDocRows(ctx, c, chunks); err != nil {
			return err
		}
		b := &pgx.Batch{}
		for i := range argSets {
			b.Queue(insertChunkSQL, argSets[i]...)
		}
		br := c.SendBatch(ctx, b)
		for range argSets {
			if _, err := br.Exec(); err != nil {
				_ = br.Close()
				return fmt.Errorf("pgvector: 写入块失败: %w", err)
			}
		}
		if err := br.Close(); err != nil {
			return fmt.Errorf("pgvector: 提交批量写入失败: %w", err)
		}
		return nil
	})
}

// chunkArgs 把一块 Chunk 编成 insertChunkSQL 的 20 个参数。
func (s *Store) chunkArgs(ch types.Chunk, vectors [][]float32, i int) ([]any, error) {
	var emb any // nil -> SQL NULL
	if len(vectors) > 0 && len(vectors[i]) > 0 {
		lit, err := vectorLiteral(vectors[i])
		if err != nil {
			return nil, fmt.Errorf("pgvector: 第 %d 个向量非法: %w", i, err)
		}
		emb = lit
	}

	meta := "{}"
	if len(ch.Meta) > 0 {
		rawMeta, err := json.Marshal(ch.Meta)
		if err != nil {
			return nil, fmt.Errorf("pgvector: 序列化块 %s 的 meta 失败: %w", ch.ID, err)
		}
		meta = string(rawMeta)
	}

	created := ch.CreatedAt
	if created.IsZero() {
		created = time.Now()
	}
	modality := string(ch.Modality)
	if modality == "" {
		modality = string(types.ModalityText)
	}

	return []any{
		ch.ID, ch.DocID, ch.Source, ch.Index, ch.Text, tokensOf(ch.Text), modality,
		ch.AssetURL, ch.Locator,
		ch.Dynasty, ch.Place, ch.Topic, ch.SourceType, ch.Credibility,
		nonNilStrings(ch.Abilities), nonNilStrings(ch.Tags), ch.Hash, meta, emb, created,
	}, nil
}

// nonNilStrings 把 nil 切片变成空切片：这些列是 NOT NULL DEFAULT '{}'，
// 而 pgx 会把 nil 切片编码成 NULL。
func nonNilStrings(v []string) []string {
	if v == nil {
		return []string{}
	}
	return v
}

// ensureDocRows 给还没登记过的 doc_id 补一行骨架，避免块指向一个不存在的文档
// （rag_docs 是文档登记表，ListDocs / Stats 都读它）。已存在的行不动 ——
// 完整元数据由 UpsertDoc 负责。
func ensureDocRows(ctx context.Context, c *pgx.Conn, chunks []types.Chunk) error {
	type skeleton struct{ source, modality string }
	seen := make(map[string]skeleton, len(chunks))
	order := make([]string, 0, len(chunks))
	for _, ch := range chunks {
		if ch.DocID == "" {
			continue
		}
		if _, ok := seen[ch.DocID]; ok {
			continue
		}
		mod := string(ch.Modality)
		if mod == "" {
			mod = string(types.ModalityText)
		}
		seen[ch.DocID] = skeleton{source: ch.Source, modality: mod}
		order = append(order, ch.DocID)
	}
	if len(order) == 0 {
		return nil
	}

	b := &pgx.Batch{}
	for _, id := range order {
		b.Queue(insertDocSkeletonSQL, id, seen[id].source, seen[id].modality)
	}
	br := c.SendBatch(ctx, b)
	for range order {
		if _, err := br.Exec(); err != nil {
			_ = br.Close()
			return fmt.Errorf("pgvector: 登记文档骨架失败: %w", err)
		}
	}
	return br.Close()
}

// ---------------------------------------------------------------------------
// 删除
// ---------------------------------------------------------------------------

// RemoveDoc 删除一个文档的全部块，返回删除条数。
func (s *Store) RemoveDoc(ctx context.Context, docID string) (int, error) {
	return s.deleteChunks(ctx, `DELETE FROM rag_chunks WHERE doc_id = $1`, docID)
}

// RemoveBySource 按来源相对路径删除（重新入库前清旧块），返回删除条数。
func (s *Store) RemoveBySource(ctx context.Context, source string) (int, error) {
	return s.deleteChunks(ctx, `DELETE FROM rag_chunks WHERE source = $1`, source)
}

func (s *Store) deleteChunks(ctx context.Context, sql string, arg any) (int, error) {
	n := 0
	err := s.withConn(ctx, func(c *pgx.Conn) error {
		tag, err := c.Exec(ctx, sql, arg)
		if err != nil {
			return err
		}
		n = int(tag.RowsAffected())
		return nil
	})
	if err != nil {
		return 0, err
	}
	return n, nil
}

// ---------------------------------------------------------------------------
// 检索
// ---------------------------------------------------------------------------

// DenseSearch 用余弦距离做稠密检索，score = 1 - distance（即余弦相似度）。
//
// 查询向量先做 L2 归一化（与 llm 侧 embedding.normalize 一致）；过滤条件由
// buildFilter 生成，语义与 types.Filter.Match 对齐。
func (s *Store) DenseSearch(ctx context.Context, vector []float32, k int, f types.Filter) ([]store.Candidate, error) {
	if k <= 0 || len(vector) == 0 {
		return []store.Candidate{}, nil
	}
	if len(vector) != s.dim {
		return nil, fmt.Errorf("pgvector: 查询向量维度 %d 与索引维度 %d 不一致", len(vector), s.dim)
	}
	q, err := vectorLiteral(normalizeL2(vector))
	if err != nil {
		return nil, fmt.Errorf("pgvector: 查询向量非法: %w", err)
	}

	// $1 = 查询向量，$2 = k，过滤条件从 $3 开始。
	where, fargs := buildFilter(f, 3)
	args := append([]any{q, k}, fargs...)

	sql := `SELECT id, 1 - (embedding <=> $1::vector) AS score
FROM rag_chunks
WHERE embedding IS NOT NULL`
	if where != "" {
		sql += " AND " + where
	}
	sql += `
ORDER BY embedding <=> $1::vector
LIMIT $2`

	return s.queryCandidates(ctx, sql, args...)
}

// SparseSearch 用预分词 + ts_rank_cd 做**近似 BM25** 的稀疏召回。
//
// 详见包注释：精确 BM25 由内置文件后端提供；这里的分数只用于排序，
// 绝对值没有意义。返回结果按分数降序，同分按 id 升序，保证可复现。
func (s *Store) SparseSearch(ctx context.Context, text string, k int, f types.Filter) ([]store.Candidate, error) {
	if k <= 0 {
		return []store.Candidate{}, nil
	}
	tokens := bm25.Tokenize(text)
	query := tsQueryFromTokens(tokens)
	if query == "" {
		// 查询里没有任何可用 token（空串 / 纯标点）：直接返回空，避免把
		// 空 tsquery 送进数据库。
		return []store.Candidate{}, nil
	}

	// 精度补丁：预分词会产生 unigram + bigram，只用 OR 匹配的话，
	// 「量子纠缠」会因为共享一个「量」字而命中「数量众多」这类无关块
	// （ts_rank_cd 没有 IDF，压不住这种噪声）。
	// 所以多字查询额外要求**至少命中一个 ≥2 字的 token**；单字查询
	// （例如查「佛」）没有长 token，则不加这条约束，保持可召回。
	longQuery := tsQueryFromTokens(longTokens(tokens))

	// $1 = 完整 tsquery，$2 = k，长 token 条件（若有）占用 $3，过滤条件依次后移。
	args := []any{query, k}
	nextParam := 3

	sql := `SELECT id, ts_rank_cd(to_tsvector('simple', text_tokens), to_tsquery('simple', $1)) AS score
FROM rag_chunks
WHERE to_tsvector('simple', text_tokens) @@ to_tsquery('simple', $1)`
	if longQuery != "" {
		sql += fmt.Sprintf("\n  AND to_tsvector('simple', text_tokens) @@ to_tsquery('simple', $%d)", nextParam)
		args = append(args, longQuery)
		nextParam++
	}

	where, fargs := buildFilter(f, nextParam)
	if where != "" {
		sql += " AND " + where
	}
	sql += `
ORDER BY score DESC, id ASC
LIMIT $2`
	args = append(args, fargs...)

	return s.queryCandidates(ctx, sql, args...)
}

func (s *Store) queryCandidates(ctx context.Context, sql string, args ...any) ([]store.Candidate, error) {
	out := []store.Candidate{}
	err := s.withConn(ctx, func(c *pgx.Conn) error {
		rows, err := c.Query(ctx, sql, args...)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var cand store.Candidate
			if err := rows.Scan(&cand.ChunkID, &cand.Score); err != nil {
				return err
			}
			out = append(out, cand)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// ---------------------------------------------------------------------------
// 块与文档的读取
// ---------------------------------------------------------------------------

const chunkColumns = `id, doc_id, source, idx, text, modality, asset_url, locator,
	dynasty, place, topic, source_type, credibility,
	abilities, tags, hash, meta, created_at`

const docColumns = `id, title, source, modality, size, chunks, assets, status, error,
	dynasty, place, topic, source_type, credibility,
	abilities, tags, fingerprint, updated_at`

// rowScanner 是 pgx.Row 的最小接口，方便 scanChunk / scanDoc 复用。
type rowScanner interface {
	Scan(dest ...any) error
}

// GetChunk 按 id 读一个块。
func (s *Store) GetChunk(ctx context.Context, id string) (types.Chunk, bool, error) {
	var (
		ch    types.Chunk
		found bool
	)
	err := s.withConn(ctx, func(c *pgx.Conn) error {
		row := c.QueryRow(ctx, `SELECT `+chunkColumns+` FROM rag_chunks WHERE id = $1`, id)
		err := scanChunk(row, &ch)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		found = true
		return nil
	})
	if err != nil {
		return types.Chunk{}, false, err
	}
	return ch, found, nil
}

func scanChunk(row rowScanner, ch *types.Chunk) error {
	var (
		modality string
		meta     []byte
	)
	if err := row.Scan(
		&ch.ID, &ch.DocID, &ch.Source, &ch.Index, &ch.Text, &modality,
		&ch.AssetURL, &ch.Locator,
		&ch.Dynasty, &ch.Place, &ch.Topic, &ch.SourceType, &ch.Credibility,
		&ch.Abilities, &ch.Tags, &ch.Hash, &meta, &ch.CreatedAt,
	); err != nil {
		return err
	}
	ch.Modality = types.Modality(modality)
	ch.Abilities = nilIfEmpty(ch.Abilities)
	ch.Tags = nilIfEmpty(ch.Tags)
	if len(meta) > 0 {
		var m map[string]string
		if err := json.Unmarshal(meta, &m); err != nil {
			return fmt.Errorf("pgvector: 解析块 %s 的 meta 失败: %w", ch.ID, err)
		}
		if len(m) > 0 {
			ch.Meta = m
		}
	}
	return nil
}

// HasHash 判断内容指纹是否已存在（入库去重用）。
//
// 空 hash 一律返回 false：它不是有效的去重键，rag_chunks_hash_uniq_idx 也是
// WHERE hash <> ” 的部分索引。
func (s *Store) HasHash(ctx context.Context, hash string) (bool, error) {
	if strings.TrimSpace(hash) == "" {
		return false, nil
	}
	var ok bool
	err := s.withConn(ctx, func(c *pgx.Conn) error {
		return c.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM rag_chunks WHERE hash = $1)`, hash).Scan(&ok)
	})
	if err != nil {
		return false, err
	}
	return ok, nil
}

const upsertDocSQL = `INSERT INTO rag_docs (
	id, title, source, modality, size, chunks, assets, status, error,
	dynasty, place, topic, source_type, credibility,
	abilities, tags, fingerprint, updated_at
) VALUES (
	$1, $2, $3, $4, $5, $6, $7, $8, $9,
	$10, $11, $12, $13, $14,
	$15, $16, $17, $18
)
ON CONFLICT (id) DO UPDATE SET
	title       = EXCLUDED.title,
	source      = EXCLUDED.source,
	modality    = EXCLUDED.modality,
	size        = EXCLUDED.size,
	chunks      = EXCLUDED.chunks,
	assets      = EXCLUDED.assets,
	status      = EXCLUDED.status,
	error       = EXCLUDED.error,
	dynasty     = EXCLUDED.dynasty,
	place       = EXCLUDED.place,
	topic       = EXCLUDED.topic,
	source_type = EXCLUDED.source_type,
	credibility = EXCLUDED.credibility,
	abilities   = EXCLUDED.abilities,
	tags        = EXCLUDED.tags,
	fingerprint = EXCLUDED.fingerprint,
	updated_at  = EXCLUDED.updated_at`

// UpsertDoc 写入 / 覆盖一份文档登记。
func (s *Store) UpsertDoc(ctx context.Context, doc types.Doc) error {
	if doc.ID == "" {
		return errors.New("pgvector: doc 缺少 id")
	}
	modality := string(doc.Modality)
	if modality == "" {
		modality = string(types.ModalityText)
	}
	status := doc.Status
	if status == "" {
		status = "pending" // indexed | failed | pending，见 docs/CONTRACT.md
	}
	updated := doc.UpdatedAt
	if updated.IsZero() {
		updated = time.Now()
	}
	return s.withConn(ctx, func(c *pgx.Conn) error {
		_, err := c.Exec(ctx, upsertDocSQL,
			doc.ID, doc.Title, doc.Source, modality, doc.Size, doc.Chunks,
			nonNilStrings(doc.Assets), status, doc.Error,
			doc.Dynasty, doc.Place, doc.Topic, doc.SourceType, doc.Credibility,
			nonNilStrings(doc.Abilities), nonNilStrings(doc.Tags), doc.Fingerprint, updated)
		if err != nil {
			return fmt.Errorf("pgvector: 写入文档 %s 失败: %w", doc.ID, err)
		}
		return nil
	})
}

func (s *Store) ReplaceDocument(ctx context.Context, doc types.Doc, chunks []types.Chunk, vectors [][]float32) (int, error) {
	if doc.ID == "" || len(chunks) != len(vectors) {
		return 0, errors.New("pgvector: 文档或向量数量非法")
	}
	args := make([][]any, len(chunks))
	for i, c := range chunks {
		if c.DocID != doc.ID || len(vectors[i]) != s.dim {
			return 0, errors.New("pgvector: 块归属或向量维度非法")
		}
		var err error
		args[i], err = s.chunkArgs(c, vectors, i)
		if err != nil {
			return 0, err
		}
	}
	n := 0
	err := s.withConn(ctx, func(c *pgx.Conn) error {
		tx, err := c.Begin(ctx)
		if err != nil {
			return err
		}
		defer tx.Rollback(ctx)
		tag, err := tx.Exec(ctx, "DELETE FROM rag_chunks WHERE doc_id=$1", doc.ID)
		if err != nil {
			return err
		}
		n = int(tag.RowsAffected())
		_, err = tx.Exec(ctx, upsertDocSQL, doc.ID, doc.Title, doc.Source, string(doc.Modality), doc.Size, doc.Chunks,
			nonNilStrings(doc.Assets), doc.Status, doc.Error, doc.Dynasty, doc.Place, doc.Topic, doc.SourceType, doc.Credibility,
			nonNilStrings(doc.Abilities), nonNilStrings(doc.Tags), doc.Fingerprint, doc.UpdatedAt)
		if err != nil {
			return err
		}
		for _, values := range args {
			if _, err := tx.Exec(ctx, insertChunkSQL, values...); err != nil {
				return err
			}
		}
		return tx.Commit(ctx)
	})
	return n, err
}

// DeleteDoc 删除一份文档登记以及它的全部块。
//
// 两块表之间没有外键（见 deploy/schema.sql 的说明），所以这里显式在一个事务里
// 先删块再删文档，保证"删文档 = 索引里不再有它的块"。
func (s *Store) DeleteDoc(ctx context.Context, docID string) error {
	return s.withConn(ctx, func(c *pgx.Conn) error {
		tx, err := c.Begin(ctx)
		if err != nil {
			return fmt.Errorf("pgvector: 开启事务失败: %w", err)
		}
		defer func() { _ = tx.Rollback(ctx) }()

		if _, err := tx.Exec(ctx, `DELETE FROM rag_chunks WHERE doc_id = $1`, docID); err != nil {
			return fmt.Errorf("pgvector: 删除文档 %s 的块失败: %w", docID, err)
		}
		if _, err := tx.Exec(ctx, `DELETE FROM rag_docs WHERE id = $1`, docID); err != nil {
			return fmt.Errorf("pgvector: 删除文档 %s 失败: %w", docID, err)
		}
		return tx.Commit(ctx)
	})
}

// GetDoc 按 id 读一份文档登记。
func (s *Store) GetDoc(ctx context.Context, id string) (types.Doc, bool, error) {
	var (
		doc   types.Doc
		found bool
	)
	err := s.withConn(ctx, func(c *pgx.Conn) error {
		row := c.QueryRow(ctx, `SELECT `+docColumns+` FROM rag_docs WHERE id = $1`, id)
		err := scanDoc(row, &doc)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		found = true
		return nil
	})
	if err != nil {
		return types.Doc{}, false, err
	}
	return doc, found, nil
}

// ListDocs 返回全部文档登记，按 updated_at 倒序、id 升序（稳定可复现）。
func (s *Store) ListDocs(ctx context.Context) ([]types.Doc, error) {
	out := []types.Doc{}
	err := s.withConn(ctx, func(c *pgx.Conn) error {
		rows, err := c.Query(ctx, `SELECT `+docColumns+` FROM rag_docs ORDER BY updated_at DESC, id ASC`)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var doc types.Doc
			if err := scanDoc(rows, &doc); err != nil {
				return err
			}
			out = append(out, doc)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

func scanDoc(row rowScanner, d *types.Doc) error {
	var modality string
	if err := row.Scan(
		&d.ID, &d.Title, &d.Source, &modality, &d.Size, &d.Chunks, &d.Assets, &d.Status, &d.Error,
		&d.Dynasty, &d.Place, &d.Topic, &d.SourceType, &d.Credibility,
		&d.Abilities, &d.Tags, &d.Fingerprint, &d.UpdatedAt,
	); err != nil {
		return err
	}
	d.Modality = types.Modality(modality)
	d.Assets = nilIfEmpty(d.Assets)
	d.Abilities = nilIfEmpty(d.Abilities)
	d.Tags = nilIfEmpty(d.Tags)
	return nil
}

// Stats 汇总索引统计（块数 / 文档数 / 维度 / 各模态分布 / 资产数）。
func (s *Store) Stats(ctx context.Context) (types.Stats, error) {
	st := types.Stats{
		Backend:    BackendName,
		Dim:        s.dim,
		Modalities: map[string]int{},
	}
	err := s.withConn(ctx, func(c *pgx.Conn) error {
		if err := c.QueryRow(ctx, `SELECT
	(SELECT count(*) FROM rag_chunks),
	(SELECT count(*) FROM rag_docs),
	(SELECT count(DISTINCT asset_url) FROM rag_chunks
	  WHERE modality = 'image' AND asset_url <> '')`).Scan(&st.Chunks, &st.Docs, &st.Assets); err != nil {
			return err
		}
		rows, err := c.Query(ctx, `SELECT modality, count(*) FROM rag_chunks GROUP BY modality ORDER BY modality`)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var (
				modality string
				n        int
			)
			if err := rows.Scan(&modality, &n); err != nil {
				return err
			}
			st.Modalities[modality] = n
		}
		if err := rows.Err(); err != nil {
			return err
		}
		rows.Close()
		return c.QueryRow(ctx, `SELECT
		ARRAY(SELECT DISTINCT dynasty FROM rag_chunks WHERE dynasty <> '' ORDER BY 1),
		ARRAY(SELECT DISTINCT place FROM rag_chunks WHERE place <> '' ORDER BY 1),
		ARRAY(SELECT DISTINCT topic FROM rag_chunks WHERE topic <> '' ORDER BY 1),
		ARRAY(SELECT DISTINCT source_type FROM rag_chunks WHERE source_type <> '' ORDER BY 1),
		ARRAY(SELECT DISTINCT credibility FROM rag_chunks WHERE credibility <> '' ORDER BY 1),
		ARRAY(SELECT DISTINCT unnest(abilities) AS ability FROM rag_chunks ORDER BY ability)`).Scan(
			&st.Facets.Dynasty, &st.Facets.Place, &st.Facets.Topic, &st.Facets.SourceType, &st.Facets.Credibility, &st.Facets.Abilities)
	})
	if err != nil {
		return types.Stats{}, err
	}
	return st, nil
}
