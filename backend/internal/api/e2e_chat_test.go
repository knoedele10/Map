package api_test

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
)

// TestChatStreamEventOrder 验证 SSE 事件顺序与内容。
//
// 契约规定的事件序是：meta → sites? → sources → delta* → structured? → done，
// 顺序错了前端就会在引用还没到的时候先渲染正文，[n] 点击跳转会失效。
func TestChatStreamEventOrder(t *testing.T) {
	const answer = "工作满 1 年不满 3 年每年 5 天年假。[1]"
	st := newTestStack(t, answer)

	resp := st.postJSON(t, "/api/v1/chat", map[string]any{
		"question": "年假多少天", "stream": true, "top_k": 5,
	})
	defer resp.Body.Close()

	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/event-stream") {
		t.Fatalf("Content-Type 应为 text/event-stream，实际 %q", ct)
	}
	if v := resp.Header.Get("X-Accel-Buffering"); v != "no" {
		t.Errorf("必须设置 X-Accel-Buffering: no（否则 Nginx 会缓冲整段回答），实际 %q", v)
	}
	if resp.Header.Get("X-Request-Id") == "" {
		t.Error("响应缺少 X-Request-Id")
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("读取流失败: %v", err)
	}
	events := parseSSE(string(body))
	if len(events) < 4 {
		t.Fatalf("事件太少: %v", eventNames(events))
	}

	// 1) 首个事件必须是 meta
	if events[0].Name != "meta" {
		t.Fatalf("首个事件应为 meta，实际 %v", eventNames(events))
	}
	// 2) sources 必须出现在任何 delta 之前
	sourcesIdx, firstDelta := -1, -1
	for i, e := range events {
		if e.Name == "sources" && sourcesIdx < 0 {
			sourcesIdx = i
		}
		if e.Name == "delta" && firstDelta < 0 {
			firstDelta = i
		}
	}
	if sourcesIdx < 0 {
		t.Fatalf("没有 sources 事件: %v", eventNames(events))
	}
	if firstDelta < 0 {
		t.Fatalf("没有 delta 事件: %v", eventNames(events))
	}
	if sourcesIdx > firstDelta {
		t.Errorf("sources（第 %d 个）必须早于第一个 delta（第 %d 个）", sourcesIdx, firstDelta)
	}
	// 3) 最后必须是 done
	if last := events[len(events)-1]; last.Name != "done" {
		t.Errorf("最后一个事件应为 done，实际 %s（全部：%v）", last.Name, eventNames(events))
	}

	// 4) 引用要能对上：sources 里应有 handbook.md
	srcData := events[sourcesIdx].JSON(t)
	sources, _ := srcData["sources"].([]any)
	if len(sources) == 0 {
		t.Fatal("sources 为空，模型无法标注 [1]")
	}
	firstSrc, _ := sources[0].(map[string]any)
	chunk, _ := firstSrc["chunk"].(map[string]any)
	if src, _ := chunk["source"].(string); !strings.Contains(src, "handbook.md") {
		t.Errorf("首位引用应来自 handbook.md，实际 %q", src)
	}

	// 5) 把所有 delta 拼起来应等于模型的完整回复
	var got strings.Builder
	for _, e := range events {
		if e.Name != "delta" {
			continue
		}
		if content, ok := e.JSON(t)["content"].(string); ok {
			got.WriteString(content)
		}
	}
	if got.String() != answer {
		t.Errorf("流式拼接结果不对:\n got=%q\nwant=%q", got.String(), answer)
	}

	// 6) done 事件要带用量与耗时
	doneData := events[len(events)-1].JSON(t)
	if _, ok := doneData["answer"].(string); !ok {
		t.Errorf("done 事件应包含完整 answer，实际 %v", doneData)
	}
	if ttft, ok := doneData["ttft_ms"].(float64); !ok || ttft < 0 {
		t.Errorf("done 事件应包含 ttft_ms，实际 %v", doneData["ttft_ms"])
	}
	usage, _ := doneData["usage"].(map[string]any)
	if usage == nil {
		t.Error("done 事件应包含 usage（token 用量）")
	}
}

// TestChatNonStream 验证 stream=false 时返回一次性 JSON。
func TestChatNonStream(t *testing.T) {
	const answer = "根据资料，年假是 5 天。[1]"
	st := newTestStack(t, answer)
	streamFalse := false
	data := decodeEnvelope(t, st.postJSON(t, "/api/v1/chat", map[string]any{
		"question": "年假多少天", "stream": streamFalse, "top_k": 5,
	}))
	if got, _ := data["answer"].(string); got != answer {
		t.Errorf("answer 不对: got=%q want=%q", got, answer)
	}
	sources, _ := data["sources"].([]any)
	if len(sources) == 0 {
		t.Error("非流式响应也必须带 sources")
	}
	if _, ok := data["elapsed_ms"].(float64); !ok {
		t.Error("非流式响应应带 elapsed_ms")
	}
}

