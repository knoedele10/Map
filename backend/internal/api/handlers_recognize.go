package api

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"ragkb/internal/rag"
)

// 拍照识景（POST /api/v1/recognize）：给一张照片，判断它是候选里的哪一处古迹。
//
// 识别的核心逻辑在 rag 包里（rag.Recognize），因为 /api/v1/chat 带图的那一轮也要用它：
// 两条路共用同一份候选表和同一套匹配规则，才不会出现"识景按钮认得出、对话页认不出"。
// 这里只负责参数校验、候选表覆盖和响应封装。

type recognizeCandidate struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Hint string `json:"hint"`
}

type recognizeRequest struct {
	// Image 是 data URL 或裸 base64，与 /api/v1/chat 的 images 同规则。
	Image string `json:"image"`
	// Candidates 可选：不传就用景点库（sites.json）生成的候选表。传了就以传进来的为准
	//（前端可以把自己地图上的站点连同 id 一起发过来，识别结果直接带回 site_id）。
	Candidates  []recognizeCandidate `json:"candidates"`
	Temperature float64              `json:"temperature"`
}

type recognizeResponse struct {
	Landmark  string   `json:"landmark"`
	SiteID    string   `json:"site_id"`
	Matched   bool     `json:"matched"`
	MatchMode string   `json:"match_mode"`
	Raw       string   `json:"raw"`
	// Candidates 只有走视觉大模型那条路才有值（检索快速通道不需要候选表）。
	Candidates []string `json:"candidates"`
	Model      string   `json:"model"`
	ElapsedMS  int      `json:"elapsed_ms"`
	// RetrievalScore 是检索快速通道给出的相似度：match_mode=retrieval 时就是采纳它的依据；
	// 回落到大模型时它是"为什么没采纳"（低于 recognize.retrieval_min_score）。
	RetrievalScore float64 `json:"retrieval_score,omitempty"`
}

const maxRecognizeCandidates = rag.RecognizeMaxCandidates

// recognize 处理拍照识景。
func (s *Server) recognize(c *gin.Context) {
	var req recognizeRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		Fail(c, CodeBadRequest, "请求体不是合法 JSON: "+err.Error())
		return
	}
	if err := validateImage(req.Image); err != nil {
		Fail(c, CodeBadRequest, "image: "+err.Error())
		return
	}
	if req.Temperature < 0 || req.Temperature > 2 {
		Fail(c, CodeBadRequest, "temperature 必须在 0~2")
		return
	}
	if len(req.Candidates) > maxRecognizeCandidates {
		Fail(c, CodeBadRequest, fmt.Sprintf("candidates 最多 %d 个（收到 %d 个）", maxRecognizeCandidates, len(req.Candidates)))
		return
	}

	// 调用方给了候选表就用它（前端把自己的站点发过来），否则用景点库生成的缺省表。
	var cands []rag.RecognizeCandidate
	if len(req.Candidates) > 0 {
		cands = make([]rag.RecognizeCandidate, 0, len(req.Candidates))
		for i, rc := range req.Candidates {
			name := strings.TrimSpace(rc.Name)
			if name == "" {
				Fail(c, CodeBadRequest, fmt.Sprintf("candidates[%d].name 不能为空", i))
				return
			}
			cands = append(cands, rag.RecognizeCandidate{ID: strings.TrimSpace(rc.ID), Name: name, Hint: rc.Hint})
		}
	}

	timeout := time.Duration(s.Cfg.Server.RequestTimeoutS) * time.Second
	if timeout <= 0 {
		timeout = 180 * time.Second
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), timeout)
	defer cancel()

	res, err := s.Rag.RecognizeWith(ctx, req.Image, cands)
	if err != nil {
		s.failAnswer(c, err)
		return
	}
	OK(c, recognizeResponse{
		Landmark:       res.Landmark,
		SiteID:         res.SiteID,
		Matched:        res.Matched,
		MatchMode:      res.MatchMode,
		Raw:            res.Raw,
		Candidates:     res.Candidates,
		Model:          res.Model,
		ElapsedMS:      res.ElapsedMS,
		RetrievalScore: res.RetrievalScore,
	})
}
