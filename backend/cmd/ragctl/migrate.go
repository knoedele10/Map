package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"flag"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"ragkb/internal/ingest"
	"ragkb/internal/store"
	"ragkb/internal/types"
)

// cmdMigrate 把旧 Python 版（numpy 向量 + jsonl 块）的索引迁进 Go 服务。
//
// 为什么值得写这个工具：重建索引要重新调用几十上百次嵌入模型（本地 Ollama 也要几分钟）。
// 旧数据里向量已经算好了，直接搬过来，零模型调用。
func cmdMigrate(args []string) error {
	fs := flag.NewFlagSet("migrate", flag.ExitOnError)
	from := fs.String("from", "../data", "旧版数据目录（含 vectors.npy 与 chunks.jsonl）")
	dryRun := fs.Bool("dry-run", false, "只解析并打印统计，不写入索引")
	batch := fs.Int("batch", 256, "每批写入的块数")
	cfgPath, err := parseFlags(fs, args)
	if err != nil {
		return err
	}
	e, err := setup(cfgPath)
	if err != nil {
		return err
	}
	defer e.Close()

	vecPath := filepath.Join(*from, "vectors.npy")
	chunkPath := filepath.Join(*from, "chunks.jsonl")
	vectors, rows, cols, err := readNPY(vecPath)
	if err != nil {
		return err
	}
	legacy, err := readLegacyChunks(chunkPath)
	if err != nil {
		return err
	}
	fmt.Printf("读到 %d 条向量（%d x %d）与 %d 条旧块记录\n", rows, rows, cols, len(legacy))

	if cols != e.cfg.Embedding.Dim {
		return fmt.Errorf("向量维度不一致：旧数据是 %d 维，当前 embedding.dim 是 %d。"+
			"换嵌入模型必须重建索引，不能直接迁移", cols, e.cfg.Embedding.Dim)
	}
	if len(legacy) != rows {
		fmt.Printf("⚠️  向量数与块数不一致（%d vs %d），按较小的那个处理\n", rows, len(legacy))
	}
	n := min(rows, len(legacy))
	if n == 0 {
		return fmt.Errorf("没有可迁移的数据")
	}

	now := time.Now().UTC()
	chunks := make([]types.Chunk, 0, n)
	vecs := make([][]float32, 0, n)
	docSeen := map[string]bool{}
	docs := map[string]types.Doc{}

	for i := 0; i < n; i++ {
		item := legacy[i]
		source := strings.TrimSpace(item.Source)
		if source == "" {
			source = "legacy-unknown"
		}
		text := strings.TrimSpace(item.Text)
		if text == "" {
			continue
		}
		docID := ingest.StableID("doc", source)
		if !docSeen[docID] {
			docSeen[docID] = true
			docs[docID] = types.Doc{
				ID:        docID,
				Title:     strings.TrimSuffix(filepath.Base(source), filepath.Ext(source)),
				Source:    source,
				Status:    "indexed",
				Modality:  types.ModalityText,
				UpdatedAt: now,
			}
		}
		chunks = append(chunks, types.Chunk{
			ID:        ingest.StableID("legacy", source, strconv.Itoa(item.Chunk)),
			DocID:     docID,
			Source:    source,
			Index:     item.Chunk,
			Text:      text,
			Modality:  types.ModalityText,
			Hash:      ingest.ContentHash(text),
			CreatedAt: now,
		})
		vecs = append(vecs, vectors[i])
	}

	// 统计每个文档的块数
	for _, c := range chunks {
		d := docs[c.DocID]
		d.Chunks++
		docs[c.DocID] = d
	}
	fmt.Printf("将写入 %d 个文档、%d 个块（维度 %d）\n", len(docs), len(chunks), cols)
	if *dryRun {
		fmt.Println("dry-run：未写入索引")
		return nil
	}

	ctx := context.Background()
	if *batch <= 0 {
		*batch = 256
	}
	for start := 0; start < len(chunks); start += *batch {
		end := min(start+*batch, len(chunks))
		if err := e.backend.Add(ctx, chunks[start:end], vecs[start:end]); err != nil {
			return fmt.Errorf("写入第 %d~%d 块失败: %w", start, end, err)
		}
		fmt.Printf("  已写入 %d/%d\n", end, len(chunks))
	}
	for _, d := range docs {
		if err := e.backend.UpsertDoc(ctx, d); err != nil {
			return fmt.Errorf("登记文档 %s 失败: %w", d.Source, err)
		}
	}
	if err := store.SaveIfNeeded(e.backend); err != nil {
		return err
	}
	fmt.Printf("\n✅ 迁移完成。\n注意：manifest 台账没有随之迁移，下次 ragctl ingest 会重新嵌入一遍；\n" +
		"      如果旧索引已经够用，直接 ragctl search 验证即可。\n")
	return nil
}

