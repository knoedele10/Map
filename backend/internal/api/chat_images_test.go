package api_test

// 图片直传（契约 v1）的进程内验证：
//   HTTP -> handlers_chat 校验 -> rag.Answer 选模型 -> llm.Chatter.Stream
// 全程用桩 Chatter，不需要 Ollama、不需要 GPU、不需要长驻端口。

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"image"
	"image/png"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"ragkb/internal/api"
	"ragkb/internal/config"
	"ragkb/internal/ingest"
	"ragkb/internal/jobs"
	"ragkb/internal/llm"
	"ragkb/internal/rag"
	"ragkb/internal/sites"
	"ragkb/internal/store/file"
)

// ---------------------------------------------------------------- 桩模型

type stubEmbedder struct{ dim int }

func (s *stubEmbedder) Embed(_ context.Context, texts []string) ([][]float32, error) {
	out := make([][]float32, len(texts))
	for i := range texts {
		v := make([]float32, s.dim)
		for j := range v {
			v[j] = float32((j%7)+1) / 100
		}
		out[i] = v
	}
	return out, nil
}
func (s *stubEmbedder) Dim() int                     { return s.dim }
func (s *stubEmbedder) Model() string                { return "stub-embed" }
func (s *stubEmbedder) Provider() string             { return "stub" }
func (s *stubEmbedder) Ping(_ context.Context) error { return nil }

// recordedCall 是桩 Chatter 收到的一次生成请求。
type recordedCall struct {
	System   string
	Messages []llm.Message
}

// stubChatter 记录被交给模型的消息，然后回一个固定片段。
type stubChatter struct {
	model string
	// recognize 是"识别调用"的回复：系统提示词里带「景点识别器」时回它。
	// 留空则所有调用都回固定片段「收到」。
	recognize string
	mu        sync.Mutex
	calls     []recordedCall
}

func (s *stubChatter) Stream(ctx context.Context, req llm.ChatRequest) (<-chan llm.Delta, error) {
	s.mu.Lock()
	s.calls = append(s.calls, recordedCall{System: req.System, Messages: req.Messages})
	reply := "收到"
	if s.recognize != "" && strings.Contains(req.System, "景点识别器") {
		reply = s.recognize
	}
	s.mu.Unlock()
	ch := make(chan llm.Delta, 2)
	ch <- llm.Delta{Content: reply}
	ch <- llm.Delta{Done: true}
	close(ch)
	return ch, nil
}
func (s *stubChatter) Model() string                { return s.model }
func (s *stubChatter) Provider() string             { return "stub" }
func (s *stubChatter) Ping(_ context.Context) error { return nil }

func (s *stubChatter) snapshot() []recordedCall {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]recordedCall(nil), s.calls...)
}

// ---------------------------------------------------------------- 测试栈

type imageTestStack struct {
	server *httptest.Server
	text   *stubChatter
	vision *stubChatter
}

// newImageTestStack 用桩模型搭一个真实 Gin 路由；text/vision 是两个不同的桩，
// 因此可以断言"带图请求真的换了模型"。
func newImageTestStack(t *testing.T) *imageTestStack {
	t.Helper()
	return newImageTestStackWith(t, "", "")
}

