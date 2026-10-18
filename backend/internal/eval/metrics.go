package eval

import (
	"path"
	"regexp"
	"strconv"
	"strings"
	"unicode"

	"ragkb/internal/types"
)

// faithfulnessNGram 是忠实度代理指标使用的中文 n-gram 长度。
//
// 取 3 而不是 4：表格类语料常被模型"合成"成新句子——例如语料是
// 「| 满 3 年不满 5 年 | 10 天 |」两格，模型答成「满 3 年不满 5 年的员工，
// 年假为 10 天」——插入的连接词会让 4-gram 大面积错位，把**正确**答案判成
// 无据。3-gram 能容忍单字差异，同时仍能识别整段捏造（捏造句的覆盖率接近 0）。
const faithfulnessNGram = 3

// faithfulnessGramThreshold 是判定单句「有据」的 n-gram 覆盖率阈值（≥60%）。
const faithfulnessGramThreshold = 0.6

// ---- 来源匹配（召回率 / MRR / 引用命中率共用）----

// normalizeSource 统一路径分隔符与大小写，去掉 "./" 前缀。
func normalizeSource(s string) string {
	s = strings.ReplaceAll(s, "\\", "/")
	s = strings.ToLower(strings.TrimSpace(s))
	s = strings.TrimPrefix(s, "./")
	return s
}

// sourceMatchesAny 判断 source 是否命中任一期望来源。
// 期望来源按**子串**匹配，因此既可写完整相对路径，也可只写文件名。
func sourceMatchesAny(expected []string, source string) bool {
	if strings.TrimSpace(source) == "" {
		return false
	}
	norm := normalizeSource(source)
	base := path.Base(norm)
	for _, e := range expected {
		e = normalizeSource(e)
		if e == "" {
			continue
		}
		if strings.Contains(norm, e) {
			return true
		}
		// 服务端 source 可能带目录前缀，因此再用文件名比一次。
		if base != "" && strings.Contains(base, e) {
			return true
		}
	}
	return false
}

// earliestExpectedRank 返回期望来源最早出现的名次（1 起），没出现返回 0。
// hits 按相关度排序；Rank<=0 时退化为数组下标。
func earliestExpectedRank(hits []types.Hit, expected []string) int {
	if len(expected) == 0 || len(hits) == 0 {
		return 0
	}
	best := 0
	for i, h := range hits {
		if !sourceMatchesAny(expected, h.Chunk.Source) {
			continue
		}
		rank := h.Rank
		if rank <= 0 {
			rank = i + 1
		}
		if best == 0 || rank < best {
			best = rank
		}
	}
	return best
}

// topSources 取前 capN 个非空 source，供报告展示。
func topSources(hits []types.Hit, capN int) []string {
	if capN <= 0 {
		return nil
	}
	out := make([]string, 0, capN)
	for _, h := range hits {
		if len(out) >= capN {
			break
		}
		if src := strings.TrimSpace(h.Chunk.Source); src != "" {
			out = append(out, src)
		}
	}
	return out
}

// ---- 答案文本与上下文 ----

// effectiveAnswer 取用于评测的答案文本：structured 模式优先用 structured.history
// （史实陈述），否则用 answer 原文。
func effectiveAnswer(out ChatOutput) string {
	if out.Structured != nil && strings.TrimSpace(out.Structured.History) != "" {
		return out.Structured.History
	}
	return out.Answer
}

// contextText 把检索到的 sources 文本拼成忠实度判定用的上下文。
func contextText(sources []types.Hit) string {
	var b strings.Builder
	for _, h := range sources {
		b.WriteString(h.Chunk.Text)
		b.WriteByte('\n')
	}
	return b.String()
}

// ---- 引用命中率 ----

// citationRe 匹配契约里的引用编号 [n]，同时兼容全角括号 【n】。
var citationRe = regexp.MustCompile(`[\[【](\d+)[\]】]`)

// extractCitations 抽取答案里出现的全部引用编号。
func extractCitations(answer string) []int {
	matches := citationRe.FindAllStringSubmatch(answer, -1)
	if len(matches) == 0 {
		return nil
	}
	nums := make([]int, 0, len(matches))
	for _, m := range matches {
		n, err := strconv.Atoi(m[1])
		if err != nil {
			continue
		}
		nums = append(nums, n)
	}
	return nums
}

