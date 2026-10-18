// Package types holds the data contracts shared by every layer of the RAG service.
package types

import (
	"crypto/rand"
	"encoding/hex"
	"strings"
	"time"
)

// Modality classifies what a chunk was made of. The frontend uses it to decide
// how to render a citation (plain text, a table, or an image card).
type Modality string

const (
	ModalityText  Modality = "text"
	ModalityTable Modality = "table"
	ModalityImage Modality = "image"
)

// Chunk is the atomic retrieval unit: what gets embedded, filtered and cited.
//
// 除了 Text，它还带一组**产品侧结构化元数据**（朝代/地点/主题/来源/可信度/能力）。
// 这些字段是检索过滤的依据，也是"未解锁的碎片不参与检索"这类玩法规则的基础。
type Chunk struct {
	ID       string   `json:"id"`
	DocID    string   `json:"doc_id"`
	Source   string   `json:"source"`
	Index    int      `json:"index"`
	Text     string   `json:"text"`
	Modality Modality `json:"modality"`
	AssetURL string   `json:"asset_url,omitempty"`
	Locator  string   `json:"locator,omitempty"`

	// 结构化元数据（过滤维度）。空字符串表示"未标注"。
	Dynasty     string   `json:"dynasty,omitempty"`     // 朝代，如 "唐"
	Place       string   `json:"place,omitempty"`       // 地点，如 "成都"
	Topic       string   `json:"topic,omitempty"`       // 主题，如 "水利"
	SourceType  string   `json:"source_type,omitempty"` // 来源类型，如 "正史"/"地方志"/"博物馆"
	Credibility string   `json:"credibility,omitempty"` // 可信度：high | medium | low
	Abilities   []string `json:"abilities,omitempty"`   // 关联能力/玩法（三国/诗词/水利/文物…）
	Tags        []string `json:"tags,omitempty"`

	// Hash 是切块内容的指纹，用于幂等去重：同样的内容重复入库不会产生新块。
	Hash string `json:"hash,omitempty"`
	// Meta 放不进入上面枚举的零散属性（页码、工作表名、图注模型等）。
	Meta      map[string]string `json:"meta,omitempty"`
	CreatedAt time.Time         `json:"created_at,omitempty"`
}

// Doc is one ingested source file.
type Doc struct {
	ID       string   `json:"id"`
	Title    string   `json:"title,omitempty"`
	Source   string   `json:"source"`
	Modality Modality `json:"modality"`
	Size     int64    `json:"size"`
	Chunks   int      `json:"chunks"`
	Assets   []string `json:"assets,omitempty"`
	Status   string   `json:"status"`
	Error    string   `json:"error,omitempty"`

	Dynasty     string   `json:"dynasty,omitempty"`
	Place       string   `json:"place,omitempty"`
	Topic       string   `json:"topic,omitempty"`
	SourceType  string   `json:"source_type,omitempty"`
	Credibility string   `json:"credibility,omitempty"`
	Abilities   []string `json:"abilities,omitempty"`
	Tags        []string `json:"tags,omitempty"`

	// Fingerprint 是文件内容的 sha256 前缀，用于增量更新。
	Fingerprint string    `json:"fingerprint"`
	UpdatedAt   time.Time `json:"updated_at"`
}

// Filter narrows a retrieval to a subset of the corpus.
//
// 语义约定：
//   - 同一次过滤里，不同维度之间是 AND；
//   - 同一维度的多个值之间是 OR（命中任意一个即可）；
//   - Abilities 是"能力门禁"：过滤集非空时，没有标注 Abilities 的块视为通用内容，
//     始终可检索；标注了 Abilities 的块必须与过滤集有交集。这样未解锁的碎片
//     不会进入上下文，既贴合剧情又省成本。
type Filter struct {
	DocIDs      []string   `json:"doc_ids,omitempty"`
	Modalities  []Modality `json:"modalities,omitempty"`
	Dynasty     []string   `json:"dynasty,omitempty"`
	Place       []string   `json:"place,omitempty"`
	Topic       []string   `json:"topic,omitempty"`
	SourceType  []string   `json:"source_type,omitempty"`
	Credibility []string   `json:"credibility,omitempty"`
	Abilities   []string   `json:"abilities,omitempty"`
	Tags        []string   `json:"tags,omitempty"`
}

// Empty 表示这个过滤条件不会筛掉任何东西。
func (f Filter) Empty() bool {
	return len(f.DocIDs) == 0 && len(f.Modalities) == 0 && len(f.Dynasty) == 0 &&
		len(f.Place) == 0 && len(f.Topic) == 0 && len(f.SourceType) == 0 &&
		len(f.Credibility) == 0 && len(f.Abilities) == 0 && len(f.Tags) == 0
}

