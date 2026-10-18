// Package config 负责加载、覆盖与校验服务配置。
//
// 优先级：默认值 < YAML 文件 < 环境变量（RAG_<SECTION>_<KEY>）。
// YAML 里的字符串支持 ${ENV_VAR} 与 ${ENV_VAR:-默认值} 展开，方便把 API Key
// 放在环境变量里而不是写进配置文件。
package config

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

// DefaultSystemPrompt 决定模型"怎么说话"。第 1 条是 RAG 能减少胡说的根本原因。
const DefaultSystemPrompt = `你是一个友善、清楚的中文助手，可以自然地进行日常闲聊。

1. 涉及具体景点、历史遗迹、地方文化或景物事实时，只根据【参考资料】回答；不要推测或编造。
2. 这类知识问题如果【参考资料】里没有足够的信息，就直接说明「知识库里没有找到相关内容」，并简要说明还缺什么信息。普通闲聊不需要知识库依据。
3. 用中文回答，条理清晰。答案来自某条资料时，在句末标注来源编号，例如 [1]。
4. 不要复述问题，不要写「根据参考资料」这类废话，直接给答案。
5. 如果多条资料之间存在冲突，把冲突指出来，不要自己选一个。
6. 参考资料里可能包含图片的文字说明（图注），可以据此描述图片内容，但不要臆造图中没有的信息。`

// ServerConfig 是 HTTP 服务本身的配置。
type ServerConfig struct {
	Addr            string   `yaml:"addr"`
	Mode            string   `yaml:"mode"`
	CORSOrigins     []string `yaml:"cors_origins"`
	AuthToken       string   `yaml:"auth_token"`
	RequestTimeoutS int      `yaml:"request_timeout_s"`
	RateLimitPerMin int      `yaml:"rate_limit_per_min"`
	TrustedProxies  []string `yaml:"trusted_proxies"`

	// ServeWeb 决定是否托管 storage.web_dir 下的页面。
	// 不写 = true（与既有部署完全一致）；写 false 后后端只提供 /api/v1/*，
	// 变成一个纯 API 服务，页面交给别的服务托管。
	ServeWeb *bool `yaml:"serve_web"`

	// OpenAPIFile 是 OpenAPI 规范文件的位置。留空时依次尝试进程工作目录
	// 与可执行文件所在目录下的 openapi.yaml。
	// 之所以做成配置项：前端已经不在后端源码树里，后端不该再去猜
	// 「页面目录的上一级」这种位置。
	OpenAPIFile string `yaml:"openapi_file"`
}

// StorageConfig 决定数据、资料、资产、前端页面的位置，以及用哪个检索后端。
// 相对路径按**进程工作目录**解析（约定：在 server/ 目录下启动）。
type StorageConfig struct {
	// Backend: file（默认，零依赖，暴力检索）| pgvector（Postgres + pgvector，上量后换）
	Backend   string `yaml:"backend"`
	DataDir   string `yaml:"data_dir"`
	DocsDir   string `yaml:"docs_dir"`
	AssetsDir string `yaml:"assets_dir"`
	WebDir    string `yaml:"web_dir"`
	// SitesFile 是结构化景点数据（兼作模型的只读工具）。
	SitesFile string `yaml:"sites_file"`
	// Pgvector 只在 backend=pgvector 时使用。
	Pgvector PgvectorConfig `yaml:"pgvector"`
}

// PgvectorConfig 是 Postgres + pgvector 后端的连接参数。
//
// 表名不可配置：schema.sql 把表固定为 rag_chunks（HNSW 索引也建在该表上），
// 所以这里不提供 table 键，避免出现"改了配置却不生效"的陷阱。
type PgvectorConfig struct {
	DSN      string `yaml:"dsn"`
	MaxConns int    `yaml:"max_conns"`
}

// ChunkingConfig 是切块参数。RAG 效果好坏一大半取决于这两个数字。
type ChunkingConfig struct {
	Size         int  `yaml:"size"`
	Overlap      int  `yaml:"overlap"`
	MinChars     int  `yaml:"min_chars"`
	MaxPerDoc    int  `yaml:"max_chunks_per_doc"`
	TableAsChunk bool `yaml:"table_as_chunk"`
}

