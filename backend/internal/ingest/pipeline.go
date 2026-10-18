package ingest

import (
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"gopkg.in/yaml.v3"

	"ragkb/internal/config"
	"ragkb/internal/ingest/loaders"
	"ragkb/internal/llm"
	"ragkb/internal/store"
	"ragkb/internal/types"
)

// Progress 是一次进度回调。
type Progress struct {
	Stage   string
	Current int
	Total   int
	Message string
}

// Stats 是一次入库的结果统计。
type Stats struct {
	FilesTotal    int `json:"files_total"`
	FilesChanged  int `json:"files_changed"`
	FilesSkipped  int `json:"files_skipped"`
	FilesFailed   int `json:"files_failed"`
	ChunksAdded   int `json:"chunks_added"`
	ChunksRemoved int `json:"chunks_removed"`
	ChunksDeduped int `json:"chunks_deduped"`
	Assets        int `json:"assets"`
	ElapsedMS     int `json:"elapsed_ms"`
}

// RunOptions 控制一次入库。
type RunOptions struct {
	// Rebuild 为 true 时先清空索引再全量重建（改了切块参数后用）。
	Rebuild bool
	// Paths 非空时只处理这些相对路径（文件或目录）。
	Paths []string
	// Meta 是调用方（例如上传接口）指定的元数据，优先级高于文件里的标注。
	Meta map[string]string
}

// Pipeline 把 docs 目录变成可检索的索引。
//
// 流水线：扫描 → 解析（多模态）→ 切块 → 图注 → 嵌入 → 入库 → 记账。
type Pipeline struct {
	cfg       *config.Config
	backend   store.Backend
	embedder  llm.Embedder
	vision    llm.Visioner
	manifest  *Manifest
	chunkOpts ChunkOptions
	logger    func(string)
	// mu 保护 manifest：入库任务在后台 goroutine 跑，同时 API 可能删除文档。
	mu       sync.Mutex
	loggerMu sync.RWMutex
}

// NewPipeline 构造入库流水线。vision 可以为 nil（表示不生成图注）。
func NewPipeline(cfg *config.Config, backend store.Backend, embedder llm.Embedder, vision llm.Visioner) (*Pipeline, error) {
	if backend == nil {
		return nil, fmt.Errorf("ingest: 存储后端不能为空")
	}
	if embedder == nil {
		return nil, fmt.Errorf("ingest: 嵌入器不能为空")
	}
	manifestPath := filepath.Join(cfg.Storage.DataDir, "manifest.json")
	m, err := LoadManifest(manifestPath)
	if err != nil {
		return nil, err
	}
	return &Pipeline{
		cfg:      cfg,
		backend:  backend,
		embedder: embedder,
		vision:   vision,
		manifest: m,
		chunkOpts: ChunkOptions{
			Size:      cfg.Chunking.Size,
			Overlap:   cfg.Chunking.Overlap,
			MinChars:  cfg.Chunking.MinChars,
			MaxPerDoc: cfg.Chunking.MaxPerDoc,
		},
		logger: func(string) {},
	}, nil
}

// SetLogger 注入日志函数（CLI 打印到控制台，服务端写进任务日志）。
func (p *Pipeline) SetLogger(f func(string)) {
	if f != nil {
		p.loggerMu.Lock()
		defer p.loggerMu.Unlock()
		p.logger = f
	}
}

func (p *Pipeline) logf(line string) {
	p.loggerMu.RLock()
	f := p.logger
	p.loggerMu.RUnlock()
	f(line)
}

// ManifestPath 返回台账文件位置。
func (p *Pipeline) ManifestPath() string {
	return filepath.Join(p.cfg.Storage.DataDir, "manifest.json")
}

// Forget 从台账里移除某个文档的记录。
//
// 为什么必须做：增量更新靠 fingerprint 判断"这个文件处理过没有"。
// 如果 API 删了文档却留下台账记录，下次入库会以为它没变而跳过，文档就永远回不来。
func (p *Pipeline) Forget(docID string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	changed := false
	for rel, entry := range p.manifest.Files {
		if entry.DocID == docID {
			delete(p.manifest.Files, rel)
			changed = true
		}
	}
	if !changed {
		return nil
	}
	return p.manifest.Save(p.ManifestPath())
}

