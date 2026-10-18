// Package eval 实现「多模态 RAG 知识库」的黄金问答集评测。
//
// 设计成**黑盒 HTTP 客户端**（见 http.go）：直接打运行中的服务
// （POST /api/v1/search、POST /api/v1/chat），因此评测测的是真实链路，
// 不依赖 internal/rag 的内部实现。路径、字段名、错误码与
// docs/CONTRACT.md（冻结契约 v2）逐字一致。
//
// # 指标定义
//
// 所有指标的分母为 0 时返回 0，绝不返回 NaN。
//
//   - 召回率 Recall：ExpectedSources 非空的用例中，"至少一个期望来源出现在
//     top-k 结果里"的比例。期望来源按子串匹配（大小写不敏感、路径分隔符归一化），
//     命中任意一个即算该用例召回成功。分母 = 期望来源非空且未出错的用例数。
//   - MRR：期望来源最早出现名次的倒数均值（没出现记 0）。分母与召回率相同。
//   - 引用命中率 CitationHit：答案（structured 模式取 structured.history）里的 [n]
//     编号**全部**落在 sources 数组范围 [1, len(sources)] 内，**且至少有一个** [n]
//     指向期望来源。无期望来源的用例 skip（不计入分母）。分母为 0 时指标为 0，
//     Text() 会说明该指标不具统计意义。
//   - 关键词命中 KeywordHit：ExpectedKeywords 全部出现在答案里（大小写不敏感，
//     且忽略空白差异，使 "10 天" 与 "10天" 等价）。ExpectedKeywords 为空的用例 skip。
//   - unknown 正确率 UnknownCorrect：ExpectUnknown=true 的用例中，答案包含
//     "没有找到" / "未找到" / "查不到"，或 structured.unknown==true 的比例。
//   - 忠实度 Faithfulness：**代理指标，不是 LLM 评判**。做法是把答案按句切开，
//     对每句抽取中文 4-gram，若该句至少 60% 的 4-gram 能在检索到的上下文里找到，
//     则该句算"有据"；忠实度 = 有据句数 / 可评估句数（汉字少于 4 个、抽不出
//     4-gram 的句子不计入）。上下文取同一次问答返回的 sources 文本拼接。
//     升级路径：把 context + answer 交给 LLM judge 逐句判定，只需替换
//     faithfulnessScore 的实现，对外字段与报告格式不变。
//
// 答案类指标（引用命中率 / 关键词命中 / unknown 正确率 / 忠实度）只统计
// UseChat=true 且答案非空的用例；纯检索模式（UseChat=false）不花 token，
// 这些指标分母为 0，按 0 报告。
package eval

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"ragkb/internal/types"
)

const (
	// DefaultTopK 是 Options.TopK<=0 时使用的 top-k，与契约 config 示例的 top_k=4 一致。
	DefaultTopK = 4
	// topSourcesCap 限制 CaseResult.TopSources 的长度，便于报告阅读。
	topSourcesCap = 5
)

// Case 是黄金集里的一条用例。
type Case struct {
	ID               string       `yaml:"id" json:"id"`
	Question         string       `yaml:"question" json:"question"`
	ExpectedSources  []string     `yaml:"expected_sources,omitempty" json:"expected_sources,omitempty"`   // 期望命中的 source（相对路径或文件名子串）
	ExpectedKeywords []string     `yaml:"expected_keywords,omitempty" json:"expected_keywords,omitempty"` // 答案里应出现的关键词
	ExpectUnknown    bool         `yaml:"expect_unknown,omitempty" json:"expect_unknown,omitempty"`       // 知识库里确实没有 → 应回答"没找到"
	Filters          types.Filter `yaml:"filters,omitempty" json:"filters,omitempty"`
	Note             string       `yaml:"note,omitempty" json:"note,omitempty"`
}

// caseYAML 是 Case 的 YAML 中间表示。
//
// 之所以需要它：types.Filter 只带 json tag，yaml.v3 对无 tag 字段会退化成
// 全小写字段名（source_type 会变成 sourcetype），与契约的 snake_case 不一致。
// 这里显式声明契约字段名，再手工映射到 types.Filter，保证 golden.yaml 里
// 写的就是契约字段名（doc_ids / source_type / ...）。
type caseYAML struct {
	ID               string     `yaml:"id"`
	Question         string     `yaml:"question"`
	ExpectedSources  []string   `yaml:"expected_sources"`
	ExpectedKeywords []string   `yaml:"expected_keywords"`
	ExpectUnknown    bool       `yaml:"expect_unknown"`
	Filters          filterYAML `yaml:"filters"`
	Note             string     `yaml:"note"`
}

