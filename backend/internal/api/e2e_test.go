package api_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"ragkb/internal/api"
	"ragkb/internal/config"
	"ragkb/internal/ingest"
	"ragkb/internal/jobs"
	"ragkb/internal/llm"
	"ragkb/internal/rag"
	"ragkb/internal/sites"
	"ragkb/internal/store/file"
)

// testStack 是一套完整的、跑在假模型之上的服务栈。
type testStack struct {
	server  *httptest.Server
	cfg     *config.Config
	stats   *ingest.Stats
	openai  *fakeOpenAI
	ollama  *fakeOllama
	backend *file.Store
}

// newTestStack 组装"入库 → 检索 → 生成"的全链路，模型全部指向 httptest 假服务。
//
// 这是本次交付里唯一能真正验证 SSE 事件顺序、引用回填、ability 门禁的测试，
// 因为它跑的是真实的 Gin 路由 + 真实 HTTP + 真实文件后端。
func newTestStack(t *testing.T, answer string) *testStack {
	t.Helper()
	dim := 1024
	ollama := newFakeOllama(t, dim)
	openai := newFakeOpenAI(t, answer)

	tmp := t.TempDir()
	cfg := config.Default()
	cfg.Server.RateLimitPerMin = 0 // 测试里不限流
	cfg.Storage.DataDir = filepath.Join(tmp, "data")
	cfg.Storage.DocsDir = filepath.Join(tmp, "docs")
	cfg.Storage.AssetsDir = filepath.Join(tmp, "assets")
	cfg.Storage.WebDir = filepath.Join(tmp, "web")
	cfg.Storage.SitesFile = filepath.Join(tmp, "sites.json")
	cfg.Embedding.BaseURL = ollama.URL
	cfg.Embedding.Dim = dim
	cfg.Chat.BaseURL = openai.URL
	cfg.Chat.APIKey = "test-key"
	if err := cfg.Validate(); err != nil {
		t.Fatalf("配置校验失败: %v", err)
	}
	if err := cfg.EnsureDirs(); err != nil {
		t.Fatalf("创建目录失败: %v", err)
	}
	writeTestDocs(t, cfg.Storage.DocsDir)
	writeTestSites(t, cfg.Storage.SitesFile)

	backend, err := file.Open(cfg.Storage.DataDir, dim, true)
	if err != nil {
		t.Fatalf("打开文件后端失败: %v", err)
	}
	t.Cleanup(func() { _ = backend.Close() })

	embedder, err := llm.NewEmbedder(cfg)
	if err != nil {
		t.Fatalf("构造嵌入器失败: %v", err)
	}
	chatter, err := llm.NewChatter(cfg)
	if err != nil {
		t.Fatalf("构造对话模型失败: %v", err)
	}
	vision, err := llm.NewVisioner(cfg)
	if err != nil {
		t.Fatalf("构造视觉模型失败: %v", err)
	}
	siteStore, err := sites.Load(cfg.Storage.SitesFile)
	if err != nil {
		t.Fatalf("加载景点数据失败: %v", err)
	}

	pipeline, err := ingest.NewPipeline(cfg, backend, embedder, vision)
	if err != nil {
		t.Fatalf("构造流水线失败: %v", err)
	}
	stats, err := pipeline.Run(context.Background(), ingest.RunOptions{}, nil)
	if err != nil {
		t.Fatalf("入库失败: %v", err)
	}
	if stats.ChunksAdded == 0 {
		t.Fatalf("入库没有产生任何块：%+v", stats)
	}

	ragSvc := rag.New(cfg, backend, embedder, chatter, siteStore, rag.NewTracer(10, ""))
	srv := api.New(api.Deps{
		Cfg: cfg, Backend: backend, Embedder: embedder, Chatter: chatter, Vision: vision,
		Rag: ragSvc, Jobs: jobs.NewManager(5), Sites: siteStore, Pipeline: pipeline,
	})
	ts := httptest.NewServer(srv.Router())
	t.Cleanup(ts.Close)

	return &testStack{server: ts, cfg: cfg, stats: stats, openai: openai, ollama: ollama, backend: backend}
}

// postJSON 发一个 JSON 请求并返回响应。
func (s *testStack) postJSON(t *testing.T, path string, body any) *http.Response {
	t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("序列化请求失败: %v", err)
	}
	resp, err := http.Post(s.server.URL+path, "application/json", bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("请求 %s 失败: %v", path, err)
	}
	return resp
}

// decodeEnvelope 解析统一信封。
func decodeEnvelope(t *testing.T, resp *http.Response) map[string]any {
	t.Helper()
	defer resp.Body.Close()
	var env map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&env); err != nil {
		t.Fatalf("响应不是合法 JSON: %v", err)
	}
	if code, _ := env["code"].(float64); code != 0 {
		t.Fatalf("业务码非 0: %v（message=%v）", env["code"], env["message"])
	}
	data, _ := env["data"].(map[string]any)
	if data == nil {
		t.Fatalf("响应缺少 data: %v", env)
	}
	return data
}

func TestIngestProducesChunksAndDocs(t *testing.T) {
	st := newTestStack(t, "ok")
	if st.stats.FilesChanged != 2 {
		t.Errorf("应处理 2 个文件，实际 %d", st.stats.FilesChanged)
	}
	statsCtx, _ := st.backend.Stats(context.Background())
	if statsCtx.Docs != 2 {
		t.Errorf("应登记 2 个文档，实际 %d", statsCtx.Docs)
	}
	if statsCtx.Dim != 1024 {
		t.Errorf("维度应为 1024，实际 %d", statsCtx.Dim)
	}
}

