package api

import (
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/gin-gonic/gin"

	"ragkb/internal/config"
	"ragkb/internal/ingest"
	"ragkb/internal/jobs"
	"ragkb/internal/llm"
	"ragkb/internal/rag"
	"ragkb/internal/sites"
	"ragkb/internal/store"
)

// Version 是服务版本号（健康检查与前端展示用）。
const Version = "0.2.0"

// Deps 是 API 层的全部依赖。全部显式传入，不在包里用全局变量。
type Deps struct {
	Cfg      *config.Config
	Backend  store.Backend
	Embedder llm.Embedder
	Chatter  llm.Chatter
	// VisionChatter 是带图请求用的模型（chat.vision_model）；为空时回退到 Chatter。
	VisionChatter llm.Chatter
	Vision        llm.Visioner
	Rag           *rag.Service
	Jobs          *jobs.Manager
	Sites         *sites.Store
	Pipeline      *ingest.Pipeline
	Logger        *slog.Logger
	StartTime     time.Time
	// OnAlive 由桌面发行版用于记录游戏窗口心跳；普通服务端可不设置。
	OnAlive func()
	// OnRestart 仅在服务由支持重启的启动器托管时设置。
	OnRestart func() error
	OnStop    func() error
	// OpenAPIPath 指向 openapi.yaml，供 /openapi.yaml 下载。
	OpenAPIPath string
}

// Server 持有依赖并提供路由。
type Server struct {
	Deps
}

// New 构造 API 服务。
func New(d Deps) *Server {
	if d.Logger == nil {
		d.Logger = slog.Default()
	}
	if d.StartTime.IsZero() {
		d.StartTime = time.Now()
	}
	if d.Jobs == nil {
		d.Jobs = jobs.NewManager(20)
	}
	return &Server{Deps: d}
}

// Router 组装路由。
func (s *Server) Router() *gin.Engine {
	if s.Cfg.Server.Mode == "release" {
		gin.SetMode(gin.ReleaseMode)
	} else {
		gin.SetMode(gin.DebugMode)
	}

	r := gin.New()
	r.MaxMultipartMemory = 16 << 20 // 16 MiB 驻留内存，更大的走临时文件
	r.Use(
		RequestID(),
		Logger(s.Logger),
		Recovery(s.Logger),
		CORS(s.Cfg.Server.CORSOrigins),
		RateLimit(s.Cfg.Server.RateLimitPerMin),
	)
	if len(s.Cfg.Server.TrustedProxies) > 0 {
		_ = r.SetTrustedProxies(s.Cfg.Server.TrustedProxies)
	}

	// 页面托管集中在 static.go，由 server.serve_web 控制（false = 纯 API 服务）。
	s.mountWeb(r)

	// 规范文件与桌面版心跳：与页面无关，始终提供。
	// 不需要鉴权，否则浏览器第一次打开什么都问不出来。
	r.GET("/openapi.yaml", s.serveOpenAPI)
	r.GET("/__shuji_alive__", func(c *gin.Context) {
		if s.OnAlive != nil {
			s.OnAlive()
		}
		c.Status(http.StatusNoContent)
	})

	v1 := r.Group("/api/v1")
	v1.Use(Auth(s.Cfg.Server.AuthToken))
	{
		v1.GET("/health", s.health)
		v1.POST("/system/restart", s.restartService)
		v1.POST("/system/stop", s.stopService)
		v1.GET("/config", s.publicConfig)
		v1.GET("/settings/models", s.getModelSettings)
		v1.PUT("/settings/models", s.saveModelSettings)
		v1.GET("/stats", s.stats)

		v1.POST("/chat", s.chat)
		v1.POST("/recognize", s.recognize) // 拍照识景：一张照片 -> 候选古迹名
		v1.POST("/search", s.search)
		v1.POST("/retrieve", s.search) // 兼容别名

		v1.POST("/assets", s.uploadAsset)
		v1.POST("/documents/upload", s.uploadAsset) // 兼容别名
		v1.GET("/assets/:doc_id/:name", s.getAsset)

		v1.POST("/ingest", s.startIngest)
		v1.GET("/ingest/jobs", s.listJobs)
		v1.GET("/ingest/jobs/:id", s.getJob)
		v1.POST("/ingest/jobs/:id/cancel", s.cancelJob)

		v1.GET("/documents", s.listDocuments)
		v1.DELETE("/documents/:id", s.deleteDocument)

		v1.GET("/sites", s.listSites)
		v1.GET("/sites/:id", s.getSite)

		v1.GET("/traces", s.listTraces)
	}

	r.NoRoute(func(c *gin.Context) {
		Fail(c, CodeNotFound, "接口不存在: "+c.Request.Method+" "+c.Request.URL.Path)
	})
	return r
}

// serveOpenAPI 返回 OpenAPI 规范。
func (s *Server) serveOpenAPI(c *gin.Context) {
	paths := []string{s.OpenAPIPath, "openapi.yaml"}
	// 后端与前端已经解耦，不再从前端目录反推规范位置；改为可执行文件同级，
	// 这样从任意工作目录启动都能找到随包释放的 openapi.yaml。
	if exe, err := os.Executable(); err == nil {
		paths = append(paths, filepath.Join(filepath.Dir(exe), "openapi.yaml"))
	}
	for _, p := range paths {
		if p == "" {
			continue
		}
		if _, err := os.Stat(p); err == nil {
			c.Header("Content-Type", "application/yaml; charset=utf-8")
			c.File(p)
			return
		}
	}
	Fail(c, CodeNotFound, "openapi.yaml 不存在")
}