// RetrievalConfig 是检索参数：候选池大小与稠密/稀疏融合权重。
type RetrievalConfig struct {
	TopK        int     `yaml:"top_k"`
	CandidateK  int     `yaml:"candidate_k"`
	HybridAlpha float64 `yaml:"hybrid_alpha"`
	MinScore    float64 `yaml:"min_score"`
	BM25K1      float64 `yaml:"bm25_k1"`
	BM25B       float64 `yaml:"bm25_b"`
	// Fusion 是多路召回合并方式：rrf（默认，只看名次）| weighted（按 alpha 加权）
	Fusion string `yaml:"fusion"`
	// Rewrite 为 true 时，多轮追问会先用一次模型调用改写成独立问题再检索
	Rewrite bool `yaml:"rewrite"`
}

// EmbeddingConfig 是检索模型（默认本地 Ollama）。
type EmbeddingConfig struct {
	Provider  string `yaml:"provider"`
	BaseURL   string `yaml:"base_url"`
	Model     string `yaml:"model"`
	APIKey    string `yaml:"api_key"`
	Dim       int    `yaml:"dim"`
	BatchSize int    `yaml:"batch_size"`
	TimeoutS  int    `yaml:"timeout_s"`
	Normalize bool   `yaml:"normalize"`
}

// ChatConfig 是生成模型（OpenAI 兼容，走 API Key）。
type ChatConfig struct {
	Provider     string            `yaml:"provider"`
	BaseURL      string            `yaml:"base_url"`
	APIKey       string            `yaml:"api_key"`
	Model        string            `yaml:"model"`
	Temperature  float64           `yaml:"temperature"`
	MaxTokens    int               `yaml:"max_tokens"`
	TimeoutS     int               `yaml:"timeout_s"`
	ExtraHeaders map[string]string `yaml:"extra_headers"`
	// VisionModel 是可选的：带图请求用它，纯文本请求仍用 Model（契约 §2）。
	// 为空时带图请求回退到 Model。
	VisionModel string `yaml:"vision_model"`
}

// VisionConfig 负责把图片变成可检索的文字（图注）。
type VisionConfig struct {
	Enabled    bool   `yaml:"enabled"`
	SharedChat bool   `yaml:"shared_chat"`
	Provider   string `yaml:"provider"`
	BaseURL    string `yaml:"base_url"`
	APIKey     string `yaml:"api_key"`
	Model      string `yaml:"model"`
	Prompt     string `yaml:"prompt"`
	MaxImages  int    `yaml:"max_images_per_doc"`
	MaxImageMB int64  `yaml:"max_image_mb"`
	TimeoutS   int    `yaml:"timeout_s"`
}

// RecognizeConfig 是照片识别的「检索快速通道」（PP-ShiTuV2 sidecar）。
//
// 相似度达到 RetrievalMinScore 就直接采用检索结果（约 100 ms、纯 CPU），
// 否则回落到视觉大模型的候选单选（约 1.6 s、要 GPU）。RetrievalURL 为空则整条链路关闭，
// 识别一律走大模型 —— 服务没起也不能让识别功能挂掉。
type RecognizeConfig struct {
	RetrievalURL      string  `yaml:"retrieval_url"`
	RetrievalMinScore float64 `yaml:"retrieval_min_score"`
	RetrievalTimeoutS int     `yaml:"retrieval_timeout_s"`
}

// IngestConfig 控制入库流水线。
type IngestConfig struct {
	MaxFileMB        int64 `yaml:"max_file_mb"`
	Workers          int   `yaml:"workers"`
	AutoIngestUpload bool  `yaml:"auto_ingest_upload"`
}

// PromptConfig 允许覆盖系统提示词。
type PromptConfig struct {
	System string `yaml:"system"`
}

// LogConfig 控制日志。
type LogConfig struct {
	Level string `yaml:"level"`
}