// Files 列出 docs 目录下所有受支持的文件（相对路径，正斜杠分隔）。
func (p *Pipeline) Files() ([]string, error) {
	root := p.cfg.Storage.DocsDir
	var out []string
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			if os.IsNotExist(err) {
				return nil
			}
			return err
		}
		if d.IsDir() {
			name := d.Name()
			// 跳过隐藏目录和常见的临时/版本控制目录
			if path != root && (strings.HasPrefix(name, ".") || name == "node_modules" || name == "__pycache__") {
				return fs.SkipDir
			}
			return nil
		}
		name := d.Name()
		if strings.HasPrefix(name, "~$") || strings.HasPrefix(name, ".") {
			return nil // Office 临时文件 / 隐藏文件
		}
		// Directory and per-file metadata are consumed by collectMeta, not
		// user documents. Indexing them as text duplicates classification data.
		if name == "_meta.json" || strings.HasSuffix(name, ".meta.json") {
			return nil
		}
		if _, ok := loaders.For(name); !ok {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		out = append(out, filepath.ToSlash(rel))
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("扫描 %s: %w", root, err)
	}
	sort.Strings(out)
	return out, nil
}

// Run 执行一次入库。report 可以为 nil。
func (p *Pipeline) Run(ctx context.Context, opts RunOptions, report func(Progress)) (*Stats, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	started := time.Now()
	if report == nil {
		report = func(Progress) {}
	}
	stats := &Stats{}

	if opts.Rebuild {
		report(Progress{Stage: "reset", Message: "清空索引，准备全量重建"})
		if err := p.reset(ctx); err != nil {
			return stats, err
		}
		p.logf("已清空索引，开始全量重建")
	}

	all, err := p.Files()
	if err != nil {
		return stats, err
	}
	files := filterPaths(all, opts.Paths)
	stats.FilesTotal = len(files)

	// 先筛掉没改动的文件，剩下的才进入解析/嵌入（这一步最省时间）。
	type pending struct {
		rel string
		fp  string
	}
	var todo []pending
	present := map[string]bool{}
	// Paths selects work; it does not redefine which files exist on disk.
	for _, rel := range all {
		present[rel] = true
	}
	for _, rel := range files {
		abs := filepath.Join(p.cfg.Storage.DocsDir, filepath.FromSlash(rel))
		data, err := os.ReadFile(abs)
		if err != nil {
			p.logf("  ⚠️ 读取失败，跳过 " + rel + ": " + err.Error())
			stats.FilesFailed++
			continue
		}
		metaRaw, _ := json.Marshal(p.collectMeta(rel, data))
		fp := FileFingerprint(append(append([]byte{}, data...), metaRaw...))
		if !opts.Rebuild {
			if entry, ok := p.manifest.Files[rel]; ok && entry.Fingerprint == fp && len(opts.Meta) == 0 {
				stats.FilesSkipped++
				continue
			}
		}
		todo = append(todo, pending{rel: rel, fp: fp})
	}

	// 清理已经从 docs 目录消失的文件。
	for rel := range p.manifest.Files {
		if !present[rel] {
			if entry := p.manifest.Files[rel]; entry.DocID != "" {
				if err := p.backend.DeleteDoc(ctx, entry.DocID); err != nil {
					return stats, fmt.Errorf("删除已移除的文档 %s: %w", rel, err)
				}
				stats.ChunksRemoved += entry.Chunks
				os.RemoveAll(p.docAssetDir(entry.DocID))
			}
			delete(p.manifest.Files, rel)
			p.logf("  🗑️  已移除: " + rel)
		}
	}

	if len(todo) == 0 {
		p.logf(fmt.Sprintf("没有新增或改动的文件（跳过 %d 个），索引共 %d 块", stats.FilesSkipped, p.chunkCount(ctx)))
		stats.ElapsedMS = int(time.Since(started).Milliseconds())
		if err := p.persist(); err != nil {
			return stats, err
		}
		if err := ctx.Err(); err != nil {
			return stats, err
		}
		if stats.FilesFailed > 0 {
			return stats, fmt.Errorf("%d 个文件读取失败", stats.FilesFailed)
		}
		return stats, nil
	}

	// 文件级并发：解析、切块、图注都是 IO/网络密集型，并行能显著缩短首次入库时间。
	workers := p.cfg.Ingest.Workers
	if workers <= 0 {
		workers = 4
	}
	if workers > len(todo) {
		workers = len(todo)
	}

	type outcome struct {
		res *fileResult
		err error
		rel string
	}
	jobs := make(chan pending)
	results := make(chan outcome)
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for job := range jobs {
				if ctx.Err() != nil {
					return
				}
				p.logf("正在解析并向量化：" + job.rel)
				res, err := p.processFile(ctx, job.rel, job.fp, opts.Meta)
				select {
				case results <- outcome{res: res, err: err, rel: job.rel}:
				case <-ctx.Done():
					return
				}
			}
		}()
	}
	go func() {
		defer close(jobs)
		for _, job := range todo {
			select {
			case jobs <- job:
			case <-ctx.Done():
				return
			}
		}
	}()
	go func() { wg.Wait(); close(results) }()

	done := 0
	for out := range results {
		done++
		if out.err != nil {
			stats.FilesFailed++
			p.logf(fmt.Sprintf("  ❌ %s: %v", out.rel, out.err))
			report(Progress{Stage: "ingest", Current: done, Total: len(todo), Message: out.rel + " 失败"})
			continue
		}
		res := out.res
		replacer, ok := p.backend.(store.DocumentReplacer)
		if !ok {
			return stats, fmt.Errorf("存储后端不支持原子文档更新")
		}
		n, err := replacer.ReplaceDocument(ctx, res.doc, res.chunks, res.vectors)
		if err != nil {
			stats.FilesFailed++
			p.logf(fmt.Sprintf("  ❌ %s 写入索引失败: %v", res.rel, err))
			continue
		}
		stats.ChunksRemoved += n
		p.manifest.Files[res.rel] = FileEntry{
			Fingerprint: res.fingerprint,
			DocID:       res.doc.ID,
			Chunks:      len(res.chunks),
			Size:        res.doc.Size,
			Assets:      res.assets,
			Meta:        res.meta,
			UpdatedAt:   time.Now().UTC(),
		}
		stats.FilesChanged++
		stats.ChunksAdded += len(res.chunks)
		stats.ChunksDeduped += res.deduped
		stats.Assets += len(res.assets)
		p.logf(fmt.Sprintf("  📄 %s → %d 块（去重跳过 %d）", res.rel, len(res.chunks), res.deduped))
		report(Progress{Stage: "ingest", Current: done, Total: len(todo), Message: res.rel})
	}

	if err := p.persist(); err != nil {
		return stats, err
	}
	stats.ElapsedMS = int(time.Since(started).Milliseconds())
	if ctx.Err() != nil {
		return stats, ctx.Err()
	}
	if stats.FilesFailed > 0 {
		return stats, fmt.Errorf("%d 个文件入库失败，请查看任务日志", stats.FilesFailed)
	}
	p.logf(fmt.Sprintf("✅ 入库完成：处理 %d 个文件，跳过 %d 个，索引共 %d 块，用时 %.1fs",
		stats.FilesChanged, stats.FilesSkipped, p.chunkCount(ctx), time.Since(started).Seconds()))
	return stats, nil
}

