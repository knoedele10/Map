package eval

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"ragkb/internal/types"
)

// ---- 假 Client：不联网，按 question 返回预置结果。 ----

type fakeClient struct {
	searchHits map[string][]types.Hit
	chatOut    map[string]ChatOutput
	searchErr  map[string]error
	chatErr    map[string]error

	searchCall int
	chatCall   int
	lastChat   ChatInput
}

func (f *fakeClient) Search(_ context.Context, query string, topK int, _ types.Filter) ([]types.Hit, error) {
	f.searchCall++
	if err := f.searchErr[query]; err != nil {
		return nil, err
	}
	hits := f.searchHits[query]
	if topK > 0 && len(hits) > topK {
		hits = hits[:topK]
	}
	return hits, nil
}

func (f *fakeClient) Chat(_ context.Context, req ChatInput) (ChatOutput, error) {
	f.chatCall++
	f.lastChat = req
	if err := f.chatErr[req.Question]; err != nil {
		return ChatOutput{}, err
	}
	return f.chatOut[req.Question], nil
}

func mkHit(source, text string, rank int) types.Hit {
	return types.Hit{
		Chunk: types.Chunk{ID: source + "#" + text, Source: source, Text: text, Modality: types.ModalityText},
		Score: 0.9,
		Rank:  rank,
		From:  "both",
	}
}

func approx(t *testing.T, name string, got, want float64) {
	t.Helper()
	if math.IsNaN(got) {
		t.Fatalf("%s 是 NaN", name)
	}
	if math.Abs(got-want) > 1e-9 {
		t.Errorf("%s = %v, want %v", name, got, want)
	}
}

func assertNoNaN(t *testing.T, r *Report) {
	t.Helper()
	for _, kv := range []struct {
		name string
		v    float64
	}{
		{"Recall", r.Recall},
		{"MRR", r.MRR},
		{"CitationHit", r.CitationHit},
		{"KeywordHit", r.KeywordHit},
		{"UnknownCorrect", r.UnknownCorrect},
		{"Faithfulness", r.Faithfulness},
	} {
		if math.IsNaN(kv.v) {
			t.Errorf("%s 是 NaN，指标不应产生 NaN", kv.name)
		}
	}
}

