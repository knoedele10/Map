// Package retrieval 是照片识别的「检索快速通道」客户端。
//
// 它把一张图发给本机的 PP-ShiTuV2 检索服务（tools/shitu/server.py），拿回最像的
// site_id 和相似度。相似度达标就直接采用，不达标再回落到视觉大模型的候选单选。
//
// 为什么要两条腿（见 docs/PADDLECLAS.md）：
//   * 检索：114 ms、纯 CPU、不占显存，91/100
//   * 视觉大模型：约 1.6 s、要 GPU，95/100
//   * 两者错的样本几乎不重叠，串起来 96/100，且六成请求由检索直接答完
//
// 服务没起、超时、返回异常 —— 一律当作「快速通道不可用」，不抛给调用方，
// 识别自动落回大模型那条路（可用性优先）。
package retrieval

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// Candidate 是检索返回的一个候选。
type Candidate struct {
	SiteID string  `json:"site_id"`
	Score  float64 `json:"score"`
}

// Result 是一次检索结果。
type Result struct {
	SiteID  string      `json:"site_id"`
	Score   float64     `json:"score"`
	TopK    []Candidate `json:"topk"`
	Elapsed int         `json:"ms"`
}

// Client 访问检索服务。零值不可用；用 New 构造。
type Client struct {
	baseURL  string
	minScore float64
	hc       *http.Client
}

// New 构造客户端。baseURL 为空表示不启用检索（Enabled 返回 false）。
func New(baseURL string, minScore float64, timeoutS int) *Client {
	if timeoutS <= 0 {
		timeoutS = 3
	}
	return &Client{
		baseURL:  strings.TrimRight(strings.TrimSpace(baseURL), "/"),
		minScore: minScore,
		hc:       &http.Client{Timeout: time.Duration(timeoutS) * time.Second},
	}
}

// Enabled 表示是否配置了检索服务。
func (c *Client) Enabled() bool { return c != nil && c.baseURL != "" }

// MinScore 是采纳检索结果所需的最低相似度。
func (c *Client) MinScore() float64 {
	if c == nil {
		return 0
	}
	return c.minScore
}

// Retrieve 检索一张图。第二个返回值表示分数是否达到阈值（达到才建议采用）。
//
// 注意：即使返回 error，调用方也应该继续走大模型那条路 —— 这只是快速通道不可用。
func (c *Client) Retrieve(ctx context.Context, image string) (Result, bool, error) {
	var out Result
	if !c.Enabled() {
		return out, false, fmt.Errorf("检索服务未配置")
	}
	body, err := json.Marshal(map[string]any{"image": image, "topk": 5})
	if err != nil {
		return out, false, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/retrieve", bytes.NewReader(body))
	if err != nil {
		return out, false, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.hc.Do(req)
	if err != nil {
		return out, false, fmt.Errorf("检索服务请求失败: %w", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusOK {
		return out, false, fmt.Errorf("检索服务返回 %d: %s", resp.StatusCode, truncate(string(raw), 200))
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return out, false, fmt.Errorf("检索结果解析失败: %w", err)
	}
	if out.SiteID == "" {
		return out, false, nil
	}
	return out, out.Score >= c.minScore, nil
}

// Ping 探活，主要给健康检查用。
func (c *Client) Ping(ctx context.Context) error {
	if !c.Enabled() {
		return fmt.Errorf("检索服务未配置")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/health", nil)
	if err != nil {
		return err
	}
	resp, err := c.hc.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("检索服务返回 %d", resp.StatusCode)
	}
	return nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