// newImageTestStackWith 与 newImageTestStack 相同，但可以塞一份景点数据、
// 并让"识别调用"回一个指定的候选名（sitesJSON 为空表示景点库为空）。
func newImageTestStackWith(t *testing.T, sitesJSON, recognize string) *imageTestStack {
	t.Helper()
	tmp := t.TempDir()
	cfg := config.Default()
	cfg.Server.RateLimitPerMin = 0
	cfg.Storage.DataDir = filepath.Join(tmp, "data")
	cfg.Storage.DocsDir = filepath.Join(tmp, "docs")
	cfg.Storage.AssetsDir = filepath.Join(tmp, "assets")
	cfg.Storage.WebDir = filepath.Join(tmp, "web")
	cfg.Storage.SitesFile = filepath.Join(tmp, "sites.json")
	cfg.Chat.BaseURL = "http://127.0.0.1:1"
	cfg.Chat.APIKey = "test-key"
	if err := cfg.Validate(); err != nil {
		t.Fatalf("配置校验失败: %v", err)
	}
	if err := cfg.EnsureDirs(); err != nil {
		t.Fatalf("创建目录失败: %v", err)
	}
	backend, err := file.Open(cfg.Storage.DataDir, 1024, true)
	if err != nil {
		t.Fatalf("打开文件后端失败: %v", err)
	}
	t.Cleanup(func() { _ = backend.Close() })

	if sitesJSON != "" {
		if err := os.WriteFile(cfg.Storage.SitesFile, []byte(sitesJSON), 0o644); err != nil {
			t.Fatalf("写景点数据失败: %v", err)
		}
	}

	embedder := &stubEmbedder{dim: 1024}
	textChatter := &stubChatter{model: "text-model"}
	visionChatter := &stubChatter{model: "vision-model", recognize: recognize}
	siteStore, err := sites.Load(cfg.Storage.SitesFile)
	if err != nil {
		t.Fatalf("加载景点数据失败: %v", err)
	}
	pipeline, err := ingest.NewPipeline(cfg, backend, embedder, nil)
	if err != nil {
		t.Fatalf("构造流水线失败: %v", err)
	}
	ragSvc := rag.NewWithVision(cfg, backend, embedder, textChatter, visionChatter, siteStore, rag.NewTracer(10, ""))
	srv := api.New(api.Deps{
		Cfg: cfg, Backend: backend, Embedder: embedder, Chatter: textChatter,
		Rag: ragSvc, Jobs: jobs.NewManager(5), Sites: siteStore, Pipeline: pipeline,
	})
	ts := httptest.NewServer(srv.Router())
	t.Cleanup(ts.Close)
	return &imageTestStack{server: ts, text: textChatter, vision: visionChatter}
}

func (s *imageTestStack) post(t *testing.T, body map[string]any) (*http.Response, map[string]any) {
	t.Helper()
	// 不写 stream 时 /chat 默认走 SSE（契约 §3），这里统一要一次性 JSON。
	if _, ok := body["stream"]; !ok {
		body["stream"] = false
	}
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("序列化请求失败: %v", err)
	}
	resp, err := http.Post(s.server.URL+"/api/v1/chat", "application/json", bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("请求失败: %v", err)
	}
	defer resp.Body.Close()
	var env map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&env); err != nil {
		t.Fatalf("响应不是合法 JSON: %v", err)
	}
	return resp, env
}

func (s *imageTestStack) postSSE(t *testing.T, body any) []string {
	t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("序列化请求失败: %v", err)
	}
	resp, err := http.Post(s.server.URL+"/api/v1/chat", "application/json", bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("请求失败: %v", err)
	}
	defer resp.Body.Close()
	raw, err = io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("读取 SSE 失败: %v", err)
	}
	return eventNames(parseSSE(string(raw)))
}

// tinyDataURL 生成一张真实可解码的 2x2 PNG 的 data URL。
func tinyDataURL(t *testing.T) string {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 2, 2))
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatalf("编码 PNG 失败: %v", err)
	}
	return "data:image/png;base64," + base64.StdEncoding.EncodeToString(buf.Bytes())
}

func codeOf(env map[string]any) float64 {
	c, _ := env["code"].(float64)
	return c
}

func dataOf(env map[string]any) map[string]any {
	d, _ := env["data"].(map[string]any)
	return d
}

// ---------------------------------------------------------------- 用例