// TestRunMetrics 用已知命中/未命中组合断言 recall/MRR/citation/keyword/unknown/faithfulness
// 的具体数值。
func TestRunMetrics(t *testing.T) {
	golden := &GoldenSet{
		Name: "metrics",
		Cases: []Case{
			{ID: "A", Question: "qa", ExpectedSources: []string{"a.md"}, ExpectedKeywords: []string{"答案"}},
			{ID: "B", Question: "qb", ExpectedSources: []string{"b.md"}, ExpectedKeywords: []string{"缺"}},
			{ID: "C", Question: "qc", ExpectUnknown: true},
		},
	}
	fake := &fakeClient{
		searchHits: map[string][]types.Hit{
			"qa": {mkHit("b.md", "无关内容", 1), mkHit("a.md", "这是答案的上下文", 2)},
			"qb": {mkHit("b.md", "无关内容", 1)},
			"qc": nil,
		},
		chatOut: map[string]ChatOutput{
			// [1] 指向 b.md（非期望来源）→ 引用未命中；句子可在上下文里找到 → 忠实度 1。
			"qa": {Answer: "[1] 这是答案。", Sources: []types.Hit{mkHit("b.md", "这是答案的上下文", 1)}},
			// 无 [n] → 引用未命中；关键词「缺」不出现；句子与上下文无关 → 忠实度 0。
			"qb": {Answer: "没有这个词。", Sources: []types.Hit{mkHit("b.md", "无关内容", 1)}},
			// 应回答没找到 → unknown 正确；无 sources → 上下文为空 → 忠实度 0。
			"qc": {Answer: "知识库里没有找到相关内容。"},
		},
	}

	rep, err := Run(context.Background(), fake, golden, Options{TopK: 4, UseChat: true})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if rep.Total != 3 || len(rep.Cases) != 3 {
		t.Fatalf("Total=%d len(Cases)=%d, want 3/3", rep.Total, len(rep.Cases))
	}

	// 召回率：A 第 2 名、B 第 1 名 → 2/2；MRR = (1/2 + 1/1)/2 = 0.75。
	approx(t, "Recall", rep.Recall, 1.0)
	approx(t, "MRR", rep.MRR, 0.75)
	// 引用命中率：A、B 均有期望来源与非空答案但都未命中 → 0/2。
	approx(t, "CitationHit", rep.CitationHit, 0)
	// 关键词命中：A 命中、B 未命中 → 1/2。
	approx(t, "KeywordHit", rep.KeywordHit, 0.5)
	// unknown：只有 C，且回答正确 → 1/1。
	approx(t, "UnknownCorrect", rep.UnknownCorrect, 1.0)
	// 忠实度：A=1.0、B=0.0；C 是"没找到"回答，没有事实陈述可核对，排除出分母 → 1/2。
	approx(t, "Faithfulness", rep.Faithfulness, 0.5)
	assertNoNaN(t, rep)

	a, b, c := rep.Cases[0], rep.Cases[1], rep.Cases[2]
	if a.RecallRank != 2 || !a.Recalled {
		t.Errorf("A: RecallRank=%d Recalled=%v, want 2/true", a.RecallRank, a.Recalled)
	}
	if a.CitationHit {
		t.Errorf("A: CitationHit=true, want false（[1] 指向非期望来源）")
	}
	if !a.KeywordHit {
		t.Errorf("A: KeywordHit=false, want true")
	}
	approx(t, "A.Faithfulness", a.Faithfulness, 1.0)
	if len(a.TopSources) != 2 || a.TopSources[0] != "b.md" || a.TopSources[1] != "a.md" {
		t.Errorf("A.TopSources=%v, want [b.md a.md]", a.TopSources)
	}
	if b.RecallRank != 1 || !b.Recalled {
		t.Errorf("B: RecallRank=%d Recalled=%v, want 1/true", b.RecallRank, b.Recalled)
	}
	if b.KeywordHit {
		t.Errorf("B: KeywordHit=true, want false")
	}
	if !c.UnknownOK {
		t.Errorf("C: UnknownOK=false, want true")
	}
	if faithApplicable(c) {
		t.Error("C 是「知识库里没有找到」类回答，不该计入忠实度分母")
	}
	if c.RecallRank != 0 || c.Recalled {
		t.Errorf("C: RecallRank=%d Recalled=%v, want 0/false（无期望来源）", c.RecallRank, c.Recalled)
	}

	// Run 传给 Chat 的格式应为契约默认的 text，且非流式由 HTTP 客户端负责。
	if fake.lastChat.AnswerFormat != "text" {
		t.Errorf("AnswerFormat=%q, want text", fake.lastChat.AnswerFormat)
	}
	if fake.searchCall != 3 || fake.chatCall != 3 {
		t.Errorf("searchCall=%d chatCall=%d, want 3/3", fake.searchCall, fake.chatCall)
	}
}

// TestFaithfulnessToleratesInsertion 是真实模型踩出来的回归。
//
// 语料写「1874 年 11 月 30 日，英格兰牛津郡 布莱尼姆宫（Blenheim Palace）」，
// 模型答成「1874 年 11 月 30 日，出生于英格兰牛津郡布莱尼姆宫…」——插入一个
// "出生于"会让下游所有 4-gram 错位，旧的"≥60% 的 4-gram 命中"算法把这条
// **完全正确**的回答判成 0 分。逐句位置覆盖法必须容忍这种改写。
func TestFaithfulnessToleratesInsertion(t *testing.T) {
	ctx := "| 出生 | 1874 年 11 月 30 日，英格兰牛津郡 布莱尼姆宫（Blenheim Palace） |"
	answer := "1874 年 11 月 30 日，出生于英格兰牛津郡布莱尼姆宫（Blenheim Palace）[1]。"
	if got := faithfulnessScore(answer, ctx); got < 0.99 {
		t.Errorf("有据的改写不该被判无据，得到 %v", got)
	}
	// 反过来，凭空捏造必须被判无据——指标不能被改成"永远 1.0"。
	if got := faithfulnessScore("丘吉尔出生于火星殖民地新上海。", ctx); got != 0 {
		t.Errorf("捏造内容应判无据，得到 %v", got)
	}
}

