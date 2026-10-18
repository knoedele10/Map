package llm

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"ragkb/internal/config"
	"ragkb/internal/types"
)

func encodeBase64(b []byte) string { return base64.StdEncoding.EncodeToString(b) }

// stripDataURL 把 "data:image/png;base64,AAAA" 还原成 "AAAA"。
func stripDataURL(s string) string {
	if strings.HasPrefix(s, "data:") {
		if i := strings.Index(s, ","); i > 0 {
			return s[i+1:]
		}
	}
	return s
}

// dataURL 把图片字节包装成 OpenAI 视觉接口要求的 data URL。
func dataURL(mime string, b []byte) string {
	if mime == "" {
		mime = "image/png"
	}
	return "data:" + mime + ";base64," + encodeBase64(b)
}

// openAIClient 是任何 OpenAI 兼容服务（OpenAI / DeepSeek / Moonshot / 智谱 / 通义）的共用客户端。
type openAIClient struct {
	baseURL string
	apiKey  string
	headers map[string]string
	client  *http.Client
}

func newOpenAIClient(baseURL, apiKey string, headers map[string]string) *openAIClient {
	return &openAIClient{
		baseURL: strings.TrimRight(baseURL, "/"),
		apiKey:  apiKey,
		headers: headers,
		// 流式响应可能持续几分钟，超时一律交给 context。
		client: &http.Client{},
	}
}

func (c *openAIClient) post(ctx context.Context, path string, payload any) (*http.Response, error) {
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("openai: 编码请求: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+path, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("openai: 构造请求: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	if c.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.apiKey)
	}
	for k, v := range c.headers {
		req.Header.Set(k, v)
	}
	resp, err := c.client.Do(req)
	if err != nil {
		return nil, &UpstreamError{Provider: "openai", Err: err}
	}
	return resp, nil
}

func openAIError(resp *http.Response) error {
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 8192))
	return &UpstreamError{Provider: "openai", Status: resp.StatusCode, Body: string(body)}
}

// ---------------------------------------------------------------- 嵌入（远程兼容接口，可选）

type openAIEmbedder struct {
	oc      *openAIClient
	model   string
	dim     int
	batch   int
	timeout time.Duration
}

func newOpenAIEmbedder(cfg *config.Config) *openAIEmbedder {
	return &openAIEmbedder{
		oc:      newOpenAIClient(cfg.Embedding.BaseURL, cfg.Embedding.APIKey, nil),
		model:   cfg.Embedding.Model,
		dim:     cfg.Embedding.Dim,
		batch:   cfg.Embedding.BatchSize,
		timeout: timeoutOf(cfg.Embedding.TimeoutS, 60*time.Second),
	}
}

func (e *openAIEmbedder) Dim() int         { return e.dim }
func (e *openAIEmbedder) Model() string    { return e.model }
func (e *openAIEmbedder) Provider() string { return "openai" }

func (e *openAIEmbedder) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	if len(texts) == 0 {
		return nil, nil
	}
	out := make([][]float32, 0, len(texts))
	for start := 0; start < len(texts); start += e.batch {
		end := start + e.batch
		if end > len(texts) {
			end = len(texts)
		}
		ctx, cancel := context.WithTimeout(ctx, e.timeout)
		resp, err := e.oc.post(ctx, "/embeddings", map[string]any{
			"model": e.model,
			"input": texts[start:end],
		})
		if err != nil {
			cancel()
			return nil, err
		}
		if resp.StatusCode != http.StatusOK {
			cancel()
			return nil, openAIError(resp)
		}
		var parsed struct {
			Data []struct {
				Embedding []float32 `json:"embedding"`
				Index     int       `json:"index"`
			} `json:"data"`
		}
		err = json.NewDecoder(resp.Body).Decode(&parsed)
		resp.Body.Close()
		cancel()
		if err != nil {
			return nil, &UpstreamError{Provider: "openai", Err: fmt.Errorf("解析 /embeddings 响应: %w", err)}
		}
		if len(parsed.Data) != end-start {
			return nil, &UpstreamError{Provider: "openai", Err: fmt.Errorf("期望 %d 条向量，实际 %d 条", end-start, len(parsed.Data))}
		}
		// 按 index 回填，避免服务端乱序。
		batchVecs := make([][]float32, end-start)
		for _, d := range parsed.Data {
			if d.Index >= 0 && d.Index < len(batchVecs) {
				batchVecs[d.Index] = d.Embedding
			}
		}
		out = append(out, batchVecs...)
	}
	for i, v := range out {
		if len(v) != e.dim {
			return nil, fmt.Errorf("嵌入维度不符：第 %d 条得到 %d 维，配置里是 %d 维（embedding.dim）", i, len(v), e.dim)
		}
	}
	return out, nil
}

