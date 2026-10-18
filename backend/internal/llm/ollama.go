package llm

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"ragkb/internal/config"
	"ragkb/internal/types"
)

// ollamaClient 是三个 Ollama 实现共享的连接参数。
type ollamaClient struct {
	baseURL string
	client  *http.Client
	timeout time.Duration
}

func newOllamaClient(baseURL string, timeoutS int) *ollamaClient {
	return &ollamaClient{
		baseURL: strings.TrimRight(baseURL, "/"),
		// 流式请求可能持续几分钟，所以不能给 Client 设全局 Timeout，
		// 超时一律靠 context 控制。
		client:  &http.Client{},
		timeout: timeoutOf(timeoutS, 60*time.Second),
	}
}

func (c *ollamaClient) post(ctx context.Context, path string, payload any) (*http.Response, error) {
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("ollama: 编码请求: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+path, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("ollama: 构造请求: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.client.Do(req)
	if err != nil {
		return nil, &UpstreamError{Provider: "ollama", Err: err}
	}
	return resp, nil
}

func ollamaError(resp *http.Response) error {
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	return &UpstreamError{Provider: "ollama", Status: resp.StatusCode, Body: string(body)}
}

// ---------------------------------------------------------------- 嵌入

type ollamaEmbedder struct {
	oc      *ollamaClient
	model   string
	dim     int
	batch   int
	baseURL string
}

func newOllamaEmbedder(cfg *config.Config) *ollamaEmbedder {
	return &ollamaEmbedder{
		oc:      newOllamaClient(cfg.Embedding.BaseURL, cfg.Embedding.TimeoutS),
		model:   cfg.Embedding.Model,
		dim:     cfg.Embedding.Dim,
		batch:   cfg.Embedding.BatchSize,
		baseURL: cfg.Embedding.BaseURL,
	}
}

func (e *ollamaEmbedder) Dim() int         { return e.dim }
func (e *ollamaEmbedder) Model() string    { return e.model }
func (e *ollamaEmbedder) Provider() string { return "ollama" }

// Embed 批量向量化。优先用 /api/embed（一次多条），旧版 Ollama 回退到 /api/embeddings。
func (e *ollamaEmbedder) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	if len(texts) == 0 {
		return nil, nil
	}
	out := make([][]float32, 0, len(texts))
	for start := 0; start < len(texts); start += e.batch {
		end := start + e.batch
		if end > len(texts) {
			end = len(texts)
		}
		vecs, err := e.embedBatch(ctx, texts[start:end])
		if err != nil {
			return nil, err
		}
		out = append(out, vecs...)
	}
	for i, v := range out {
		if len(v) != e.dim {
			return nil, fmt.Errorf("ollama 嵌入维度不符：第 %d 条得到 %d 维，配置里是 %d 维（embedding.dim）", i, len(v), e.dim)
		}
	}
	return out, nil
}