// TestRunSearchOnly 验证 UseChat=false 时只调检索、不调 Chat，答案类指标分母为 0。
func TestRunSearchOnly(t *testing.T) {
	golden := &GoldenSet{Cases: []Case{
		{ID: "A", Question: "qa", ExpectedSources: []string{"a.md"}, ExpectedKeywords: []string{"答案"}},
	}}
	fake := &fakeClient{searchHits: map[string][]types.Hit{"qa": {mkHit("a.md", "文本", 1)}}}

	rep, err := Run(context.Background(), fake, golden, Options{UseChat: false})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if fake.chatCall != 0 {
		t.Errorf("chatCall=%d, want 0（纯检索不应调 Chat）", fake.chatCall)
	}
	approx(t, "Recall", rep.Recall, 1.0)
	approx(t, "MRR", rep.MRR, 1.0)
	approx(t, "CitationHit", rep.CitationHit, 0)
	approx(t, "KeywordHit", rep.KeywordHit, 0)
	approx(t, "UnknownCorrect", rep.UnknownCorrect, 0)
	approx(t, "Faithfulness", rep.Faithfulness, 0)
	assertNoNaN(t, rep)
	if rep.Cases[0].Answer != "" {
		t.Errorf("Answer=%q, want 空", rep.Cases[0].Answer)
	}
}

// TestRunZeroDenominatorsNoNaN 验证没有任何期望时所有指标为 0 且不产生 NaN。
func TestRunZeroDenominatorsNoNaN(t *testing.T) {
	golden := &GoldenSet{Cases: []Case{{ID: "Z", Question: "qz"}}}
	fake := &fakeClient{searchHits: map[string][]types.Hit{"qz": {mkHit("x.md", "随便", 1)}}}

	rep, err := Run(context.Background(), fake, golden, Options{UseChat: false})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	for _, kv := range []struct {
		name string
		v    float64
	}{
		{"Recall", rep.Recall}, {"MRR", rep.MRR}, {"CitationHit", rep.CitationHit},
		{"KeywordHit", rep.KeywordHit}, {"UnknownCorrect", rep.UnknownCorrect}, {"Faithfulness", rep.Faithfulness},
	} {
		if kv.v != 0 {
			t.Errorf("%s = %v, want 0", kv.name, kv.v)
		}
	}
	assertNoNaN(t, rep)
	if txt := rep.Text(); !strings.Contains(txt, "分母为 0") {
		t.Errorf("Text() 未说明分母为 0:\n%s", txt)
	}
}

// TestUnknownDetection 覆盖 expect_unknown 的三种表述与 structured.unknown。
func TestUnknownDetection(t *testing.T) {
	golden := &GoldenSet{Cases: []Case{
		{ID: "k1", Question: "q1", ExpectUnknown: true},
		{ID: "k2", Question: "q2", ExpectUnknown: true},
		{ID: "k3", Question: "q3", ExpectUnknown: true},
		{ID: "k4", Question: "q4", ExpectUnknown: true},
		{ID: "k5", Question: "q5", ExpectUnknown: true},
		{ID: "k6", Question: "q6"}, // 非 expect_unknown，即使答「没有找到」也不计入
	}}
	fake := &fakeClient{
		chatOut: map[string]ChatOutput{
			"q1": {Answer: "知识库里没有找到相关内容。"},
			"q2": {Answer: "未找到相关信息。"},
			"q3": {Answer: "查不到。"},
			"q4": {Answer: "抱歉，我无法回答。", Structured: &types.StructuredAnswer{Unknown: true}},
			"q5": {Answer: "公司 2024 年营业收入为 1 亿元。"},
			"q6": {Answer: "没有找到相关内容。"},
		},
	}
	rep, err := Run(context.Background(), fake, golden, Options{UseChat: true})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	// 5 条 expect_unknown 中 4 条正确；k6 不计入分母。
	approx(t, "UnknownCorrect", rep.UnknownCorrect, 0.8)
	assertNoNaN(t, rep)
	for i, want := range []bool{true, true, true, true, false} {
		if got := rep.Cases[i].UnknownOK; got != want {
			t.Errorf("k%d: UnknownOK=%v, want %v", i+1, got, want)
		}
	}
}