// filterYAML 与契约 2. Filter 的字段名一一对应。
type filterYAML struct {
	DocIDs      []string `yaml:"doc_ids"`
	Modalities  []string `yaml:"modalities"`
	Dynasty     []string `yaml:"dynasty"`
	Place       []string `yaml:"place"`
	Topic       []string `yaml:"topic"`
	SourceType  []string `yaml:"source_type"`
	Credibility []string `yaml:"credibility"`
	Abilities   []string `yaml:"abilities"`
	Tags        []string `yaml:"tags"`
}

// UnmarshalYAML 让 golden.yaml 使用契约的 snake_case 过滤字段名。
func (c *Case) UnmarshalYAML(value *yaml.Node) error {
	var cy caseYAML
	if err := value.Decode(&cy); err != nil {
		return err
	}
	c.ID = cy.ID
	c.Question = cy.Question
	c.ExpectedSources = cy.ExpectedSources
	c.ExpectedKeywords = cy.ExpectedKeywords
	c.ExpectUnknown = cy.ExpectUnknown
	c.Note = cy.Note
	c.Filters = types.Filter{
		DocIDs:      cy.Filters.DocIDs,
		Modalities:  toModalities(cy.Filters.Modalities),
		Dynasty:     cy.Filters.Dynasty,
		Place:       cy.Filters.Place,
		Topic:       cy.Filters.Topic,
		SourceType:  cy.Filters.SourceType,
		Credibility: cy.Filters.Credibility,
		Abilities:   cy.Filters.Abilities,
		Tags:        cy.Filters.Tags,
	}
	return nil
}

func toModalities(in []string) []types.Modality {
	if len(in) == 0 {
		return nil
	}
	out := make([]types.Modality, 0, len(in))
	for _, m := range in {
		out = append(out, types.Modality(strings.TrimSpace(m)))
	}
	return out
}

// GoldenSet 是一份黄金问答集。
type GoldenSet struct {
	Name  string `yaml:"name" json:"name"`
	Cases []Case `yaml:"cases" json:"cases"`
}

// LoadGolden 读取并校验一份黄金集。校验规则：
// 路径非空、YAML 可解析、至少一条用例、每条用例 id 与 question 非空、id 不重复。
func LoadGolden(path string) (*GoldenSet, error) {
	if strings.TrimSpace(path) == "" {
		return nil, fmt.Errorf("eval: golden 文件路径为空")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("eval: 读取 golden 文件 %s: %w", path, err)
	}
	var gs GoldenSet
	if err := yaml.Unmarshal(raw, &gs); err != nil {
		return nil, fmt.Errorf("eval: 解析 golden 文件 %s: %w", path, err)
	}
	if len(gs.Cases) == 0 {
		return nil, fmt.Errorf("eval: golden 文件 %s 没有任何用例", path)
	}
	seen := make(map[string]bool, len(gs.Cases))
	for i := range gs.Cases {
		c := &gs.Cases[i]
		c.ID = strings.TrimSpace(c.ID)
		c.Question = strings.TrimSpace(c.Question)
		if c.ID == "" {
			return nil, fmt.Errorf("eval: golden 文件 %s 第 %d 条用例缺少 id", path, i+1)
		}
		if c.Question == "" {
			return nil, fmt.Errorf("eval: golden 用例 %s 缺少 question", c.ID)
		}
		if seen[c.ID] {
			return nil, fmt.Errorf("eval: golden 用例 id 重复: %s", c.ID)
		}
		seen[c.ID] = true
	}
	return &gs, nil
}

// Client 抽象被测服务，方便测试注入假实现。
type Client interface {
	Search(ctx context.Context, query string, topK int, f types.Filter) ([]types.Hit, error)
	Chat(ctx context.Context, req ChatInput) (ChatOutput, error)
}

// ChatInput 是一次问答请求。
type ChatInput struct {
	Question     string
	TopK         int
	Filters      types.Filter
	AnswerFormat string // "text" | "structured"
}

// ChatOutput 是一次问答结果。
type ChatOutput struct {
	Answer     string
	Structured *types.StructuredAnswer
	Sources    []types.Hit
	ElapsedMS  int
}

// Options 控制一次评测。
type Options struct {
	TopK    int
	UseChat bool // false 时只测检索（不花 token）
}

