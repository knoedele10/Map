package api

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"ragkb/internal/llm"
	"ragkb/internal/rag"
	"ragkb/internal/types"
)

type chatRequest struct {
	Question string `json:"question"`
	// Images 是当前这一轮携带的图片（契约 §1）：data URL 或裸 base64。
	Images           []string            `json:"images"`
	Stream           *bool               `json:"stream"`
	AnswerFormat     string              `json:"answer_format"`
	TopK             int                 `json:"top_k"`
	Temperature      float64             `json:"temperature"`
	SessionID        string              `json:"session_id"`
	History          []types.ChatMessage `json:"history"`
	Filters          types.Filter        `json:"filters"`
	Abilities        []string            `json:"abilities"`
	UnlockedMemories []string            `json:"unlocked_memories"`
	RongbaoPersona   bool                `json:"rongbao_persona"`
	GuardianUnlocked bool                `json:"guardian_unlocked"`
	UseTools         *bool               `json:"use_tools"`
	Rewrite          *bool               `json:"rewrite"`
}

type searchRequest struct {
	Query     string       `json:"query"`
	TopK      int          `json:"top_k"`
	Filters   types.Filter `json:"filters"`
	Abilities []string     `json:"abilities"`
}

// 图片直传上限（契约 §1）。
const (
	maxImagesPerTurn = 4        // 每轮最多 4 张
	maxImageB64Bytes = 12 << 20 // 单张 base64 字符串 <= 12 MiB
	maxImageBytes    = 8 << 20  // 单张解码后 <= 8 MiB
)

// validateImages 校验一轮消息里的图片；field 只用于错误信息。
func validateImages(field string, imgs []string) error {
	if len(imgs) > maxImagesPerTurn {
		return fmt.Errorf("%s 最多 %d 张（收到 %d 张）", field, maxImagesPerTurn, len(imgs))
	}
	for i, raw := range imgs {
		if err := validateImage(raw); err != nil {
			return fmt.Errorf("%s[%d]: %w", field, i, err)
		}
	}
	return nil
}

// validateImage 校验单张图片：data URL 或裸 base64，base64 串 <= 12 MiB，解码后 <= 8 MiB。
func validateImage(raw string) error {
	if raw == "" {
		return errors.New("图片为空")
	}
	if len(raw) > maxImageB64Bytes {
		return fmt.Errorf("base64 字符串过长（%d 字节，上限 %d 字节）", len(raw), maxImageB64Bytes)
	}
	payload := raw
	if strings.HasPrefix(raw, "data:") {
		if !strings.HasPrefix(raw, "data:image/") {
			return errors.New("data URL 必须是 data:image/…")
		}
		comma := strings.Index(raw, ",")
		if comma < 0 || !strings.Contains(raw[:comma], ";base64") {
			return errors.New("data URL 必须是 data:image/<type>;base64,<data>")
		}
		payload = raw[comma+1:]
	}
	decoded, err := base64.StdEncoding.DecodeString(payload)
	if err != nil {
		if decoded, err = base64.RawStdEncoding.DecodeString(payload); err != nil {
			return fmt.Errorf("base64 解码失败: %v", err)
		}
	}
	if len(decoded) > maxImageBytes {
		return fmt.Errorf("图片解码后过大（%d 字节，上限 %d 字节）", len(decoded), maxImageBytes)
	}
	return nil
}