// TestCitationHit 直接覆盖引用判定的边界：越界、无引用、全角括号、指向非期望来源。
func TestCitationHit(t *testing.T) {
	cases := []struct {
		name     string
		expected []string
		answer   string
		sources  []types.Hit
		want     bool
	}{
		{"命中期望来源", []string{"a.md"}, "见 [1]。", []types.Hit{mkHit("a.md", "", 1)}, true},
		{"编号越界", []string{"a.md"}, "[1][2]", []types.Hit{mkHit("a.md", "", 1)}, false},
		{"多编号均在范围内且一个指向期望", []string{"a.md"}, "[1][2]", []types.Hit{mkHit("a.md", "", 1), mkHit("b.md", "", 2)}, true},
		{"无引用", []string{"a.md"}, "没有引用", []types.Hit{mkHit("a.md", "", 1)}, false},
		{"无期望来源则 skip", nil, "[1]", []types.Hit{mkHit("a.md", "", 1)}, false},
		{"全角括号", []string{"a.md"}, "【1】", []types.Hit{mkHit("a.md", "", 1)}, true},
		{"编号指向非期望来源", []string{"a.md"}, "[2]", []types.Hit{mkHit("b.md", "", 1), mkHit("a.md", "", 2)}, true},
	}
	for _, tc := range cases {
		if got := citationHit(tc.expected, tc.answer, tc.sources); got != tc.want {
			t.Errorf("%s: citationHit=%v, want %v", tc.name, got, tc.want)
		}
	}
}

// TestKeywordHitNormalization 验证关键词匹配忽略大小写与空白差异。
func TestKeywordHitNormalization(t *testing.T) {
	cases := []struct {
		name     string
		keywords []string
		answer   string
		want     bool
	}{
		{"空白差异", []string{"10 天"}, "答案是10天。", true},
		{"反向空白差异", []string{"10天"}, "答案是 10 天。", true},
		{"大小写不敏感", []string{"ABC"}, "xx abc yy", true},
		{"缺一个关键词", []string{"答案", "缺"}, "这是答案", false},
		{"未声明关键词", nil, "任意答案", false},
		{"空答案", []string{"答案"}, "", false},
	}
	for _, tc := range cases {
		if got := keywordHit(tc.keywords, tc.answer); got != tc.want {
			t.Errorf("%s: keywordHit=%v, want %v", tc.name, got, tc.want)
		}
	}
}

// TestFaithfulnessScore 覆盖代理指标的具体取值。
func TestFaithfulnessScore(t *testing.T) {
	cases := []struct {
		name    string
		answer  string
		context string
		want    float64
	}{
		{"完全有据", "员工入职满一年后享受带薪年假。", "员工入职满一年后享受带薪年假。", 1.0},
		{"完全无据", "完全无关的另一句话。", "员工入职满一年后享受带薪年假。", 0.0},
		{"空答案", "", "任何上下文", 0.0},
		{"空上下文", "员工入职满一年后享受带薪年假。", "", 0.0},
		{"汉字不足 4 个无法评估", "好的。", "好的", 0.0},
		{"一半句子有据", "员工入职满一年后享受带薪年假。完全无关的另一句话。", "员工入职满一年后享受带薪年假。", 0.5},
	}
	for _, tc := range cases {
		got := faithfulnessScore(tc.answer, tc.context)
		if math.Abs(got-tc.want) > 1e-9 {
			t.Errorf("%s: faithfulnessScore=%v, want %v", tc.name, got, tc.want)
		}
	}
}

