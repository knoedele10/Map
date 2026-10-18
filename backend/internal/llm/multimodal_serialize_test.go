package llm_test

// 厂商在 internal/llm 里已经写好了多模态序列化，但从来没有被 HTTP 层调用过。
// 这个测试证明：一旦 llm.Message.Images 被填上，图片真的会以各家要求的形状
// 出现在发给模型的 HTTP 请求体里。全程进程内，不需要 Ollama。

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"ragkb/internal/config"
	"ragkb/internal/llm"
)

func drain(ch <-chan llm.Delta) {
	for range ch {
	}
}

func TestOllamaSerializesImagesAsBareBase64(t *testing.T) {
	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Errorf("请求体不是合法 JSON: %v", err)
		}
		w.Header().Set("Content-Type", "application/x-ndjson")
		fmt.Fprintln(w, `{"message":{"content":"ok"},"done":true}`)
	}))
	defer srv.Close()

	cfg := config.Default()
	cfg.Chat.Provider = "ollama"
	cfg.Chat.BaseURL = srv.URL
	cfg.Chat.Model = "qwen2.5vl:7b"
	chatter, err := llm.NewChatter(cfg)
	if err != nil {
		t.Fatalf("构造 Ollama chatter 失败: %v", err)
	}
	if chatter == nil {
		t.Fatal("Ollama chatter 不该为 nil")
	}

	stream, err := chatter.Stream(context.Background(), llm.ChatRequest{
		System: "sys",
		Messages: []llm.Message{{
			Role:    "user",
			Content: "这张图里有什么？",
			Images:  []string{"data:image/png;base64,QUJD"},
		}},
	})
	if err != nil {
		t.Fatalf("Stream 失败: %v", err)
	}
	drain(stream)

	if got["model"] != "qwen2.5vl:7b" {
		t.Fatalf("model = %v, want qwen2.5vl:7b", got["model"])
	}
	msgs, _ := got["messages"].([]any)
	if len(msgs) != 2 {
		t.Fatalf("messages 条数 = %d, want 2（system + user）: %v", len(msgs), got)
	}
	user, _ := msgs[1].(map[string]any)
	imgs, _ := user["images"].([]any)
	if len(imgs) != 1 {
		t.Fatalf("Ollama 请求体里 images = %v, want 长度 1 —— 图片没有传到供应商", user["images"])
	}
	// Ollama 要裸 base64：data URL 前缀必须被剥掉。
	if imgs[0] != "QUJD" {
		t.Fatalf("Ollama images[0] = %v, want \"QUJD\"（前缀未剥离）", imgs[0])
	}
}

func TestOllamaOmitsImagesWhenTextOnly(t *testing.T) {
	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&got)
		w.Header().Set("Content-Type", "application/x-ndjson")
		fmt.Fprintln(w, `{"message":{"content":"ok"},"done":true}`)
	}))
	defer srv.Close()

	cfg := config.Default()
	cfg.Chat.Provider = "ollama"
	cfg.Chat.BaseURL = srv.URL
	cfg.Chat.Model = "qwen2.5:7b"
	chatter, err := llm.NewChatter(cfg)
	if err != nil {
		t.Fatalf("构造 Ollama chatter 失败: %v", err)
	}
	stream, err := chatter.Stream(context.Background(), llm.ChatRequest{
		Messages: []llm.Message{{Role: "user", Content: "你好"}},
	})
	if err != nil {
		t.Fatalf("Stream 失败: %v", err)
	}
	drain(stream)

	msgs, _ := got["messages"].([]any)
	user, _ := msgs[0].(map[string]any)
	if _, present := user["images"]; present {
		t.Fatalf("纯文本请求不该带 images 字段: %v", user)
	}
	if user["content"] != "你好" {
		t.Fatalf("content = %v, want 你好", user["content"])
	}
}