func (e *ollamaEmbedder) embedBatch(ctx context.Context, texts []string) ([][]float32, error) {
	ctx, cancel := context.WithTimeout(ctx, e.oc.timeout)
	defer cancel()

	resp, err := e.oc.post(ctx, "/api/embed", map[string]any{"model": e.model, "input": texts})
	if err != nil {
		return nil, err
	}
	if resp.StatusCode == http.StatusNotFound {
		// 老版本 Ollama 没有 /api/embed，逐条走 /api/embeddings。
		resp.Body.Close()
		return e.embedLegacy(ctx, texts)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, ollamaError(resp)
	}
	defer resp.Body.Close()

	var parsed struct {
		Embeddings [][]float32 `json:"embeddings"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		return nil, &UpstreamError{Provider: "ollama", Err: fmt.Errorf("解析 /api/embed 响应: %w", err)}
	}
	if len(parsed.Embeddings) != len(texts) {
		return nil, &UpstreamError{Provider: "ollama", Err: fmt.Errorf("期望 %d 条向量，实际 %d 条", len(texts), len(parsed.Embeddings))}
	}
	return parsed.Embeddings, nil
}

func (e *ollamaEmbedder) embedLegacy(ctx context.Context, texts []string) ([][]float32, error) {
	out := make([][]float32, 0, len(texts))
	for _, t := range texts {
		resp, err := e.oc.post(ctx, "/api/embeddings", map[string]any{"model": e.model, "prompt": t})
		if err != nil {
			return nil, err
		}
		if resp.StatusCode != http.StatusOK {
			return nil, ollamaError(resp)
		}
		var parsed struct {
			Embedding []float32 `json:"embedding"`
		}
		err = json.NewDecoder(resp.Body).Decode(&parsed)
		resp.Body.Close()
		if err != nil {
			return nil, &UpstreamError{Provider: "ollama", Err: err}
		}
		out = append(out, parsed.Embedding)
	}
	return out, nil
}

// Ping 检查 Ollama 是否可达、模型是否已经 pull 下来。
func (e *ollamaEmbedder) Ping(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, e.oc.baseURL+"/api/tags", nil)
	if err != nil {
		return &UpstreamError{Provider: "ollama", Err: err}
	}
	resp, err := e.oc.client.Do(req)
	if err != nil {
		return &UpstreamError{Provider: "ollama", Err: fmt.Errorf("连不上 %s（Ollama 启动了吗？）: %w", e.baseURL, err)}
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return ollamaError(resp)
	}
	var parsed struct {
		Models []struct {
			Name  string `json:"name"`
			Model string `json:"model"`
		} `json:"models"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		return &UpstreamError{Provider: "ollama", Err: err}
	}
	for _, m := range parsed.Models {
		name := m.Name
		if name == "" {
			name = m.Model
		}
		if name == e.model || strings.HasPrefix(name, e.model+":") {
			return nil
		}
	}
	return fmt.Errorf("Ollama 里没有模型 %q，请先执行: ollama pull %s", e.model, e.model)
}

// ---------------------------------------------------------------- 对话

type ollamaChatter struct {
	oc      *ollamaClient
	model   string
	temp    float64
	maxTok  int
	baseURL string
}

func newOllamaChatter(cfg *config.Config) *ollamaChatter {
	return &ollamaChatter{
		oc:      newOllamaClient(cfg.Chat.BaseURL, cfg.Chat.TimeoutS),
		model:   cfg.Chat.Model,
		temp:    cfg.Chat.Temperature,
		maxTok:  cfg.Chat.MaxTokens,
		baseURL: cfg.Chat.BaseURL,
	}
}

func (c *ollamaChatter) Model() string    { return c.model }
func (c *ollamaChatter) Provider() string { return "ollama" }