// Config 是整份配置。
type Config struct {
	Server    ServerConfig    `yaml:"server"`
	Storage   StorageConfig   `yaml:"storage"`
	Chunking  ChunkingConfig  `yaml:"chunking"`
	Retrieval RetrievalConfig `yaml:"retrieval"`
	Embedding EmbeddingConfig `yaml:"embedding"`
	Chat      ChatConfig      `yaml:"chat"`
	Vision    VisionConfig    `yaml:"vision"`
	Recognize RecognizeConfig `yaml:"recognize"`
	Ingest    IngestConfig    `yaml:"ingest"`
	Prompt    PromptConfig    `yaml:"prompt"`
	Log       LogConfig       `yaml:"log"`

	// Path 记录配置文件的实际位置（未提供时为空），便于日志里说明。
	Path string `yaml:"-"`
}

// Default 返回一份可以直接跑起来的默认配置：检索走本地 Ollama，生成走 API Key。
func Default() *Config {
	return &Config{
		Server: ServerConfig{
			Addr:            "127.0.0.1:8080",
			Mode:            "debug",
			CORSOrigins:     []string{"*"},
			RequestTimeoutS: 180,
			RateLimitPerMin: 120,
			TrustedProxies:  []string{"127.0.0.1"},
		},
		Storage: StorageConfig{
			Backend:   "file",
			DataDir:   "data",
			DocsDir:   "../docs",
			AssetsDir: "data/assets",
			WebDir:    "web",
			SitesFile: "configs/sites.json",
			Pgvector: PgvectorConfig{
				MaxConns: 8,
			},
		},
		Chunking: ChunkingConfig{
			Size:         600,
			Overlap:      90,
			MinChars:     20,
			MaxPerDoc:    5000,
			TableAsChunk: true,
		},
		Retrieval: RetrievalConfig{
			TopK:        4,
			CandidateK:  20,
			HybridAlpha: 0.6,
			MinScore:    0,
			BM25K1:      1.5,
			BM25B:       0.75,
			Fusion:      "rrf",
			Rewrite:     true,
		},
		Embedding: EmbeddingConfig{
			Provider:  "ollama",
			BaseURL:   "http://127.0.0.1:11434",
			Model:     "bge-m3",
			Dim:       1024,
			BatchSize: 8,
			TimeoutS:  60,
			Normalize: true,
		},
		Chat: ChatConfig{
			Provider:    "openai",
			BaseURL:     "https://api.deepseek.com/v1",
			Model:       "deepseek-chat",
			Temperature: 0.1,
			MaxTokens:   1024,
			TimeoutS:    180,
		},
		Vision: VisionConfig{
			Enabled:    false,
			Provider:   "openai",
			BaseURL:    "https://api.openai.com/v1",
			Model:      "gpt-4o-mini",
			Prompt:     "用中文详细描述这张图片的内容：如果是图表，请把图表里的关键数据、坐标轴、趋势都写出来；如果是截图，请把可见的文字读出来；如果是照片，请描述主体、场景与关键细节。只输出描述本身，不要客套话。",
			MaxImages:  50,
			MaxImageMB: 8,
			TimeoutS:   90,
		},
		Recognize: RecognizeConfig{
			// 缺省指向 tools/shitu/server.py 的默认端口；服务没起就自动回落到视觉大模型。
			RetrievalURL:      "http://127.0.0.1:8095",
			RetrievalMinScore: 0.6,
			RetrievalTimeoutS: 3,
		},
		Ingest: IngestConfig{
			MaxFileMB:        32,
			Workers:          4,
			AutoIngestUpload: true,
		},
		Prompt: PromptConfig{System: DefaultSystemPrompt},
		Log:    LogConfig{Level: "info"},
	}
}

var envPattern = regexp.MustCompile("\\$\\{([A-Za-z_][A-Za-z0-9_]*)(?::-([^}]*))?\\}")