// CaseResult 是一条用例的评测结果。
type CaseResult struct {
	Case         Case                    `json:"case"`
	Recalled     bool                    `json:"recalled"`
	RecallRank   int                     `json:"recall_rank"` // 期望来源最早出现在第几名，0 = 没出现
	CitationHit  bool                    `json:"citation_hit"`
	KeywordHit   bool                    `json:"keyword_hit"`
	UnknownOK    bool                    `json:"unknown_ok"`
	Faithfulness float64                 `json:"faithfulness"` // 0~1，代理指标
	Answer       string                  `json:"answer,omitempty"`
	TopSources   []string                `json:"top_sources,omitempty"`
	Structured   *types.StructuredAnswer `json:"structured,omitempty"`
	Err          string                  `json:"err,omitempty"`
}

// Report 是一次评测的汇总。
type Report struct {
	Total          int          `json:"total"`
	Recall         float64      `json:"recall"`       // 召回率
	MRR            float64      `json:"mrr"`          // 平均倒数排名
	CitationHit    float64      `json:"citation_hit"` // 引用命中率
	KeywordHit     float64      `json:"keyword_hit"`
	UnknownCorrect float64      `json:"unknown_correct"`
	Faithfulness   float64      `json:"faithfulness"` // 忠实度（代理指标）
	Cases          []CaseResult `json:"cases"`
	ElapsedMS      int          `json:"elapsed_ms"`
}

// Run 依次评测 golden 里的每条用例。
//
// 每条用例总是先调 Search 拿 top-k（用于召回率 / MRR）；UseChat=true 时再调一次
// Chat（answer_format="text"，非流式 JSON），用于引用 / 关键词 / unknown / 忠实度。
// 单条用例出错只记在该用例的 Err 上，不影响其他用例；用例出错时它的指标不计入分母。
func Run(ctx context.Context, c Client, golden *GoldenSet, opts Options) (*Report, error) {
	if c == nil {
		return nil, fmt.Errorf("eval: client 不能为 nil")
	}
	if golden == nil {
		return nil, fmt.Errorf("eval: golden 不能为 nil")
	}
	if len(golden.Cases) == 0 {
		return nil, fmt.Errorf("eval: golden 没有任何用例")
	}
	topK := opts.TopK
	if topK <= 0 {
		topK = DefaultTopK
	}

	start := time.Now()
	report := &Report{Total: len(golden.Cases), Cases: make([]CaseResult, 0, len(golden.Cases))}

	for _, tc := range golden.Cases {
		res := CaseResult{Case: tc}

		if err := ctx.Err(); err != nil {
			res.Err = fmt.Sprintf("context: %v", err)
			report.Cases = append(report.Cases, res)
			continue
		}

		hits, err := c.Search(ctx, tc.Question, topK, tc.Filters)
		if err != nil {
			res.Err = fmt.Sprintf("search: %v", err)
			report.Cases = append(report.Cases, res)
			continue
		}
		res.RecallRank = earliestExpectedRank(hits, tc.ExpectedSources)
		res.Recalled = res.RecallRank > 0
		res.TopSources = topSources(hits, topSourcesCap)

		if opts.UseChat {
			out, err := c.Chat(ctx, ChatInput{
				Question:     tc.Question,
				TopK:         topK,
				Filters:      tc.Filters,
				AnswerFormat: "text",
			})
			if err != nil {
				res.Err = fmt.Sprintf("chat: %v", err)
				report.Cases = append(report.Cases, res)
				continue
			}
			answer := effectiveAnswer(out)
			res.Answer = answer
			res.CitationHit = citationHit(tc.ExpectedSources, answer, out.Sources)
			res.KeywordHit = keywordHit(tc.ExpectedKeywords, answer)
			res.Structured = out.Structured
			res.UnknownOK = unknownOK(tc.ExpectUnknown, answer, out.Structured)
			res.Faithfulness = faithfulnessScore(answer, contextText(out.Sources))
		}

		report.Cases = append(report.Cases, res)
	}

	aggregate(report)
	report.ElapsedMS = int(time.Since(start).Milliseconds())
	return report, nil
}