type legacyChunk struct {
	Text   string `json:"text"`
	Source string `json:"source"`
	Chunk  int    `json:"chunk"`
}

// readLegacyChunks 读 jsonl，字段名做宽容匹配（旧版可能用 file/path/index）。
func readLegacyChunks(path string) ([]legacyChunk, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("打开 %s: %w", path, err)
	}
	defer f.Close()

	var out []legacyChunk
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var raw map[string]any
		if err := json.Unmarshal([]byte(line), &raw); err != nil {
			return nil, fmt.Errorf("解析 %s 的某一行失败: %w", path, err)
		}
		item := legacyChunk{Text: pickString(raw, "text", "content", "chunk_text")}
		item.Source = pickString(raw, "source", "file", "path", "doc", "filename")
		item.Chunk = pickInt(raw, "chunk", "index", "chunk_index", "i")
		out = append(out, item)
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("读取 %s: %w", path, err)
	}
	return out, nil
}

func pickString(m map[string]any, keys ...string) string {
	for _, k := range keys {
		if v, ok := m[k]; ok {
			if s, ok := v.(string); ok {
				return s
			}
		}
	}
	return ""
}

func pickInt(m map[string]any, keys ...string) int {
	for _, k := range keys {
		if v, ok := m[k]; ok {
			switch n := v.(type) {
			case float64:
				return int(n)
			case int:
				return n
			}
		}
	}
	return 0
}

var (
	npyShapeRe = regexp.MustCompile(`'shape':\s*\((\d+),\s*(\d+)\)`)
	npyDescrRe = regexp.MustCompile(`'descr':\s*'([^']+)'`)
)

// readNPY 解析 numpy .npy（v1/v2/v3 头），只支持 little-endian float32。
func readNPY(path string) ([][]float32, int, int, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, 0, 0, fmt.Errorf("打开 %s: %w", path, err)
	}
	magic := []byte{0x93, 'N', 'U', 'M', 'P', 'Y'}
	if len(raw) < 12 || !bytes.Equal(raw[:6], magic) {
		return nil, 0, 0, fmt.Errorf("%s 不是合法的 .npy 文件", path)
	}
	major := raw[6]
	off := 8
	var headerLen int
	switch major {
	case 1:
		if len(raw) < 10 {
			return nil, 0, 0, fmt.Errorf("%s 头部被截断", path)
		}
		headerLen = int(binary.LittleEndian.Uint16(raw[8:10]))
		off = 10
	default:
		if len(raw) < 12 {
			return nil, 0, 0, fmt.Errorf("%s 头部被截断", path)
		}
		headerLen = int(binary.LittleEndian.Uint32(raw[8:12]))
		off = 12
	}
	if off+headerLen > len(raw) {
		return nil, 0, 0, fmt.Errorf("%s 头部长度非法", path)
	}
	header := string(raw[off : off+headerLen])
	dataStart := off + headerLen

	if m := npyDescrRe.FindStringSubmatch(header); len(m) == 3 {
		if m[1] != "<f4" {
			return nil, 0, 0, fmt.Errorf("只支持 little-endian float32（<f4），当前是 %q", m[1])
		}
	}
	m := npyShapeRe.FindStringSubmatch(header)
	if len(m) != 3 {
		return nil, 0, 0, fmt.Errorf("无法从 %s 的头部解析 shape：%s", path, header)
	}
	rows, _ := strconv.Atoi(m[1])
	cols, _ := strconv.Atoi(m[2])
	if rows <= 0 || cols <= 0 {
		return nil, 0, 0, fmt.Errorf("shape 非法：(%d, %d)", rows, cols)
	}
	need := dataStart + rows*cols*4
	if need > len(raw) {
		return nil, 0, 0, fmt.Errorf("%s 数据被截断：需要 %d 字节，实际 %d", path, need, len(raw))
	}

	vectors := make([][]float32, rows)
	for i := 0; i < rows; i++ {
		vec := make([]float32, cols)
		for j := 0; j < cols; j++ {
			pos := dataStart + (i*cols+j)*4
			vec[j] = math.Float32frombits(binary.LittleEndian.Uint32(raw[pos : pos+4]))
		}
		vectors[i] = vec
	}
	return vectors, rows, cols, nil
}