func (e *openAIEmbedder) Ping(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, e.oc.baseURL+"/models", nil)
	if err != nil {
		return err
	}
	if e.oc.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+e.oc.apiKey)
	}
	resp, err := e.oc.client.Do(req)
	if err != nil {
		return &UpstreamError{Provider: "openai", Err: err}
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return openAIError(resp)
	}
	return nil
}

// ---------------------------------------------------------------- 对话

type openAIChatter struct {
	oc      *openAIClient
	model   string
	temp    float64
	maxTok  int
	timeout time.Duration
}

func newOpenAIChatter(cfg *config.Config) *openAIChatter {
	return &openAIChatter{
		oc:      newOpenAIClient(cfg.Chat.BaseURL, cfg.Chat.APIKey, cfg.Chat.ExtraHeaders),
		model:   cfg.Chat.Model,
		temp:    cfg.Chat.Temperature,
		maxTok:  cfg.Chat.MaxTokens,
		timeout: timeoutOf(cfg.Chat.TimeoutS, 180*time.Second),
	}
}

func (c *openAIChatter) Model() string    { return c.model }
func (c *openAIChatter) Provider() string { return "openai" }

// Ping 只验证凭证与地址可用（GET /models 最轻量）。
func (c *openAIChatter) Ping(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.oc.baseURL+"/models", nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.oc.apiKey)
	for k, v := range c.oc.headers {
		req.Header.Set(k, v)
	}
	resp, err := c.oc.client.Do(req)
	if err != nil {
		return &UpstreamError{Provider: "openai", Err: err}
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return openAIError(resp)
	}
	return nil
}

type openAIChatPayload struct {
	Model         string           `json:"model"`
	Messages      []map[string]any `json:"messages"`
	Temperature   float64          `json:"temperature"`
	MaxTokens     int              `json:"max_tokens"`
	Stream        bool             `json:"stream"`
	StreamOptions map[string]any   `json:"stream_options,omitempty"`
}

func (c *openAIChatter) buildPayload(req ChatRequest, stream bool, withUsage bool) openAIChatPayload {
	msgs := make([]map[string]any, 0, len(req.Messages)+1)
	if req.System != "" {
		msgs = append(msgs, map[string]any{"role": "system", "content": req.System})
	}
	for _, m := range req.Messages {
		if len(m.Images) == 0 {
			msgs = append(msgs, map[string]any{"role": m.Role, "content": m.Content})
			continue
		}
		// 多模态消息：文本 + 若干图片。
		parts := []map[string]any{{"type": "text", "text": m.Content}}
		for _, img := range m.Images {
			url := img
			if !strings.HasPrefix(url, "data:") && !strings.HasPrefix(url, "http") {
				url = "data:image/png;base64," + img
			}
			parts = append(parts, map[string]any{
				"type":      "image_url",
				"image_url": map[string]any{"url": url},
			})
		}
		msgs = append(msgs, map[string]any{"role": m.Role, "content": parts})
	}

	temp := req.Temperature
	if temp <= 0 {
		temp = c.temp
	}
	maxTok := req.MaxTokens
	if maxTok <= 0 {
		maxTok = c.maxTok
	}
	p := openAIChatPayload{
		Model:       c.model,
		Messages:    msgs,
		Temperature: temp,
		MaxTokens:   maxTok,
		Stream:      stream,
	}
	if stream && withUsage {
		p.StreamOptions = map[string]any{"include_usage": true}
	}
	return p
}