// Match 判断一个块是否满足过滤条件。文件后端与 pgvector 后端共用这一份语义，
// 保证换存储不会改变检索结果。
func (f Filter) Match(c Chunk) bool {
	if len(f.DocIDs) > 0 && !containsFold(f.DocIDs, c.DocID) {
		return false
	}
	if len(f.Modalities) > 0 {
		ok := false
		for _, m := range f.Modalities {
			if m == c.Modality {
				ok = true
				break
			}
		}
		if !ok {
			return false
		}
	}
	if len(f.Dynasty) > 0 && !containsFold(f.Dynasty, c.Dynasty) {
		return false
	}
	if len(f.Place) > 0 && !containsFold(f.Place, c.Place) {
		return false
	}
	if len(f.Topic) > 0 && !containsFold(f.Topic, c.Topic) {
		return false
	}
	if len(f.SourceType) > 0 && !containsFold(f.SourceType, c.SourceType) {
		return false
	}
	if len(f.Credibility) > 0 && !containsFold(f.Credibility, c.Credibility) {
		return false
	}
	if len(f.Tags) > 0 && !intersectsFold(f.Tags, c.Tags) {
		return false
	}
	if len(f.Abilities) > 0 && len(c.Abilities) > 0 && !intersectsFold(f.Abilities, c.Abilities) {
		return false
	}
	return true
}

func containsFold(set []string, v string) bool {
	for _, s := range set {
		if strings.EqualFold(strings.TrimSpace(s), strings.TrimSpace(v)) {
			return true
		}
	}
	return false
}

func intersectsFold(set, vals []string) bool {
	for _, v := range vals {
		if containsFold(set, v) {
			return true
		}
	}
	return false
}

// Hit is one retrieval result: the chunk plus how it scored.
type Hit struct {
	Chunk  Chunk   `json:"chunk"`
	Score  float64 `json:"score"`          // 融合后归一化到 0~1
	Dense  float64 `json:"dense_score"`    // 余弦相似度
	Sparse float64 `json:"sparse_score"`   // BM25 原始分
	Rank   int     `json:"rank"`           // 从 1 开始
	From   string  `json:"from,omitempty"` // 该结果来自哪一路：dense | sparse | both
}

// ChatMessage is one turn of conversation history.
type ChatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
	// Images carries this turn's images (data URL or bare base64). They are handed
	// straight to the generation model; they never go through embedding.
	Images []string `json:"images,omitempty"`
}

// Usage mirrors the token accounting returned by OpenAI-compatible providers.
type Usage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

// StructuredAnswer 是"史实边界"要求的强约束输出：
// 把史实陈述、资料来源、演绎说明分开，避免模型自由发挥时把演绎写成史实。
type StructuredAnswer struct {
	History        string   `json:"history"`        // 史实陈述：只写有资料支撑的部分
	Sources        []string `json:"sources"`        // 资料来源：带编号的引用说明
	Interpretation string   `json:"interpretation"` // 演绎说明：无史料支撑的推断/传说
	Confidence     string   `json:"confidence"`     // high | medium | low
	Unknown        bool     `json:"unknown"`        // true 表示知识库里查不到
}

// SourceRef 是一条可溯源的出处，用于版权署名与可信度标注。
type SourceRef struct {
	Title   string `json:"title,omitempty"`
	URL     string `json:"url,omitempty"`
	License string `json:"license,omitempty"`
	Note    string `json:"note,omitempty"`
}

// Site 是结构化景点数据（GET /api/v1/sites/:id），既给前端渲染，
// 也作为**只读工具**在生成前注入模型上下文。
type Site struct {
	ID             string            `json:"id"`
	Name           string            `json:"name"`
	Aliases        []string          `json:"aliases,omitempty"`
	Dynasty        string            `json:"dynasty,omitempty"`
	Place          string            `json:"place,omitempty"`
	Topic          string            `json:"topic,omitempty"`
	Ability        string            `json:"ability,omitempty"` // 关联玩法能力，用作检索过滤
	Summary        string            `json:"summary,omitempty"`
	Description    string            `json:"description,omitempty"`
	Facts          map[string]string `json:"facts,omitempty"`          // 结构化字段：高度/年代/坐标…
	Interpretation string            `json:"interpretation,omitempty"` // 演绎说明（与史实分开）
	Sources        []SourceRef       `json:"sources,omitempty"`
	Lat            float64           `json:"lat,omitempty"`
	Lng            float64           `json:"lng,omitempty"`
	Tags           []string          `json:"tags,omitempty"`
}

// Facets 是索引里出现过的元数据取值，前端据此生成筛选器，不必硬编码词典。
type Facets struct {
	Dynasty     []string `json:"dynasty"`
	Place       []string `json:"place"`
	Topic       []string `json:"topic"`
	SourceType  []string `json:"source_type"`
	Credibility []string `json:"credibility"`
	Abilities   []string `json:"abilities"`
}

// Stats 是索引统计。
type Stats struct {
	Backend    string         `json:"backend"`
	Chunks     int            `json:"chunks"`
	Docs       int            `json:"docs"`
	Dim        int            `json:"dim"`
	Modalities map[string]int `json:"modalities"`
	Assets     int            `json:"assets"`
	DataDir    string         `json:"data_dir,omitempty"`
	Facets     Facets         `json:"facets"`
}

// NewID returns a short random identifier.
func NewID() string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return hex.EncodeToString([]byte(time.Now().Format(time.RFC3339Nano)))
	}
	return hex.EncodeToString(b[:])
}