// aggregate 按上面注释里的分母口径汇总指标。分母为 0 → 0，不产生 NaN。
func aggregate(r *Report) {
	var recallN, recallHitN, citeN, citeHitN, kwN, kwHitN, unkN, unkOKN, faithN int
	var mrrSum, faithSum float64

	for i := range r.Cases {
		c := &r.Cases[i]
		if recallApplicable(*c) {
			recallN++
			if c.Recalled {
				recallHitN++
			}
			if c.RecallRank > 0 {
				mrrSum += 1 / float64(c.RecallRank)
			}
		}
		if citationApplicable(*c) {
			citeN++
			if c.CitationHit {
				citeHitN++
			}
		}
		if keywordApplicable(*c) {
			kwN++
			if c.KeywordHit {
				kwHitN++
			}
		}
		if unknownApplicable(*c) {
			unkN++
			if c.UnknownOK {
				unkOKN++
			}
		}
		if faithApplicable(*c) {
			faithN++
			faithSum += c.Faithfulness
		}
	}

	r.Recall = ratio(recallHitN, recallN)
	r.MRR = ratioFloat(mrrSum, recallN)
	r.CitationHit = ratio(citeHitN, citeN)
	r.KeywordHit = ratio(kwHitN, kwN)
	r.UnknownCorrect = ratio(unkOKN, unkN)
	r.Faithfulness = ratioFloat(faithSum, faithN)
}

// ratio 是整数比；分母为 0 时返回 0。
func ratio(hit, total int) float64 {
	if total <= 0 {
		return 0
	}
	return float64(hit) / float64(total)
}

// ratioFloat 是浮点均值；分母为 0 时返回 0。
func ratioFloat(sum float64, total int) float64 {
	if total <= 0 {
		return 0
	}
	return sum / float64(total)
}

// ---- 分母口径：Run 的 aggregate 与 Text() 共用这几个判定，保证报告与 JSON 一致。----

// recallApplicable：有期望来源且请求未出错。
func recallApplicable(c CaseResult) bool {
	return len(c.Case.ExpectedSources) > 0 && c.Err == ""
}

// citationApplicable：有期望来源、请求未出错、答案非空。
func citationApplicable(c CaseResult) bool {
	return len(c.Case.ExpectedSources) > 0 && c.Err == "" && c.Answer != ""
}

// keywordApplicable：声明了关键词、请求未出错、答案非空。
func keywordApplicable(c CaseResult) bool {
	return len(c.Case.ExpectedKeywords) > 0 && c.Err == "" && c.Answer != ""
}

// unknownApplicable：expect_unknown 用例、请求未出错、答案非空。
func unknownApplicable(c CaseResult) bool {
	return c.Case.ExpectUnknown && c.Err == "" && c.Answer != ""
}

// faithApplicable：请求未出错、答案非空，且不是"知识库里没有找到"。
//
// 后者没有事实陈述可核对，计入分母会把忠实度拉低成噪声。
func faithApplicable(c CaseResult) bool {
	return c.Err == "" && c.Answer != "" && !looksNotFound(c.Answer, c.Structured)
}