type fileResult struct {
	rel         string
	fingerprint string
	meta        map[string]string
	doc         types.Doc
	chunks      []types.Chunk
	vectors     [][]float32
	assets      []string
	deduped     int
}

// processFile 处理单个文件：解析 → 切块 → 图注 → 嵌入。
// 注意这里**不写索引**，写索引由 Run 在主 goroutine 串行完成，避免并发写竞争。
func (p *Pipeline) processFile(ctx context.Context, rel, fingerprint string, override map[string]string) (*fileResult, error) {
	abs := filepath.Join(p.cfg.Storage.DocsDir, filepath.FromSlash(rel))
	data, err := os.ReadFile(abs)
	if err != nil {
		return nil, fmt.Errorf("读取文件: %w", err)
	}
	if maxBytes := p.cfg.Ingest.MaxFileMB << 20; maxBytes > 0 && int64(len(data)) > maxBytes {
		return nil, fmt.Errorf("文件 %.1f MB 超过上限 %d MB（改 ingest.max_file_mb）",
			float64(len(data))/1024/1024, p.cfg.Ingest.MaxFileMB)
	}

	loader, ok := loaders.For(rel)
	if !ok {
		return nil, fmt.Errorf("不支持的文件类型 %s", filepath.Ext(rel))
	}
	res, loadErr := loader.Load(rel, data)
	if loadErr != nil && (res == nil || len(res.Parts) == 0) {
		return nil, fmt.Errorf("解析失败: %w", loadErr)
	}
	if loadErr != nil {
		// PDF 这类解析器可能给出"部分结果 + 错误"，此时保留可用内容并记录告警。
		p.logf(fmt.Sprintf("  ⚠️ %s 解析有告警: %v", rel, loadErr))
	}

	meta := p.collectMeta(rel, data)
	for k, v := range override {
		if v != "" {
			meta[k] = v
		}
	}

	docID := StableID("doc", rel)
	doc := types.Doc{
		ID:          docID,
		Title:       meta["title"],
		Source:      rel,
		Size:        int64(len(data)),
		Status:      "indexed",
		Fingerprint: fingerprint,
		UpdatedAt:   time.Now().UTC(),
	}
	applyMeta(&doc.Dynasty, &doc.Place, &doc.Topic, &doc.SourceType, &doc.Credibility, &doc.Abilities, &doc.Tags, meta)

	// 重新入库前先清掉旧资产，避免残留孤儿图片。
	assetDir := p.docAssetDir(docID)
	if err := os.RemoveAll(assetDir); err != nil {
		p.logf(fmt.Sprintf("  ⚠️ 清理旧资产目录失败: %v", err))
	}

	var chunks []types.Chunk
	var assets []string
	imageIndex := 0

	for _, part := range res.Parts {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		switch part.Kind {
		case "image":
			if part.Asset == nil {
				continue
			}
			imageIndex++
			name := part.Asset.Name
			if name == "" {
				name = fmt.Sprintf("img_%03d", imageIndex)
			}
			if err := p.saveAsset(assetDir, name, part.Asset.Data); err != nil {
				p.logf(fmt.Sprintf("  ⚠️ 保存图片 %s 失败: %v", name, err))
				continue
			}
			assets = append(assets, name)
			caption := p.caption(ctx, part.Asset, rel)
			text := fmt.Sprintf("[图片] %s（来自 %s %s）\n%s", name, rel, part.Locator, caption)
			chunks = append(chunks, types.Chunk{
				ID:       StableID(docID, "img", name),
				DocID:    docID,
				Source:   rel,
				Index:    len(chunks),
				Text:     text,
				Modality: types.ModalityImage,
				AssetURL: "/api/v1/assets/" + docID + "/" + name,
				Locator:  part.Locator,
				Meta:     part.Asset.Meta,
			})

		case "table":
			for _, piece := range ChunkTable(part.Text, p.chunkOpts) {
				chunks = append(chunks, types.Chunk{
					ID:       StableID(docID, "t", fmt.Sprint(len(chunks))),
					DocID:    docID,
					Source:   rel,
					Index:    len(chunks),
					Text:     piece,
					Modality: types.ModalityTable,
					Locator:  part.Locator,
					Meta:     pageMeta(part.Page),
				})
			}

		default:
			for _, piece := range ChunkText(part.Text, p.chunkOpts) {
				chunks = append(chunks, types.Chunk{
					ID:       StableID(docID, "c", fmt.Sprint(len(chunks))),
					DocID:    docID,
					Source:   rel,
					Index:    len(chunks),
					Text:     piece,
					Modality: types.ModalityText,
					Locator:  part.Locator,
					Meta:     pageMeta(part.Page),
				})
			}
		}
	}

	if len(chunks) == 0 {
		doc.Status = "failed"
		doc.Error = "没有解析出任何可检索内容"
		return &fileResult{rel: rel, fingerprint: fingerprint, meta: meta, doc: doc}, nil
	}

	// 统一补元数据 + 内容 hash
	for i := range chunks {
		c := &chunks[i]
		applyMeta(&c.Dynasty, &c.Place, &c.Topic, &c.SourceType, &c.Credibility, &c.Abilities, &c.Tags, meta)
		// Identical text in different documents keeps its own metadata and citations.
		c.Hash = StableID(docID, c.ID, c.Text)
		c.CreatedAt = time.Now().UTC()
	}

	// Unchanged files are skipped by manifest. Changed documents must retain all
	// their chunks until the replacement commits; never deduplicate against old rows.
	var pending []int
	for i := range chunks {
		pending = append(pending, i)
	}
	deduped := len(chunks) - len(pending)

	var vectors [][]float32
	if len(pending) > 0 {
		texts := make([]string, len(pending))
		for i, idx := range pending {
			texts[i] = chunks[idx].Text
		}
		vecs, err := p.embedder.Embed(ctx, texts)
		if err != nil {
			return nil, fmt.Errorf("向量化失败: %w", err)
		}
		vectors = vecs
		kept := make([]types.Chunk, len(pending))
		for i, idx := range pending {
			kept[i] = chunks[idx]
		}
		chunks = kept
	}

	doc.Chunks = len(chunks)
	doc.Assets = assets
	doc.Modality = dominantModality(chunks)
	return &fileResult{
		rel:         rel,
		fingerprint: fingerprint,
		meta:        meta,
		doc:         doc,
		chunks:      chunks,
		vectors:     vectors,
		assets:      assets,
		deduped:     deduped,
	}, nil
}

