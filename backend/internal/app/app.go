// Package app 装配并运行 HTTP 服务。
//
// 抽出来是为了让 cmd/server（开发用）和 cmd/shuji（自包含发行版）共用同一套启动流程，
// 避免发行版和开发版行为漂移。
package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"sync/atomic"
	"syscall"
	"time"

	"ragkb/internal/api"
	"ragkb/internal/config"
	"ragkb/internal/ingest"
	"ragkb/internal/jobs"
	"ragkb/internal/llm"
	"ragkb/internal/rag"
	"ragkb/internal/retrieval"
	"ragkb/internal/sites"
	"ragkb/internal/store/registry"
)

// Options 控制启动行为。
type Options struct {
	// ConfigPath 为空时用 configs/config.yaml（相对当前工作目录）。
	ConfigPath string
	// Ingest 为真时，启动服务前先跑一次入库。
	Ingest bool
	// Rebuild 配合 Ingest：清空索引后全量重建。
	Rebuild bool
	// ServerAddr 非空时覆盖配置文件中的监听地址，供桌面发行版使用独立端口。
	ServerAddr string
	// Stop 在桌面窗口关闭时收到信号，让服务端与窗口一起退出。
	Stop <-chan struct{}
	// DesktopHeartbeat 根据网页心跳判断独立游戏窗口是否仍然存在。
	DesktopHeartbeat bool
	// ControlMarker is written only when the process is supervised by start.ps1.
	ControlMarker string
}