func (c *ollamaChatter) Ping(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.oc.baseURL+"/api/tags", nil)
	if err != nil {
		return &UpstreamError{Provider: "ollama", Err: err}
	}
	resp, err := c.oc.client.Do(req)
	if err != nil {
		return &UpstreamError{Provider: "ollama", Err: err}
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return ollamaError(resp)
	}
	var parsed struct {
		Models []struct {
			Name string `json:"name"`
		} `json:"models"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		return &UpstreamError{Provider: "ollama", Err: err}
	}
	for _, m := range parsed.Models {
		if m.Name == c.model || strings.HasPrefix(m.Name, c.model+":") {
			return nil
		}
	}
	return fmt.Errorf("Ollama 里没有模型 %q，请先执行: ollama pull %s", c.model, c.model)
}

func (c *ollamaChatter) Stream(ctx context.Context, req ChatRequest) (<-chan Delta, error) {
	msgs := make([]map[string]any, 0, len(req.Messages)+1)
	if req.System != "" {
		msgs = append(msgs, map[string]any{"role": "system", "content": req.System})
	}
	for _, m := range req.Messages {
		msg := map[string]any{"role": m.Role, "content": m.Content}
		if len(m.Images) > 0 {
			// Ollama 的图片字段是裸 base64（不带 data URL 前缀）。
			imgs := make([]string, 0, len(m.Images))
			for _, img := range m.Images {
				imgs = append(imgs, stripDataURL(img))
			}
			msg["images"] = imgs
		}
		msgs = append(msgs, msg)
	}

	temp := req.Temperature
	if temp <= 0 {
		temp = c.temp
	}
	maxTok := req.MaxTokens
	if maxTok <= 0 {
		maxTok = c.maxTok
	}

	payload := map[string]any{
		"model":    c.model,
		"messages": msgs,
		"stream":   true,
		"options":  map[string]any{"temperature": temp, "num_predict": maxTok},
	}

	resp, err := c.oc.post(ctx, "/api/chat", payload)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, ollamaError(resp)
	}

	ch := make(chan Delta, 32)
	go func() {
		defer close(ch)
		defer resp.Body.Close()

		scanner := bufio.NewScanner(resp.Body)
		scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
		for scanner.Scan() {
			line := bytes.TrimSpace(scanner.Bytes())
			if len(line) == 0 {
				continue
			}
			var chunk struct {
				Message struct {
					Content string `json:"content"`
				} `json:"message"`
				Done            bool `json:"done"`
				PromptEvalCount int  `json:"prompt_eval_count"`
				EvalCount       int  `json:"eval_count"`
			}
			if err := json.Unmarshal(line, &chunk); err != nil {
				continue // 单个坏行不该中断整段生成
			}
			if chunk.Message.Content != "" {
				if !sendDelta(ctx, ch, Delta{Content: chunk.Message.Content}) {
					return
				}
			}
			if chunk.Done {
				usage := &types.Usage{
					PromptTokens:     chunk.PromptEvalCount,
					CompletionTokens: chunk.EvalCount,
					TotalTokens:      chunk.PromptEvalCount + chunk.EvalCount,
				}
				sendDelta(ctx, ch, Delta{Done: true, Usage: usage})
				return
			}
		}
		if err := scanner.Err(); err != nil && ctx.Err() == nil {
			sendDelta(ctx, ch, Delta{Done: true, Err: &UpstreamError{Provider: "ollama", Err: err}})
			return
		}
		if ctx.Err() == nil {
			sendDelta(ctx, ch, Delta{Done: true, Err: &UpstreamError{Provider: "ollama", Err: io.ErrUnexpectedEOF}})
		}
	}()
	return ch, nil
}

// ---------------------------------------------------------------- 视觉

type ollamaVisioner struct {
	oc      *ollamaClient
	model   string
	prompt  string
	timeout time.Duration
}

func newOllamaVisioner(cfg *config.Config) *ollamaVisioner {
	return &ollamaVisioner{
		oc:      newOllamaClient(cfg.Vision.BaseURL, cfg.Vision.TimeoutS),
		model:   cfg.Vision.Model,
		prompt:  cfg.Vision.Prompt,
		timeout: timeoutOf(cfg.Vision.TimeoutS, 90*time.Second),
	}
}

func (v *ollamaVisioner) Model() string    { return v.model }
func (v *ollamaVisioner) Provider() string { return "ollama" }

func (v *ollamaVisioner) Ping(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, v.oc.baseURL+"/api/tags", nil)
	if err != nil {
		return err
	}
	resp, err := v.oc.client.Do(req)
	if err != nil {
		return &UpstreamError{Provider: "ollama", Err: err}
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return ollamaError(resp)
	}
	var parsed struct {
		Models []struct {
			Name string `json:"name"`
		} `json:"models"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		return err
	}
	for _, m := range parsed.Models {
		if m.Name == v.model || strings.HasPrefix(m.Name, v.model+":") {
			return nil
		}
	}
	return fmt.Errorf("Ollama 里没有视觉模型 %q，请先执行: ollama pull %s", v.model, v.model)
}

func (v *ollamaVisioner) Caption(ctx context.Context, image []byte, mime string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, v.timeout)
	defer cancel()

	payload := map[string]any{
		"model":  v.model,
		"stream": false,
		"messages": []map[string]any{{
			"role":    "user",
			"content": v.prompt,
			"images":  []string{encodeBase64(image)},
		}},
	}
	resp, err := v.oc.post(ctx, "/api/chat", payload)
	if err != nil {
		return "", err
	}
	if resp.StatusCode != http.StatusOK {
		return "", ollamaError(resp)
	}
	defer resp.Body.Close()

	var parsed struct {
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		return "", &UpstreamError{Provider: "ollama", Err: err}
	}
	return strings.TrimSpace(parsed.Message.Content), nil
}

// ---------------------------------------------------------------- 小工具

// sendDelta 在 ctx 取消时返回 false，让生产端尽快退出。
func sendDelta(ctx context.Context, ch chan<- Delta, d Delta) bool {
	select {
	case ch <- d:
		return true
	case <-ctx.Done():
		return false
	}
}
