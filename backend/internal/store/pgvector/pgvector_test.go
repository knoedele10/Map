package pgvector

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"ragkb/internal/types"
)

// testDim 与 deploy/schema.sql 里 rag_chunks.embedding 的 vector(1024) 必须一致。
const testDim = 1024

// ===========================================================================
// 纯逻辑单元测试（不需要数据库）
// ===========================================================================

func TestOpenRejectsBadArgs(t *testing.T) {
	ctx := context.Background()
	if _, err := Open(ctx, "   ", 1024); err == nil {
		t.Fatal("空 dsn 应该报错")
	}
	if _, err := Open(ctx, "postgres://localhost/db", 0); err == nil {
		t.Fatal("dim<=0 应该报错")
	}
	if _, err := Open(ctx, "postgres://localhost/db", -3); err == nil {
		t.Fatal("dim<0 应该报错")
	}
	if _, err := Open(ctx, ":// 不是 dsn", 1024); err == nil {
		t.Fatal("非法 dsn 应该报错")
	}
}

func TestNormalizeL2(t *testing.T) {
	got := normalizeL2([]float32{3, 4})
	want := []float32{0.6, 0.8}
	for i := range want {
		if diff := got[i] - want[i]; diff > 1e-6 || diff < -1e-6 {
			t.Fatalf("normalizeL2([3,4]) = %v，期望 %v", got, want)
		}
	}

	// 归一化后模长应该是 1。
	var sum float64
	for _, x := range got {
		sum += float64(x) * float64(x)
	}
	if sum < 0.999999 || sum > 1.000001 {
		t.Fatalf("归一化后模长平方 = %v，期望 1", sum)
	}

	// 不修改入参。
	in := []float32{3, 4}
	_ = normalizeL2(in)
	if in[0] != 3 || in[1] != 4 {
		t.Fatalf("normalizeL2 修改了入参: %v", in)
	}

	// 零向量原样返回，不产生 NaN。
	zero := normalizeL2([]float32{0, 0, 0})
	if len(zero) != 3 || zero[0] != 0 || zero[1] != 0 || zero[2] != 0 {
		t.Fatalf("零向量应原样返回，得到 %v", zero)
	}
}

func TestVectorLiteral(t *testing.T) {
	lit, err := vectorLiteral([]float32{1, 0.5, -2.25})
	if err != nil {
		t.Fatalf("vectorLiteral 报错: %v", err)
	}
	if lit != "[1,0.5,-2.25]" {
		t.Fatalf("vectorLiteral = %q，期望 %q", lit, "[1,0.5,-2.25]")
	}

	if _, err := vectorLiteral(nil); err == nil {
		t.Fatal("空向量应该报错")
	}
	nan := float32(0)
	nan = float32(0) / nan // NaN
	if _, err := vectorLiteral([]float32{nan}); err == nil {
		t.Fatal("NaN 应该报错")
	}
	inf := float32(1)
	inf = inf / float32(0) // +Inf
	if _, err := vectorLiteral([]float32{inf}); err == nil {
		t.Fatal("Inf 应该报错")
	}
}

func TestParseVectorDim(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want int
		ok   bool
	}{
		{"vector(1024)", 1024, true},
		{" vector(768) ", 768, true},
		{"vector", 0, false},
		{"vector()", 0, false},
		{"vector(0)", 0, false},
		{"vector(abc)", 0, false},
		{"text", 0, false},
		{"", 0, false},
	} {
		got, err := parseVectorDim(tc.in)
		if tc.ok {
			if err != nil || got != tc.want {
				t.Fatalf("parseVectorDim(%q) = (%d, %v)，期望 (%d, nil)", tc.in, got, err, tc.want)
			}
			continue
		}
		if err == nil {
			t.Fatalf("parseVectorDim(%q) 应该报错，得到 %d", tc.in, got)
		}
	}
}

func TestTSQueryFromTokens(t *testing.T) {
	// OR 表达式 + 去重。
	got := tsQueryFromTokens([]string{"乐", "山", "乐"})
	if got != "'乐' | '山'" {
		t.Fatalf("tsQueryFromTokens = %q，期望 %q", got, "'乐' | '山'")
	}
	// 大写会被小写化（bm25.Tokenize 已经小写，这里再兜一层）。
	if got := tsQueryFromTokens([]string{"Han"}); got != "'han'" {
		t.Fatalf("tsQueryFromTokens = %q，期望 %q", got, "'han'")
	}
	// 非字母数字汉字的 token 直接丢弃：即使有奇怪输入也不会进 tsquery 语法。
	if got := tsQueryFromTokens([]string{"a'b", "a|b", "x:y", "好"}); got != "'好'" {
		t.Fatalf("tsQueryFromTokens = %q，期望 %q", got, "'好'")
	}
	if got := tsQueryFromTokens(nil); got != "" {
		t.Fatalf("空 token 列表应得到空串，得到 %q", got)
	}
}

