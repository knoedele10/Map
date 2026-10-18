package api_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// fakeOllama 模拟本地 Ollama 的嵌入接口。
//
// 向量由文本的字符直方图派生：同一段文本永远得到同一个向量，
// 不同文本之间也不完全一样——足以验证"入库→检索"的管道，而不需要真的模型。
type fakeOllama struct {
	*httptest.Server
	mu      sync.Mutex
	batches int
	texts   int
}

func newFakeOllama(t *testing.T, dim int) *fakeOllama {
	t.Helper()
	f := &fakeOllama{}
	mux := http.NewServeMux()
	mux.HandleFunc("/api/tags", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{"models": []map[string]string{{"name": "bge-m3:latest"}}})
	})
	mux.HandleFunc("/api/embed", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Model string   `json:"model"`
			Input []string `json:"input"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		f.mu.Lock()
		f.batches++
		f.texts += len(body.Input)
		f.mu.Unlock()

		vecs := make([][]float32, len(body.Input))
		for i, s := range body.Input {
			vecs[i] = fakeVector(s, dim)
		}
		writeJSON(w, map[string]any{"model": body.Model, "embeddings": vecs})
	})
	f.Server = httptest.NewServer(mux)
	t.Cleanup(f.Close)
	return f
}

// fakeVector 把文本映射成确定性的 1024 维单位向量。
func fakeVector(s string, dim int) []float32 {
	v := make([]float32, dim)
	for _, r := range s {
		v[int(r)%dim] += 1
	}
	var norm float64
	for _, x := range v {
		norm += float64(x) * float64(x)
	}
	if norm == 0 {
		v[0] = 1
		return v
	}
	norm = sqrt(norm)
	for i := range v {
		v[i] = float32(float64(v[i]) / norm)
	}
	return v
}

func sqrt(x float64) float64 {
	if x <= 0 {
		return 0
	}
	// 牛顿迭代，避免为一个函数引入 math 依赖以外的复杂度
	z := x
	for i := 0; i < 32; i++ {
		z = (z + x/z) / 2
	}
	return z
}

// fakeOpenAI 模拟 OpenAI 兼容的对话接口（流式 + 非流式 + /models）。
type fakeOpenAI struct {
	*httptest.Server
	mu       sync.Mutex
	requests int
	// answer 是固定回复，测试里断言它能被拆成多个 delta 事件。
	answer string
}

func newFakeOpenAI(t *testing.T, answer string) *fakeOpenAI {
	t.Helper()
	f := &fakeOpenAI{answer: answer}
	mux := http.NewServeMux()
	mux.HandleFunc("/models", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{"data": []map[string]string{{"id": "deepseek-chat"}}})
	})
	mux.HandleFunc("/chat/completions", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Stream bool `json:"stream"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		f.mu.Lock()
		f.requests++
		f.mu.Unlock()

		if !body.Stream {
			writeJSON(w, map[string]any{
				"choices": []map[string]any{{"message": map[string]string{"role": "assistant", "content": f.answer}}},
				"usage":   map[string]int{"prompt_tokens": 10, "completion_tokens": 5, "total_tokens": 15},
			})
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		flusher, _ := w.(http.Flusher)
		// 把答案拆成 3 个 chunk 推出去，模拟真实流式
		parts := splitEvery(f.answer, 3)
		for i, p := range parts {
			payload := map[string]any{
				"choices": []map[string]any{{"delta": map[string]string{"content": p}}},
			}
			if i == len(parts)-1 {
				payload["usage"] = map[string]int{"prompt_tokens": 10, "completion_tokens": 5, "total_tokens": 15}
			}
			raw, _ := json.Marshal(payload)
			fmt.Fprintf(w, "data: %s\n\n", raw)
			if flusher != nil {
				flusher.Flush()
			}
		}
		fmt.Fprint(w, "data: [DONE]\n\n")
		if flusher != nil {
			flusher.Flush()
		}
	})
	f.Server = httptest.NewServer(mux)
	t.Cleanup(f.Close)
	return f
}

func splitEvery(s string, n int) []string {
	r := []rune(s)
	if len(r) <= n {
		return []string{s}
	}
	size := (len(r) + n - 1) / n
	var out []string
	for i := 0; i < len(r); i += size {
		end := i + size
		if end > len(r) {
			end = len(r)
		}
		out = append(out, string(r[i:end]))
	}
	return out
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

// parseSSE 把 SSE 响应体拆成 (event, data) 序列。
func parseSSE(body string) []sseEvent {
	var out []sseEvent
	for _, block := range strings.Split(body, "\n\n") {
		block = strings.TrimSpace(block)
		if block == "" || strings.HasPrefix(block, ":") {
			continue
		}
		var ev sseEvent
		for _, line := range strings.Split(block, "\n") {
			switch {
			case strings.HasPrefix(line, "event: "):
				ev.Name = strings.TrimPrefix(line, "event: ")
			case strings.HasPrefix(line, "data: "):
				ev.Data = strings.TrimPrefix(line, "data: ")
			}
		}
		if ev.Name != "" {
			out = append(out, ev)
		}
	}
	return out
}

type sseEvent struct {
	Name string
	Data string
}

func (e sseEvent) JSON(t *testing.T) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(e.Data), &m); err != nil {
		t.Fatalf("事件 %s 的 data 不是合法 JSON: %v (%s)", e.Name, err, e.Data)
	}
	return m
}

func eventNames(evs []sseEvent) []string {
	out := make([]string, 0, len(evs))
	for _, e := range evs {
		out = append(out, e.Name)
	}
	return out
}