// 补丁前 Images 永远是空的：这是本测试要钉死的核心行为。
func TestChatImageReachesChatterAsImages(t *testing.T) {
	s := newImageTestStack(t)
	url := tinyDataURL(t)

	resp, env := s.post(t, map[string]any{"question": "这张图里有什么？", "images": []string{url}})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("HTTP = %d, want 200（env=%v）", resp.StatusCode, env)
	}
	if codeOf(env) != 0 {
		t.Fatalf("code = %v, want 0（message=%v）", env["code"], env["message"])
	}

	textCalls, visionCalls := s.text.snapshot(), s.vision.snapshot()
	if len(textCalls) != 0 {
		t.Fatalf("带图请求不该走文本模型，却调用了 %d 次", len(textCalls))
	}
	// 带图的一轮是两次视觉调用：先受限候选识别（认出是哪一处古迹），
	// 再带着识别结果回答。见 rag.Answer 的「1.5 图片识别」。
	if len(visionCalls) != 2 {
		t.Fatalf("视觉模型调用次数 = %d, want 2（识别 + 回答）", len(visionCalls))
	}
	// 两次调用都必须把图片带过去，否则图片根本没从 HTTP 传到模型层。
	for i, call := range visionCalls {
		msgs := call.Messages
		if len(msgs) != 1 {
			t.Fatalf("第 %d 次调用消息条数 = %d, want 1", i+1, len(msgs))
		}
		if msgs[0].Role != "user" {
			t.Fatalf("第 %d 次调用 role = %q, want user", i+1, msgs[0].Role)
		}
		if len(msgs[0].Images) != 1 {
			t.Fatalf("第 %d 次调用 Images 长度 = %d, want 1 —— 说明图片没有从 HTTP 传到模型层", i+1, len(msgs[0].Images))
		}
		if msgs[0].Images[0] != url {
			t.Fatalf("第 %d 次调用 Images[0] 与请求里的 data URL 不一致", i+1)
		}
	}
	// 第一次是识别：提示词是候选清单，不是用户的问题。
	if !strings.Contains(visionCalls[0].Messages[0].Content, "只输出选项名称") {
		t.Fatalf("第一次调用不是受限候选识别：%q", visionCalls[0].Messages[0].Content)
	}
	// 第二次是回答：带着用户原始问题。
	if !strings.Contains(visionCalls[1].Messages[0].Content, "这张图里有什么？") {
		t.Fatalf("第二次调用没有带用户问题：%q", visionCalls[1].Messages[0].Content)
	}
	// 识别结论要回给前端（前端可以据此显示「识别：武侯祠」）。
	vision, ok := dataOf(env)["vision"].(map[string]any)
	if !ok {
		t.Fatalf("响应缺少 data.vision：%v", dataOf(env))
	}
	if _, ok := vision["landmark"].(string); !ok {
		t.Fatalf("data.vision.landmark 缺失或不是字符串：%v", vision)
	}
	// data.model 必须报告本次实际使用的模型（契约 §2）。
	if got, _ := dataOf(env)["model"].(string); got != "vision-model" {
		t.Fatalf("data.model = %q, want vision-model", got)
	}
}

// 带图请求必须先识别出景点，再把景点塞进工具结果、拿景点名去检索。
//
// 这是「给图片它能认出武侯祠」的核心：不做识别的话，用户问「这是什么地方？」，
// 检索词就是「这是什么地方？」，什么都检索不到，最后回一句「知识库里没有找到相关内容」。
func TestChatImageRecognizesLandmarkAndGroundsAnswer(t *testing.T) {
	const sitesJSON = `{"sites":[{"id":"wuhou","name":"武侯祠","aliases":["武侯祠博物馆"],
		"summary":"纪念诸葛亮的祠堂，君臣合祀。","description":"成都武侯祠博物馆。",
		"facts":{"地址":"成都市武侯区武侯祠大街231号"}}]}`
	s := newImageTestStackWith(t, sitesJSON, "武侯祠")
	url := tinyDataURL(t)

	resp, env := s.post(t, map[string]any{"question": "这是什么地方？", "images": []string{url}})
	if resp.StatusCode != http.StatusOK || codeOf(env) != 0 {
		t.Fatalf("HTTP=%d env=%v", resp.StatusCode, env)
	}

	vision, ok := dataOf(env)["vision"].(map[string]any)
	if !ok {
		t.Fatalf("响应缺少 data.vision：%v", dataOf(env))
	}
	if got, _ := vision["landmark"].(string); got != "武侯祠" {
		t.Fatalf("识别 landmark = %q, want 武侯祠（模型原话 %v）", got, vision["raw"])
	}
	if got, _ := vision["site_id"].(string); got != "wuhou" {
		t.Fatalf("识别 site_id = %q, want wuhou", got)
	}
	if got, _ := vision["matched"].(bool); !got {
		t.Fatalf("识别 matched = false, want true")
	}

	// 识别出来的景点必须进 data.sites：结构化景点数据是模型答得出内容的依据。
	sitesOut, _ := dataOf(env)["sites"].([]any)
	if len(sitesOut) == 0 {
		t.Fatalf("data.sites 为空——识别出的景点没有注入工具结果：%v", dataOf(env))
	}
	first, _ := sitesOut[0].(map[string]any)
	if got, _ := first["id"].(string); got != "wuhou" {
		t.Fatalf("data.sites[0].id = %q, want wuhou", got)
	}

	// 回答调用要带【图片识别】结论，模型才知道该按哪个景点答。
	visionCalls := s.vision.snapshot()
	if len(visionCalls) != 2 {
		t.Fatalf("视觉模型调用次数 = %d, want 2", len(visionCalls))
	}
	answerPrompt := visionCalls[1].Messages[0].Content
	for _, want := range []string{"【图片识别】", "武侯祠", "这是什么地方？"} {
		if !strings.Contains(answerPrompt, want) {
			t.Fatalf("回答提示词里缺少 %q：%q", want, answerPrompt)
		}
	}
}