func TestTokensOf(t *testing.T) {
	got := tokensOf("乐山大佛")
	// 预分词结果必须是空格连接的：这样 to_tsvector('simple', ...) 才会
	// 把它们当成独立的 lexeme，而不是一整个"乐山大佛"。
	if !strings.Contains(got, " ") {
		t.Fatalf("tokensOf 应该用空格连接 token，得到 %q", got)
	}
	for _, want := range []string{"乐", "山", "大", "佛", "乐山", "大佛"} {
		if !strings.Contains(" "+got+" ", " "+want+" ") {
			t.Fatalf("tokensOf(%q) = %q，缺少 token %q", "乐山大佛", got, want)
		}
	}
}

func TestBuildFilter(t *testing.T) {
	// 空过滤：不产生任何条件。
	if clause, args := buildFilter(types.Filter{}, 3); clause != "" || len(args) != 0 {
		t.Fatalf("空过滤应该得到空条件，得到 %q / %v", clause, args)
	}

	// 占位符序号从 next 开始，且值都在 args 里。
	clause, args := buildFilter(types.Filter{DocIDs: []string{"d1"}}, 3)
	if clause != "lower(btrim(doc_id)) = ANY($3::text[])" {
		t.Fatalf("doc_ids 条件 = %q", clause)
	}
	if len(args) != 1 || !reflect.DeepEqual(args[0], []string{"d1"}) {
		t.Fatalf("doc_ids 参数 = %v", args)
	}

	// 模态是精确比较，不做 trim / 折叠。
	clause, args = buildFilter(types.Filter{Modalities: []types.Modality{types.ModalityText}}, 1)
	if clause != "modality = ANY($1::text[])" {
		t.Fatalf("modalities 条件 = %q", clause)
	}
	if !reflect.DeepEqual(args[0], []string{"text"}) {
		t.Fatalf("modalities 参数 = %v", args[0])
	}

	// 标量维度会 trim + 小写。
	clause, args = buildFilter(types.Filter{Place: []string{" Leshan "}}, 1)
	if !reflect.DeepEqual(args[0], []string{"leshan"}) {
		t.Fatalf("place 参数应被 trim+小写，得到 %v", args[0])
	}

	// abilities 门禁：空数组的块必须始终可检索 —— SQL 里必须有 cardinality(...)=0。
	clause, _ = buildFilter(types.Filter{Abilities: []string{"水利"}}, 1)
	if !strings.Contains(clause, "cardinality(abilities) = 0") {
		t.Fatalf("abilities 条件缺少「空数组始终可检索」分支: %q", clause)
	}
	// tags 没有这个豁免：空 tags 的块不匹配。
	clause, _ = buildFilter(types.Filter{Tags: []string{"石刻"}}, 1)
	if strings.Contains(clause, "cardinality(tags) = 0") {
		t.Fatalf("tags 不应有空数组豁免: %q", clause)
	}

	// 全维度：9 个条件用 AND 连接。
	clause, args = buildFilter(types.Filter{
		DocIDs:      []string{"d1"},
		Modalities:  []types.Modality{types.ModalityText},
		Dynasty:     []string{"唐"},
		Place:       []string{"乐山"},
		Topic:       []string{"水利"},
		SourceType:  []string{"地方志"},
		Credibility: []string{"high"},
		Abilities:   []string{"水利"},
		Tags:        []string{"石刻"},
	}, 1)
	if n := strings.Count(clause, " AND "); n != 8 {
		t.Fatalf("全维度过滤应该有 9 个条件（8 个 AND），得到 %d: %q", n, clause)
	}
	if len(args) != 9 {
		t.Fatalf("全维度过滤应该有 9 个参数，得到 %d", len(args))
	}
	// 参数序号必须连续：$1..$9。
	for i := 1; i <= 9; i++ {
		if !strings.Contains(clause, "$"+strconv.Itoa(i)) {
			t.Fatalf("条件里缺少占位符 $%d: %q", i, clause)
		}
	}
}

// TestBuildFilterNeverInlinesUserInput 是本包的安全底线：
// 用户输入只能出现在参数里，绝不能出现在 SQL 文本里。
func TestBuildFilterNeverInlinesUserInput(t *testing.T) {
	evil := []string{"唐'; DROP TABLE rag_chunks; --"}
	clause, args := buildFilter(types.Filter{
		Dynasty:   evil,
		Place:     evil,
		Topic:     evil,
		Tags:      evil,
		Abilities: evil,
	}, 1)

	if strings.Contains(clause, "DROP TABLE") || strings.Contains(clause, "'") {
		t.Fatalf("用户输入被拼进了 SQL 文本: %q", clause)
	}
	if len(args) != 5 {
		t.Fatalf("应该只有 5 个参数，得到 %d", len(args))
	}
	// 值会被 trim + 小写（为了和 containsFold 的 EqualFold 对齐），
	// 但必须原样出现在参数里，而不是 SQL 文本里。
	want := strings.ToLower(strings.TrimSpace(evil[0]))
	for _, a := range args {
		vals, ok := a.([]string)
		if !ok || len(vals) != 1 || vals[0] != want {
			t.Fatalf("参数内容不对: %#v（期望 %q）", a, want)
		}
	}
}

