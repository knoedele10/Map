package rag

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"ragkb/internal/llm"
)

// 拍照识景：给一张照片，判断它是候选里的哪一处古迹。
//
// 为什么不让模型自由回答「这是哪儿」：本机实测（work/recognize/exp1_report.txt、
// exp2_report.txt），自由回答在 42 张真·成都武侯祠照片上只命中 38 张，还会把武侯祠
// 说成「桃园三结义景区」；换成「候选受限单选 + 每个候选给一句肉眼可见的特征」之后
// 命中 41/42，而且杜甫草堂 / 宽窄巷子 / 非景点照片 0 误报。
//
// 这里同样服务于 /api/v1/chat：带图的一轮先识别出景点，再拿景点名去检索，
// 否则用户问「这是什么地方？」，检索词就是「这是什么地方？」，必然什么都检索不到。
const (
	// RecognizeMaxCandidates 是候选表上限。
	RecognizeMaxCandidates = 20
	recognizeMaxTokens     = 64
)

// RecognizeCandidate 是一个候选古迹。Hint 写的是「照片里看得见的东西」。
type RecognizeCandidate struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Hint string `json:"hint"`
}

// RecognizeResult 是一次图片识别的结果。Raw 是模型原话，不加工，方便人工复核。
type RecognizeResult struct {
	Landmark   string   `json:"landmark"`
	SiteID     string   `json:"site_id"`
	Matched    bool     `json:"matched"`
	MatchMode  string   `json:"match_mode"`
	Raw        string   `json:"raw"`
	Candidates []string `json:"candidates,omitempty"`
	Model      string   `json:"model"`
	ElapsedMS  int      `json:"elapsed_ms"`
	// RetrievalScore 是检索快速通道给出的相似度。达标时 MatchMode=retrieval、
	// 结果直接来自它；不达标时会回落到大模型，这个字段留着说明"为什么回落"。
	RetrievalScore float64 `json:"retrieval_score,omitempty"`
}

// visualHints 是候选的「照片里看得见的特征」。
//
// 用人工写的视觉特征，而不是景点简介：简介是史实叙述（「纪念诸葛亮的祠堂」），
// 对视觉模型没有区分度；「红墙夹道、翠竹成荫」才是它真能从像素里对上号的东西。
// 实测这一步值 7 个百分点（无特征 38/42 → 有特征 41/42）。
// 这几条在 2026-10-14 的交叉评测（work/harvest/cross-matrix.txt）之后改过一次：
// 杜甫草堂只有 70%（3 张被判成武侯祠、2 张三苏祠），金沙 80%（4 张被判成三星堆）。
// 让视觉模型逐张描述那些判错的照片之后才看清原因——
//   * 草堂判错的几张拍的是工部祠 / 大雅堂 / 杜甫像 / 盆景园，原提示只写了「茅草屋」，
//     模型找不到对应物就滑向同样「川西祠堂院落」的武侯祠和三苏祠；
//   * 金沙判错的几张是金面具展柜特写，而三星堆的提示里也写着「面具」，两边撞车。
// 所以把「庙里那块匾写什么」「立着谁的像」「是金面具还是青铜面具」这些
// 肉眼真能看见的区分点写进去，并明确写出「不是……」。
var visualHints = map[string]string{
	"wuhou":      "成都武侯祠：朱红宫墙夹道、翠竹成荫，明清祠堂院落，匾额写「武侯祠」「三义庙」「汉昭烈庙」，有诸葛亮殿与刘备惠陵土冢",
	"dufu":       "杜甫草堂：成片竹林与浣花溪水景，茅草屋顶的茅屋故居，园区立杜甫雕像，匾额写「草堂」「工部祠」「大雅堂」「盆景园」，红柱灰瓦的川西院落",
	"dujiangyan": "都江堰：岷江分水鱼嘴、安澜索桥、二王庙等古代水利工程",
	"sanxingdui": "三星堆博物馆：青铜色螺旋曲面新馆，园区立巨大青铜纵目面具雕塑，馆内是青铜纵目面具、青铜神树",
	"jinsha":     "金沙遗址博物馆：园区有巨大的金色太阳神鸟圆形雕塑、土黄色方形展馆与大跨度钢结构的遗迹馆保护棚，馆内太阳神鸟金饰与金面具，展柜常配暗红墙面",
	"leshan":     "乐山大佛：依山崖凿成的巨大弥勒坐佛",
	"jianmen":    "剑门关：剑门关楼、绝壁栈道、蜀道险关",
	"sansu":      "三苏祠：眉山苏氏祠堂园林，苏宅古井、银杏与木假山堂，匾额写「三苏祠」，园中没有茅草屋、也没有杜甫像",
	"kuanzhai":   "宽窄巷子：清代川西民居街巷、青砖门头、商业步行街",
}

// extraCandidates 永远排在候选表末尾，保证「都不是」始终可选。
var extraCandidates = []RecognizeCandidate{
	{Name: "其他景点", Hint: "以上都不是的景点"},
	{Name: "不是景点", Hint: "照片里没有景点（人物、物品、截图、食物等）"},
}

