package api

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/gin-gonic/gin"

	"ragkb/internal/ingest"
	"ragkb/internal/ingest/loaders"
	"ragkb/internal/store"
)

// uploadAsset 接收上传文件，落盘到 docs 目录，并（可选）立刻触发增量入库。
//
// 设计取舍：上传与入库解耦。即使已有入库任务在跑导致本次无法启动，
// 文件也已经安全落盘，下次增量入库会自动捡起来——不会丢数据。
func (s *Server) uploadAsset(c *gin.Context) {
	header, err := c.FormFile("file")
	if err != nil {
		Fail(c, CodeBadRequest, "缺少 multipart 字段 file: "+err.Error())
		return
	}
	source := c.PostForm("source")
	if source == "" {
		source = header.Filename
	}
	rel, err := sanitizeRel(source)
	if err != nil {
		Fail(c, CodeBadRequest, err.Error())
		return
	}
	if _, ok := loaders.For(rel); !ok {
		Fail(c, CodeBadRequest, fmt.Sprintf("不支持的文件类型 %s，支持：%s",
			path.Ext(rel), strings.Join(loaders.Supported(), ", ")))
		return
	}
	if maxBytes := s.Cfg.Ingest.MaxFileMB << 20; maxBytes > 0 && header.Size > maxBytes {
		Fail(c, CodeBadRequest, fmt.Sprintf("文件 %.1f MB 超过上限 %d MB",
			float64(header.Size)/1024/1024, s.Cfg.Ingest.MaxFileMB))
		return
	}

	// 元数据：既接受整体 JSON（meta 字段），也接受扁平的 meta_xxx 字段
	meta := map[string]string{}
	if raw := c.PostForm("meta"); raw != "" {
		var parsed map[string]any
		if err := json.Unmarshal([]byte(raw), &parsed); err != nil {
			Fail(c, CodeBadRequest, "meta 不是合法 JSON: "+err.Error())
			return
		}
		for k, v := range parsed {
			meta[strings.ToLower(k)] = fmt.Sprint(v)
		}
	}
	for key, vals := range c.Request.PostForm {
		if strings.HasPrefix(key, "meta_") && len(vals) > 0 && vals[0] != "" {
			meta[strings.TrimPrefix(key, "meta_")] = vals[0]
		}
	}
	dst := filepath.Join(s.Cfg.Storage.DocsDir, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		Fail(c, CodeInternal, "创建目录失败: "+err.Error())
		return
	}
	tmp, err := os.CreateTemp(filepath.Dir(dst), ".upload-*")
	if err != nil {
		Fail(c, CodeInternal, err.Error())
		return
	}
	tmpPath := tmp.Name()
	tmp.Close()
	defer os.Remove(tmpPath)
	if err := c.SaveUploadedFile(header, tmpPath); err != nil {
		Fail(c, CodeInternal, "保存文件失败: "+err.Error())
		return
	}
	if err := os.Rename(tmpPath, dst); err != nil {
		Fail(c, CodeInternal, "替换文件失败: "+err.Error())
		return
	}
	if len(meta) > 0 {
		if raw, err := json.MarshalIndent(meta, "", "  "); err == nil {
			if err := os.WriteFile(dst+".meta.json", raw, 0o644); err != nil {
				Fail(c, CodeInternal, "元数据保存失败: "+err.Error())
				return
			}
		}
	}

	docID := ingest.StableID("doc", rel)
	resp := gin.H{
		"asset_id": docID,
		"doc_id":   docID,
		"source":   rel,
		"size":     header.Size,
		"indexed":  false,
		"job_id":   "",
	}

	// 默认上传即入库；传 ingest=false 则只落盘（批量上传时先攒后入更省调用）。
	if c.PostForm("ingest") == "true" || (c.PostForm("ingest") == "" && s.Cfg.Ingest.AutoIngestUpload) {
		if s.Pipeline == nil {
			resp["note"] = "入库流水线未初始化，文件已落盘但未索引"
			OK(c, resp)
			return
		}
		pipeline := s.Pipeline
		opts := ingest.RunOptions{Paths: []string{rel}, Meta: meta}
		job, err := s.Jobs.Start("ingest", func(ctx context.Context, logf func(string), report func(int, int, string)) (any, error) {
			pipeline.SetLogger(logf)
			stats, err := pipeline.Run(ctx, opts, func(p ingest.Progress) { report(p.Current, p.Total, p.Message) })
			return stats, err
		})
		if err != nil {
			resp["note"] = "文件已落盘，但当前有入库任务在跑，稍后增量入库会处理它"
			OK(c, resp)
			return
		}
		resp["indexed"] = true
		resp["job_id"] = job.ID()
	}
	OK(c, resp)
}