// TestAddValidatesBeforeTouchingDB 用 nil 连接池构造 Store：所有校验都必须发生在
// 借连接之前，否则这里会 panic。
func TestAddValidatesBeforeTouchingDB(t *testing.T) {
	ctx := context.Background()
	s := &Store{dim: 4} // pool 故意为 nil

	if err := s.Add(ctx, nil, nil); err != nil {
		t.Fatalf("空输入应该直接返回 nil，得到 %v", err)
	}
	if err := s.Add(ctx, []types.Chunk{{ID: "x"}}, [][]float32{{1, 2, 3}}); err == nil {
		t.Fatal("向量维度不一致应该报错")
	}
	if err := s.Add(ctx, []types.Chunk{{ID: "x"}}, [][]float32{{1, 2, 3, 4}, {1, 2, 3, 4}}); err == nil {
		t.Fatal("chunks 与 vectors 数量不一致应该报错")
	}
	if err := s.Add(ctx, []types.Chunk{{}}, nil); err == nil {
		t.Fatal("缺少 id 应该报错")
	}
	if err := s.UpsertDoc(ctx, types.Doc{}); err == nil {
		t.Fatal("doc 缺少 id 应该报错")
	}
	if _, err := s.DenseSearch(ctx, []float32{1, 2, 3}, 5, types.Filter{}); err == nil {
		t.Fatal("查询向量维度不一致应该报错")
	}
	if _, err := s.DenseSearch(ctx, nil, 5, types.Filter{}); err != nil {
		t.Fatalf("空查询向量应该返回空结果，得到 %v", err)
	}
	if got, err := s.SparseSearch(ctx, "   ", 5, types.Filter{}); err != nil || len(got) != 0 {
		t.Fatalf("纯标点查询应该返回空，得到 %v / %v", got, err)
	}
	if got, err := s.SparseSearch(ctx, "", 5, types.Filter{}); err != nil || len(got) != 0 {
		t.Fatalf("空查询应该返回空，得到 %v / %v", got, err)
	}
}

// ===========================================================================
// 集成测试（需要 RAG_TEST_PG_DSN）
//
//	$env:RAG_TEST_PG_DSN="postgres://postgres:rag@127.0.0.1:55432/rag?sslmode=disable"
//	go test ./internal/store/pgvector/... -count=1 -v
// ===========================================================================

// seedTime 用微秒精度：PostgreSQL 的 timestamptz 只到微秒，纳秒部分会被截断。
var seedTime = time.Date(2026, 10, 10, 13, 22, 0, 123456000, time.UTC)

// seedChunks 覆盖各种过滤组合：有/无 abilities、有/无 tags、三种模态、大小写。
func seedChunks() []types.Chunk {
	return []types.Chunk{
		{
			ID: "c1", DocID: "d1", Source: "四川地方志/乐山大佛.md", Index: 0,
			Text:     "乐山大佛通高71米，开凿于唐开元元年，由海通禅师主持。",
			Modality: types.ModalityText, Locator: "第 1 块",
			Dynasty: "唐", Place: "乐山", Topic: "水利", SourceType: "地方志", Credibility: "high",
			Abilities: []string{"水利"}, Tags: []string{"大佛", "石刻"},
			Hash: "hash-c1", Meta: map[string]string{"page": "2"}, CreatedAt: seedTime,
		},
		{
			ID: "c2", DocID: "d1", Source: "四川地方志/乐山大佛.md", Index: 1,
			Text:     "杜甫在成都草堂写下《春夜喜雨》。",
			Modality: types.ModalityText,
			Dynasty:  "唐", Place: "成都", Topic: "诗词", SourceType: "正史", Credibility: "medium",
			Abilities: []string{"诗词"}, Tags: []string{"诗歌"},
			Hash: "hash-c2", CreatedAt: seedTime,
		},
		{
			ID: "c3", DocID: "d2", Source: "水利/都江堰.md", Index: 0,
			Text:     "都江堰是战国时期秦国蜀郡太守李冰主持修建的水利工程。",
			Modality: types.ModalityText,
			Dynasty:  "战国", Place: "成都", Topic: "水利", SourceType: "地方志", Credibility: "high",
			// abilities / tags 都留空：通用内容，abilities 过滤时必须始终可检索。
			Hash: "hash-c3", CreatedAt: seedTime,
		},
		{
			ID: "c4", DocID: "d2", Source: "水利/都江堰.md", Index: 1,
			Text:     "乐山大佛景区内的石刻造像数量众多。",
			Modality: types.ModalityTable,
			Dynasty:  "唐", Place: "乐山", Topic: "文物", SourceType: "博物馆", Credibility: "medium",
			Abilities: []string{"水利", "文物"}, Tags: []string{"石刻"},
			Hash: "hash-c4", CreatedAt: seedTime,
		},
		{
			ID: "c5", DocID: "d3", Source: "图片/大佛.md", Index: 0,
			Text:     "乐山大佛的图片资料。",
			Modality: types.ModalityImage, AssetURL: "/api/v1/assets/d3/leshan.png",
			Dynasty: "唐", Place: "乐山", Topic: "文物", SourceType: "博物馆", Credibility: "low",
			Abilities: []string{"文物"}, Tags: []string{"图片"},
			Hash: "hash-c5", CreatedAt: seedTime,
		},
		{
			ID: "c6", DocID: "d3", Source: "图片/大佛.md", Index: 1,
			Text:     "宋代杭州西湖的疏浚工程。",
			Modality: types.ModalityText,
			// 故意用小写 ASCII：用来验证 SQL 过滤和 Filter.Match 都做大小写折叠。
			Dynasty: "宋", Place: "hangzhou", Topic: "水利", SourceType: "正史", Credibility: "low",
			Hash: "hash-c6", CreatedAt: seedTime,
		},
	}
}