// citationHit 判定引用是否命中：
//  1. 用例必须声明了期望来源（否则该指标 skip，这里返回 false）；
//  2. 答案里至少有一个 [n]；
//  3. 所有 [n] 都落在 sources 的有效范围 [1, len(sources)] 内；
//  4. 至少有一个 [n] 指向期望来源。
func citationHit(expected []string, answer string, sources []types.Hit) bool {
	if len(expected) == 0 {
		return false
	}
	nums := extractCitations(answer)
	if len(nums) == 0 {
		return false
	}
	hitExpected := false
	for _, n := range nums {
		if n < 1 || n > len(sources) {
			return false
		}
		if sourceMatchesAny(expected, sources[n-1].Chunk.Source) {
			hitExpected = true
		}
	}
	return hitExpected
}

// ---- 关键词命中 ----

// normalizeForMatch 用于关键词匹配：大小写不敏感 + 忽略空白差异，
// 使 "10 天" 与 "10天"、全角/半角空格差异不影响判定。
func normalizeForMatch(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range strings.ToLower(s) {
		if unicode.IsSpace(r) {
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

// keywordHit 判定所有期望关键词是否都出现在答案里（忽略大小写与空白）。
// 未声明关键词时返回 false；该用例在汇总时 skip，不计入分母。
func keywordHit(keywords []string, answer string) bool {
	if len(keywords) == 0 {
		return false
	}
	norm := normalizeForMatch(answer)
	if norm == "" {
		return false
	}
	checked := 0
	for _, kw := range keywords {
		k := normalizeForMatch(kw)
		if k == "" {
			continue
		}
		checked++
		if !strings.Contains(norm, k) {
			return false
		}
	}
	return checked > 0
}

// ---- unknown 判定 ----

// unknownPhrases 是契约约定的「查不到」表述。
var unknownPhrases = []string{"没有找到", "未找到", "查不到"}

// unknownOK 判定一次「知识库里没有」的问答是否回答正确：
// structured.unknown==true，或答案包含契约约定的「没找到」表述。
// 非 ExpectUnknown 用例恒为 false（汇总时 skip）。
func unknownOK(expectUnknown bool, answer string, structured *types.StructuredAnswer) bool {
	if !expectUnknown {
		return false
	}
	if structured != nil && structured.Unknown {
		return true
	}
	for _, p := range unknownPhrases {
		if strings.Contains(answer, p) {
			return true
		}
	}
	return false
}

// ---- 忠实度（代理指标）----

// looksNotFound 判断一个回答是否只是"知识库里没有找到"。
//
// 这类回答没有做出任何事实陈述，无从谈忠实，计入分母只会把指标拉低。
// 加长度限制是为了不误伤"资料里没有找到 X，但 Y 是……"这种既有内容
// 又提到查不到的正常回答。
func looksNotFound(answer string, structured *types.StructuredAnswer) bool {
	if structured != nil && structured.Unknown {
		return true
	}
	t := strings.TrimSpace(answer)
	if t == "" {
		return true
	}
	// 只看第一句：模型的常见写法是「知识库里没有找到相关内容。」后面再补一段
	// 解释为什么没有。整段用长度阈值判断会漏掉这种（真实踩过）。
	first := t
	if i := strings.IndexAny(t, "。！？!?\n"); i >= 0 {
		first = strings.TrimSpace(t[:i])
	}
	if len([]rune(first)) > 40 {
		return false
	}
	for _, p := range unknownPhrases {
		if strings.Contains(first, p) {
			return true
		}
	}
	return false
}

// faithfulnessScore 计算忠实度代理指标（逐句位置覆盖法）：
//
// 把答案按句切开，去掉空白后在句子上滑窗取 4-gram；某个位置的 4-gram 能在
// 上下文里找到，就算该位置被"支撑"。句子被支撑的比例 ≥60% 即算「有据」，
// 返回 有据句数 / 可评估句数。汉字少于 4 个的句子不计入；无答案或无上下文返回 0。
//
// 为什么不是"≥60% 的 4-gram 命中"：那种算法对插入/改写极其敏感——模型在
// 原文中间加一个"出生于"，下游所有 4-gram 全部错位失配，**完全正确的答案会被
// 判成 0 分**（真实踩过：1874 年 11 月 30 日那条）。按位置覆盖只要求内容有出处，
// 允许连接词与语序差异；而整段凭空捏造依然会因为覆盖率为 0 被判无据。
//
// 这是**代理指标，不是 LLM 评判**。升级路径：把 answer + context 交给 LLM judge
// 逐句判定，只需替换本函数实现，对外字段与报告格式不变。
func faithfulnessScore(answer, ctxText string) float64 {
	answer = strings.TrimSpace(answer)
	if answer == "" || strings.TrimSpace(ctxText) == "" {
		return 0
	}
	normCtx := normalizeForCompare(ctxText)

	total, supported := 0, 0
	for _, sentence := range splitSentences(answer) {
		runes := []rune(normalizeForCompare(sentence))
		if len(runes) < faithfulnessNGram {
			continue
		}
		covered := make([]bool, len(runes))
		hit := 0
		for i := 0; i+faithfulnessNGram <= len(runes); i++ {
			if !strings.Contains(normCtx, string(runes[i:i+faithfulnessNGram])) {
				continue
			}
			for j := i; j < i+faithfulnessNGram; j++ {
				if !covered[j] {
					covered[j] = true
					hit++
				}
			}
		}
		total++
		if float64(hit)/float64(len(runes)) >= faithfulnessGramThreshold {
			supported++
		}
	}
	if total == 0 {
		return 0
	}
	return float64(supported) / float64(total)
}

// normalizeForCompare 只保留汉字、字母、数字，丢掉空白、标点与 [1] 这类
// 引用标记的方括号。
//
// 必须这么做：覆盖率是"被支撑字符数 / 句子字符数"，若把 "[1]" 和句末句号
// 也算进分母，一句完全有据的短句覆盖率会被稀释到阈值以下（真实踩过）。
func normalizeForCompare(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	runes := []rune(s)
	for i := 0; i < len(runes); i++ {
		// 先整段丢掉 [1] / [1,3] 这类引用标记：它是出处元数据，不是事实陈述，
		// 留在分母里会把一句完全有据的短句覆盖率稀释到阈值以下（真实踩过）。
		if runes[i] == '[' && isCitationBody(runes, i) {
			i = citationEnd(runes, i)
			continue
		}
		if unicode.IsLetter(runes[i]) || unicode.IsDigit(runes[i]) {
			b.WriteRune(runes[i])
		}
	}
	return b.String()
}

// isCitationBody 判断 runes[i] == '[' 开始的是不是 [1] / [1,3] / [1-3] 这类引用标记。
func isCitationBody(runes []rune, i int) bool {
	j := i + 1
	seen := false
	for ; j < len(runes) && runes[j] != ']'; j++ {
		r := runes[j]
		if unicode.IsDigit(r) {
			seen = true
			continue
		}
		if r == ',' || r == '-' || r == ' ' {
			continue
		}
		return false
	}
	return seen && j < len(runes)
}

// citationEnd 返回引用标记 ']' 的下标；调用方保证 isCitationBody 已为真。
func citationEnd(runes []rune, i int) int {
	for j := i + 1; j < len(runes); j++ {
		if runes[j] == ']' {
			return j
		}
	}
	return i
}

// splitSentences 按中英文句末标点与换行切句，丢弃空白片段。
func splitSentences(s string) []string {
	var out []string
	var b strings.Builder
	flush := func() {
		if seg := strings.TrimSpace(b.String()); seg != "" {
			out = append(out, seg)
		}
		b.Reset()
	}
	for _, r := range s {
		if isSentenceEnd(r) {
			flush()
			continue
		}
		b.WriteRune(r)
	}
	flush()
	return out
}

func isSentenceEnd(r rune) bool {
	switch r {
	case '。', '！', '？', '!', '?', '；', ';', '\n', '\r', '\t':
		return true
	}
	return false
}

// chineseNGrams 抽取字符串里每段连续汉字的所有 n-gram。
// 非汉字字符（标点、数字、字母、空白）会打断连续段。
func chineseNGrams(s string, n int) []string {
	if n <= 0 {
		return nil
	}
	var out []string
	run := make([]rune, 0, 32)
	flush := func() {
		if len(run) >= n {
			for i := 0; i+n <= len(run); i++ {
				out = append(out, string(run[i:i+n]))
			}
		}
		run = run[:0]
	}
	for _, r := range s {
		if unicode.Is(unicode.Han, r) {
			run = append(run, r)
			continue
		}
		flush()
	}
	flush()
	return out
}

// removeWhitespace 去掉所有空白字符，避免换行/空格把上下文里的 4-gram 切断。
func removeWhitespace(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		if unicode.IsSpace(r) {
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}