// caption 调用视觉模型生成中文图注。未配置视觉模型时退回"仅按文件名可检索"。
func (p *Pipeline) caption(ctx context.Context, asset *loaders.Asset, rel string) string {
	if p.vision == nil {
		return "（未启用视觉模型，本图只能按文件名检索；在配置里打开 vision.enabled 可生成图注）"
	}
	if maxBytes := p.cfg.Vision.MaxImageMB << 20; maxBytes > 0 && int64(len(asset.Data)) > maxBytes {
		return fmt.Sprintf("（图片 %.1f MB 超过 vision.max_image_mb，未生成图注）", float64(len(asset.Data))/1024/1024)
	}
	cctx, cancel := context.WithTimeout(ctx, time.Duration(max(1, p.cfg.Vision.TimeoutS))*time.Second)
	defer cancel()
	text, err := p.vision.Caption(cctx, asset.Data, asset.MIME)
	if err != nil {
		p.logf(fmt.Sprintf("  ⚠️ 图注生成失败（%s）: %v", rel, err))
		return "（图注生成失败，本图只能按文件名检索）"
	}
	if strings.TrimSpace(text) == "" {
		return "（视觉模型返回了空图注）"
	}
	return text
}

func (p *Pipeline) saveAsset(dir, name string, data []byte) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	// 只取文件名，防止 name 里带路径分隔符写出目录外。
	safe := filepath.Base(filepath.FromSlash(name))
	return os.WriteFile(filepath.Join(dir, safe), data, 0o644)
}