func seedDocs() []types.Doc {
	return []types.Doc{
		{
			ID: "d1", Title: "乐山大佛", Source: "四川地方志/乐山大佛.md",
			Modality: types.ModalityText, Size: 4821, Chunks: 2, Status: "indexed",
			Dynasty: "唐", Place: "乐山", Topic: "水利", SourceType: "地方志", Credibility: "high",
			Abilities: []string{"水利"}, Tags: []string{"大佛"},
			Fingerprint: "fp-d1", UpdatedAt: seedTime,
		},
		{
			ID: "d2", Title: "都江堰", Source: "水利/都江堰.md",
			Modality: types.ModalityText, Size: 2048, Chunks: 2, Status: "indexed",
			Dynasty: "战国", Place: "成都", Topic: "水利", SourceType: "地方志", Credibility: "high",
			Fingerprint: "fp-d2", UpdatedAt: seedTime.Add(time.Minute),
		},
	}
}

// unitVector 返回第 i 个标准基向量（已归一化）。
func unitVector(i int) []float32 {
	v := make([]float32, testDim)
	v[i%testDim] = 1
	return v
}

func seedVectors() [][]float32 {
	v := make([][]float32, len(seedChunks()))
	for i := range v {
		v[i] = unitVector(i)
	}
	return v
}

func testDSN(t *testing.T) string {
	t.Helper()
	dsn := os.Getenv("RAG_TEST_PG_DSN")
	if dsn == "" {
		t.Skip("未设置 RAG_TEST_PG_DSN，跳过 pgvector 集成测试（CI 友好）")
	}
	return dsn
}

func schemaPath(t *testing.T) string {
	t.Helper()
	p := filepath.Join("..", "..", "..", "deploy", "schema.sql")
	if _, err := os.Stat(p); err != nil {
		t.Fatalf("找不到 deploy/schema.sql（期望在 %s）: %v", p, err)
	}
	return p
}

// applySchema 用裸连接执行 schema.sql。
// 必须先建表再 Open：Open 会拿 rag_chunks.embedding 的真实维度做校验。
func applySchema(ctx context.Context, t *testing.T, dsn string) {
	t.Helper()
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatalf("连接测试库失败: %v", err)
	}
	defer func() { _ = conn.Close(ctx) }()

	ddl, err := os.ReadFile(schemaPath(t))
	if err != nil {
		t.Fatalf("读取 deploy/schema.sql 失败: %v", err)
	}
	// pgx 在无参数时走简单查询协议，所以可以一次发多条 DDL。
	if _, err := conn.Exec(ctx, string(ddl)); err != nil {
		t.Fatalf("执行 deploy/schema.sql 失败: %v", err)
	}
}

func newTestStore(t *testing.T) (*Store, context.Context) {
	t.Helper()
	dsn := testDSN(t)
	ctx := context.Background()
	applySchema(ctx, t, dsn)

	s, err := Open(ctx, dsn, testDim)
	if err != nil {
		t.Fatalf("Open 失败: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })

	if err := s.withConn(ctx, func(c *pgx.Conn) error {
		_, err := c.Exec(ctx, "TRUNCATE rag_chunks, rag_docs RESTART IDENTITY")
		return err
	}); err != nil {
		t.Fatalf("清空表失败: %v", err)
	}
	return s, ctx
}

func mustAdd(t *testing.T, s *Store, ctx context.Context, chunks []types.Chunk, vectors [][]float32) {
	t.Helper()
	if err := s.Add(ctx, chunks, vectors); err != nil {
		t.Fatalf("Add 失败: %v", err)
	}
}

func countChunks(t *testing.T, s *Store, ctx context.Context) int {
	t.Helper()
	n := 0
	if err := s.withConn(ctx, func(c *pgx.Conn) error {
		return c.QueryRow(ctx, "SELECT count(*) FROM rag_chunks").Scan(&n)
	}); err != nil {
		t.Fatalf("统计块数失败: %v", err)
	}
	return n
}

func allChunks(t *testing.T, s *Store, ctx context.Context) []types.Chunk {
	t.Helper()
	out := []types.Chunk{}
	if err := s.withConn(ctx, func(c *pgx.Conn) error {
		rows, err := c.Query(ctx, "SELECT "+chunkColumns+" FROM rag_chunks ORDER BY id")
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var ch types.Chunk
			if err := scanChunk(rows, &ch); err != nil {
				return err
			}
			out = append(out, ch)
		}
		return rows.Err()
	}); err != nil {
		t.Fatalf("读取全部块失败: %v", err)
	}
	return out
}

// selectIDsByFilter 直接用 buildFilter 生成的条件查库，用于和 Filter.Match 对比。
func selectIDsByFilter(t *testing.T, s *Store, ctx context.Context, f types.Filter) []string {
	t.Helper()
	clause, args := buildFilter(f, 1)
	q := "SELECT id FROM rag_chunks"
	if clause != "" {
		q += " WHERE " + clause
	}
	q += " ORDER BY id"

	out := []string{}
	if err := s.withConn(ctx, func(c *pgx.Conn) error {
		rows, err := c.Query(ctx, q, args...)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				return err
			}
			out = append(out, id)
		}
		return rows.Err()
	}); err != nil {
		t.Fatalf("SQL 过滤查询失败: %v", err)
	}
	return out
}