// TestRunSearchErrorExcluded 验证单条用例请求失败只记 Err，且不计入分母。
func TestRunSearchErrorExcluded(t *testing.T) {
	golden := &GoldenSet{Cases: []Case{
		{ID: "X", Question: "qx", ExpectedSources: []string{"x.md"}},
		{ID: "Y", Question: "qy", ExpectedSources: []string{"y.md"}},
	}}
	fake := &fakeClient{
		searchHits: map[string][]types.Hit{"qy": {mkHit("y.md", "文本", 1)}},
		searchErr:  map[string]error{"qx": errors.New("boom")},
	}
	rep, err := Run(context.Background(), fake, golden, Options{UseChat: false})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !strings.Contains(rep.Cases[0].Err, "boom") {
		t.Errorf("X.Err=%q, want 含 boom", rep.Cases[0].Err)
	}
	// 只有 Y 计入分母 → 召回率 1.0。
	approx(t, "Recall", rep.Recall, 1.0)
	approx(t, "MRR", rep.MRR, 1.0)
	assertNoNaN(t, rep)
}

// TestLoadGoldenRealFile 解析仓库里的 eval/golden.yaml（测试工作目录是包目录）。
func TestLoadGoldenRealFile(t *testing.T) {
	gs, err := LoadGolden(filepath.Join("..", "..", "eval", "golden.yaml"))
	if err != nil {
		t.Fatalf("LoadGolden: %v", err)
	}
	if len(gs.Cases) < 10 {
		t.Fatalf("用例数 = %d, want >= 10", len(gs.Cases))
	}
	if strings.TrimSpace(gs.Name) == "" {
		t.Errorf("GoldenSet.Name 为空")
	}

	var withSources, withKeywords, unknown, filtered int
	byID := make(map[string]Case, len(gs.Cases))
	for _, c := range gs.Cases {
		byID[c.ID] = c
		if len(c.ExpectedSources) > 0 {
			withSources++
		}
		if len(c.ExpectedKeywords) > 0 {
			withKeywords++
		}
		if c.ExpectUnknown {
			unknown++
		}
		if !c.Filters.Empty() {
			filtered++
		}
		if strings.TrimSpace(c.Note) == "" {
			t.Errorf("用例 %s 缺少 note", c.ID)
		}
		// This historical employee/WWII golden set is a parser fixture; its private
		// corpus is not part of the Shuji delivery. The deployed corpus is checked
		// independently in TestConnectedGoldenSourcesExist.
	}
	if withSources < 8 {
		t.Errorf("带 expected_sources 的用例 = %d, want >= 8", withSources)
	}
	if withKeywords < 8 {
		t.Errorf("带 expected_keywords 的用例 = %d, want >= 8", withKeywords)
	}
	if unknown < 2 {
		t.Errorf("expect_unknown 用例 = %d, want >= 2", unknown)
	}
	if filtered < 2 {
		t.Errorf("带 filters 的用例 = %d, want >= 2", filtered)
	}

	// 校验 filters 的 snake_case 映射（契约 2. Filter）。
	if f, ok := byID["f01_churchill_iron_curtain_ability"]; !ok {
		t.Errorf("缺少用例 f01_churchill_iron_curtain_ability")
	} else if len(f.Filters.Abilities) != 1 || f.Filters.Abilities[0] != "历史" {
		t.Errorf("f01 abilities = %v, want [历史]", f.Filters.Abilities)
	}
	if f, ok := byID["f02_wwii_pearl_harbor_modality"]; !ok {
		t.Errorf("缺少用例 f02_wwii_pearl_harbor_modality")
	} else if len(f.Filters.Modalities) != 1 || f.Filters.Modalities[0] != types.ModalityText {
		t.Errorf("f02 modalities = %v, want [text]", f.Filters.Modalities)
	}
}

func TestConnectedGoldenSourcesExist(t *testing.T) {
	gs, err := LoadGolden(filepath.Join("..", "..", "eval", "golden-connected.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range gs.Cases {
		for _, source := range c.ExpectedSources {
			path := filepath.Join("..", "..", "..", "data", "corpus", source)
			if _, err := os.Stat(path); err != nil {
				t.Errorf("active golden source %s: %v", source, err)
			}
		}
	}
}

// TestLoadGoldenInvalid 覆盖加载失败的几种情况。
func TestLoadGoldenInvalid(t *testing.T) {
	if _, err := LoadGolden(""); err == nil {
		t.Errorf("空路径应报错")
	}
	dir := t.TempDir()
	write := func(name, body string) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatalf("写临时文件: %v", err)
		}
		return p
	}
	if _, err := LoadGolden(write("empty.yaml", "name: x\ncases: []\n")); err == nil {
		t.Errorf("无用例应报错")
	}
	if _, err := LoadGolden(write("noq.yaml", "cases:\n  - id: a\n")); err == nil {
		t.Errorf("缺 question 应报错")
	}
	if _, err := LoadGolden(write("dup.yaml", "cases:\n  - id: a\n    question: q\n  - id: a\n    question: q2\n")); err == nil {
		t.Errorf("id 重复应报错")
	}
	if _, err := LoadGolden(write("bad.yaml", "cases: [ { id: a, question: q\n")); err == nil {
		t.Errorf("非法 YAML 应报错")
	}
}

