package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"ragkb/internal/ingest"
	"ragkb/internal/rag"
	"ragkb/internal/types"
)

// cmdIngest 执行入库。
func cmdIngest(args []string) error {
	fs := flag.NewFlagSet("ingest", flag.ExitOnError)
	rebuild := fs.Bool("rebuild", false, "清空索引后全量重建")
	paths := fs.String("paths", "", "只处理这些相对路径，逗号分隔")
	cfgPath, err := parseFlags(fs, args)
	if err != nil {
		return err
	}
	e, err := setup(cfgPath)
	if err != nil {
		return err
	}
	defer e.Close()

	e.pipeline.SetLogger(func(line string) { fmt.Println(line) })
	opts := ingest.RunOptions{Rebuild: *rebuild}
	if *paths != "" {
		opts.Paths = splitComma(*paths)
	}
	started := time.Now()
	stats, err := e.pipeline.Run(context.Background(), opts, nil)
	if err != nil {
		return err
	}
	fmt.Printf("\n处理 %d 个文件（跳过 %d，失败 %d），新增 %d 块，去重跳过 %d 块，图片 %d 张，用时 %.1fs\n",
		stats.FilesChanged, stats.FilesSkipped, stats.FilesFailed,
		stats.ChunksAdded, stats.ChunksDeduped, stats.Assets, time.Since(started).Seconds())
	return nil
}

// cmdSearch 只做检索，不花生成模型的 token。
func cmdSearch(args []string) error {
	fs := flag.NewFlagSet("search", flag.ExitOnError)
	q := fs.String("q", "", "查询语句（必填）")
	k := fs.Int("k", 0, "返回条数，默认取配置里的 top_k")
	dynasty := fs.String("dynasty", "", "按朝代过滤")
	place := fs.String("place", "", "按地点过滤")
	topic := fs.String("topic", "", "按主题过滤")
	abilities := fs.String("abilities", "", "按能力过滤，逗号分隔（三国/诗词/水利/文物）")
	asJSON := fs.Bool("json", false, "输出原始 JSON")
	cfgPath, err := parseFlags(fs, args)
	if err != nil {
		return err
	}
	if strings.TrimSpace(*q) == "" {
		return fmt.Errorf("必须提供 -q 查询语句")
	}
	e, err := setup(cfgPath)
	if err != nil {
		return err
	}
	defer e.Close()

	// Filter 的每个维度都是"多选或"语义，所以单值也要包成切片。
	filter := types.Filter{}
	if *dynasty != "" {
		filter.Dynasty = splitComma(*dynasty)
	}
	if *place != "" {
		filter.Place = splitComma(*place)
	}
	if *topic != "" {
		filter.Topic = splitComma(*topic)
	}
	if *abilities != "" {
		filter.Abilities = splitComma(*abilities)
	}

	started := time.Now()
	hits, err := e.rag.Retrieve(context.Background(), *q, rag.SearchOptions{TopK: *k, Filters: filter})
	if err != nil {
		return err
	}
	if *asJSON {
		out, _ := json.MarshalIndent(hits, "", "  ")
		fmt.Println(string(out))
		return nil
	}
	if len(hits) == 0 {
		fmt.Println("没有命中任何块。检查 docs 目录是否已入库（ragctl ingest）。")
		return nil
	}
	fmt.Printf("命中 %d 条，用时 %dms\n\n", len(hits), time.Since(started).Milliseconds())
	for i, h := range hits {
		fmt.Printf("%d. [%.4f] %s\n", i+1, h.Score, h.Chunk.Source)
		fmt.Printf("   位置=%s 模态=%s 融合来源=%s 稠密=%.4f 稀疏=%.4f\n",
			orDash(h.Chunk.Locator), h.Chunk.Modality, orDash(h.From), h.Dense, h.Sparse)
		if h.Chunk.Dynasty != "" || h.Chunk.Place != "" || h.Chunk.Topic != "" {
			fmt.Printf("   元数据: 朝代=%s 地点=%s 主题=%s 可信度=%s\n",
				orDash(h.Chunk.Dynasty), orDash(h.Chunk.Place), orDash(h.Chunk.Topic), orDash(h.Chunk.Credibility))
		}
		fmt.Printf("   %s\n\n", preview(h.Chunk.Text, 160))
	}
	return nil
}