func TestIntegrationSchemaObjects(t *testing.T) {
	s, ctx := newTestStore(t)

	var tables []string
	if err := s.withConn(ctx, func(c *pgx.Conn) error {
		rows, err := c.Query(ctx, "SELECT tablename FROM pg_tables WHERE schemaname = current_schema() ORDER BY tablename")
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var name string
			if err := rows.Scan(&name); err != nil {
				return err
			}
			tables = append(tables, name)
		}
		return rows.Err()
	}); err != nil {
		t.Fatalf("查询表清单失败: %v", err)
	}
	for _, want := range []string{"rag_docs", "rag_chunks", "sites", "fragments", "conversations", "messages"} {
		if !contains(tables, want) {
			t.Errorf("schema.sql 没有建出表 %s（实际有 %v）", want, tables)
		}
	}

	var indexes []string
	if err := s.withConn(ctx, func(c *pgx.Conn) error {
		rows, err := c.Query(ctx, "SELECT indexname FROM pg_indexes WHERE schemaname = current_schema()")
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var name string
			if err := rows.Scan(&name); err != nil {
				return err
			}
			indexes = append(indexes, name)
		}
		return rows.Err()
	}); err != nil {
		t.Fatalf("查询索引清单失败: %v", err)
	}
	for _, want := range []string{
		"rag_chunks_embedding_hnsw_idx",
		"rag_chunks_hash_uniq_idx",
		"rag_chunks_source_idx",
		"rag_chunks_abilities_gin_idx",
		"rag_chunks_tags_gin_idx",
		"rag_chunks_tokens_gin_idx",
		"rag_chunks_doc_idx_uniq",
	} {
		if !contains(indexes, want) {
			t.Errorf("schema.sql 没有建出索引 %s（实际有 %v）", want, indexes)
		}
	}

	var ext string
	if err := s.withConn(ctx, func(c *pgx.Conn) error {
		return c.QueryRow(ctx, "SELECT extname FROM pg_extension WHERE extname = 'vector'").Scan(&ext)
	}); err != nil {
		t.Errorf("vector 扩展没有安装: %v", err)
	}
}

func TestIntegrationOpenDimMismatch(t *testing.T) {
	dsn := testDSN(t)
	ctx := context.Background()
	applySchema(ctx, t, dsn)

	if _, err := Open(ctx, dsn, 768); err == nil {
		t.Fatal("dim 与 schema 不一致时 Open 必须报错")
	} else {
		t.Logf("维度不一致时的报错（符合预期）: %v", err)
	}
}

func TestIntegrationChunkAndDocRoundTrip(t *testing.T) {
	s, ctx := newTestStore(t)
	chunks := seedChunks()

	for _, d := range seedDocs() {
		if err := s.UpsertDoc(ctx, d); err != nil {
			t.Fatalf("UpsertDoc(%s) 失败: %v", d.ID, err)
		}
	}
	mustAdd(t, s, ctx, chunks, seedVectors())

	// 块往返。
	got, ok, err := s.GetChunk(ctx, "c1")
	if err != nil || !ok {
		t.Fatalf("GetChunk(c1) = (%v, %v, %v)", got, ok, err)
	}
	want := chunks[0]
	if !got.CreatedAt.Equal(want.CreatedAt) {
		t.Errorf("created_at 往返不一致: got %v want %v", got.CreatedAt, want.CreatedAt)
	}
	got.CreatedAt, want.CreatedAt = time.Time{}, time.Time{}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("块往返不一致:\n got = %#v\nwant = %#v", got, want)
	}

	if _, ok, err := s.GetChunk(ctx, "不存在"); err != nil || ok {
		t.Fatalf("不存在的块应该返回 (_, false, nil)，得到 (_, %v, %v)", ok, err)
	}

	// 文档往返。
	doc, ok, err := s.GetDoc(ctx, "d1")
	if err != nil || !ok {
		t.Fatalf("GetDoc(d1) = (%v, %v, %v)", doc, ok, err)
	}
	wantDoc := seedDocs()[0]
	if !doc.UpdatedAt.Equal(wantDoc.UpdatedAt) {
		t.Errorf("updated_at 往返不一致: got %v want %v", doc.UpdatedAt, wantDoc.UpdatedAt)
	}
	doc.UpdatedAt, wantDoc.UpdatedAt = time.Time{}, time.Time{}
	if !reflect.DeepEqual(doc, wantDoc) {
		t.Errorf("文档往返不一致:\n got = %#v\nwant = %#v", doc, wantDoc)
	}

	docs, err := s.ListDocs(ctx)
	if err != nil {
		t.Fatalf("ListDocs 失败: %v", err)
	}
	// seedChunks() 里引用了 d3，而 Add 会给缺失的 doc_id 补一行骨架
	// （status=pending，等 UpsertDoc 覆盖）——这是"不建外键"设计的配套行为，
	// 所以这里应该是 3 份文档，不是 2 份。
	if len(docs) != 3 {
		t.Fatalf("ListDocs 应该返回 3 份文档（d1/d2 + Add 补的 d3 骨架），得到 %d", len(docs))
	}
	pos := map[string]int{}
	for i, d := range docs {
		pos[d.ID] = i
	}
	if _, ok := pos["d3"]; !ok {
		t.Error("Add 应为 chunk 里引用的、尚未登记的 doc_id 补一行骨架文档")
	}
	// 按 updated_at 倒序：d2 比 d1 晚。
	if pos["d2"] > pos["d1"] {
		t.Errorf("ListDocs 应按 updated_at 倒序（d2 在 d1 之前），实际顺序 %v", pos)
	}

	// HasHash。
	if ok, err := s.HasHash(ctx, "hash-c1"); err != nil || !ok {
		t.Errorf("HasHash(hash-c1) = (%v, %v)，期望 true", ok, err)
	}
	if ok, err := s.HasHash(ctx, "没有这个 hash"); err != nil || ok {
		t.Errorf("HasHash(未知) = (%v, %v)，期望 false", ok, err)
	}
	if ok, err := s.HasHash(ctx, ""); err != nil || ok {
		t.Errorf("HasHash(\"\") = (%v, %v)，期望 false", ok, err)
	}
}