// ExpandEnv 把 ${VAR} / ${VAR:-default} 展开成环境变量的值。
func ExpandEnv(s string) string {
	return envPattern.ReplaceAllStringFunc(s, func(m string) string {
		g := envPattern.FindStringSubmatch(m)
		if v, ok := os.LookupEnv(g[1]); ok && v != "" {
			return v
		}
		return g[2]
	})
}

// Load 读取配置文件并叠加环境变量。path 为空时只用默认值 + 环境变量。
// DefaultConfigPath 是未显式指定 -config 时查找的配置文件。
//
// 文件不存在是允许的（完全靠默认值 + 环境变量也能跑），但**必须去找**：
// 否则 configs/config.yaml 会被静默忽略，用户改了配置却毫无效果。
const DefaultConfigPath = "configs/config.yaml"

func Load(path string) (*Config, error) {
	cfg := Default()

	if path == "" {
		path = DefaultConfigPath
	}

	if path != "" {
		abs, err := filepath.Abs(path)
		if err != nil {
			return nil, fmt.Errorf("解析配置路径 %q: %w", path, err)
		}
		raw, err := os.ReadFile(abs)
		switch {
		case err == nil:
			dec := yaml.NewDecoder(strings.NewReader(ExpandEnv(string(raw))))
			dec.KnownFields(true) // 配置项写错名字时立刻报错，而不是默默用默认值
			if err := dec.Decode(cfg); err != nil {
				return nil, fmt.Errorf("解析配置文件 %s: %w", abs, err)
			}
			cfg.Path = abs
		case os.IsNotExist(err):
			// 配置文件不存在是允许的：完全靠默认值 + 环境变量也能跑。
		default:
			return nil, fmt.Errorf("读取配置文件 %s: %w", abs, err)
		}
	}

	applyEnv(cfg)

	if err := cfg.finish(); err != nil {
		return nil, err
	}
	return cfg, nil
}

// applyEnv 用反射把 RAG_<SECTION>_<KEY> 环境变量写回配置，避免手写几十行 if。
func applyEnv(cfg *Config) {
	v := reflect.ValueOf(cfg).Elem()
	t := v.Type()
	for i := 0; i < t.NumField(); i++ {
		section := strings.ToUpper(strings.Split(t.Field(i).Tag.Get("yaml"), ",")[0])
		if section == "" || section == "-" {
			continue
		}
		secVal := v.Field(i)
		if secVal.Kind() != reflect.Struct {
			continue
		}
		secType := secVal.Type()
		for j := 0; j < secType.NumField(); j++ {
			key := strings.ToUpper(strings.Split(secType.Field(j).Tag.Get("yaml"), ",")[0])
			if key == "" || key == "-" {
				continue
			}
			raw, ok := os.LookupEnv("RAG_" + section + "_" + key)
			if !ok {
				continue
			}
			field := secVal.Field(j)
			if !field.CanSet() {
				continue
			}
			setField(field, raw)
		}
	}
}

func setField(field reflect.Value, raw string) {
	if field.Kind() == reflect.Ptr {
		value := reflect.New(field.Type().Elem())
		setField(value.Elem(), raw)
		field.Set(value)
		return
	}
	switch field.Kind() {
	case reflect.String:
		field.SetString(raw)
	case reflect.Bool:
		if b, err := strconv.ParseBool(strings.TrimSpace(raw)); err == nil {
			field.SetBool(b)
		}
	case reflect.Int, reflect.Int64:
		if n, err := strconv.ParseInt(strings.TrimSpace(raw), 10, 64); err == nil {
			field.SetInt(n)
		}
	case reflect.Float64:
		if f, err := strconv.ParseFloat(strings.TrimSpace(raw), 64); err == nil {
			field.SetFloat(f)
		}
	case reflect.Slice:
		if field.Type().Elem().Kind() != reflect.String {
			return
		}
		parts := strings.Split(raw, ",")
		out := make([]string, 0, len(parts))
		for _, p := range parts {
			if p = strings.TrimSpace(p); p != "" {
				out = append(out, p)
			}
		}
		field.Set(reflect.ValueOf(out))
	case reflect.Map:
		if field.Type().Key().Kind() != reflect.String || field.Type().Elem().Kind() != reflect.String {
			return
		}
		m := map[string]string{}
		for _, pair := range strings.Split(raw, ",") {
			if k, val, found := strings.Cut(pair, "="); found {
				m[strings.TrimSpace(k)] = strings.TrimSpace(val)
			}
		}
		field.Set(reflect.ValueOf(m))
	}
}

