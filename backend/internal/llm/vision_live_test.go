package llm_test

// 真机图注检查：默认跳过，只有 RAGKB_LIVE_VISION=1 时才跑（需要本地 Ollama）。
//
// 它走的是 llm.NewVisioner -> ollamaVisioner.Caption —— 也就是入库生成图注用的**同一条**代码路径，
// 并且直接读 backend/run/config.local.yaml，因此能证明改完 vision.model 之后图注链路没坏。
// 它不写任何索引，所以不会污染共享的 backend/run/data。

import (
	"bytes"
	"context"
	"image"
	"image/color"
	"image/png"
	"os"
	"testing"
	"time"

	"ragkb/internal/config"
	"ragkb/internal/llm"
)

func TestLiveVisionCaption(t *testing.T) {
	if os.Getenv("RAGKB_LIVE_VISION") != "1" {
		t.Skip("设置 RAGKB_LIVE_VISION=1 才跑真机图注（需要本地 Ollama）")
	}
	path := os.Getenv("RAGKB_LIVE_CONFIG")
	if path == "" {
		t.Fatal("需要 RAGKB_LIVE_CONFIG 指向 config.local.yaml")
	}
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("加载配置失败: %v", err)
	}
	t.Logf("config vision: enabled=%v provider=%s model=%s", cfg.Vision.Enabled, cfg.Vision.Provider, cfg.Vision.Model)
	if !cfg.Vision.Enabled {
		t.Fatal("vision.enabled 必须为 true")
	}

	img := image.NewRGBA(image.Rect(0, 0, 96, 96))
	for x := 0; x < 96; x++ {
		for y := 0; y < 96; y++ {
			img.Set(x, y, color.RGBA{R: 220, G: 40, B: 40, A: 255})
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatalf("编码 PNG 失败: %v", err)
	}

	visioner, err := llm.NewVisioner(cfg)
	if err != nil {
		t.Fatalf("构造视觉模型失败: %v", err)
	}
	if visioner == nil {
		t.Fatal("visioner 为 nil")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 240*time.Second)
	defer cancel()
	start := time.Now()
	caption, err := visioner.Caption(ctx, buf.Bytes(), "image/png")
	if err != nil {
		t.Fatalf("真机图注失败（model=%s）: %v", visioner.Model(), err)
	}
	t.Logf("真机图注成功: model=%s elapsed=%s caption=%q", visioner.Model(), time.Since(start).Round(time.Millisecond), caption)
	if caption == "" {
		t.Fatal("图注为空")
	}
}
