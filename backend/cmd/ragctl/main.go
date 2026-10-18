// Command ragctl 是配套命令行工具：入库、检索、问答、评测、迁移。
//
// 用法：
//
//	ragctl ingest -rebuild              # 全量重建索引
//	ragctl search -q "年假多少天"        # 只看检索结果（不花 token）
//	ragctl ask -q "年假多少天"           # 完整问答（会调用生成模型）
//	ragctl stats                        # 索引统计
//	ragctl eval -chat                   # 跑黄金集评测
//	ragctl migrate -from ../data        # 迁移旧 Python 版索引
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"

	"ragkb/internal/config"
	"ragkb/internal/ingest"
	"ragkb/internal/llm"
	"ragkb/internal/rag"
	"ragkb/internal/sites"
	"ragkb/internal/store"
	"ragkb/internal/store/registry"
)

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	var err error
	switch os.Args[1] {
	case "ingest":
		err = cmdIngest(os.Args[2:])
	case "search":
		err = cmdSearch(os.Args[2:])
	case "ask":
		err = cmdAsk(os.Args[2:])
	case "stats":
		err = cmdStats(os.Args[2:])
	case "eval":
		err = cmdEval(os.Args[2:])
	case "migrate":
		err = cmdMigrate(os.Args[2:])
	case "help", "-h", "--help":
		usage()
		return
	default:
		err = fmt.Errorf("未知子命令 %q", os.Args[1])
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "错误: "+err.Error())
		os.Exit(1)
	}
}

func usage() {
	fmt.Print(`ragctl — RAG 知识库命令行工具

子命令：
  ingest   把 docs/ 目录灌进索引      ragctl ingest [-config 路径] [-rebuild] [-paths a,b]
  search   只做检索，不调用生成模型   ragctl search -q "问题" [-k 5] [-dynasty 唐] [-abilities 三国,诗词]
  ask      完整问答（要配 API Key）  ragctl ask -q "问题" [-structured] [-k 5]
  stats    查看索引统计              ragctl stats
  eval     跑黄金集评测              ragctl eval [-golden eval/golden.yaml] [-chat]
  migrate  迁移旧 Python 版索引       ragctl migrate -from ../data [-docs ../docs]

公共参数：-config <路径>（默认 configs/config.yaml，不存在则用默认值 + 环境变量）
`)
}

// env 是 CLI 各子命令共用的依赖集合。
type env struct {
	cfg      *config.Config
	backend  store.Backend
	embedder llm.Embedder
	chatter  llm.Chatter
	vision   llm.Visioner
	sites    *sites.Store
	pipeline *ingest.Pipeline
	rag      *rag.Service
}

func setup(configPath string) (*env, error) {
	cfg, err := config.Load(configPath)
	if err != nil {
		return nil, err
	}
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	if err := cfg.EnsureDirs(); err != nil {
		return nil, err
	}
	// CLI 的日志走 stderr，stdout 留给结果，方便管道处理
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn})))

	backend, err := registry.Open(context.Background(), cfg)
	if err != nil {
		return nil, err
	}
	embedder, err := llm.NewEmbedder(cfg)
	if err != nil {
		return nil, err
	}
	chatter, err := llm.NewChatter(cfg)
	if err != nil {
		return nil, err
	}
	vision, err := llm.NewVisioner(cfg)
	if err != nil {
		return nil, err
	}
	siteStore, err := sites.Load(cfg.Storage.SitesFile)
	if err != nil {
		return nil, err
	}
	pipeline, err := ingest.NewPipeline(cfg, backend, embedder, vision)
	if err != nil {
		return nil, err
	}
	visionChat, err := llm.NewVisionChatter(cfg, chatter)
	if err != nil {
		return nil, err
	}
	tracer := rag.NewTracer(20, "")
	return &env{
		cfg: cfg, backend: backend, embedder: embedder, chatter: chatter,
		vision: vision, sites: siteStore, pipeline: pipeline,
		rag: rag.NewWithVision(cfg, backend, embedder, chatter, visionChat, siteStore, tracer),
	}, nil
}

func (e *env) Close() {
	if e.backend != nil {
		_ = e.backend.Close()
	}
}

// parseFlags 统一处理 -config 参数。
func parseFlags(fs *flag.FlagSet, args []string) (string, error) {
	cfgPath := fs.String("config", "", "配置文件路径")
	if err := fs.Parse(args); err != nil {
		return "", err
	}
	return *cfgPath, nil
}

// ctxOf 返回一个带超时的上下文。
func ctxOf() (context.Context, context.CancelFunc) {
	return context.WithCancel(context.Background())
}