// WebEnabled 报告是否需要托管前端页面。
//
// 两个条件同时满足才托管：serve_web 没有被显式关掉，且 web_dir 非空。
// 不写 serve_web、web_dir 保持默认值时，行为与改动前完全一致。
func (c *Config) WebEnabled() bool {
	if c.Server.ServeWeb != nil && !*c.Server.ServeWeb {
		return false
	}
	return strings.TrimSpace(c.Storage.WebDir) != ""
}

// finish 把相对路径变成绝对路径，并做参数校验。
func (c *Config) finish() error {
	for _, p := range []*string{&c.Storage.DataDir, &c.Storage.DocsDir, &c.Storage.AssetsDir} {
		abs, err := filepath.Abs(*p)
		if err != nil {
			return fmt.Errorf("解析路径 %q: %w", *p, err)
		}
		*p = abs
	}
	// WebDir 单独处理：显式置空表示「不托管页面」，绝不能被 filepath.Abs
	// 悄悄变成当前工作目录 —— 那会把整个工作目录挂到 /static 上。
	if strings.TrimSpace(c.Storage.WebDir) != "" {
		abs, err := filepath.Abs(c.Storage.WebDir)
		if err != nil {
			return fmt.Errorf("解析路径 %q: %w", c.Storage.WebDir, err)
		}
		c.Storage.WebDir = abs
	}
	// 同理：留空表示「自己去猜」，不能被 Abs 变成工作目录下的 openapi.yaml。
	if strings.TrimSpace(c.Server.OpenAPIFile) != "" {
		abs, err := filepath.Abs(c.Server.OpenAPIFile)
		if err != nil {
			return fmt.Errorf("解析路径 %q: %w", c.Server.OpenAPIFile, err)
		}
		c.Server.OpenAPIFile = abs
	}
	if c.Prompt.System == "" {
		c.Prompt.System = DefaultSystemPrompt
	}
	if c.Log.Level == "" {
		c.Log.Level = "info"
	}
	if c.Server.Mode == "" {
		c.Server.Mode = "debug"
	}
	// 检索快速通道：只填了 URL 也要有一份能用的阈值/超时（0 会被当成"永远不采纳"）。
	if c.Recognize.RetrievalMinScore <= 0 {
		c.Recognize.RetrievalMinScore = 0.6
	}
	if c.Recognize.RetrievalTimeoutS <= 0 {
		c.Recognize.RetrievalTimeoutS = 3
	}
	return c.Validate()
}