// TestHTTPClientEnvelope 用 httptest 起假服务，验证信封解析、请求体与鉴权头。
func TestHTTPClientEnvelope(t *testing.T) {
	var gotSearchAuth, gotChatAuth string
	var gotSearchBody, gotChatBody map[string]any

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("method = %s, want POST", r.Method)
		}
		body, _ := io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v1/search":
			gotSearchAuth = r.Header.Get("Authorization")
			_ = json.Unmarshal(body, &gotSearchBody)
			_, _ = w.Write([]byte(`{"code":0,"message":"ok","request_id":"r1","data":{"hits":[{"chunk":{"id":"c1","source":"示例-员工手册.md","text":"年假 10 天"},"score":0.9,"dense_score":0.8,"sparse_score":7.2,"rank":1,"from":"both"}],"elapsed_ms":12,"candidates":{"dense":20,"sparse":20}}}`))
		case "/api/v1/chat":
			gotChatAuth = r.Header.Get("Authorization")
			_ = json.Unmarshal(body, &gotChatBody)
			_, _ = w.Write([]byte(`{"code":0,"message":"ok","request_id":"r2","data":{"answer":"年假 10 天 [1]。","structured":null,"sources":[{"chunk":{"id":"c1","source":"示例-员工手册.md","text":"年假 10 天"},"score":0.9,"rank":1}],"elapsed_ms":42,"model":"deepseek-chat"}}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	// 末尾多余的 "/" 应被裁掉，否则路径会变成 //api/v1/...
	c := NewHTTPClient(srv.URL+"/", "tok")

	hits, err := c.Search(context.Background(), "年假", 3, types.Filter{Abilities: []string{"历史"}})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(hits) != 1 || hits[0].Chunk.Source != "示例-员工手册.md" || hits[0].Rank != 1 {
		t.Fatalf("hits = %+v", hits)
	}
	if gotSearchAuth != "Bearer tok" {
		t.Errorf("search Authorization = %q, want Bearer tok", gotSearchAuth)
	}
	if gotSearchBody["query"] != "年假" {
		t.Errorf("query = %v", gotSearchBody["query"])
	}
	if gotSearchBody["top_k"].(float64) != 3 {
		t.Errorf("top_k = %v, want 3", gotSearchBody["top_k"])
	}
	fl, ok := gotSearchBody["filters"].(map[string]any)
	if !ok {
		t.Fatalf("filters 未按契约序列化: %v", gotSearchBody["filters"])
	}
	ab, ok := fl["abilities"].([]any)
	if !ok || len(ab) != 1 || ab[0] != "历史" {
		t.Errorf("filters.abilities = %v, want [历史]", fl["abilities"])
	}

	out, err := c.Chat(context.Background(), ChatInput{Question: "年假", TopK: 3, AnswerFormat: "structured"})
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	if out.Answer != "年假 10 天 [1]。" {
		t.Errorf("Answer = %q", out.Answer)
	}
	if len(out.Sources) != 1 || out.Sources[0].Chunk.Source != "示例-员工手册.md" {
		t.Errorf("Sources = %+v", out.Sources)
	}
	if out.ElapsedMS != 42 {
		t.Errorf("ElapsedMS = %d, want 42", out.ElapsedMS)
	}
	if out.Structured != nil {
		t.Errorf("Structured = %+v, want nil", out.Structured)
	}
	if gotChatAuth != "Bearer tok" {
		t.Errorf("chat Authorization = %q, want Bearer tok", gotChatAuth)
	}
	if gotChatBody["stream"] != false {
		t.Errorf("stream = %v, want false", gotChatBody["stream"])
	}
	if gotChatBody["answer_format"] != "structured" {
		t.Errorf("answer_format = %v, want structured", gotChatBody["answer_format"])
	}

	// token 为空时不发送 Authorization。
	anon := NewHTTPClient(srv.URL, "")
	if _, err := anon.Search(context.Background(), "年假", 1, types.Filter{}); err != nil {
		t.Fatalf("匿名 Search: %v", err)
	}
	if gotSearchAuth != "" {
		t.Errorf("匿名请求带了 Authorization = %q", gotSearchAuth)
	}
}

// TestHTTPClientErrorCodes 验证契约错误码 40100 / 40001 与非信封响应的处理。
func TestHTTPClientErrorCodes(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v1/search":
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"code":40100,"message":"未认证","data":null,"request_id":"r3"}`))
		case "/api/v1/chat":
			// HTTP 200 但业务码非 0。
			_, _ = w.Write([]byte(`{"code":40001,"message":"question 不能为空","data":null,"request_id":"r4"}`))
		}
	}))
	defer srv.Close()
	c := NewHTTPClient(srv.URL, "")

	_, err := c.Search(context.Background(), "x", 1, types.Filter{})
	var he *HTTPError
	if !errors.As(err, &he) {
		t.Fatalf("Search err = %v, want *HTTPError", err)
	}
	if he.Status != http.StatusUnauthorized || he.Code != 40100 {
		t.Errorf("Search HTTPError = %+v, want 401/40100", he)
	}
	if he.RequestID != "r3" || he.Message != "未认证" {
		t.Errorf("Search HTTPError 字段 = %+v", he)
	}

	_, err = c.Chat(context.Background(), ChatInput{Question: "x"})
	if !errors.As(err, &he) {
		t.Fatalf("Chat err = %v, want *HTTPError", err)
	}
	if he.Status != http.StatusOK || he.Code != 40001 {
		t.Errorf("Chat HTTPError = %+v, want 200/40001", he)
	}

	// 非信封响应（例如网关返回纯文本）。
	raw := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write([]byte("upstream boom"))
	}))
	defer raw.Close()
	_, err = NewHTTPClient(raw.URL, "").Search(context.Background(), "x", 1, types.Filter{})
	if !errors.As(err, &he) {
		t.Fatalf("非信封响应 err = %v, want *HTTPError", err)
	}
	if he.Status != http.StatusBadGateway {
		t.Errorf("非信封 HTTPError.Status = %d, want 502", he.Status)
	}
}