// TestChatStructuredSplitsHistoryAndInterpretation 验证史实/演绎分离。
func TestChatStructuredSplitsHistoryAndInterpretation(t *testing.T) {
	structured := `{"history":"唐代开凿的弥勒佛坐像，高 71 米。[1]","sources":["[1] leshan.json —— 可信度 high"],"interpretation":"民间传说其建造与镇水有关，属演绎。","confidence":"high","unknown":false}`
	st := newTestStack(t, structured)

	resp := st.postJSON(t, "/api/v1/chat", map[string]any{
		"question": "乐山大佛多高", "stream": true, "answer_format": "structured",
	})
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	events := parseSSE(string(body))

	idx := -1
	for i, e := range events {
		if e.Name == "structured" {
			idx = i
		}
	}
	if idx < 0 {
		t.Fatalf("structured 模式下必须推送 structured 事件: %v", eventNames(events))
	}
	data := events[idx].JSON(t)
	if h, _ := data["history"].(string); !strings.Contains(h, "71 米") {
		t.Errorf("history 应含史实数字，实际 %v", data["history"])
	}
	if v, _ := data["interpretation"].(string); !strings.Contains(v, "传说") {
		t.Errorf("interpretation 应含演绎说明，实际 %v", data["interpretation"])
	}
	if c, _ := data["confidence"].(string); c != "high" {
		t.Errorf("confidence 应为 high，实际 %v", data["confidence"])
	}
	// structured 事件必须出现在 done 之前
	if last := events[len(events)-1]; last.Name != "done" {
		t.Errorf("structured 之后应以 done 收尾，实际 %v", eventNames(events))
	}
}

// TestChatFallsBackWhenModelReturnsProse 验证模型不按格式返回时不崩、降级为 low。
func TestChatFallsBackWhenModelReturnsProse(t *testing.T) {
	st := newTestStack(t, "我直接说人话，不带 JSON。")
	resp := st.postJSON(t, "/api/v1/chat", map[string]any{
		"question": "年假多少天", "stream": true, "answer_format": "structured",
	})
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	events := parseSSE(string(body))

	for _, e := range events {
		if e.Name != "structured" {
			continue
		}
		data := e.JSON(t)
		if c, _ := data["confidence"].(string); c != "low" {
			t.Errorf("格式解析失败时应降级为 confidence=low，实际 %v", c)
		}
		if h, _ := data["history"].(string); !strings.Contains(h, "人话") {
			t.Errorf("降级时应把原文放进 history，实际 %v", data["history"])
		}
		return
	}
	t.Fatal("缺少 structured 事件")
}

// TestChatRejectsBadInput 验证参数校验返回契约错误码。
func TestChatRejectsBadInput(t *testing.T) {
	st := newTestStack(t, "ok")
	cases := []struct {
		name string
		body map[string]any
		code float64
	}{
		{"空问题", map[string]any{"question": "   "}, 40001},
		{"非法格式", map[string]any{"question": "年假", "answer_format": "xml"}, 40001},
		{"top_k 越界", map[string]any{"question": "年假", "top_k": 99}, 40001},
		{"temperature 越界", map[string]any{"question": "年假", "temperature": 9}, 40001},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resp := st.postJSON(t, "/api/v1/chat", tc.body)
			defer resp.Body.Close()
			if resp.StatusCode != http.StatusBadRequest {
				t.Errorf("HTTP 状态应为 400，实际 %d", resp.StatusCode)
			}
			var env map[string]any
			if err := json.NewDecoder(resp.Body).Decode(&env); err != nil {
				t.Fatalf("错误响应不是 JSON: %v", err)
			}
			if code, _ := env["code"].(float64); code != tc.code {
				t.Errorf("业务码应为 %v，实际 %v（message=%v）", tc.code, env["code"], env["message"])
			}
			if env["request_id"] == nil || env["request_id"] == "" {
				t.Error("错误响应必须带 request_id")
			}
		})
	}
}

// TestSitesAsReadonlyTool 验证景点数据既是接口也是模型上下文。
func TestSitesAsReadonlyTool(t *testing.T) {
	st := newTestStack(t, "乐山大佛高 71 米。[1]")
	data := decodeEnvelope(t, st.postJSON(t, "/api/v1/chat", map[string]any{
		"question": "乐山大佛有多高", "stream": false,
	}))
	sitesHit, _ := data["sites"].([]any)
	if len(sitesHit) == 0 {
		t.Fatal("问题提到乐山大佛时，应预取结构化景点数据")
	}
	first, _ := sitesHit[0].(map[string]any)
	if name, _ := first["name"].(string); name != "乐山大佛" {
		t.Errorf("预取的景点应为乐山大佛，实际 %v", name)
	}
	// 单独查接口
	resp, err := http.Get(st.server.URL + "/api/v1/sites/leshan")
	if err != nil {
		t.Fatalf("查询景点失败: %v", err)
	}
	site := decodeEnvelope(t, resp)
	facts, _ := site["facts"].(map[string]any)
	if facts == nil || facts["高度"] != "71 米" {
		t.Errorf("结构化字段应包含高度=71 米，实际 %v", site["facts"])
	}
}