// chat 处理问答：SSE 流式（默认）或一次性 JSON。
func (s *Server) chat(c *gin.Context) {
	var req chatRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		Fail(c, CodeBadRequest, "请求体不是合法 JSON: "+err.Error())
		return
	}
	req.Question = strings.TrimSpace(req.Question)
	if req.Question == "" {
		Fail(c, CodeBadRequest, "question 不能为空")
		return
	}
	if len([]rune(req.Question)) > 2000 {
		Fail(c, CodeBadRequest, "question 过长（上限 2000 字）")
		return
	}
	if s.Rag.Chatter() == nil {
		Fail(c, CodeNotReady, "生成模型未配置：请设置 chat.api_key 或环境变量 RAG_CHAT_API_KEY")
		return
	}
	if req.AnswerFormat == "" {
		req.AnswerFormat = "text"
	}
	if req.AnswerFormat != "text" && req.AnswerFormat != "structured" {
		Fail(c, CodeBadRequest, "answer_format 只能是 text 或 structured")
		return
	}
	if req.TopK < 0 || req.TopK > 20 {
		Fail(c, CodeBadRequest, "top_k 必须在 1~20")
		return
	}
	if req.Temperature < 0 || req.Temperature > 2 {
		Fail(c, CodeBadRequest, "temperature 必须在 0~2")
		return
	}
	if len(req.History) > 20 {
		req.History = req.History[len(req.History)-20:]
	}
	if len(req.Abilities) > 0 {
		req.Filters.Abilities = append(req.Filters.Abilities, req.Abilities...)
	}
	for _, m := range req.Filters.Modalities {
		if m != types.ModalityText && m != types.ModalityTable && m != types.ModalityImage {
			Fail(c, CodeBadRequest, "modalities 只能是 text / table / image")
			return
		}
	}

	// 图片直传校验（契约 §1 限制表）：超出即 400 / 40001。
	if err := validateImages("images", req.Images); err != nil {
		Fail(c, CodeBadRequest, err.Error())
		return
	}
	for i := range req.History {
		if err := validateImages(fmt.Sprintf("history[%d].images", i), req.History[i].Images); err != nil {
			Fail(c, CodeBadRequest, err.Error())
			return
		}
	}

	useTools := true
	if req.UseTools != nil {
		useTools = *req.UseTools
	}
	rewrite := s.Cfg.Retrieval.Rewrite
	if req.Rewrite != nil {
		rewrite = *req.Rewrite
	}

	timeout := time.Duration(s.Cfg.Server.RequestTimeoutS) * time.Second
	if timeout <= 0 {
		timeout = 180 * time.Second
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), timeout)
	defer cancel()

	answerReq := rag.AnswerRequest{
		Question:         req.Question,
		Images:           req.Images,
		History:          req.History,
		TopK:             req.TopK,
		Temperature:      req.Temperature,
		AnswerFormat:     req.AnswerFormat,
		Filters:          req.Filters,
		UnlockedMemories: req.UnlockedMemories,
		RongbaoPersona:   req.RongbaoPersona,
		GuardianUnlocked: req.GuardianUnlocked,
		UseTools:         useTools,
		Rewrite:          rewrite,
		RequestID:        RequestIDOf(c),
	}

	if !wantsStream(c, req.Stream) {
		result, err := s.Rag.Answer(ctx, answerReq, nil)
		if err != nil {
			s.failAnswer(c, err)
			return
		}
		OK(c, result)
		return
	}

	w, ok := NewSSE(c)
	if !ok {
		Fail(c, CodeInternal, "当前服务器不支持流式响应（http.Flusher 不可用）")
		return
	}
	defer w.Close()

	// 事件直接透传：rag 的事件名与契约里的 SSE 事件名一一对应。
	result, err := s.Rag.Answer(ctx, answerReq, func(ev rag.Event) error {
		return w.Event(ev.Type, ev.Data)
	})
	if err != nil {
		if w.Closed() || errors.Is(err, context.Canceled) {
			return // 客户端断开，不是错误
		}
		code := CodeInternal
		if llm.IsUpstream(err) {
			code = CodeUpstream
		}
		_ = w.Event(rag.EventError, gin.H{"code": code, "message": err.Error(), "request_id": RequestIDOf(c)})
		return
	}
	_ = result
}

// failAnswer 把问答错误映射成业务码。
func (s *Server) failAnswer(c *gin.Context, err error) {
	switch {
	case errors.Is(err, llm.ErrNotConfigured):
		Fail(c, CodeNotReady, err.Error())
	case llm.IsUpstream(err):
		Fail(c, CodeUpstream, "上游模型服务错误: "+err.Error())
	case errors.Is(err, context.DeadlineExceeded):
		Fail(c, CodeUpstream, "生成超时，请缩小 top_k 或换更快的模型")
	default:
		Fail(c, CodeInternal, err.Error())
	}
}

// search 只做检索不调用生成模型：调试与评测全靠它。
func (s *Server) search(c *gin.Context) {
	var req searchRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		Fail(c, CodeBadRequest, "请求体不是合法 JSON: "+err.Error())
		return
	}
	req.Query = strings.TrimSpace(req.Query)
	if req.Query == "" {
		Fail(c, CodeBadRequest, "query 不能为空")
		return
	}
	if len(req.Abilities) > 0 {
		req.Filters.Abilities = append(req.Filters.Abilities, req.Abilities...)
	}
	if req.TopK < 0 || req.TopK > 50 {
		Fail(c, CodeBadRequest, "top_k 必须在 1~50")
		return
	}

	started := time.Now()
	ctx, cancel := context.WithTimeout(c.Request.Context(), 30*time.Second)
	defer cancel()

	hits, err := s.Rag.Retrieve(ctx, req.Query, rag.SearchOptions{TopK: req.TopK, Filters: req.Filters})
	if err != nil {
		s.failAnswer(c, err)
		return
	}
	OK(c, gin.H{"hits": hits, "elapsed_ms": int(time.Since(started).Milliseconds())})
}