func TestIntegrationDenseSearch(t *testing.T) {
	s, ctx := newTestStore(t)
	mustAdd(t, s, ctx, seedChunks(), seedVectors())

	// 查询向量 = c1 的向量，c1 必须排第一，余弦相似度接近 1。
	cands, err := s.DenseSearch(ctx, unitVector(0), 10, types.Filter{})
	if err != nil {
		t.Fatalf("DenseSearch 失败: %v", err)
	}
	if len(cands) != len(seedChunks()) {
		t.Fatalf("应该召回全部 %d 个块，得到 %d", len(seedChunks()), len(cands))
	}
	if cands[0].ChunkID != "c1" {
		t.Fatalf("最近邻应该是 c1，得到 %v", cands)
	}
	if cands[0].Score < 0.999 {
		t.Fatalf("c1 的余弦相似度应该接近 1，得到 %v", cands[0].Score)
	}
	for i := 1; i < len(cands); i++ {
		if cands[i].Score > cands[i-1].Score {
			t.Fatalf("稠密结果没有按分数降序: %v", cands)
		}
	}

	// 未归一化的查询向量：归一化后结果必须一样（余弦对缩放不敏感）。
	scaled := unitVector(0)
	for i := range scaled {
		scaled[i] *= 7.5
	}
	scaledCands, err := s.DenseSearch(ctx, scaled, 10, types.Filter{})
	if err != nil {
		t.Fatalf("DenseSearch（未归一化查询）失败: %v", err)
	}
	if len(scaledCands) != len(cands) || scaledCands[0].ChunkID != cands[0].ChunkID {
		t.Fatalf("归一化前后的结果不一致: %v vs %v", scaledCands, cands)
	}
	if diff := scaledCands[0].Score - cands[0].Score; diff > 1e-5 || diff < -1e-5 {
		t.Fatalf("归一化前后的分数不一致: %v vs %v", scaledCands[0].Score, cands[0].Score)
	}

	// k 限制生效。
	top2, err := s.DenseSearch(ctx, unitVector(0), 2, types.Filter{})
	if err != nil {
		t.Fatalf("DenseSearch(k=2) 失败: %v", err)
	}
	if len(top2) != 2 {
		t.Fatalf("k=2 应该只返回 2 条，得到 %d", len(top2))
	}
}

func TestIntegrationSparseSearchChinese(t *testing.T) {
	s, ctx := newTestStore(t)
	mustAdd(t, s, ctx, seedChunks(), seedVectors())

	cands, err := s.SparseSearch(ctx, "乐山大佛", 10, types.Filter{})
	if err != nil {
		t.Fatalf("SparseSearch 失败: %v", err)
	}
	got := map[string]float64{}
	for _, c := range cands {
		got[c.ChunkID] = c.Score
	}
	for _, want := range []string{"c1", "c4", "c5"} { // 这三块都含"乐山大佛"
		if _, ok := got[want]; !ok {
			t.Errorf("中文稀疏检索漏掉了 %s（实际命中 %v）", want, cands)
		}
	}
	for _, notWant := range []string{"c2", "c3", "c6"} {
		if _, ok := got[notWant]; ok {
			t.Errorf("中文稀疏检索不该命中 %s（实际命中 %v）", notWant, cands)
		}
	}
	for i := 1; i < len(cands); i++ {
		if cands[i].Score > cands[i-1].Score {
			t.Fatalf("稀疏结果没有按分数降序: %v", cands)
		}
	}
	for _, c := range cands {
		if c.Score <= 0 {
			t.Errorf("命中块的 ts_rank_cd 应该 > 0，%s 得到 %v", c.ChunkID, c.Score)
		}
	}

	// 单字查询也应该能命中（unigram 预分词）。
	one, err := s.SparseSearch(ctx, "佛", 10, types.Filter{})
	if err != nil {
		t.Fatalf("SparseSearch(单字) 失败: %v", err)
	}
	if len(one) == 0 {
		t.Fatal("单字查询应该命中至少一块")
	}

	// 查不到的词返回空，不报错。
	none, err := s.SparseSearch(ctx, "量子纠缠", 10, types.Filter{})
	if err != nil {
		t.Fatalf("SparseSearch(无关词) 失败: %v", err)
	}
	if len(none) != 0 {
		t.Errorf("无关词不该命中，得到 %v", none)
	}
}