func (c *openAIChatter) Stream(ctx context.Context, req ChatRequest) (<-chan Delta, error) {
	resp, err := c.oc.post(ctx, "/chat/completions", c.buildPayload(req, true, true))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode == http.StatusBadRequest {
		// 有些网关不认 stream_options，去掉重试一次。
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		resp.Body.Close()
		if bytes.Contains(body, []byte("stream_options")) {
			resp, err = c.oc.post(ctx, "/chat/completions", c.buildPayload(req, true, false))
			if err != nil {
				return nil, err
			}
		} else {
			return nil, &UpstreamError{Provider: "openai", Status: http.StatusBadRequest, Body: string(body)}
		}
	}
	if resp.StatusCode != http.StatusOK {
		return nil, openAIError(resp)
	}

	ch := make(chan Delta, 32)
	go func() {
		defer close(ch)
		defer resp.Body.Close()

		scanner := bufio.NewScanner(resp.Body)
		scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
		finished := false
		for scanner.Scan() {
			line := strings.TrimSpace(scanner.Text())
			if line == "" || strings.HasPrefix(line, ":") {
				continue
			}
			if !strings.HasPrefix(line, "data:") {
				continue
			}
			data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
			if data == "[DONE]" {
				sendDelta(ctx, ch, Delta{Done: true})
				return
			}
			var chunk struct {
				Choices []struct {
					Delta struct {
						Content string `json:"content"`
					} `json:"delta"`
					FinishReason string `json:"finish_reason"`
				} `json:"choices"`
				Usage *struct {
					PromptTokens     int `json:"prompt_tokens"`
					CompletionTokens int `json:"completion_tokens"`
					TotalTokens      int `json:"total_tokens"`
				} `json:"usage"`
			}
			if err := json.Unmarshal([]byte(data), &chunk); err != nil {
				continue // 单个坏块不该中断整段生成
			}
			if chunk.Usage != nil {
				sendDelta(ctx, ch, Delta{Usage: &types.Usage{
					PromptTokens:     chunk.Usage.PromptTokens,
					CompletionTokens: chunk.Usage.CompletionTokens,
					TotalTokens:      chunk.Usage.TotalTokens,
				}})
			}
			if len(chunk.Choices) > 0 && chunk.Choices[0].Delta.Content != "" {
				if !sendDelta(ctx, ch, Delta{Content: chunk.Choices[0].Delta.Content}) {
					return
				}
			}
			if len(chunk.Choices) > 0 && chunk.Choices[0].FinishReason != "" {
				finished = true
			}
		}
		if err := scanner.Err(); err != nil && ctx.Err() == nil {
			sendDelta(ctx, ch, Delta{Done: true, Err: &UpstreamError{Provider: "openai", Err: err}})
			return
		}
		if !finished {
			sendDelta(ctx, ch, Delta{Done: true, Err: &UpstreamError{Provider: "openai", Err: io.ErrUnexpectedEOF}})
			return
		}
		sendDelta(ctx, ch, Delta{Done: true})
	}()
	return ch, nil
}

// ---------------------------------------------------------------- 视觉

type openAIVisioner struct {
	oc      *openAIClient
	model   string
	prompt  string
	timeout time.Duration
}

func newOpenAIVisioner(cfg *config.Config) *openAIVisioner {
	return &openAIVisioner{
		oc:      newOpenAIClient(cfg.Vision.BaseURL, cfg.Vision.APIKey, nil),
		model:   cfg.Vision.Model,
		prompt:  cfg.Vision.Prompt,
		timeout: timeoutOf(cfg.Vision.TimeoutS, 90*time.Second),
	}
}

func (v *openAIVisioner) Model() string    { return v.model }
func (v *openAIVisioner) Provider() string { return "openai" }

func (v *openAIVisioner) Ping(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, v.oc.baseURL+"/models", nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+v.oc.apiKey)
	resp, err := v.oc.client.Do(req)
	if err != nil {
		return &UpstreamError{Provider: "openai", Err: err}
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return openAIError(resp)
	}
	return nil
}

func (v *openAIVisioner) Caption(ctx context.Context, image []byte, mime string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, v.timeout)
	defer cancel()

	payload := map[string]any{
		"model": v.model,
		"messages": []map[string]any{{
			"role": "user",
			"content": []map[string]any{
				{"type": "text", "text": v.prompt},
				{"type": "image_url", "image_url": map[string]any{"url": dataURL(mime, image)}},
			},
		}},
		"max_tokens": 1024,
	}
	resp, err := v.oc.post(ctx, "/chat/completions", payload)
	if err != nil {
		return "", err
	}
	if resp.StatusCode != http.StatusOK {
		return "", openAIError(resp)
	}
	defer resp.Body.Close()

	var parsed struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		return "", &UpstreamError{Provider: "openai", Err: fmt.Errorf("解析视觉响应: %w", err)}
	}
	if len(parsed.Choices) == 0 {
		return "", &UpstreamError{Provider: "openai", Err: fmt.Errorf("视觉接口没有返回任何结果")}
	}
	return strings.TrimSpace(parsed.Choices[0].Message.Content), nil
}