// getAsset 提供文档内抽出的图片（给前端渲染多模态引用）。
func (s *Server) getAsset(c *gin.Context) {
	docID := c.Param("doc_id")
	name := c.Param("name")
	if !isHexID(docID) || name == "" || name != filepath.Base(name) {
		Fail(c, CodeBadRequest, "非法的资产路径")
		return
	}
	full := filepath.Join(s.Cfg.Storage.AssetsDir, docID, name)
	if _, err := os.Stat(full); err != nil {
		Fail(c, CodeNotFound, "资产不存在")
		return
	}
	c.Header("Cache-Control", "public, max-age=86400")
	c.File(full)
}

// listDocuments 列出已登记文档。
func (s *Server) listDocuments(c *gin.Context) {
	docs, err := s.Backend.ListDocs(c.Request.Context())
	if err != nil {
		Fail(c, CodeInternal, "读取文档列表失败: "+err.Error())
		return
	}
	OK(c, gin.H{"documents": docs, "total": len(docs)})
}

// deleteDocument 删除文档及其全部块、图片与台账记录。
func (s *Server) deleteDocument(c *gin.Context) {
	id := c.Param("id")
	ctx := c.Request.Context()
	doc, ok, err := s.Backend.GetDoc(ctx, id)
	if err != nil || !ok {
		Fail(c, CodeNotFound, "文档不存在: "+id)
		return
	}
	if s.Jobs.Running() != nil {
		Fail(c, CodeConflict, "请先等待入库任务结束或取消任务，再删除文档")
		return
	}
	if err := s.Backend.DeleteDoc(ctx, id); err != nil {
		Fail(c, CodeInternal, "删除文档失败: "+err.Error())
		return
	}
	_ = os.RemoveAll(filepath.Join(s.Cfg.Storage.AssetsDir, id))
	// 必须同时清台账：否则下次增量入库会以为文件没变而跳过，文档永远回不来。
	if s.Pipeline != nil {
		if err := s.Pipeline.Forget(id); err != nil {
			Fail(c, CodeInternal, "清理台账失败: "+err.Error())
			return
		}
	}
	if err := store.SaveIfNeeded(s.Backend); err != nil {
		Fail(c, CodeInternal, "保存删除结果失败: "+err.Error())
		return
	}
	OK(c, gin.H{"doc_id": id, "source": doc.Source, "chunks_removed": doc.Chunks, "deleted": true})
}

// listSites 返回全部结构化景点数据。
func (s *Server) listSites(c *gin.Context) {
	if s.Sites == nil || s.Sites.Len() == 0 {
		OK(c, gin.H{"sites": []any{}, "total": 0, "note": "尚未配置 configs/sites.json"})
		return
	}
	list := s.Sites.List()
	OK(c, gin.H{"sites": list, "total": len(list)})
}

// getSite 按 id 或名称/别名查单个景点。
func (s *Server) getSite(c *gin.Context) {
	if s.Sites == nil {
		Fail(c, CodeNotFound, "尚未配置景点数据")
		return
	}
	site, ok := s.Sites.Get(c.Param("id"))
	if !ok {
		Fail(c, CodeNotFound, "景点不存在: "+c.Param("id"))
		return
	}
	OK(c, site)
}

// sanitizeRel 把上传路径规范化并挡掉目录穿越。
func sanitizeRel(p string) (string, error) {
	p = filepath.ToSlash(strings.TrimSpace(p))
	p = strings.TrimPrefix(p, "/")
	if p == "" || strings.Contains(p, ":") {
		return "", fmt.Errorf("非法的上传路径: %q", p)
	}
	clean := path.Clean(p)
	if clean == "." || clean == ".." || strings.HasPrefix(clean, "../") || strings.Contains(clean, "/../") {
		return "", fmt.Errorf("非法的上传路径（不允许 ..）: %q", p)
	}
	return clean, nil
}

func isHexID(s string) bool {
	if len(s) != 16 {
		return false
	}
	for _, r := range s {
		if !((r >= '0' && r <= '9') || (r >= 'a' && r <= 'f')) {
			return false
		}
	}
	return true
}