// 文本请求零回归：仍然走文本模型，且 Images 必须为空。
func TestChatTextOnlyIsUnchanged(t *testing.T) {
	s := newImageTestStack(t)

	resp, env := s.post(t, map[string]any{"question": "乐山大佛有多高？"})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("HTTP = %d, want 200", resp.StatusCode)
	}
	textCalls, visionCalls := s.text.snapshot(), s.vision.snapshot()
	if len(textCalls) != 1 {
		t.Fatalf("文本模型调用次数 = %d, want 1", len(textCalls))
	}
	if len(visionCalls) != 0 {
		t.Fatalf("纯文本请求不该走视觉模型，却调用了 %d 次", len(visionCalls))
	}
	msgs := textCalls[0].Messages
	if len(msgs) != 1 {
		t.Fatalf("消息条数 = %d, want 1", len(msgs))
	}
	if len(msgs[0].Images) != 0 {
		t.Fatalf("纯文本请求的 Images 应为空，实际 %v", msgs[0].Images)
	}
	if got, _ := dataOf(env)["model"].(string); got != "text-model" {
		t.Fatalf("data.model = %q, want text-model", got)
	}
	// 响应信封字段集合与打补丁前一致。
	data := dataOf(env)
	for _, k := range []string{"answer", "sources", "elapsed_ms", "ttft_ms", "model", "request_id"} {
		if _, ok := data[k]; !ok {
			t.Fatalf("响应缺少字段 %q：%v", k, data)
		}
	}
}

// 裸 base64 也要接受（契约 §1：后端会补前缀）。
func TestChatBareBase64Accepted(t *testing.T) {
	s := newImageTestStack(t)
	bare := base64.StdEncoding.EncodeToString([]byte("not-a-real-image"))
	resp, env := s.post(t, map[string]any{"question": "看图", "images": []string{bare}})
	if resp.StatusCode != http.StatusOK || codeOf(env) != 0 {
		t.Fatalf("裸 base64 应被接受：HTTP=%d env=%v", resp.StatusCode, env)
	}
	visionCalls := s.vision.snapshot()
	if len(visionCalls) != 2 {
		t.Fatalf("视觉模型调用次数 = %d, want 2（识别 + 回答）", len(visionCalls))
	}
	for i, call := range visionCalls {
		if len(call.Messages) != 1 || len(call.Messages[0].Images) != 1 {
			t.Fatalf("第 %d 次调用没有把裸 base64 透传到模型层: %+v", i+1, call.Messages)
		}
		if call.Messages[0].Images[0] != bare {
			t.Fatalf("第 %d 次调用改写了裸 base64：%q", i+1, call.Messages[0].Images[0])
		}
	}
}

func TestChatImageValidation(t *testing.T) {
	s := newImageTestStack(t)
	url := tinyDataURL(t)
	five := []string{url, url, url, url, url}
	// 超过 12 MiB 的单张 base64 字符串。
	huge := "data:image/png;base64," + strings.Repeat("A", 12<<20)

	cases := []struct {
		name string
		body map[string]any
	}{
		{"5 张图超过上限", map[string]any{"question": "x", "images": five}},
		{"单张超过 12MiB", map[string]any{"question": "x", "images": []string{huge}}},
		{"data URL 不是 image", map[string]any{"question": "x", "images": []string{"data:text/plain;base64,AAAA"}}},
		{"不是合法 base64", map[string]any{"question": "x", "images": []string{"!!!!not base64!!!!"}}},
		{"history 单轮 5 张", map[string]any{"question": "x", "history": []map[string]any{{"role": "user", "content": "看", "images": five}}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resp, env := s.post(t, tc.body)
			if resp.StatusCode != http.StatusBadRequest {
				t.Fatalf("HTTP = %d, want 400（env=%v）", resp.StatusCode, env)
			}
			if codeOf(env) != 40001 {
				t.Fatalf("code = %v, want 40001（env=%v）", env["code"], env)
			}
		})
	}
	if n := len(s.text.snapshot()) + len(s.vision.snapshot()); n != 0 {
		t.Fatalf("非法请求不该调用任何模型，实际调用 %d 次", n)
	}
}