// TestIntegrationFilterMatchesTypesFilter 是本包最关键的防漂移测试：
// 同一组数据、同一批 Filter，同时跑 SQL 过滤和 types.Filter.Match，
// 断言两边选出的块集合完全一致。SQL 条件一旦和权威语义分叉，这里立刻红。
func TestIntegrationFilterMatchesTypesFilter(t *testing.T) {
	s, ctx := newTestStore(t)
	mustAdd(t, s, ctx, seedChunks(), seedVectors())

	all := allChunks(t, s, ctx)
	if len(all) != len(seedChunks()) {
		t.Fatalf("库里应该有 %d 个块，得到 %d", len(seedChunks()), len(all))
	}

	cases := []struct {
		name string
		f    types.Filter
	}{
		{"空过滤", types.Filter{}},
		{"按文档", types.Filter{DocIDs: []string{"d1"}}},
		{"按文档（大小写+空格）", types.Filter{DocIDs: []string{" D1 "}}},
		{"按文档（多值 OR）", types.Filter{DocIDs: []string{"d2", "d3"}}},
		{"按模态 text", types.Filter{Modalities: []types.Modality{types.ModalityText}}},
		{"按模态 text+image", types.Filter{Modalities: []types.Modality{types.ModalityText, types.ModalityImage}}},
		{"按模态 table", types.Filter{Modalities: []types.Modality{types.ModalityTable}}},
		{"按朝代", types.Filter{Dynasty: []string{"唐"}}},
		{"按朝代（多值）", types.Filter{Dynasty: []string{"唐", "宋"}}},
		{"按地点（ASCII 大小写折叠）", types.Filter{Place: []string{"HangZhou"}}},
		{"按地点（中文）", types.Filter{Place: []string{"乐山"}}},
		{"按主题", types.Filter{Topic: []string{"水利"}}},
		{"按来源类型", types.Filter{SourceType: []string{"地方志"}}},
		{"按可信度", types.Filter{Credibility: []string{"high", "low"}}},
		{"按标签（空标签的块必须被排除）", types.Filter{Tags: []string{"石刻"}}},
		{"按标签（图片）", types.Filter{Tags: []string{"图片"}}},
		{"abilities 门禁（空 abilities 的块始终可检索）", types.Filter{Abilities: []string{"水利"}}},
		{"abilities 门禁（文物）", types.Filter{Abilities: []string{"文物"}}},
		{"abilities 门禁（多个）", types.Filter{Abilities: []string{"水利", "诗词"}}},
		{"abilities + 标签 + 朝代", types.Filter{
			Abilities: []string{"水利"}, Tags: []string{"石刻"}, Dynasty: []string{"唐"},
		}},
		{"全维度组合", types.Filter{
			DocIDs: []string{"d1", "d2"}, Modalities: []types.Modality{types.ModalityText},
			Dynasty: []string{"唐"}, Place: []string{"乐山"}, Topic: []string{"水利"},
			SourceType: []string{"地方志"}, Credibility: []string{"high"},
			Abilities: []string{"水利"}, Tags: []string{"石刻"},
		}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// SQL 侧。
			gotIDs := selectIDsByFilter(t, s, ctx, tc.f)
			sort.Strings(gotIDs)

			// 权威语义侧：types.Filter.Match。
			wantIDs := []string{}
			for _, ch := range all {
				if tc.f.Match(ch) {
					wantIDs = append(wantIDs, ch.ID)
				}
			}
			sort.Strings(wantIDs)

			if !reflect.DeepEqual(gotIDs, wantIDs) {
				t.Fatalf("SQL 过滤与 Filter.Match 不一致:\n SQL  = %v\nMatch = %v\n条件 = %+v", gotIDs, wantIDs, tc.f)
			}

			// 同一条过滤条件走真实的稠密检索路径，结果集合也必须一致
			// （k 远大于块数，所以不会有截断）。
			cands, err := s.DenseSearch(ctx, unitVector(0), 100, tc.f)
			if err != nil {
				t.Fatalf("DenseSearch 失败: %v", err)
			}
			denseIDs := []string{}
			for _, c := range cands {
				denseIDs = append(denseIDs, c.ChunkID)
			}
			sort.Strings(denseIDs)
			if !reflect.DeepEqual(denseIDs, wantIDs) {
				t.Fatalf("DenseSearch 的过滤结果与 Filter.Match 不一致:\n dense = %v\nMatch = %v", denseIDs, wantIDs)
			}
		})
	}
}