func TestSearchReturnsCitedSource(t *testing.T) {
	st := newTestStack(t, "ok")
	data := decodeEnvelope(t, st.postJSON(t, "/api/v1/search", map[string]any{"query": "年假多少天", "top_k": 5}))
	hits, _ := data["hits"].([]any)
	if len(hits) == 0 {
		t.Fatal("检索没有命中任何块")
	}
	first, _ := hits[0].(map[string]any)
	chunk, _ := first["chunk"].(map[string]any)
	if src, _ := chunk["source"].(string); !strings.Contains(src, "handbook.md") {
		t.Errorf("首位命中应来自 handbook.md，实际 %q（score=%v）", src, first["score"])
	}
	if text, _ := chunk["text"].(string); !strings.Contains(text, "年假") {
		t.Errorf("命中块里应含关键词「年假」，实际 %q", text)
	}
}

// TestAbilityGating 验证"没解锁的碎片不进上下文"。
func TestAbilityGating(t *testing.T) {
	st := newTestStack(t, "ok")
	// handbook.md 标了 abilities: 历史，notice.md 没标（通用内容，始终可检索）
	all := decodeEnvelope(t, st.postJSON(t, "/api/v1/search", map[string]any{"query": "年假", "top_k": 10}))
	allHits, _ := all["hits"].([]any)
	if len(allHits) < 2 {
		t.Fatalf("不过滤时应命中两个文件，实际 %d", len(allHits))
	}

	locked := decodeEnvelope(t, st.postJSON(t, "/api/v1/search", map[string]any{
		"query": "年假", "top_k": 10, "filters": map[string]any{"abilities": []string{"三国"}},
	}))
	lockedHits, _ := locked["hits"].([]any)
	for _, h := range lockedHits {
		m, _ := h.(map[string]any)
		chunk, _ := m["chunk"].(map[string]any)
		src, _ := chunk["source"].(string)
		if strings.Contains(src, "handbook.md") {
			t.Fatalf("abilities 未解锁 历史 时不应返回 handbook.md，实际返回了（hits=%d）", len(lockedHits))
		}
	}
	if len(lockedHits) == 0 {
		t.Error("未标注 abilities 的通用内容应始终可检索，实际一条都没返回")
	}
}

func TestHealthReportsDependencies(t *testing.T) {
	st := newTestStack(t, "ok")
	resp, err := http.Get(st.server.URL + "/api/v1/health")
	if err != nil {
		t.Fatalf("健康检查请求失败: %v", err)
	}
	data := decodeEnvelope(t, resp)
	emb, _ := data["embedding"].(map[string]any)
	if ok, _ := emb["ok"].(bool); !ok {
		t.Errorf("嵌入模型应就绪，实际 %v", emb)
	}
	chat, _ := data["chat"].(map[string]any)
	if ok, _ := chat["ok"].(bool); !ok {
		t.Errorf("对话模型应就绪，实际 %v", chat)
	}
	index, _ := data["index"].(map[string]any)
	if chunks, _ := index["chunks"].(float64); chunks <= 0 {
		t.Errorf("索引块数应大于 0，实际 %v", index["chunks"])
	}
}

func TestStatsExposesFacets(t *testing.T) {
	st := newTestStack(t, "ok")
	resp, err := http.Get(st.server.URL + "/api/v1/stats")
	if err != nil {
		t.Fatalf("统计请求失败: %v", err)
	}
	data := decodeEnvelope(t, resp)
	facets, _ := data["facets"].(map[string]any)
	if facets == nil {
		t.Fatal("stats 应返回 facets")
	}
	if ab, _ := facets["abilities"].([]any); len(ab) == 0 {
		t.Errorf("facets.abilities 应包含 历史，实际 %v", facets["abilities"])
	}
}

// writeTestDocs 造两个文档：一个带 abilities 元数据，一个不带。
func writeTestDocs(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("创建 docs 目录失败: %v", err)
	}
	handbook := `---
topic: 制度
source_type: 内部制度
credibility: high
abilities: 历史
---

# 员工手册

## 年假制度

工作满 1 年不满 3 年的员工，每年享有 5 天带薪年假；满 3 年不满 5 年为 10 天。
年假需提前 3 个工作日提交申请，由直属主管审批。当年度未休完的年假最多结转 3 天到次年。
`
	notice := `# 考勤通知

年假与调休的申请入口统一在 OA 系统，每月 5 号前提交上月汇总。
迟到超过 30 分钟按旷工半天处理，特殊情况需在当天补交说明。
`
	if err := os.WriteFile(filepath.Join(dir, "handbook.md"), []byte(handbook), 0o644); err != nil {
		t.Fatalf("写入 handbook.md 失败: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "notice.md"), []byte(notice), 0o644); err != nil {
		t.Fatalf("写入 notice.md 失败: %v", err)
	}
}

// writeTestSites 造一条景点数据，验证"别名扩写"与只读工具。
func writeTestSites(t *testing.T, path string) {
	t.Helper()
	body := map[string]any{"sites": []map[string]any{{
		"id": "leshan", "name": "乐山大佛", "aliases": []string{"凌云大佛"},
		"dynasty": "唐", "place": "乐山", "topic": "水利",
		"summary": "唐代开凿的弥勒佛坐像",
		"facts":   map[string]string{"高度": "71 米", "开凿年代": "713 年"},
	}}}
	raw, _ := json.MarshalIndent(body, "", "  ")
	if err := os.WriteFile(path, raw, 0o644); err != nil {
		t.Fatalf("写入景点数据失败: %v", err)
	}
}
