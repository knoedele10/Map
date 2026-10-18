package eval

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"ragkb/internal/types"
)

// 契约 v2 的接口路径（API 前缀 /api/v1）。
const (
	searchPath = "/api/v1/search"
	chatPath   = "/api/v1/chat"
)

// defaultHTTPTimeout 是单次 HTTP 调用的超时；问答可能较慢，留足时间。
const defaultHTTPTimeout = 120 * time.Second

// maxResponseBytes 限制读取的响应体大小（契约请求体上限 32 MiB）。
const maxResponseBytes = 32 << 20

// HTTPError 是服务端返回的非成功响应：HTTP 非 2xx，或信封 code != 0。
// 字段与契约 1.1 / 1.3 对应。
type HTTPError struct {
	Status    int    // HTTP 状态码
	Code      int    // 契约业务错误码；响应体不是信封时为 0
	Message   string // 契约 message
	RequestID string // 契约 request_id
}

func (e *HTTPError) Error() string {
	return fmt.Sprintf("eval: 服务端返回错误 HTTP %d code=%d message=%s request_id=%s",
		e.Status, e.Code, e.Message, e.RequestID)
}

// envelope 是契约 1.1 的统一响应信封。
type envelope struct {
	Code      int             `json:"code"`
	Message   string          `json:"message"`
	Data      json.RawMessage `json:"data"`
	RequestID string          `json:"request_id"`
}

// searchRequest 对应契约 3.5 POST /api/v1/search 的请求体。
type searchRequest struct {
	Query   string       `json:"query"`
	TopK    int          `json:"top_k,omitempty"`
	Filters types.Filter `json:"filters"`
}

// searchResponse 对应 3.5 的 data。
type searchResponse struct {
	Hits      []types.Hit `json:"hits"`
	ElapsedMS int         `json:"elapsed_ms"`
}

// chatRequest 对应契约 3.4 POST /api/v1/chat 的请求体。
// 评测走非流式（stream=false）一次性 JSON，便于判定引用与关键词。
type chatRequest struct {
	Question     string       `json:"question"`
	Stream       bool         `json:"stream"`
	AnswerFormat string       `json:"answer_format"`
	TopK         int          `json:"top_k,omitempty"`
	Filters      types.Filter `json:"filters"`
}

// chatResponse 对应 3.4 非流式响应的 data。
type chatResponse struct {
	Answer     string                  `json:"answer"`
	Structured *types.StructuredAnswer `json:"structured"`
	Sources    []types.Hit             `json:"sources"`
	ElapsedMS  int                     `json:"elapsed_ms"`
}

// httpClient 是 Client 的黑盒 HTTP 实现。
type httpClient struct {
	baseURL string
	token   string
	hc      *http.Client
}

// NewHTTPClient 指向真实服务；baseURL 例如 http://127.0.0.1:8080，token 可为空。
// token 非空时按契约 1.2 发送 Authorization: Bearer <token>。
func NewHTTPClient(baseURL, token string) Client {
	return &httpClient{
		baseURL: strings.TrimRight(strings.TrimSpace(baseURL), "/"),
		token:   strings.TrimSpace(token),
		hc:      &http.Client{Timeout: defaultHTTPTimeout},
	}
}

// Search 调 POST /api/v1/search，返回 data.hits。
func (c *httpClient) Search(ctx context.Context, query string, topK int, f types.Filter) ([]types.Hit, error) {
	var data searchResponse
	if err := c.do(ctx, searchPath, searchRequest{Query: query, TopK: topK, Filters: f}, &data); err != nil {
		return nil, err
	}
	return data.Hits, nil
}

// Chat 调 POST /api/v1/chat（stream=false），返回一次性 JSON 结果。
func (c *httpClient) Chat(ctx context.Context, req ChatInput) (ChatOutput, error) {
	format := strings.TrimSpace(req.AnswerFormat)
	if format == "" {
		format = "text"
	}
	body := chatRequest{
		Question:     req.Question,
		Stream:       false,
		AnswerFormat: format,
		TopK:         req.TopK,
		Filters:      req.Filters,
	}
	var data chatResponse
	if err := c.do(ctx, chatPath, body, &data); err != nil {
		return ChatOutput{}, err
	}
	return ChatOutput{
		Answer:     data.Answer,
		Structured: data.Structured,
		Sources:    data.Sources,
		ElapsedMS:  data.ElapsedMS,
	}, nil
}

// do 发送 JSON 请求并解析契约信封。
// HTTP 非 2xx 或 code != 0 → *HTTPError；响应体不是信封 → 也返回 *HTTPError。
func (c *httpClient) do(ctx context.Context, p string, reqBody any, out any) error {
	if c.baseURL == "" {
		return fmt.Errorf("eval: baseURL 为空")
	}
	raw, err := json.Marshal(reqBody)
	if err != nil {
		return fmt.Errorf("eval: 序列化 %s 请求体: %w", p, err)
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+p, bytes.NewReader(raw))
	if err != nil {
		return fmt.Errorf("eval: 构造 %s 请求: %w", p, err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "application/json")
	if c.token != "" {
		httpReq.Header.Set("Authorization", "Bearer "+c.token)
	}

	resp, err := c.hc.Do(httpReq)
	if err != nil {
		return fmt.Errorf("eval: 请求 %s: %w", p, err)
	}
	defer resp.Body.Close()

	bodyBytes, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
	if err != nil {
		return fmt.Errorf("eval: 读取 %s 响应: %w", p, err)
	}

	var env envelope
	if err := json.Unmarshal(bodyBytes, &env); err != nil {
		return &HTTPError{
			Status:  resp.StatusCode,
			Message: fmt.Sprintf("响应不是契约信封: %v; body=%s", err, truncate(string(bodyBytes), 200)),
		}
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 || env.Code != 0 {
		return &HTTPError{
			Status:    resp.StatusCode,
			Code:      env.Code,
			Message:   env.Message,
			RequestID: env.RequestID,
		}
	}
	if out == nil {
		return nil
	}
	if len(env.Data) == 0 || string(env.Data) == "null" {
		return &HTTPError{
			Status:    resp.StatusCode,
			Code:      env.Code,
			Message:   "响应信封 data 为空",
			RequestID: env.RequestID,
		}
	}
	if err := json.Unmarshal(env.Data, out); err != nil {
		return fmt.Errorf("eval: 解析 %s 的 data: %w", p, err)
	}
	return nil
}

func truncate(s string, n int) string {
	if n <= 0 || len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