// Validate 返回所有配置问题的清单，启动时一次说清，而不是跑一半才炸。
func (c *Config) Validate() error {
	var problems []string
	add := func(format string, args ...any) { problems = append(problems, fmt.Sprintf(format, args...)) }

	if c.Server.Addr == "" {
		add("server.addr 不能为空")
	}
	// 后端选择：file 是零依赖默认值，pgvector 需要 DSN。
	switch c.Storage.Backend {
	case "file":
	case "pgvector":
		if c.Storage.Pgvector.DSN == "" {
			add("storage.backend=pgvector 时必须提供 storage.pgvector.dsn（或环境变量 RAG_STORAGE_PGVECTOR_DSN）")
		}
	case "":
		c.Storage.Backend = "file"
	default:
		add("storage.backend 只支持 file 或 pgvector（当前 %q）", c.Storage.Backend)
	}
	if c.Storage.DataDir == "" {
		add("storage.data_dir 不能为空")
	}
	if c.Storage.DocsDir == "" {
		add("storage.docs_dir 不能为空")
	}
	if c.Chunking.Size < 50 {
		add("chunking.size 至少要 50（当前 %d），太小会把句子切碎", c.Chunking.Size)
	}
	if c.Chunking.Overlap < 0 || c.Chunking.Overlap >= c.Chunking.Size {
		add("chunking.overlap 必须 >= 0 且小于 chunking.size（当前 overlap=%d, size=%d）",
			c.Chunking.Overlap, c.Chunking.Size)
	}
	if c.Retrieval.TopK < 1 || c.Retrieval.TopK > 50 {
		add("retrieval.top_k 必须在 1~50（当前 %d）", c.Retrieval.TopK)
	}
	if c.Retrieval.CandidateK < c.Retrieval.TopK {
		add("retrieval.candidate_k（%d）不能小于 retrieval.top_k（%d）",
			c.Retrieval.CandidateK, c.Retrieval.TopK)
	}
	if c.Retrieval.HybridAlpha < 0 || c.Retrieval.HybridAlpha > 1 {
		add("retrieval.hybrid_alpha 必须在 0~1（当前 %v）", c.Retrieval.HybridAlpha)
	}
	switch c.Retrieval.Fusion {
	case "rrf", "weighted":
	case "":
		c.Retrieval.Fusion = "rrf"
	default:
		add("retrieval.fusion 只支持 rrf 或 weighted（当前 %q）", c.Retrieval.Fusion)
	}
	switch c.Embedding.Provider {
	case "ollama", "openai":
	default:
		add("embedding.provider 只支持 ollama 或 openai（当前 %q）", c.Embedding.Provider)
	}
	if c.Embedding.Model == "" {
		add("embedding.model 不能为空")
	}
	if c.Embedding.Dim <= 0 {
		add("embedding.dim 必须为正数（bge-m3 是 1024）")
	}
	if c.Embedding.BatchSize <= 0 {
		c.Embedding.BatchSize = 8
	}
	switch c.Chat.Provider {
	case "openai", "ollama":
	default:
		add("chat.provider 只支持 openai 或 ollama（当前 %q）", c.Chat.Provider)
	}
	if c.Chat.Model == "" {
		add("chat.model 不能为空")
	}
	if c.Chat.Provider == "openai" && c.Chat.BaseURL == "" {
		add("chat.base_url 不能为空（例如 https://api.deepseek.com/v1）")
	}
	if c.Vision.Enabled {
		if c.Vision.Model == "" {
			add("vision.enabled 为 true 时 vision.model 不能为空")
		}
		if c.Vision.Provider == "openai" && c.Vision.BaseURL == "" {
			add("vision.enabled 为 true 时 vision.base_url 不能为空")
		}
	}
	if c.Ingest.MaxFileMB <= 0 {
		add("ingest.max_file_mb 必须为正数")
	}
	if c.Ingest.Workers <= 0 {
		c.Ingest.Workers = 4
	}
	if len(problems) > 0 {
		return fmt.Errorf("配置有问题：\n  - %s", strings.Join(problems, "\n  - "))
	}
	return nil
}

// EnsureDirs 创建运行期需要的目录。
func (c *Config) EnsureDirs() error {
	for _, dir := range []string{c.Storage.DataDir, c.Storage.DocsDir, c.Storage.AssetsDir} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("创建目录 %s: %w", dir, err)
		}
	}
	return nil
}

// ChatConfigured 表示生成模型是否具备可用的凭证/地址。
// 缺 API Key 不算致命错误：服务照样起，健康检查会明确告诉前端哪一环没就绪。
func (c *Config) ChatConfigured() bool {
	if c.Chat.Provider == "ollama" {
		return c.Chat.BaseURL != ""
	}
	return c.Chat.APIKey != ""
}

// VisionConfigured 表示图片图注能力是否可用。
func (c *Config) VisionConfigured() bool {
	if !c.Vision.Enabled {
		return false
	}
	if c.Vision.Provider == "ollama" {
		return c.Vision.BaseURL != ""
	}
	return c.Vision.APIKey != ""
}