// knownCandidates 是 sites.json 里没有、但照片里很常见的地点，补进候选表。
var knownCandidates = []RecognizeCandidate{
	{ID: "kuanzhai", Name: "宽窄巷子", Hint: visualHints["kuanzhai"]},
}

// Candidates 返回缺省候选表：景点库里"有视觉特征"的景点 + 常见补充 + 「其他景点 / 不是景点」。
//
// 只有写了人工视觉特征的景点才进候选表。sites.json 里有「成都」这种城市级条目，
// 它跟任何一张成都的照片都对得上——放进候选表就会把宽窄巷子、武侯祠的照片全吞成「成都」。
// 没写特征的景点（含城市级条目）因此不参与识别，识别不出来会落到「其他景点」。
// 只有在一个特征都没写的情况下（换了一份完全不同的 sites.json）才退化成"全都当候选"。
//
// 候选表来自 sites.json，所以识别结果天然带 site_id，可以直接拿去查景点卡片。
func (s *Service) Candidates() []RecognizeCandidate {
	limit := RecognizeMaxCandidates - len(extraCandidates)
	out := make([]RecognizeCandidate, 0, RecognizeMaxCandidates)
	seen := map[string]bool{}
	if s.sites != nil {
		for _, site := range s.sites.List() {
			if len(out) >= limit {
				break
			}
			hint := visualHints[site.ID]
			if hint == "" {
				continue
			}
			out = append(out, RecognizeCandidate{ID: site.ID, Name: site.Name, Hint: hint})
			seen[site.Name] = true
		}
	}
	if len(out) == 0 && s.sites != nil {
		for _, site := range s.sites.List() {
			if len(out) >= limit {
				break
			}
			out = append(out, RecognizeCandidate{ID: site.ID, Name: site.Name, Hint: firstSentence(site.Summary)})
			seen[site.Name] = true
		}
	}
	for _, c := range knownCandidates {
		if len(out) >= limit {
			break
		}
		if c.Name == "" || seen[c.Name] {
			continue
		}
		out = append(out, c)
		seen[c.Name] = true
	}
	return append(out, extraCandidates...)
}

// Recognize 用缺省候选表识别一张图。
//
// 传 nil 而不是 s.Candidates()：候选表为空是「用缺省表」的信号，
// 只有这条路才会先走检索快速通道（见 RecognizeWith）。
func (s *Service) Recognize(ctx context.Context, image string) (*RecognizeResult, error) {
	return s.RecognizeWith(ctx, image, nil)
}

// RecognizeWith 用指定候选表识别一张图。候选表为空时回退到缺省表。
//
// 两步走：
//  1. 候选表为空（= 用缺省表）时，先问检索快速通道（PP-ShiTuV2，约 100 ms、纯 CPU）。
//     相似度达标就直接返回，连大模型都不用叫。
//  2. 否则走原来的受限候选单选（视觉大模型）。
//
// 调用方显式给了候选表（例如游戏页"细看是哪一处"的 8 个细分候选）时**不会**走检索 ——
// 检索库里只有景点级别，答不了细分。
func (s *Service) RecognizeWith(ctx context.Context, image string, cands []RecognizeCandidate) (*RecognizeResult, error) {
	if strings.TrimSpace(image) == "" {
		return nil, errors.New("图片为空")
	}
	// rejectedScore 记录"快速通道看过了但没把握"的分数，回落时带出去，方便排查。
	rejectedScore := 0.0
	if len(cands) == 0 {
		if res, score, ok := s.recognizeByRetrieval(ctx, image); ok {
			return res, nil
		} else {
			rejectedScore = score
		}
	}
	// 带图请求走视觉模型；没配视觉模型就回退到文本模型（契约 §2 规则 2）。
	chatter := s.visionChatter
	if chatter == nil {
		chatter = s.chatter
	}
	if chatter == nil {
		return nil, fmt.Errorf("%w: 生成模型未配置（chat.api_key 为空）", llm.ErrNotConfigured)
	}
	if len(cands) == 0 {
		cands = s.Candidates()
	}
	if len(cands) > RecognizeMaxCandidates {
		cands = cands[:RecognizeMaxCandidates]
	}

	started := time.Now()
	raw, err := s.askOnce(ctx, chatter, image, recognizePrompt(cands))
	if err != nil {
		return nil, err
	}
	cand, mode, matched := matchCandidate(raw, cands)
	res := &RecognizeResult{
		Landmark:       cand.Name,
		SiteID:         cand.ID,
		Matched:        matched,
		MatchMode:      mode,
		Raw:            raw,
		Model:          chatter.Model(),
		RetrievalScore: rejectedScore,
	}
	if !matched {
		res.Landmark = trimPunct(raw)
	}
	for _, c := range cands {
		res.Candidates = append(res.Candidates, c.Name)
	}
	res.ElapsedMS = int(time.Since(started).Milliseconds())
	return res, nil
}