func (p *Pipeline) docAssetDir(docID string) string {
	return filepath.Join(p.cfg.Storage.AssetsDir, docID)
}

// collectMeta 汇总文件自带的元数据，优先级：目录 _meta.json < 文件旁 .meta.json < 文件内 front matter。
func (p *Pipeline) collectMeta(rel string, data []byte) map[string]string {
	meta := map[string]string{}
	abs := filepath.Join(p.cfg.Storage.DocsDir, filepath.FromSlash(rel))

	if m := readMetaJSON(filepath.Join(filepath.Dir(abs), "_meta.json")); m != nil {
		for k, v := range m {
			meta[k] = v
		}
	}
	if m := readMetaJSON(abs + ".meta.json"); m != nil {
		for k, v := range m {
			meta[k] = v
		}
	}
	if m := readFrontMatter(data); m != nil {
		for k, v := range m {
			meta[k] = v
		}
	}
	return meta
}

func readMetaJSON(path string) map[string]string {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var parsed map[string]any
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return nil
	}
	return flattenMeta(parsed)
}

// readFrontMatter 解析 Markdown 的 YAML front matter（--- 开头、--- 结束）。
func readFrontMatter(data []byte) map[string]string {
	text := string(data)
	if !strings.HasPrefix(text, "---") {
		return nil
	}
	rest := text[3:]
	idx := strings.Index(rest, "\n---")
	if idx < 0 {
		return nil
	}
	body := rest[:idx]
	var parsed map[string]any
	if err := yaml.Unmarshal([]byte(body), &parsed); err != nil {
		return nil
	}
	return flattenMeta(parsed)
}

