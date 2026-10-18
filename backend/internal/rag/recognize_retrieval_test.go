package rag

// 照片识别的「检索快速通道」验证：PP-ShiTu 分数达标就直接用，不达标/服务挂了就回落视觉大模型。
//
// 这条路是 docs/PADDLECLAS.md 的落地：检索约 100 ms、纯 CPU，大模型约 1.6 s、要 GPU，
// 两者错的样本几乎不重叠，所以做成"先检索、不行再叫大模型"的级联。

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	"ragkb/internal/config"
	"ragkb/internal/llm"
	"ragkb/internal/retrieval"
	"ragkb/internal/sites"
)

// stubVision 是桩视觉模型：记下被叫了几次，回一个固定候选名。
type stubVision struct {
	reply string
	calls int32
}

func (s *stubVision) Stream(_ context.Context, _ llm.ChatRequest) (<-chan llm.Delta, error) {
	atomic.AddInt32(&s.calls, 1)
	ch := make(chan llm.Delta, 2)
	ch <- llm.Delta{Content: s.reply}
	ch <- llm.Delta{Done: true}
	close(ch)
	return ch, nil
}
func (s *stubVision) Model() string                { return "stub-vision" }
func (s *stubVision) Provider() string             { return "stub" }
func (s *stubVision) Ping(_ context.Context) error { return nil }
func (s *stubVision) Calls() int                   { return int(atomic.LoadInt32(&s.calls)) }

// newRetrievalService 造一个挂着桩检索服务的 rag.Service。retrievalURL 为空表示不启用检索。
func newRetrievalService(t *testing.T, retrievalURL string, minScore float64, chatter *stubVision) *Service {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "sites.json")
	body := `{"sites":[{"id":"wuhou","name":"武侯祠"},{"id":"dufu","name":"杜甫草堂"}]}`
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("写景点数据失败: %v", err)
	}
	store, err := sites.Load(path)
	if err != nil {
		t.Fatalf("加载景点数据失败: %v", err)
	}
	svc := NewWithVision(config.Default(), nil, nil, chatter, chatter, store, nil)
	if retrievalURL != "" {
		svc.SetRetrieval(retrieval.New(retrievalURL, minScore, 3))
	}
	return svc
}

