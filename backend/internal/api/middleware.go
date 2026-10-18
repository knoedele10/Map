package api

import (
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"

	"ragkb/internal/types"
)

const requestIDKey = "request_id"

// RequestIDOf 取当前请求的 request_id。
func RequestIDOf(c *gin.Context) string {
	if v, ok := c.Get(requestIDKey); ok {
		if s, ok := v.(string); ok {
			return s
		}
	}
	return ""
}

// RequestID 给每个请求分配一个 ID，同时回写响应头，方便把前端报错和后端日志对上。
func RequestID() gin.HandlerFunc {
	return func(c *gin.Context) {
		id := c.GetHeader("X-Request-Id")
		if id == "" || len(id) > 64 {
			id = types.NewID()
		}
		c.Set(requestIDKey, id)
		c.Writer.Header().Set("X-Request-Id", id)
		c.Next()
	}
}

// Logger 打印结构化访问日志。
func Logger(log *slog.Logger) gin.HandlerFunc {
	return func(c *gin.Context) {
		start := time.Now()
		c.Next()
		latency := time.Since(start)
		attrs := []any{
			"method", c.Request.Method,
			"path", c.Request.URL.Path,
			"status", c.Writer.Status(),
			"latency_ms", latency.Milliseconds(),
			"request_id", RequestIDOf(c),
			"client", c.ClientIP(),
		}
		if errs := c.Errors.String(); errs != "" {
			attrs = append(attrs, "errors", errs)
		}
		switch {
		case c.Writer.Status() >= 500:
			log.Error("请求失败", attrs...)
		case c.Writer.Status() >= 400:
			log.Warn("请求异常", attrs...)
		default:
			log.Info("请求完成", attrs...)
		}
	}
}

// Recovery 捕获 panic，返回统一信封而不是裸 500。
func Recovery(log *slog.Logger) gin.HandlerFunc {
	return func(c *gin.Context) {
		defer func() {
			if r := recover(); r != nil {
				log.Error("处理请求时 panic", "request_id", RequestIDOf(c), "path", c.Request.URL.Path, "panic", r)
				Fail(c, CodeInternal, "服务内部错误，请把 request_id 提供给后端排查")
			}
		}()
		c.Next()
	}
}

// CORS 允许前端跨域接入（开发期常见：前端 5173，后端 8080）。
func CORS(origins []string) gin.HandlerFunc {
	allowAll := false
	allowed := map[string]bool{}
	for _, o := range origins {
		o = strings.TrimSpace(o)
		if o == "*" {
			allowAll = true
		}
		if o != "" {
			allowed[o] = true
		}
	}
	return func(c *gin.Context) {
		origin := c.GetHeader("Origin")
		if origin != "" && (allowAll || allowed[origin]) {
			c.Writer.Header().Set("Access-Control-Allow-Origin", origin)
			c.Writer.Header().Add("Vary", "Origin")
			c.Writer.Header().Set("Access-Control-Allow-Methods", "GET,POST,PUT,DELETE,OPTIONS")
			c.Writer.Header().Set("Access-Control-Allow-Headers", "Authorization,Content-Type,X-Request-Id")
			c.Writer.Header().Set("Access-Control-Expose-Headers", "X-Request-Id")
			c.Writer.Header().Set("Access-Control-Max-Age", "43200")
		}
		if c.Request.Method == http.MethodOptions {
			c.AbortWithStatus(http.StatusNoContent)
			return
		}
		c.Next()
	}
}

// Auth 校验 Bearer token。token 为空时不做校验（本地联调默认）。
func Auth(token string) gin.HandlerFunc {
	return func(c *gin.Context) {
		if token == "" {
			c.Next()
			return
		}
		// health 永远放行：否则前端连"服务是否活着"都问不出来。
		if c.Request.URL.Path == "/api/v1/health" {
			c.Next()
			return
		}
		got := strings.TrimSpace(strings.TrimPrefix(c.GetHeader("Authorization"), "Bearer "))
		if got != token {
			Fail(c, CodeUnauthorized, "未认证：请在 Authorization 头里带 Bearer <token>")
			return
		}
		c.Next()
	}
}

// rateBucket 是单 IP 的令牌桶。
type rateBucket struct {
	tokens   float64
	lastSeen time.Time
}

// RateLimit 做每 IP 限流。
//
// 为什么必须限流：一次问答至少要调用一次嵌入 + 一次生成，有的还要多轮改写；
// 不限流的话，一个循环脚本就能把 API Key 的额度烧光。
func RateLimit(perMinute int) gin.HandlerFunc {
	if perMinute <= 0 {
		return func(c *gin.Context) { c.Next() }
	}
	capacity := float64(perMinute)
	refillPerSec := capacity / 60.0

	var mu sync.Mutex
	buckets := map[string]*rateBucket{}

	// 定期清理长时间不活跃的桶，避免 map 无限增长
	go func() {
		ticker := time.NewTicker(5 * time.Minute)
		defer ticker.Stop()
		for range ticker.C {
			cutoff := time.Now().Add(-10 * time.Minute)
			mu.Lock()
			for ip, b := range buckets {
				if b.lastSeen.Before(cutoff) {
					delete(buckets, ip)
				}
			}
			mu.Unlock()
		}
	}()

	return func(c *gin.Context) {
		ip := c.ClientIP()
		now := time.Now()

		mu.Lock()
		b, ok := buckets[ip]
		if !ok {
			b = &rateBucket{tokens: capacity, lastSeen: now}
			buckets[ip] = b
		}
		elapsed := now.Sub(b.lastSeen).Seconds()
		b.tokens = min(capacity, b.tokens+elapsed*refillPerSec)
		b.lastSeen = now
		allowed := b.tokens >= 1
		if allowed {
			b.tokens--
		}
		mu.Unlock()

		if !allowed {
			c.Header("Retry-After", "5")
			Fail(c, CodeRateLimited, "请求过于频繁，请稍后再试（每分钟上限见 server.rate_limit_per_min）")
			return
		}
		c.Next()
	}
}