func TestIntegrationAddIsIdempotent(t *testing.T) {
	s, ctx := newTestStore(t)
	chunks := seedChunks()
	vectors := seedVectors()

	mustAdd(t, s, ctx, chunks, vectors)
	first := countChunks(t, s, ctx)
	if first != len(chunks) {
		t.Fatalf("首次写入应该有 %d 块，得到 %d", len(chunks), first)
	}

	// 1) 完全重复写入。
	mustAdd(t, s, ctx, chunks, vectors)

	// 2) 同 (doc_id, idx)、换 id：命中 rag_chunks_doc_idx_uniq。
	dupIdx := chunks[0]
	dupIdx.ID = "c1-dup-idx"
	mustAdd(t, s, ctx, []types.Chunk{dupIdx}, [][]float32{vectors[0]})

	// 3) 同 hash、换 doc_id 与 idx：命中 rag_chunks_hash_uniq_idx。
	dupHash := chunks[0]
	dupHash.ID = "c1-dup-hash"
	dupHash.DocID = "d9"
	dupHash.Index = 99
	mustAdd(t, s, ctx, []types.Chunk{dupHash}, [][]float32{vectors[0]})

	if second := countChunks(t, s, ctx); second != first {
		t.Fatalf("重复写入后块数应该仍是 %d，得到 %d", first, second)
	}

	// 3 的 doc_id 是新的，所以会补一行文档骨架（这是 Add 的预期副作用）。
	if _, ok, err := s.GetDoc(ctx, "d9"); err != nil || !ok {
		t.Errorf("Add 应该为新的 doc_id 补一行骨架: (%v, %v)", ok, err)
	}

	// 空 hash 的块不参与 hash 去重：两条空 hash 的块都能写进去。
	noHashA := chunks[5]
	noHashA.ID, noHashA.DocID, noHashA.Index, noHashA.Hash = "nh-a", "d10", 0, ""
	noHashB := chunks[5]
	noHashB.ID, noHashB.DocID, noHashB.Index, noHashB.Hash = "nh-b", "d10", 1, ""
	mustAdd(t, s, ctx, []types.Chunk{noHashA, noHashB}, [][]float32{vectors[5], vectors[5]})
	if n := countChunks(t, s, ctx); n != first+2 {
		t.Fatalf("空 hash 的块不应该被互相去重，期望 %d 块，得到 %d", first+2, n)
	}
}

func TestIntegrationRemoveAndStats(t *testing.T) {
	s, ctx := newTestStore(t)
	mustAdd(t, s, ctx, seedChunks(), seedVectors())

	// RemoveDoc：d1 有 c1、c2 两块。
	n, err := s.RemoveDoc(ctx, "d1")
	if err != nil {
		t.Fatalf("RemoveDoc 失败: %v", err)
	}
	if n != 2 {
		t.Fatalf("RemoveDoc(d1) 应该删掉 2 块，得到 %d", n)
	}
	if _, ok, _ := s.GetChunk(ctx, "c1"); ok {
		t.Fatal("c1 应该已经被删除")
	}
	if again, err := s.RemoveDoc(ctx, "d1"); err != nil || again != 0 {
		t.Fatalf("重复 RemoveDoc 应该返回 0 且不报错，得到 (%d, %v)", again, err)
	}

	// RemoveBySource：水利/都江堰.md 有 c3、c4 两块。
	n, err = s.RemoveBySource(ctx, "水利/都江堰.md")
	if err != nil {
		t.Fatalf("RemoveBySource 失败: %v", err)
	}
	if n != 2 {
		t.Fatalf("RemoveBySource 应该删掉 2 块，得到 %d", n)
	}

	// 剩下 c5、c6（都在 d3）。
	st, err := s.Stats(ctx)
	if err != nil {
		t.Fatalf("Stats 失败: %v", err)
	}
	if st.Backend != BackendName {
		t.Errorf("Stats.Backend = %q，期望 %q", st.Backend, BackendName)
	}
	if st.Dim != testDim {
		t.Errorf("Stats.Dim = %d，期望 %d", st.Dim, testDim)
	}
	if st.Chunks != 2 {
		t.Errorf("Stats.Chunks = %d，期望 2", st.Chunks)
	}
	if st.Modalities[string(types.ModalityImage)] != 1 || st.Modalities[string(types.ModalityText)] != 1 {
		t.Errorf("Stats.Modalities = %v，期望 image:1 text:1", st.Modalities)
	}
	// 只有 c5 是 image 且带 asset_url。
	if st.Assets != 1 {
		t.Errorf("Stats.Assets = %d，期望 1", st.Assets)
	}

	// DeleteDoc：删掉 d3 的登记与块。
	if err := s.DeleteDoc(ctx, "d3"); err != nil {
		t.Fatalf("DeleteDoc 失败: %v", err)
	}
	if _, ok, _ := s.GetDoc(ctx, "d3"); ok {
		t.Error("d3 的文档登记应该已经被删除")
	}
	st, err = s.Stats(ctx)
	if err != nil {
		t.Fatalf("Stats 失败: %v", err)
	}
	if st.Chunks != 0 {
		t.Errorf("DeleteDoc 之后应该没有块，得到 %d", st.Chunks)
	}
}

func contains(haystack []string, needle string) bool {
	for _, s := range haystack {
		if s == needle {
			return true
		}
	}
	return false
}
