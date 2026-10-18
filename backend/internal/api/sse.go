package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
)

// SSEWriter 是事件流写入器。
//
// 几个必须处理的细节（不处理就会在生产环境出问题）：
//   - X-Accel-Buffering: no —— 否则 Nginx 会把整个响应缓冲到结束才吐给浏览器；
//   - 每 15s 一个 : ping 注释行 —— 否则中间的代理会掐掉"空闲"连接；
//   - 每次写完 Flush —— 否则打字机效果变成"一次吐完"；
//   - 加锁 —— 心跳 goroutine 与主流程会并发写同一个 ResponseWriter。
type SSEWriter struct {
	c       *gin.Context
	flusher http.Flusher
	mu      sync.Mutex
	closed  bool
	stop    chan struct{}
}

// NewSSE 写好响应头并返回写入器。ok 为 false 表示当前环境不支持流式。
func NewSSE(c *gin.Context) (*SSEWriter, bool) {
	flusher, ok := c.Writer.(http.Flusher)
	if !ok {
		return nil, false
	}
	h := c.Writer.Header()
	h.Set("Content-Type", "text/event-stream; charset=utf-8")
	h.Set("Cache-Control", "no-cache, no-transform")
	h.Set("Connection", "keep-alive")
	h.Set("X-Accel-Buffering", "no") // 关掉反向代理缓冲
	c.Writer.WriteHeader(http.StatusOK)
	flusher.Flush()

	w := &SSEWriter{c: c, flusher: flusher, stop: make(chan struct{})}
	go w.heartbeat()
	return w, true
}

// heartbeat 周期性发送注释行保活。
func (w *SSEWriter) heartbeat() {
	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-w.stop:
			return
		case <-w.c.Request.Context().Done():
			return
		case <-ticker.C:
			w.Comment("ping")
		}
	}
}

// Event 发送一个具名事件。data 会被序列化成单行 JSON。
func (w *SSEWriter) Event(name string, data any) error {
	raw, err := json.Marshal(data)
	if err != nil {
		return err
	}
	// JSON 里不会出现裸换行（已转义），所以直接拼一行是安全的。
	return w.write(fmt.Sprintf("event: %s\ndata: %s\n\n", name, raw))
}

// Comment 发送注释行（心跳 / 调试）。
func (w *SSEWriter) Comment(text string) error {
	return w.write(": " + text + "\n\n")
}

func (w *SSEWriter) write(s string) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed {
		return nil
	}
	if _, err := w.c.Writer.WriteString(s); err != nil {
		w.closed = true
		return err
	}
	w.flusher.Flush()
	return nil
}

// Closed 表示客户端已断开或写入失败。
func (w *SSEWriter) Closed() bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.closed
}

// Close 停止心跳并关闭流。
func (w *SSEWriter) Close() {
	w.mu.Lock()
	alreadyClosed := w.closed
	w.closed = true
	w.mu.Unlock()
	if !alreadyClosed {
		close(w.stop)
	}
}

// wantsStream 判断客户端是否要流式响应：
// 显式 stream=false 时返回 false；Accept 明确要 JSON 时也返回 false。
func wantsStream(c *gin.Context, explicit *bool) bool {
	if explicit != nil {
		return *explicit
	}
	if strings.Contains(c.GetHeader("Accept"), "text/event-stream") {
		return true
	}
	return true // 默认流式：RAG 的首字延迟远小于整段生成时间
}
