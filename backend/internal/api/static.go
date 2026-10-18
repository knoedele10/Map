package api

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/gin-gonic/gin"
)

// 本文件是**后端与前端之间的唯一耦合点**：页面托管。
//
// 后端本身只提供 /api/v1/*；要不要顺手把页面也托管出去，由
// server.serve_web 决定（storage.web_dir 为空同样视为不托管）。
// 关掉之后 cmd/server 就是一个纯 API 服务，前端交给任何别的服务去托管，
// 换前端不需要动后端一行代码。
//
// 默认仍然是「托管」（serve_web 未配置即视为 true），这样既有部署与
// 自包含发行版的行为完全不变。

// mountWeb 按配置注册页面路由；不托管时一行都不注册。
func (s *Server) mountWeb(r *gin.Engine) {
	// 两种信号分开报，排障时能一眼看出是开关关的还是路径没配。
	switch {
	case s.Cfg.Server.ServeWeb != nil && !*s.Cfg.Server.ServeWeb:
		s.Logger.Info("serve_web=false：后端只提供 /api/v1/*，不托管任何页面")
		return
	case strings.TrimSpace(s.Cfg.Storage.WebDir) == "":
		s.Logger.Info("web_dir 为空：后端只提供 /api/v1/*，不托管任何页面")
		return
	}
	s.Logger.Info("托管前端页面", "web_dir", s.Cfg.Storage.WebDir)

	r.GET("/", s.serveIndex)
	r.Static("/static", s.Cfg.Storage.WebDir)

	// 蜀迹地图页用绝对路径请求离线瓦片与字形包（与发行版 exe 的内置服务同一套约定），
	// 只挂在 /static 下页面会拿到 404，地图就是一片空白。
	r.GET("/chengdu-city.pmtiles", s.serveMapTiles)
	r.GET("/fonts/*filepath", s.serveMapFonts)
}

// serveIndex 返回页面目录下的 index.html；缺失时回落到一张说明接口在哪的兜底页。
func (s *Server) serveIndex(c *gin.Context) {
	path := filepath.Join(s.Cfg.Storage.WebDir, "index.html")
	if _, err := os.Stat(path); err != nil {
		c.Data(http.StatusOK, "text/html; charset=utf-8", []byte(indexFallback))
		return
	}
	c.File(path)
}

// serveMapTiles 返回离线矢量瓦片包。PMTiles 依赖 HTTP Range，c.File 走 http.ServeFile 已支持。
func (s *Server) serveMapTiles(c *gin.Context) {
	path := filepath.Join(s.Cfg.Storage.WebDir, "shuji", "chengdu-city.pmtiles")
	if _, err := os.Stat(path); err != nil {
		Fail(c, CodeNotFound, "chengdu-city.pmtiles 不存在（应放在 web/shuji/ 下）")
		return
	}
	c.Header("Cache-Control", "public, max-age=86400")
	c.File(path)
}

// serveMapFonts 返回地图字形包 /fonts/<fontstack>/<range>.pbf（同上绝对路径约定）。
func (s *Server) serveMapFonts(c *gin.Context) {
	rel := strings.TrimPrefix(c.Param("filepath"), "/")
	clean := filepath.Clean(rel)
	if clean == "." || filepath.IsAbs(clean) || strings.Contains(clean, "..") {
		Fail(c, CodeNotFound, "字体路径不合法")
		return
	}
	path := filepath.Join(s.Cfg.Storage.WebDir, "shuji", "fonts", clean)
	if _, err := os.Stat(path); err != nil {
		Fail(c, CodeNotFound, "字形包不存在: "+rel)
		return
	}
	c.Header("Cache-Control", "public, max-age=86400")
	c.File(path)
}

// indexFallback 是页面缺失时的兜底页，至少告诉调用方接口在哪。
const indexFallback = `<!doctype html>
<html lang="zh-CN"><head><meta charset="utf-8"><title>RAG 知识库服务</title>
<style>body{font-family:-apple-system,"Segoe UI","Microsoft YaHei",sans-serif;max-width:720px;margin:80px auto;padding:0 20px;color:#1a1a1a;line-height:1.7}
code{background:#f4f4f5;padding:2px 6px;border-radius:4px}</style></head>
<body>
<h1>RAG 知识库服务正在运行</h1>
<p>接口可用（本实例未托管页面）：</p>
<ul>
  <li><code>GET /api/v1/health</code> — 依赖状态</li>
  <li><code>POST /api/v1/chat</code> — 问答（SSE 流式）</li>
  <li><code>POST /api/v1/search</code> — 纯检索</li>
  <li><code>GET /openapi.yaml</code> — 接口规范</li>
</ul>
</body></html>`