// Text 渲染人类可读的中文报告：总体指标表 + 逐用例表 + 失败用例清单 + 口径说明。
// 对 nil 接收者安全，不会 panic。
func (r *Report) Text() string {
	if r == nil {
		return "=== RAG 评测报告 ===\n报告为 nil\n"
	}
	var b strings.Builder
	total := r.Total
	if total == 0 {
		total = len(r.Cases)
	}

	b.WriteString("=== RAG 评测报告 ===\n")
	fmt.Fprintf(&b, "用例数: %d    总耗时: %d ms\n\n", total, r.ElapsedMS)

	var recallN, citeN, kwN, unkN, faithN int
	for _, c := range r.Cases {
		if recallApplicable(c) {
			recallN++
		}
		if citationApplicable(c) {
			citeN++
		}
		if keywordApplicable(c) {
			kwN++
		}
		if unknownApplicable(c) {
			unkN++
		}
		if faithApplicable(c) {
			faithN++
		}
	}

	b.WriteString("指标                数值      分母\n")
	fmt.Fprintf(&b, "%-18s  %-8.3f  %d\n", "召回率", r.Recall, recallN)
	fmt.Fprintf(&b, "%-18s  %-8.3f  %d\n", "MRR", r.MRR, recallN)
	fmt.Fprintf(&b, "%-18s  %-8.3f  %d\n", "引用命中率", r.CitationHit, citeN)
	fmt.Fprintf(&b, "%-18s  %-8.3f  %d\n", "关键词命中", r.KeywordHit, kwN)
	fmt.Fprintf(&b, "%-18s  %-8.3f  %d\n", "unknown 正确率", r.UnknownCorrect, unkN)
	fmt.Fprintf(&b, "%-18s  %-8.3f  %d\n", "忠实度(代理指标)", r.Faithfulness, faithN)
	b.WriteString("\n")

	b.WriteString("逐用例:\n")
	fmt.Fprintf(&b, "%-28s %-4s %-4s %-4s %-6s %-7s %-6s %s\n",
		"ID", "召回", "名次", "引用", "关键词", "unknown", "忠实度", "备注")
	for _, c := range r.Cases {
		fmt.Fprintf(&b, "%-28s %-4s %-4s %-4s %-6s %-7s %-6s %s\n",
			c.Case.ID,
			yesNoMark(recallApplicable(c), c.Recalled),
			rankText(recallApplicable(c), c.RecallRank),
			yesNoMark(citationApplicable(c), c.CitationHit),
			yesNoMark(keywordApplicable(c), c.KeywordHit),
			yesNoMark(unknownApplicable(c), c.UnknownOK),
			faithText(faithApplicable(c), c.Faithfulness),
			caseNote(c),
		)
	}

	b.WriteString("\n失败用例:\n")
	failCount := 0
	for _, c := range r.Cases {
		reasons := failureReasons(c)
		if len(reasons) == 0 {
			continue
		}
		failCount++
		fmt.Fprintf(&b, "- %s: %s\n", c.Case.ID, strings.Join(reasons, "；"))
	}
	if failCount == 0 {
		b.WriteString("（无）\n")
	}

	b.WriteString("\n说明:\n")
	b.WriteString("- 忠实度是**代理指标**：把答案按句切开，只保留汉字/字母/数字（丢掉空白、标点与 [1] 这类引用标记）后滑窗取 3-gram，被上下文支撑的字符占比 ≥60% 即算该句「有据」；忠实度 = 有据句数 / 可评估句数。\\n")
	b.WriteString("- 它**只做字面比对，不理解语义**，因此有两类偏差：一是同义改写（把「年假」写成「带薪年休假」）可能被误判无据；二是把上下文里的几个事实拼接成新句子时，连接词占比过高也可能被误判无据。阈值与 n-gram 长度是依据真实模型输出校准的，换模型或换语料后建议重新核对。\\n")
	b.WriteString("- 因此这个数字应读作「未检测到无出处的表述」，而不是「事实已核实」。升级路径：接入 LLM judge 逐句判定并替换 faithfulnessScore，对外字段与报告格式不变。\\n")
	b.WriteString("- 答案类指标（引用 / 关键词 / unknown / 忠实度）只统计「答案非空」的用例；纯检索模式（UseChat=false）这些分母为 0，按 0 报告。\\n")
	b.WriteString("- 「知识库里没有找到」类回答没有事实陈述可核对，已从忠实度分母中排除（表中显示 -）。\\n")
	if recallN == 0 {
		b.WriteString("- 召回率 / MRR 分母为 0：没有用例声明 expected_sources（或全部请求失败），按 0 报告。\n")
	}
	if citeN == 0 {
		b.WriteString("- 引用命中率分母为 0：没有用例同时具备 expected_sources 与非空答案，该指标不具统计意义，按 0 报告。\n")
	}
	if kwN == 0 {
		b.WriteString("- 关键词命中分母为 0：没有用例声明 expected_keywords 且有非空答案，按 0 报告。\n")
	}
	if unkN == 0 {
		b.WriteString("- unknown 正确率分母为 0：没有 expect_unknown 用例（或答案缺失），按 0 报告。\n")
	}
	return b.String()
}

func yesNoMark(applicable, ok bool) string {
	if !applicable {
		return "-"
	}
	if ok {
		return "是"
	}
	return "否"
}

func rankText(applicable bool, rank int) string {
	if !applicable {
		return "-"
	}
	return fmt.Sprintf("%d", rank)
}

func faithText(applicable bool, v float64) string {
	if !applicable {
		return "-"
	}
	return fmt.Sprintf("%.2f", v)
}

func caseNote(c CaseResult) string {
	if c.Err != "" {
		return "错误: " + c.Err
	}
	return ""
}

// failureReasons 汇总一条用例的失败原因，供 Text() 的失败清单使用。
func failureReasons(c CaseResult) []string {
	if c.Err != "" {
		return []string{"请求失败: " + c.Err}
	}
	var reasons []string
	if recallApplicable(c) && !c.Recalled {
		reasons = append(reasons, fmt.Sprintf("未召回期望来源 %v", c.Case.ExpectedSources))
	}
	if citationApplicable(c) && !c.CitationHit {
		reasons = append(reasons, "引用未命中（[n] 越界或无 [n] 指向期望来源）")
	}
	if keywordApplicable(c) && !c.KeywordHit {
		reasons = append(reasons, fmt.Sprintf("关键词未全部命中（期望 %v）", c.Case.ExpectedKeywords))
	}
	if unknownApplicable(c) && !c.UnknownOK {
		reasons = append(reasons, "应回答「没找到」但未判定为 unknown")
	}
	return reasons
}