// stubShitu 起一个假的 PP-ShiTu 服务。
func stubShitu(t *testing.T, siteID string, score float64, status int) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/health" {
			w.WriteHeader(http.StatusOK)
			return
		}
		if status != http.StatusOK {
			w.WriteHeader(status)
			_, _ = w.Write([]byte(`{"error":"boom"}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"site_id": siteID, "score": score, "ms": 92,
			"topk": []map[string]any{{"site_id": siteID, "score": score}},
		})
	}))
	t.Cleanup(srv.Close)
	return srv
}

const anyImage = "data:image/jpeg;base64,AAAA"

// 分数达标 —— 直接采用检索结果，而且**不能**去叫大模型（这是快的关键）。
func TestRecognizeUsesRetrievalWhenScorePasses(t *testing.T) {
	srv := stubShitu(t, "wuhou", 0.83, http.StatusOK)
	chatter := &stubVision{reply: "杜甫草堂"}
	svc := newRetrievalService(t, srv.URL, 0.6, chatter)

	res, err := svc.Recognize(context.Background(), anyImage)
	if err != nil {
		t.Fatalf("识别失败: %v", err)
	}
	if res.SiteID != "wuhou" || res.Landmark != "武侯祠" {
		t.Fatalf("应采用检索结果 wuhou/武侯祠，实际 %s/%s", res.SiteID, res.Landmark)
	}
	if res.MatchMode != "retrieval" {
		t.Errorf("MatchMode = %q, want retrieval", res.MatchMode)
	}
	if !res.Matched {
		t.Error("Matched 应为 true")
	}
	if res.RetrievalScore != 0.83 {
		t.Errorf("RetrievalScore = %v, want 0.83", res.RetrievalScore)
	}
	if chatter.Calls() != 0 {
		t.Errorf("检索达标时不该调视觉大模型，实际调了 %d 次", chatter.Calls())
	}
}

// 分数不够 —— 回落大模型，并且把"为什么回落"的分数带出来。
func TestRecognizeFallsBackWhenScoreLow(t *testing.T) {
	srv := stubShitu(t, "wuhou", 0.31, http.StatusOK)
	chatter := &stubVision{reply: "杜甫草堂"}
	svc := newRetrievalService(t, srv.URL, 0.6, chatter)

	res, err := svc.Recognize(context.Background(), anyImage)
	if err != nil {
		t.Fatalf("识别失败: %v", err)
	}
	if chatter.Calls() != 1 {
		t.Fatalf("分数不够时必须回落大模型，实际调用 %d 次", chatter.Calls())
	}
	if res.SiteID != "dufu" || res.MatchMode == "retrieval" {
		t.Errorf("应采用大模型结果 dufu，实际 %s/%s", res.SiteID, res.MatchMode)
	}
	if res.RetrievalScore != 0.31 {
		t.Errorf("回落时也要带出检索分数，RetrievalScore = %v", res.RetrievalScore)
	}
}

// 检索服务挂了 —— 不能影响识别：照样回落大模型，且不报错。
func TestRecognizeSurvivesRetrievalFailure(t *testing.T) {
	for _, tc := range []struct {
		name   string
		server func(t *testing.T) string
	}{
		{"服务返回 500", func(t *testing.T) string { return stubShitu(t, "", 0, http.StatusInternalServerError).URL }},
		{"服务没起", func(t *testing.T) string { return "http://127.0.0.1:1" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			chatter := &stubVision{reply: "武侯祠"}
			svc := newRetrievalService(t, tc.server(t), 0.6, chatter)

			res, err := svc.Recognize(context.Background(), anyImage)
			if err != nil {
				t.Fatalf("检索坏了不该让识别报错: %v", err)
			}
			if chatter.Calls() != 1 {
				t.Fatalf("应回落大模型一次，实际 %d 次", chatter.Calls())
			}
			if res.SiteID != "wuhou" {
				t.Errorf("SiteID = %q, want wuhou", res.SiteID)
			}
		})
	}
}

// 显式给了候选表（游戏页"细看是哪一处"）时不能走检索：检索库里只有景点级别，答不了细分。
func TestRecognizeWithCustomCandidatesSkipsRetrieval(t *testing.T) {
	srv := stubShitu(t, "wuhou", 0.99, http.StatusOK)
	chatter := &stubVision{reply: "红墙夹道"}
	svc := newRetrievalService(t, srv.URL, 0.6, chatter)

	res, err := svc.RecognizeWith(context.Background(), anyImage, []RecognizeCandidate{
		{ID: "wuhou-redwall", Name: "红墙夹道"},
		{ID: "wuhou-hall", Name: "诸葛亮殿"},
	})
	if err != nil {
		t.Fatalf("识别失败: %v", err)
	}
	if chatter.Calls() != 1 {
		t.Fatalf("自定义候选表必须走大模型，实际调用 %d 次", chatter.Calls())
	}
	if res.Landmark != "红墙夹道" || res.MatchMode == "retrieval" {
		t.Errorf("应采用细分候选结果，实际 %s/%s", res.Landmark, res.MatchMode)
	}
}

// 没配检索（retrieval_url 为空）—— 行为必须与升级前完全一致。
func TestRecognizeWithoutRetrievalIsUnchanged(t *testing.T) {
	chatter := &stubVision{reply: "武侯祠"}
	svc := newRetrievalService(t, "", 0, chatter)

	res, err := svc.Recognize(context.Background(), anyImage)
	if err != nil {
		t.Fatalf("识别失败: %v", err)
	}
	if chatter.Calls() != 1 {
		t.Fatalf("没配检索时应直接走大模型，实际 %d 次", chatter.Calls())
	}
	if res.SiteID != "wuhou" || res.MatchMode != "exact" {
		t.Errorf("应与升级前一致：exact 命中武侯祠，实际 %s/%s", res.SiteID, res.MatchMode)
	}
	if res.RetrievalScore != 0 {
		t.Errorf("没走检索时不该有检索分数，实际 %v", res.RetrievalScore)
	}
}
