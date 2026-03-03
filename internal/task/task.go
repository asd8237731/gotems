package task

import (
	"sync"
	"time"
)

// Priority 任务优先级
type Priority int

const (
	PriorityLow    Priority = 0
	PriorityNormal Priority = 1
	PriorityHigh   Priority = 2
	PriorityCritical Priority = 3
)

// TaskStatus 任务状态
type TaskStatus int

const (
	TaskPending    TaskStatus = iota // 待分配
	TaskAssigned                     // 已分配
	TaskRunning                      // 执行中
	TaskCompleted                    // 已完成
	TaskFailed                       // 失败
	TaskCancelled                    // 已取消
)

func (s TaskStatus) String() string {
	switch s {
	case TaskPending:
		return "pending"
	case TaskAssigned:
		return "assigned"
	case TaskRunning:
		return "running"
	case TaskCompleted:
		return "completed"
	case TaskFailed:
		return "failed"
	case TaskCancelled:
		return "cancelled"
	default:
		return "unknown"
	}
}

// Task 代表一个待执行的编码任务
type Task struct {
	ID           string            `json:"id"`
	Prompt       string            `json:"prompt"`
	WorkDir      string            `json:"work_dir"`
	Priority     Priority          `json:"priority"`
	Status       TaskStatus        `json:"status"`
	AssignedTo   string            `json:"assigned_to,omitempty"`
	DependsOn    []string          `json:"depends_on,omitempty"`
	Tags         []string          `json:"tags,omitempty"`
	Metadata     map[string]any    `json:"metadata,omitempty"`
	Result       string            `json:"result,omitempty"`
	Error        string            `json:"error,omitempty"`
	CreatedAt    time.Time         `json:"created_at"`
	StartedAt    *time.Time        `json:"started_at,omitempty"`
	CompletedAt  *time.Time        `json:"completed_at,omitempty"`
}

// Pool 是一个共享任务池，支持多个 Agent 并发操作
type Pool struct {
	mu      sync.RWMutex
	tasks   map[string]*Task
	order   []string // 按插入顺序维护 ID 列表
	notify  chan string
}

// NewPool 创建任务池
func NewPool() *Pool {
	return &Pool{
		tasks:  make(map[string]*Task),
		notify: make(chan string, 100),
	}
}

// Add 添加任务到池中
func (p *Pool) Add(t *Task) {
	p.mu.Lock()
	defer p.mu.Unlock()
	t.CreatedAt = time.Now()
	t.Status = TaskPending
	p.tasks[t.ID] = t
	p.order = append(p.order, t.ID)
	// 非阻塞通知
	select {
	case p.notify <- t.ID:
	default:
	}
}

// Claim 领取一个待分配的任务
func (p *Pool) Claim(agentID string) *Task {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, id := range p.order {
		t := p.tasks[id]
		if t.Status == TaskPending && p.depsResolved(t) {
			t.Status = TaskAssigned
			t.AssignedTo = agentID
			now := time.Now()
			t.StartedAt = &now
			return t
		}
	}
	return nil
}

// Complete 标记任务完成
func (p *Pool) Complete(taskID string, result string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if t, ok := p.tasks[taskID]; ok {
		t.Status = TaskCompleted
		t.Result = result
		now := time.Now()
		t.CompletedAt = &now
	}
}

// Fail 标记任务失败
func (p *Pool) Fail(taskID string, errMsg string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if t, ok := p.tasks[taskID]; ok {
		t.Status = TaskFailed
		t.Error = errMsg
		now := time.Now()
		t.CompletedAt = &now
	}
}

// Get 获取指定任务
func (p *Pool) Get(taskID string) *Task {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.tasks[taskID]
}

// All 返回所有任务（副本）
func (p *Pool) All() []*Task {
	p.mu.RLock()
	defer p.mu.RUnlock()
	result := make([]*Task, 0, len(p.order))
	for _, id := range p.order {
		result = append(result, p.tasks[id])
	}
	return result
}

// Pending 返回所有待处理任务数
func (p *Pool) Pending() int {
	p.mu.RLock()
	defer p.mu.RUnlock()
	count := 0
	for _, t := range p.tasks {
		if t.Status == TaskPending {
			count++
		}
	}
	return count
}

// Notify 返回新任务通知 channel
func (p *Pool) Notify() <-chan string {
	return p.notify
}

// depsResolved 检查依赖是否已完成（需持有锁）
func (p *Pool) depsResolved(t *Task) bool {
	for _, depID := range t.DependsOn {
		dep, ok := p.tasks[depID]
		if !ok || dep.Status != TaskCompleted {
			return false
		}
	}
	return true
}