// TestTextNoPanic 验证 Text() 对 nil 与真实报告都不 panic，并包含代理指标说明。
func TestTextNoPanic(t *testing.T) {
	var nilReport *Report
	if got := nilReport.Text(); !strings.Contains(got, "nil") {
		t.Errorf("nil Report.Text() = %q", got)
	}

	golden := &GoldenSet{Cases: []Case{
		{ID: "A", Question: "qa", ExpectedSources: []string{"a.md"}, ExpectedKeywords: []string{"答案"}},
		{ID: "C", Question: "qc", ExpectUnknown: true},
	}}
	fake := &fakeClient{
		searchHits: map[string][]types.Hit{"qa": {mkHit("a.md", "这是答案", 1)}},
		chatOut: map[string]ChatOutput{
			"qa": {Answer: "[1] 这是答案。", Sources: []types.Hit{mkHit("a.md", "这是答案", 1)}},
			"qc": {Answer: "没有找到。", Sources: []types.Hit{}},
		},
	}
	rep, err := Run(context.Background(), fake, golden, Options{UseChat: true})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	txt := rep.Text()
	t.Log("\n" + txt)
	for _, want := range []string{"=== RAG 评测报告 ===", "召回率", "MRR", "引用命中率", "关键词命中", "unknown 正确率", "忠实度", "代理指标", "失败用例"} {
		if !strings.Contains(txt, want) {
			t.Errorf("Text() 缺少 %q:\n%s", want, txt)
		}
	}
	if strings.Contains(txt, "%!") {
		t.Errorf("Text() 含格式化错误:\n%s", txt)
	}
}