// Run 加载配置、装配依赖、启动 HTTP 服务，并阻塞直到收到退出信号。
func Run(opts Options) error {
	cfg, err := config.Load(opts.ConfigPath)
	if err != nil {
		return err
	}
	if cfg.Vision.SharedChat {
		cfg.Vision.Enabled = true
		cfg.Vision.Provider = cfg.Chat.Provider
		cfg.Vision.BaseURL = cfg.Chat.BaseURL
		cfg.Vision.APIKey = cfg.Chat.APIKey
		cfg.Vision.Model = cfg.Chat.Model
	}
	if err := cfg.Validate(); err != nil {
		return err
	}
	if opts.ServerAddr != "" {
		cfg.Server.Addr = opts.ServerAddr
	}
	if err := cfg.EnsureDirs(); err != nil {
		return err
	}

	logger := newLogger(cfg.Log.Level)
	slog.SetDefault(logger)

	logger.Info("配置已加载",
		"config", cfg.Path,
		"backend", cfg.Storage.Backend,
		"docs_dir", cfg.Storage.DocsDir,
		"embedding", cfg.Embedding.Provider+"/"+cfg.Embedding.Model,
		"chat", cfg.Chat.Provider+"/"+cfg.Chat.Model,
		"chat_configured", cfg.ChatConfigured(),
	)

	ctx := context.Background()

	// ---- 存储后端 -------------------------------------------------------
	backend, err := registry.Open(ctx, cfg)
	if err != nil {
		return err
	}
	defer backend.Close()

	// ---- 模型：检索走本地，生成走 API Key --------------------------------
	embedder, err := llm.NewEmbedder(cfg)
	if err != nil {
		return fmt.Errorf("初始化嵌入模型: %w", err)
	}
	chatter, err := llm.NewChatter(cfg)
	if err != nil {
		return fmt.Errorf("初始化对话模型: %w", err)
	}
	visionChatter, err := llm.NewVisionChatter(cfg, chatter)
	if err != nil {
		return fmt.Errorf("初始化视觉对话模型: %w", err)
	}
	vision, err := llm.NewVisioner(cfg)
	if err != nil {
		return fmt.Errorf("初始化视觉模型: %w", err)
	}
	if chatter == nil {
		logger.Warn("对话模型未配置，/api/v1/chat 会返回 50300；检索接口仍可用",
			"hint", "在配置里填 chat.api_key，或设置环境变量 RAG_CHAT_API_KEY")
	}
	if vision == nil {
		logger.Info("视觉模型未启用，图片只能按文件名检索", "hint", "设置 vision.enabled=true 可生成图注")
	}

	// ---- 景点数据（兼作模型的只读工具） ---------------------------------
	siteStore, err := sites.Load(cfg.Storage.SitesFile)
	if err != nil {
		return err
	}
	logger.Info("景点数据已加载", "file", cfg.Storage.SitesFile, "count", siteStore.Len())

	// ---- 入库流水线 -----------------------------------------------------
	pipeline, err := ingest.NewPipeline(cfg, backend, embedder, vision)
	if err != nil {
		return err
	}
	jobManager := jobs.NewManager(20)
	controlRequested := make(chan string, 1)
	var onRestart func() error
	var onStop func() error
	if opts.ControlMarker != "" {
		requestControl := func(action string) error {
			if err := os.MkdirAll(filepath.Dir(opts.ControlMarker), 0o755); err != nil {
				return err
			}
			if err := os.WriteFile(opts.ControlMarker, []byte(action+" "+time.Now().Format(time.RFC3339)), 0o600); err != nil {
				return err
			}
			time.AfterFunc(750*time.Millisecond, func() { controlRequested <- action })
			return nil
		}
		onRestart = func() error { return requestControl("restart") }
		onStop = func() error { return requestControl("stop") }
	}

	// ---- 问答服务 -------------------------------------------------------
	tracer := rag.NewTracer(100, filepath.Join(cfg.Storage.DataDir, "traces.jsonl"))
	defer tracer.Close()
	ragService := rag.NewWithVision(cfg, backend, embedder, chatter, visionChatter, siteStore, tracer)

	// ---- 照片识别的检索快速通道（可选） ---------------------------------
	// 挂上它之后，/recognize 与对话页的带图提问会先问本机 PP-ShiTuV2（约 100 ms、纯 CPU），
	// 相似度达标就直接返回；服务没起或分数不够就自动回落视觉大模型（见 docs/PADDLECLAS.md）。
	if cfg.Recognize.RetrievalURL != "" {
		retrievalClient := retrieval.New(cfg.Recognize.RetrievalURL, cfg.Recognize.RetrievalMinScore, cfg.Recognize.RetrievalTimeoutS)
		ragService.SetRetrieval(retrievalClient)
		logger.Info("照片识别快速通道已启用",
			"url", cfg.Recognize.RetrievalURL,
			"min_score", cfg.Recognize.RetrievalMinScore,
			"timeout_s", cfg.Recognize.RetrievalTimeoutS)
	} else {
		logger.Info("照片识别快速通道未启用（recognize.retrieval_url 为空），识别一律走视觉大模型")
	}

	// ---- 可选：启动前先入库 ---------------------------------------------
	if opts.Ingest {
		logger.Info("开始预入库", "rebuild", opts.Rebuild)
		pipeline.SetLogger(func(line string) { logger.Info(line) })
		stats, err := pipeline.Run(ctx, ingest.RunOptions{Rebuild: opts.Rebuild}, func(p ingest.Progress) {
			if p.Total > 0 && p.Current%10 == 0 {
				logger.Info("入库进度", "stage", p.Stage, "current", p.Current, "total", p.Total, "message", p.Message)
			}
		})
		if err != nil {
			return fmt.Errorf("预入库失败: %w", err)
		}
		logger.Info("预入库完成", "files_changed", stats.FilesChanged, "files_skipped", stats.FilesSkipped,
			"chunks_added", stats.ChunksAdded, "chunks_deduped", stats.ChunksDeduped,
			"assets", stats.Assets, "elapsed_ms", stats.ElapsedMS)
	}

	// ---- 桌面窗口心跳 ---------------------------------------------------
	var lastAlive atomic.Int64
	lastAlive.Store(time.Now().UnixNano())
	onAlive := func() { lastAlive.Store(time.Now().UnixNano()) }
	desktopStop := make(chan struct{})
	if opts.DesktopHeartbeat {
		go func() {
			ticker := time.NewTicker(5 * time.Second)
			defer ticker.Stop()
			for range ticker.C {
				last := time.Unix(0, lastAlive.Load())
				if time.Since(last) > 50*time.Second {
					close(desktopStop)
					return
				}
			}
		}()
	}

	// ---- HTTP -----------------------------------------------------------
	srv := api.New(api.Deps{
		Cfg:           cfg,
		Backend:       backend,
		Embedder:      embedder,
		Chatter:       chatter,
		VisionChatter: visionChatter,
		Vision:        vision,
		Rag:           ragService,
		Jobs:          jobManager,
		Sites:         siteStore,
		Pipeline:      pipeline,
		Logger:        logger,
		OpenAPIPath:   cfg.Server.OpenAPIFile, // 留空则回落到 CWD / 可执行文件同级
		OnAlive:       onAlive,
		OnRestart:     onRestart,
		OnStop:        onStop,
	})

	httpServer := &http.Server{
		Addr:              cfg.Server.Addr,
		Handler:           srv.Router(),
		ReadHeaderTimeout: 10 * time.Second,
		// 注意：不能设 WriteTimeout——SSE 长连接会被它掐断。
		IdleTimeout: 120 * time.Second,
	}

	errCh := make(chan error, 1)
	go func() {
		logger.Info("服务已启动", "addr", "http://"+cfg.Server.Addr, "demo", "http://"+cfg.Server.Addr+"/", "openapi", "http://"+cfg.Server.Addr+"/openapi.yaml")
		if err := httpServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)

	select {
	case err := <-errCh:
		return err
	case sig := <-stop:
		logger.Info("收到退出信号，正在优雅关闭", "signal", sig.String())
	case <-opts.Stop:
		logger.Info("游戏窗口已关闭，正在停止本地服务")
	case action := <-controlRequested:
		logger.Info("收到网页服务控制请求，正在优雅关闭", "action", action)
	case <-desktopStop:
		logger.Info("游戏窗口心跳已停止，正在关闭本地服务")
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := httpServer.Shutdown(shutdownCtx); err != nil {
		logger.Warn("优雅关闭超时", "error", err)
	}
	if err := jobManager.Shutdown(shutdownCtx); err != nil {
		logger.Warn("等待入库任务退出超时", "error", err)
	}
	logger.Info("已退出")
	return nil
}

func newLogger(level string) *slog.Logger {
	var lv slog.Level
	switch level {
	case "debug":
		lv = slog.LevelDebug
	case "warn":
		lv = slog.LevelWarn
	case "error":
		lv = slog.LevelError
	default:
		lv = slog.LevelInfo
	}
	return slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: lv}))
}