// 带图与不带图的 SSE 事件序列必须完全一致（契约 §3）。
func TestChatImageSSESequenceUnchanged(t *testing.T) {
	s := newImageTestStack(t)
	plain := s.postSSE(t, map[string]any{"question": "乐山大佛有多高？", "stream": true})
	withImg := s.postSSE(t, map[string]any{"question": "这张图里有什么？", "images": []string{tinyDataURL(t)}, "stream": true})
	if len(plain) == 0 {
		t.Fatal("纯文本 SSE 没有任何事件")
	}
	if strings.Join(plain, ",") != strings.Join(withImg, ",") {
		t.Fatalf("SSE 事件序列不一致:\n text = %v\nimage = %v", plain, withImg)
	}
	if plain[0] != "meta" {
		t.Fatalf("首个事件应为 meta，实际 %v", plain)
	}
	if plain[len(plain)-1] != "done" {
		t.Fatalf("最后一个事件应为 done，实际 %v", plain)
	}
}

// D14：只有 history 带图、本轮不带新图时，也必须走视觉模型。
// 回归前这里会切到文本模型，上游直接 502（Multimodal data provided, but model does not support...）。
func TestChatHistoryOnlyImagesSelectVisionModel(t *testing.T) {
	s := newImageTestStack(t)
	url := tinyDataURL(t)
	resp, env := s.post(t, map[string]any{
		"question": "它是什么颜色？",
		"rewrite":  false, // 关掉改写，"文本桩 0 次"才是干净的断言
		"history": []map[string]any{
			{"role": "user", "content": "这张图里有什么？", "images": []string{url}},
			{"role": "assistant", "content": "是一只熊猫。"},
		},
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("HTTP = %d, want 200（env=%v）", resp.StatusCode, env)
	}
	if codeOf(env) != 0 {
		t.Fatalf("code = %v, want 0（message=%v）", env["code"], env["message"])
	}
	textCalls, visionCalls := s.text.snapshot(), s.vision.snapshot()
	if len(textCalls) != 0 {
		t.Fatalf("history 带图的请求不该走文本模型，却调用了 %d 次 —— 这正是 502 的根因", len(textCalls))
	}
	if len(visionCalls) != 1 {
		t.Fatalf("视觉模型调用次数 = %d, want 1", len(visionCalls))
	}
	msgs := visionCalls[0].Messages
	// 历史 2 轮（user 带图 + assistant）+ 本轮 user = 3 条。
	if len(msgs) != 3 {
		t.Fatalf("消息条数 = %d, want 3（历史 2 轮 + 本轮）", len(msgs))
	}
	if len(msgs[0].Images) != 1 || msgs[0].Images[0] != url {
		t.Fatalf("历史消息的图片没有透传到模型层: %+v", msgs[0])
	}
	if len(msgs[1].Images) != 0 {
		t.Fatalf("历史里的 assistant 轮不该带 Images: %+v", msgs[1])
	}
	if len(msgs[2].Images) != 0 {
		t.Fatalf("本轮没有图片，不该带 Images: %+v", msgs[2])
	}
	if got, _ := dataOf(env)["model"].(string); got != "vision-model" {
		t.Fatalf("data.model = %q, want vision-model", got)
	}
}

// 同一个 bug 类的第二道闸：多轮改写走的是**文本**模型，它绝不能带上图片。
// 如果哪天有人把 trimHistory 的结果直接塞进改写请求，这里会红。
func TestChatRewriteWithHistoryImagesStaysTextOnly(t *testing.T) {
	s := newImageTestStack(t)
	url := tinyDataURL(t)
	resp, env := s.post(t, map[string]any{
		"question": "它是什么颜色？", // 默认 rewrite=true 且 <=25 字，会触发改写
		"history": []map[string]any{
			{"role": "user", "content": "这张图里有什么？", "images": []string{url}},
		},
	})
	if resp.StatusCode != http.StatusOK || codeOf(env) != 0 {
		t.Fatalf("HTTP=%d env=%v", resp.StatusCode, env)
	}
	textCalls, visionCalls := s.text.snapshot(), s.vision.snapshot()
	if len(textCalls) != 1 {
		t.Fatalf("改写应调用文本模型 1 次，实际 %d 次", len(textCalls))
	}
	for i, m := range textCalls[0].Messages {
		if len(m.Images) != 0 {
			t.Fatalf("改写请求第 %d 条消息带了图片，文本模型会 502: %+v", i, m)
		}
	}
	if len(visionCalls) != 1 {
		t.Fatalf("回答应调用视觉模型 1 次，实际 %d 次", len(visionCalls))
	}
	if got, _ := dataOf(env)["model"].(string); got != "vision-model" {
		t.Fatalf("data.model = %q, want vision-model", got)
	}
}
