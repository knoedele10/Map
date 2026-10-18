// Package jobs 管理异步长任务（目前是入库）。
//
// 为什么必须异步：首次入库要调用成百上千次嵌入模型，同步 HTTP 请求必然超时。
// 所以接口只返回 job_id，前端轮询进度。
package jobs

import (
	"context"
	"fmt"
	"sync"
	"time"

	"ragkb/internal/types"
)

// 任务状态。
const (
	StatusRunning   = "running"
	StatusSucceeded = "succeeded"
	StatusFailed    = "failed"
	StatusCancelled = "cancelled"
)

// Progress 是任务进度。
type Progress struct {
	Current int `json:"current"`
	Total   int `json:"total"`
	Percent int `json:"percent"`
}

// Snapshot 是任务的安全快照（可以直接 JSON 序列化，不带锁和 cancel 函数）。
type Snapshot struct {
	JobID      string     `json:"job_id"`
	Mode       string     `json:"mode"`
	Status     string     `json:"status"`
	Progress   Progress   `json:"progress"`
	Message    string     `json:"message"`
	Logs       []string   `json:"logs"`
	Stats      any        `json:"stats,omitempty"`
	Error      string     `json:"error,omitempty"`
	StartedAt  time.Time  `json:"started_at"`
	FinishedAt *time.Time `json:"finished_at"`
}

// maxLogs 是每个任务保留的日志行数上限，防止内存被长任务撑爆。
const maxLogs = 200

// Job 是一次任务执行。
type Job struct {
	id     string
	mode   string
	mu     sync.Mutex
	status string
	prog   Progress
	msg    string
	logs   []string
	stats  any
	errMsg string
	start  time.Time
	end    *time.Time
	cancel context.CancelFunc
}

// ID 返回任务 ID。
func (j *Job) ID() string { return j.id }

// Snapshot 返回一份加锁拷贝。
func (j *Job) Snapshot() Snapshot {
	j.mu.Lock()
	defer j.mu.Unlock()
	logs := make([]string, len(j.logs))
	copy(logs, j.logs)
	return Snapshot{
		JobID:      j.id,
		Mode:       j.mode,
		Status:     j.status,
		Progress:   j.prog,
		Message:    j.msg,
		Logs:       logs,
		Stats:      j.stats,
		Error:      j.errMsg,
		StartedAt:  j.start,
		FinishedAt: j.end,
	}
}

// Log 追加一行日志。
func (j *Job) Log(line string) {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.logs = append(j.logs, line)
	if len(j.logs) > maxLogs {
		j.logs = j.logs[len(j.logs)-maxLogs:]
	}
	j.msg = line
}

// Report 更新进度。
func (j *Job) Report(current, total int, message string) {
	j.mu.Lock()
	defer j.mu.Unlock()
	p := Progress{Current: current, Total: total}
	if total > 0 {
		p.Percent = current * 100 / total
		if p.Percent > 100 {
			p.Percent = 100
		}
	}
	j.prog = p
	if message != "" {
		j.msg = message
		j.logs = append(j.logs, message)
		if len(j.logs) > maxLogs {
			j.logs = j.logs[len(j.logs)-maxLogs:]
		}
	}
}

// Manager 持有全部任务。
type Manager struct {
	mu     sync.RWMutex
	jobs   map[string]*Job
	order  []string
	max    int
	closed bool
	wg     sync.WaitGroup
}

// NewManager 创建任务管理器，max 是保留的历史任务数。
func NewManager(max int) *Manager {
	if max <= 0 {
		max = 20
	}
	return &Manager{jobs: map[string]*Job{}, max: max}
}

// Func 是任务体：拿到 ctx、日志函数与进度函数，返回结果统计。
type Func func(ctx context.Context, logf func(string), report func(current, total int, message string)) (any, error)

// Start 启动一个任务。同一时刻只允许一个任务运行（入库会重写整个索引，
// 并发跑只会互相覆盖），已有任务运行时返回冲突错误。
func (m *Manager) Start(mode string, fn Func) (*Job, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return nil, fmt.Errorf("服务正在关闭，无法开始任务")
	}
	if running := m.runningLocked(); running != nil {
		return nil, fmt.Errorf("已有入库任务在运行（job_id=%s），请等它结束或先取消", running.id)
	}

	ctx, cancel := context.WithCancel(context.Background())
	job := &Job{
		id:     "j_" + types.NewID()[:8],
		mode:   mode,
		status: StatusRunning,
		start:  time.Now(),
		cancel: cancel,
	}
	m.jobs[job.id] = job
	m.order = append(m.order, job.id)
	m.trimLocked()

	m.wg.Add(1)
	go func() {
		defer m.wg.Done()
		defer cancel()
		stats, err := fn(ctx, job.Log, job.Report)
		job.mu.Lock()
		defer job.mu.Unlock()
		now := time.Now()
		job.end = &now
		job.stats = stats
		switch {
		case err != nil && ctx.Err() != nil:
			job.status = StatusCancelled
			job.errMsg = "任务已取消"
		case err != nil:
			job.status = StatusFailed
			job.errMsg = err.Error()
		default:
			job.status = StatusSucceeded
		}
	}()
	return job, nil
}

// Shutdown stops accepting work, cancels active jobs, and waits for their writes.
func (m *Manager) Shutdown(ctx context.Context) error {
	m.mu.Lock()
	m.closed = true
	for _, job := range m.jobs {
		job.cancel()
	}
	m.mu.Unlock()
	done := make(chan struct{})
	go func() { m.wg.Wait(); close(done) }()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Get 按 ID 取任务。
func (m *Manager) Get(id string) (*Job, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	j, ok := m.jobs[id]
	return j, ok
}

// List 返回全部任务快照，最新的在前。
func (m *Manager) List() []Snapshot {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]Snapshot, 0, len(m.order))
	for i := len(m.order) - 1; i >= 0; i-- {
		out = append(out, m.jobs[m.order[i]].Snapshot())
	}
	return out
}

// Running 返回当前正在运行的任务，没有则返回 nil。
func (m *Manager) Running() *Job {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.runningLocked()
}

func (m *Manager) runningLocked() *Job {
	for _, id := range m.order {
		j := m.jobs[id]
		j.mu.Lock()
		running := j.status == StatusRunning
		j.mu.Unlock()
		if running {
			return j
		}
	}
	return nil
}

// Cancel 取消任务。返回 false 表示任务不存在或已经结束。
func (m *Manager) Cancel(id string) bool {
	m.mu.RLock()
	j, ok := m.jobs[id]
	m.mu.RUnlock()
	if !ok {
		return false
	}
	j.mu.Lock()
	running := j.status == StatusRunning
	j.mu.Unlock()
	if !running {
		return false
	}
	j.cancel()
	return true
}

func (m *Manager) trimLocked() {
	for len(m.order) > m.max {
		oldest := m.order[0]
		j := m.jobs[oldest]
		j.mu.Lock()
		finished := j.status != StatusRunning
		j.mu.Unlock()
		if !finished {
			return // 运行中的任务不能丢
		}
		delete(m.jobs, oldest)
		m.order = m.order[1:]
	}
}
