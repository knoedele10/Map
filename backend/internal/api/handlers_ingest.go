package api

import (
	"context"

	"github.com/gin-gonic/gin"

	"ragkb/internal/ingest"
)

type ingestRequest struct {
	// Mode: incremental（默认，只处理新增/改动）| full（清空重建）
	Mode    string            `json:"mode"`
	Rebuild *bool             `json:"rebuild"`
	Paths   []string          `json:"paths"`
	Meta    map[string]string `json:"meta"`
}

// startIngest 触发一次入库任务，立刻返回 job_id。
//
// 为什么必须异步：首次入库要调用成百上千次嵌入模型，同步请求必然超时。
func (s *Server) startIngest(c *gin.Context) {
	var req ingestRequest
	if err := c.ShouldBindJSON(&req); err != nil && err.Error() != "EOF" {
		Fail(c, CodeBadRequest, "请求体不是合法 JSON: "+err.Error())
		return
	}
	if s.Pipeline == nil {
		Fail(c, CodeNotReady, "入库流水线未初始化")
		return
	}
	rebuild := req.Mode == "full"
	if req.Rebuild != nil {
		rebuild = *req.Rebuild
	}
	if req.Mode != "" && req.Mode != "full" && req.Mode != "incremental" {
		Fail(c, CodeBadRequest, "mode 只能是 incremental 或 full")
		return
	}

	opts := ingest.RunOptions{Rebuild: rebuild, Paths: req.Paths, Meta: req.Meta}
	pipeline := s.Pipeline

	job, err := s.Jobs.Start("ingest", func(ctx context.Context, logf func(string), report func(int, int, string)) (any, error) {
		pipeline.SetLogger(logf)
		logf("开始入库：docs=" + s.Cfg.Storage.DocsDir + " rebuild=" + boolStr(rebuild))
		stats, err := pipeline.Run(ctx, opts, func(p ingest.Progress) {
			report(p.Current, p.Total, p.Message)
		})
		return stats, err
	})
	if err != nil {
		Fail(c, CodeConflict, err.Error())
		return
	}
	OK(c, gin.H{"job_id": job.ID(), "mode": "ingest", "status": "running", "rebuild": rebuild})
}

// listJobs 返回全部入库任务（最新在前）。
func (s *Server) listJobs(c *gin.Context) {
	OK(c, gin.H{"jobs": s.Jobs.List()})
}

// getJob 查询单个任务进度。
func (s *Server) getJob(c *gin.Context) {
	job, ok := s.Jobs.Get(c.Param("id"))
	if !ok {
		Fail(c, CodeNotFound, "任务不存在: "+c.Param("id"))
		return
	}
	OK(c, job.Snapshot())
}

// cancelJob 取消任务（ctx 会传到流水线的每一层，正在跑的嵌入批次结束后退出）。
func (s *Server) cancelJob(c *gin.Context) {
	id := c.Param("id")
	if _, ok := s.Jobs.Get(id); !ok {
		Fail(c, CodeNotFound, "任务不存在: "+id)
		return
	}
	if !s.Jobs.Cancel(id) {
		Fail(c, CodeConflict, "任务已结束，无法取消")
		return
	}
	OK(c, gin.H{"job_id": id, "cancelled": true})
}

func boolStr(b bool) string {
	if b {
		return "true"
	}
	return "false"
}
