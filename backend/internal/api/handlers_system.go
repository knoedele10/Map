package api

import (
	"context"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"

	"ragkb/internal/rag"
	"ragkb/internal/types"
)

// depStatus 是单个依赖的状态。
type depStatus struct {
	OK       bool   `json:"ok"`
	Provider string `json:"provider"`
	Model    string `json:"model"`
	Dim      int    `json:"dim,omitempty"`
	Detail   string `json:"detail"`
}

// health 汇报各依赖是否就绪。
//
// HTTP 状态码恒为 200：这是"我能跑，但某一环没配好"的正常状态，
// 前端应当根据 status 字段提示用户，而不是把整个页面判成故障。
func (s *Server) health(c *gin.Context) {
	ctx, cancel := context.WithTimeout(c.Request.Context(), 8*time.Second)
	defer cancel()

	emb := depStatus{Provider: s.Embedder.Provider(), Model: s.Embedder.Model(), Dim: s.Embedder.Dim()}
	if err := s.Embedder.Ping(ctx); err != nil {
		emb.Detail = err.Error()
	} else {
		emb.OK = true
	}

	chat := depStatus{Provider: s.Cfg.Chat.Provider, Model: s.Cfg.Chat.Model}
	switch {
	case s.Chatter == nil:
		chat.Detail = "未配置 API Key（设置 chat.api_key 或环境变量 RAG_CHAT_API_KEY）"
	default:
		chat.Provider = s.Chatter.Provider()
		chat.Model = s.Chatter.Model()
		if err := s.Chatter.Ping(ctx); err != nil {
			chat.Detail = err.Error()
		} else {
			chat.OK = true
		}
	}

	vision := depStatus{Provider: s.Cfg.Vision.Provider, Model: s.Cfg.Vision.Model}
	switch {
	case s.Vision == nil:
		vision.Detail = "未启用（vision.enabled=false，图片仅按文件名检索）"
	default:
		vision.Provider = s.Vision.Provider()
		vision.Model = s.Vision.Model()
		if err := s.Vision.Ping(ctx); err != nil {
			vision.Detail = err.Error()
		} else {
			vision.OK = true
		}
	}

	st, _ := s.Backend.Stats(ctx)
	status := "ok"
	if !emb.OK || !chat.OK {
		status = "degraded"
	}

	OK(c, gin.H{
		"status":    status,
		"version":   Version,
		"uptime_s":  int(time.Since(s.StartTime).Seconds()),
		"embedding": emb,
		"chat":      chat,
		"vision":    vision,
		"index": gin.H{
			"chunks":  st.Chunks,
			"docs":    st.Docs,
			"dim":     st.Dim,
			"backend": st.Backend,
		},
	})
}

// publicConfig 只暴露前端需要的非敏感配置。
func (s *Server) publicConfig(c *gin.Context) {
	accept := []string{".txt", ".md", ".markdown", ".docx", ".pdf", ".xlsx", ".csv", ".html", ".htm", ".png", ".jpg", ".jpeg", ".webp", ".gif", ".bmp"}
	OK(c, gin.H{
		"version":         Version,
		"auth_required":   s.Cfg.Server.AuthToken != "",
		"embedding_model": s.Cfg.Embedding.Model,
		"chat_model":      s.Cfg.Chat.Model,
		"chat_provider":   s.Cfg.Chat.Provider,
		"vision_enabled":  s.Vision != nil,
		"retrieval": gin.H{
			"top_k":        s.Cfg.Retrieval.TopK,
			"candidate_k":  s.Cfg.Retrieval.CandidateK,
			"hybrid_alpha": s.Cfg.Retrieval.HybridAlpha,
			"fusion":       s.Cfg.Retrieval.Fusion,
			"min_score":    s.Cfg.Retrieval.MinScore,
			"rewrite":      s.Cfg.Retrieval.Rewrite,
		},
		"chunking": gin.H{
			"size":    s.Cfg.Chunking.Size,
			"overlap": s.Cfg.Chunking.Overlap,
		},
		"upload": gin.H{
			"max_mb": s.Cfg.Ingest.MaxFileMB,
			"accept": accept,
		},
		"features": gin.H{
			"stream":            true,
			"structured_answer": true,
			"history":           true,
			"filters":           true,
			"abilities":         true,
			"tools":             s.Sites != nil && s.Sites.Len() > 0,
			"traces":            true,
			"sites":             s.Sites != nil && s.Sites.Len() > 0,
			"pgvector":          s.Cfg.Storage.Backend == "pgvector",
		},
	})
}

// stats 返回索引统计与可过滤的元数据取值。
func (s *Server) stats(c *gin.Context) {
	st, err := s.Backend.Stats(c.Request.Context())
	if err != nil {
		Fail(c, CodeInternal, "读取索引统计失败: "+err.Error())
		return
	}
	OK(c, st)
}

// listTraces 返回最近的问答留痕，用于排障复现。
func (s *Server) listTraces(c *gin.Context) {
	limit := 20
	if v := c.Query("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			limit = n
		}
	}
	tracer := s.Rag.Tracer()
	if tracer == nil {
		OK(c, gin.H{"traces": []rag.Trace{}, "total": 0})
		return
	}
	traces := tracer.List(limit)
	OK(c, gin.H{"traces": traces, "total": len(traces)})
}

// facetsOf 返回可用的元数据取值（给前端生成筛选器）。
func facetsOf(st types.Stats) types.Facets { return st.Facets }