// cmdAsk 走完整问答链路（要配 chat.api_key）。
func cmdAsk(args []string) error {
	fs := flag.NewFlagSet("ask", flag.ExitOnError)
	q := fs.String("q", "", "问题（必填）")
	k := fs.Int("k", 0, "检索条数")
	structured := fs.Bool("structured", false, "要求结构化输出（史实/来源/演绎三段）")
	showSources := fs.Bool("sources", true, "是否打印引用来源")
	history := fs.String("history", "", "多轮历史，格式 user:问题|assistant:回答（用于测指代改写）")
	rewrite := fs.Bool("rewrite", false, "开启查询改写（带指代的追问需要它）")
	abilities := fs.String("abilities", "", "只检索这些能力，逗号分隔（对应蓉宝已解锁的碎片）")
	cfgPath, err := parseFlags(fs, args)
	if err != nil {
		return err
	}
	if strings.TrimSpace(*q) == "" {
		return fmt.Errorf("必须提供 -q 问题")
	}
	e, err := setup(cfgPath)
	if err != nil {
		return err
	}
	defer e.Close()
	if e.chatter == nil {
		return fmt.Errorf("对话模型未配置：填 chat.api_key 或设置 RAG_CHAT_API_KEY")
	}

	format := "text"
	if *structured {
		format = "structured"
	}
	msgs, err := parseHistory(*history)
	if err != nil {
		return err
	}
	if *rewrite && len(msgs) == 0 {
		return fmt.Errorf("-rewrite 需要同时提供 -history")
	}
	filter := types.Filter{}
	if *abilities != "" {
		filter.Abilities = splitComma(*abilities)
	}
	result, err := e.rag.Answer(context.Background(), rag.AnswerRequest{
		Question: *q, History: msgs, TopK: *k, AnswerFormat: format,
		Filters: filter, UseTools: true, Rewrite: *rewrite,
	}, func(ev rag.Event) error {
		if ev.Type == rag.EventDelta {
			if m, ok := ev.Data.(map[string]any); ok {
				if s, ok := m["content"].(string); ok {
					fmt.Print(s)
				}
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	fmt.Println()
	if *showSources {
		fmt.Println("\n--- 引用来源 ---")
		for i, h := range result.Sources {
			fmt.Printf("[%d] %s %s (score=%.4f, from=%s)\n", i+1, h.Chunk.Source, h.Chunk.Locator, h.Score, h.From)
		}
	}
	if result.Usage != nil {
		fmt.Printf("\n--- 用量 ---\ntokens: prompt=%d completion=%d total=%d\n",
			result.Usage.PromptTokens, result.Usage.CompletionTokens, result.Usage.TotalTokens)
	}
	if result.RewrittenQuestion != "" && result.RewrittenQuestion != *q {
		fmt.Printf("改写后的问题: %s\n", result.RewrittenQuestion)
	}
	fmt.Printf("耗时 %dms（首字 %dms），模型 %s\n", result.ElapsedMS, result.TTFTMS, result.Model)
	return nil
}

// parseHistory 解析 -history 参数："user:问题|assistant:回答"。
//
// 存在的意义是让多轮指代改写（"那杜鲁门主义呢？"）能在命令行里被测到，
// 否则只能起服务打 HTTP。
func parseHistory(s string) ([]types.ChatMessage, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil, nil
	}
	var out []types.ChatMessage
	for _, part := range strings.Split(s, "|") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		i := strings.Index(part, ":")
		if i <= 0 {
			return nil, fmt.Errorf("-history 片段 %q 缺少「角色:」前缀", part)
		}
		role := strings.ToLower(strings.TrimSpace(part[:i]))
		if role != "user" && role != "assistant" {
			return nil, fmt.Errorf("-history 角色只能是 user 或 assistant，得到 %q", role)
		}
		content := strings.TrimSpace(part[i+1:])
		if content == "" {
			return nil, fmt.Errorf("-history 片段 %q 的内容为空", part)
		}
		out = append(out, types.ChatMessage{Role: role, Content: content})
	}
	return out, nil
}

// cmdStats 打印索引统计。
func cmdStats(args []string) error {
	fs := flag.NewFlagSet("stats", flag.ExitOnError)
	cfgPath, err := parseFlags(fs, args)
	if err != nil {
		return err
	}
	e, err := setup(cfgPath)
	if err != nil {
		return err
	}
	defer e.Close()

	st, err := e.backend.Stats(context.Background())
	if err != nil {
		return err
	}
	out, _ := json.MarshalIndent(st, "", "  ")
	fmt.Println(string(out))
	fmt.Fprintf(os.Stderr, "\n数据目录: %s\ndocs 目录: %s\n", e.cfg.Storage.DataDir, e.cfg.Storage.DocsDir)
	return nil
}

func splitComma(s string) []string {
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

func preview(s string, n int) string {
	s = strings.ReplaceAll(s, "\n", " ")
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}