// recognizeByRetrieval 先问检索快速通道。第二个返回值是检索给出的相似度（哪怕没达标），
// 第三个返回值表示"是否可以直接采用"。
//
// 任何异常（服务没起、超时、返回坏数据）都当作快速通道不可用：返回 false，让调用方走大模型。
// 可用性优先——识别功能不能因为一个可选组件挂了就整体不可用。
func (s *Service) recognizeByRetrieval(ctx context.Context, image string) (*RecognizeResult, float64, bool) {
	if s.retrieval == nil || !s.retrieval.Enabled() {
		return nil, 0, false
	}
	started := time.Now()
	res, accepted, err := s.retrieval.Retrieve(ctx, image)
	if err != nil {
		return nil, 0, false
	}
	if !accepted {
		return nil, res.Score, false
	}
	name := res.SiteID
	if s.sites != nil {
		if site, ok := s.sites.Get(res.SiteID); ok && site.Name != "" {
			name = site.Name
		}
	}
	return &RecognizeResult{
		Landmark:  name,
		SiteID:    res.SiteID,
		Matched:   true,
		MatchMode: "retrieval",
		Raw: fmt.Sprintf("PP-ShiTuV2 检索相似度 %.3f（阈值 %.2f）",
			res.Score, s.retrieval.MinScore()),
		Model:          "pp-shitu-v2",
		ElapsedMS:      int(time.Since(started).Milliseconds()),
		RetrievalScore: res.Score,
	}, res.Score, true
}

// recognizePrompt 拼出受限选择提示词。候选清单是唯一的事实来源，模型只能从里面挑。
func recognizePrompt(cands []RecognizeCandidate) string {
	var b strings.Builder
	b.WriteString("这张照片拍的是下面哪一个？每个选项后面是它的典型视觉特征。")
	b.WriteString("\n")
	for _, c := range cands {
		if c.Hint != "" {
			fmt.Fprintf(&b, "- %s：%s\n", c.Name, c.Hint)
		} else {
			fmt.Fprintf(&b, "- %s\n", c.Name)
		}
	}
	b.WriteString("只输出选项名称，不要解释。")
	return b.String()
}

// matchCandidate 把模型输出对回候选名。
//
// 模型很爱加标点或前缀（「武侯祠。」/「答案：武侯祠」），所以先精确匹配再包含匹配；
// 包含匹配按名字长度从长到短，避免短名先吃掉长名。
func matchCandidate(raw string, cands []RecognizeCandidate) (RecognizeCandidate, string, bool) {
	flat := trimPunct(raw)
	for _, c := range cands {
		if flat == c.Name {
			return c, "exact", true
		}
	}
	best := -1
	for i, c := range cands {
		if strings.Contains(flat, c.Name) {
			if best < 0 || len([]rune(c.Name)) > len([]rune(cands[best].Name)) {
				best = i
			}
		}
	}
	if best >= 0 {
		return cands[best], "contains", true
	}
	// 兜底：模型说了个候选表以外的说法（例如只说「武侯」）。
	if strings.Contains(flat, "武侯") {
		for _, c := range cands {
			if c.Name == "武侯祠" {
				return c, "contains", true
			}
		}
	}
	return RecognizeCandidate{}, "fallback", false
}

// trimPunct 去掉空白与句读，方便和候选名比对。
func trimPunct(s string) string {
	flat := strings.Join(strings.Fields(s), "")
	return strings.Trim(flat, "。.,，,：:；;！!？?、\"'“”")
}

// askOnce 跑一次非流式生成：读干净 Stream 通道，拼出完整回答。
// 识别只要一个词，所以 num_predict 压到 64，实测单张 0.3s 左右。
func (s *Service) askOnce(ctx context.Context, chatter llm.Chatter, image, prompt string) (string, error) {
	ch, err := chatter.Stream(ctx, llm.ChatRequest{
		System:      "你是景点识别器。只输出选项名称，不要解释。",
		Messages:    []llm.Message{{Role: "user", Content: prompt, Images: []string{image}}},
		Temperature: 0,
		MaxTokens:   recognizeMaxTokens,
	})
	if err != nil {
		return "", err
	}
	var b strings.Builder
	for d := range ch {
		if d.Err != nil {
			return "", d.Err
		}
		b.WriteString(d.Content)
		if d.Done {
			break
		}
	}
	out := strings.TrimSpace(b.String())
	if out == "" {
		return "", errors.New("模型没有返回任何内容")
	}
	return out, nil
}

// firstSentence 取简介的第一句，给没有人工视觉特征的景点兜底。
func firstSentence(s string) string {
	s = strings.TrimSpace(strings.Join(strings.Fields(s), " "))
	if s == "" {
		return ""
	}
	for _, sep := range []string{"。", "；", ";"} {
		if i := strings.Index(s, sep); i > 0 {
			s = s[:i]
			break
		}
	}
	r := []rune(s)
	if len(r) > 60 {
		return string(r[:60])
	}
	return s
}
