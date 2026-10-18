// Package llm 是模型接入层：本地 Ollama 做嵌入，OpenAI 兼容接口做对话与视觉。
//
// 上层（ingest / rag）只依赖这里的接口，不关心 HTTP 细节，因此换模型供应商
// 只需要改配置，不需要改业务代码。
package llm

import (
	"context"
	"errors"
	"fmt"
	"time"

	"ragkb/internal/config"
	"ragkb/internal/types"
)

// ErrNotConfigured 表示该能力没有配置（例如没填 API Key）。
var ErrNotConfigured = errors.New("llm: 未配置")

// UpstreamError 是模型服务返回的错误，API 层据此回 50200。
type UpstreamError struct {
	Provider string
	Status   int
	Body     string
	Err      error
}

func (e *UpstreamError) Error() string {
	if e.Status > 0 {
		return fmt.Sprintf("%s 返回 HTTP %d: %s", e.Provider, e.Status, truncate(e.Body, 400))
	}
	return fmt.Sprintf("%s 调用失败: %v", e.Provider, e.Err)
}

func (e *UpstreamError) Unwrap() error { return e.Err }

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// Embedder 把文本转成向量。检索模型（本地 Ollama bge-m3）。
type Embedder interface {
	Embed(ctx context.Context, texts []string) ([][]float32, error)
	Dim() int
	Model() string
	Provider() string
	// Ping 检查服务可达且模型已就绪。
	Ping(ctx context.Context) error
}

// Message 是发给对话模型的一轮消息。Images 里的元素是 data URL（data:image/png;base64,...）。
type Message struct {
	Role    string   `json:"role"`
	Content string   `json:"content"`
	Images  []string `json:"images,omitempty"`
}

// ChatRequest 是一次生成请求。
type ChatRequest struct {
	System      string
	Messages    []Message
	Temperature float64
	MaxTokens   int
}

// Delta 是流式生成过程中的一个片段；Done 为 true 表示结束，Err 非空表示中途出错。
type Delta struct {
	Content string
	Usage   *types.Usage
	Done    bool
	Err     error
}

// Chatter 是生成模型（走 API Key）。
type Chatter interface {
	// Stream 返回一个只读通道，调用方必须读干净或取消 ctx，否则会泄漏 goroutine。
	Stream(ctx context.Context, req ChatRequest) (<-chan Delta, error)
	Model() string
	Provider() string
	Ping(ctx context.Context) error
}

// Visioner 把一张图片转成可检索的文字描述。
type Visioner interface {
	Caption(ctx context.Context, image []byte, mime string) (string, error)
	Model() string
	Provider() string
	Ping(ctx context.Context) error
}

// NewEmbedder 按配置构造嵌入器。
func NewEmbedder(cfg *config.Config) (Embedder, error) {
	switch cfg.Embedding.Provider {
	case "ollama":
		return newOllamaEmbedder(cfg), nil
	case "openai":
		if cfg.Embedding.APIKey == "" {
			return nil, fmt.Errorf("%w: embedding.provider=openai 需要 embedding.api_key", ErrNotConfigured)
		}
		return newOpenAIEmbedder(cfg), nil
	default:
		return nil, fmt.Errorf("不支持的 embedding.provider: %q", cfg.Embedding.Provider)
	}
}

// NewChatter 按配置构造对话模型。
//
// 缺少 API Key 时返回 (nil, nil) 而不是错误：检索、入库、评测都不需要生成模型，
// 没有 Key 就把整个服务拦住是不合理的。调用方拿到 nil 应让 /chat 返回 50300。
func NewChatter(cfg *config.Config) (Chatter, error) {
	switch cfg.Chat.Provider {
	case "ollama":
		return newOllamaChatter(cfg), nil
	case "openai":
		if cfg.Chat.APIKey == "" {
			return nil, nil
		}
		return newOpenAIChatter(cfg), nil
	default:
		return nil, fmt.Errorf("不支持的 chat.provider: %q", cfg.Chat.Provider)
	}
}

// NewVisionChatter uses vision credentials when explicitly configured, so a text
// provider (for example DeepSeek) can coexist with an Ollama vision model.
func NewVisionChatter(cfg *config.Config, fallback Chatter) (Chatter, error) {
	copyCfg := *cfg
	if cfg.Vision.Enabled && cfg.Vision.Model != "" {
		copyCfg.Chat.Provider = cfg.Vision.Provider
		copyCfg.Chat.BaseURL = cfg.Vision.BaseURL
		copyCfg.Chat.APIKey = cfg.Vision.APIKey
		copyCfg.Chat.Model = cfg.Vision.Model
		copyCfg.Chat.TimeoutS = cfg.Vision.TimeoutS
		return NewChatter(&copyCfg)
	}
	if cfg.Chat.VisionModel != "" {
		copyCfg.Chat.Model = cfg.Chat.VisionModel
		return NewChatter(&copyCfg)
	}
	return fallback, nil
}

// NewVisioner 按配置构造视觉模型；未启用时返回 (nil, nil)。
func NewVisioner(cfg *config.Config) (Visioner, error) {
	if !cfg.Vision.Enabled {
		return nil, nil
	}
	switch cfg.Vision.Provider {
	case "ollama":
		return newOllamaVisioner(cfg), nil
	case "openai":
		if cfg.Vision.APIKey == "" {
			return nil, fmt.Errorf("%w: vision.provider=openai 需要 vision.api_key", ErrNotConfigured)
		}
		return newOpenAIVisioner(cfg), nil
	default:
		return nil, fmt.Errorf("不支持的 vision.provider: %q", cfg.Vision.Provider)
	}
}

// IsUpstream 判断错误是否来自上游模型服务。
func IsUpstream(err error) bool {
	var ue *UpstreamError
	return errors.As(err, &ue)
}

func timeoutOf(seconds int, fallback time.Duration) time.Duration {
	if seconds <= 0 {
		return fallback
	}
	return time.Duration(seconds) * time.Second
}
