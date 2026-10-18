package rag

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"time"

	"ragkb/internal/types"
)

// TraceHit 是留痕里的一条命中摘要。
type TraceHit struct {
	ChunkID string  `json:"chunk_id"`
	Source  string  `json:"source"`
	Score   float64 `json:"score"`
	From    string  `json:"from"`
}

// Trace 是一次问答链路的完整留痕。
//
// 为什么要留痕：RAG 出问题时只有三种可能——没检索到、检索错了、模型没按资料写。
// 不把"当时检索到什么、prompt 长什么样、模型回了什么"存下来，就只能靠猜。
type Trace struct {
	RequestID   string       `json:"request_id"`
	At          time.Time    `json:"at"`
	Question    string       `json:"question"`
	Rewritten   string       `json:"rewritten_question,omitempty"`
	Hits        []TraceHit   `json:"hits"`
	Sites       []string     `json:"sites,omitempty"`
	PromptChars int          `json:"prompt_chars"`
	AnswerChars int          `json:"answer_chars"`
	Model       string       `json:"model"`
	ElapsedMS   int          `json:"elapsed_ms"`
	TTFTMS      int          `json:"ttft_ms"`
	Usage       *types.Usage `json:"usage,omitempty"`
	Error       string       `json:"error,omitempty"`
}

// Tracer 是内存环形缓冲 + 可选 JSONL 落盘。
type Tracer struct {
	mu   sync.RWMutex
	buf  []Trace
	max  int
	file *os.File
}

// NewTracer 创建留痕器；path 非空时同时追加写入 JSONL 文件。
func NewTracer(max int, path string) *Tracer {
	if max <= 0 {
		max = 100
	}
	t := &Tracer{max: max}
	if path != "" {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err == nil {
			if previous, err := os.Open(path); err == nil {
				scanner := bufio.NewScanner(previous)
				scanner.Buffer(make([]byte, 4096), 2<<20)
				for scanner.Scan() {
					var trace Trace
					if json.Unmarshal(scanner.Bytes(), &trace) == nil {
						t.buf = append(t.buf, trace)
						if len(t.buf) > max {
							t.buf = t.buf[len(t.buf)-max:]
						}
					}
				}
				previous.Close()
			}
			if info, err := os.Stat(path); err == nil && info.Size() >= 16<<20 {
				_ = os.Rename(path, path+".1")
			}
			// 追加模式打开，进程重启后历史仍然可查
			if f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600); err == nil {
				t.file = f
			}
		}
	}
	return t
}

// Add 记录一条留痕。
func (t *Tracer) Add(tr Trace) {
	if tr.At.IsZero() {
		tr.At = time.Now()
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	t.buf = append(t.buf, tr)
	if len(t.buf) > t.max {
		t.buf = t.buf[len(t.buf)-t.max:]
	}
	if t.file != nil {
		if info, err := t.file.Stat(); err == nil && info.Size() >= 16<<20 {
			path := t.file.Name()
			t.file.Close()
			_ = os.Rename(path, path+".1")
			t.file, _ = os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
		}
	}
	if t.file != nil {
		if raw, err := json.Marshal(tr); err == nil {
			t.file.Write(append(raw, '\n'))
		}
	}
}

// List 返回最近 limit 条，最新的在前。
func (t *Tracer) List(limit int) []Trace {
	t.mu.RLock()
	defer t.mu.RUnlock()
	if limit <= 0 || limit > len(t.buf) {
		limit = len(t.buf)
	}
	out := make([]Trace, 0, limit)
	for i := len(t.buf) - 1; i >= 0 && len(out) < limit; i-- {
		out = append(out, t.buf[i])
	}
	return out
}

// Close 关闭底层文件。
func (t *Tracer) Close() error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.file != nil {
		return t.file.Close()
	}
	return nil
}