// flattenMeta 把 YAML/JSON 的任意值压成字符串（数组用逗号连接）。
func flattenMeta(in map[string]any) map[string]string {
	out := map[string]string{}
	for k, v := range in {
		key := strings.ToLower(strings.TrimSpace(k))
		switch val := v.(type) {
		case nil:
			continue
		case string:
			out[key] = strings.TrimSpace(val)
		case []any:
			parts := make([]string, 0, len(val))
			for _, item := range val {
				parts = append(parts, fmt.Sprint(item))
			}
			out[key] = strings.Join(parts, ",")
		default:
			out[key] = fmt.Sprint(val)
		}
	}
	return out
}

// applyMeta 把元数据映射到 Chunk/Doc 的结构化字段。
//
// 同时接受中英文键名：写文档的人不该被迫记英文。
func applyMeta(dynasty, place, topic, sourceType, credibility *string, abilities, tags *[]string, meta map[string]string) {
	pick := func(keys ...string) string {
		for _, k := range keys {
			if v, ok := meta[k]; ok && v != "" {
				return v
			}
		}
		return ""
	}
	if v := pick("dynasty", "朝代"); v != "" {
		*dynasty = v
	}
	if v := pick("place", "地点", "location"); v != "" {
		*place = v
	}
	if v := pick("topic", "主题", "category"); v != "" {
		*topic = v
	}
	if v := pick("source_type", "来源", "source", "出处"); v != "" {
		*sourceType = v
	}
	if v := pick("credibility", "可信度"); v != "" {
		*credibility = strings.ToLower(v)
	}
	if v := pick("abilities", "ability", "能力", "玩法"); v != "" {
		*abilities = splitList(v)
	}
	if v := pick("tags", "标签"); v != "" {
		*tags = splitList(v)
	}
}

func splitList(s string) []string {
	fields := strings.FieldsFunc(s, func(r rune) bool {
		return r == ',' || r == '，' || r == ';' || r == '；' || r == '|' || r == ' '
	})
	out := make([]string, 0, len(fields))
	for _, f := range fields {
		if f = strings.TrimSpace(f); f != "" {
			out = append(out, f)
		}
	}
	return out
}

func pageMeta(page int) map[string]string {
	if page <= 0 {
		return nil
	}
	return map[string]string{"page": fmt.Sprint(page)}
}

func dominantModality(chunks []types.Chunk) types.Modality {
	counts := map[types.Modality]int{}
	for _, c := range chunks {
		counts[c.Modality]++
	}
	best, bestN := types.ModalityText, -1
	// 固定顺序遍历，保证结果确定
	for _, m := range []types.Modality{types.ModalityText, types.ModalityTable, types.ModalityImage} {
		if counts[m] > bestN {
			best, bestN = m, counts[m]
		}
	}
	return best
}

// reset 清空索引与台账（全量重建）。
func (p *Pipeline) reset(ctx context.Context) error {
	docs, err := p.backend.ListDocs(ctx)
	if err != nil {
		return err
	}
	for _, d := range docs {
		if err := p.backend.DeleteDoc(ctx, d.ID); err != nil {
			return err
		}
		os.RemoveAll(p.docAssetDir(d.ID))
	}
	p.manifest.Files = map[string]FileEntry{}
	return nil
}

func (p *Pipeline) persist() error {
	if err := store.SaveIfNeeded(p.backend); err != nil {
		return fmt.Errorf("落盘索引失败: %w", err)
	}
	return p.manifest.Save(p.ManifestPath())
}

func (p *Pipeline) chunkCount(ctx context.Context) int {
	st, err := p.backend.Stats(ctx)
	if err != nil {
		return 0
	}
	return st.Chunks
}

// filterPaths 把 RunOptions.Paths（文件或目录）展开成实际要处理的文件集合。
func filterPaths(all []string, want []string) []string {
	if len(want) == 0 {
		return all
	}
	prefixes := make([]string, 0, len(want))
	exact := map[string]bool{}
	for _, w := range want {
		w = strings.Trim(strings.ReplaceAll(strings.TrimSpace(w), "\\", "/"), "/")
		if w == "" {
			continue
		}
		exact[w] = true
		prefixes = append(prefixes, w+"/")
	}
	out := make([]string, 0, len(all))
	for _, f := range all {
		if exact[f] {
			out = append(out, f)
			continue
		}
		for _, pre := range prefixes {
			if strings.HasPrefix(f, pre) {
				out = append(out, f)
				break
			}
		}
	}
	return out
}
