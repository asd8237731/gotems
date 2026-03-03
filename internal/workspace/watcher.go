package workspace

import (
	"context"
	"log/slog"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// FileEvent 文件变更事件
type FileEvent struct {
	Path      string    `json:"path"`
	Op        string    `json:"op"` // "create", "write", "remove", "rename"
	AgentID   string    `json:"agent_id"`
	Timestamp time.Time `json:"timestamp"`
}

// Watcher 监控多个 worktree 中的文件变更
// 使用轮询方式代替 fsnotify，避免引入额外依赖
type Watcher struct {
	mu        sync.RWMutex
	workspace *Workspace
	interval  time.Duration
	events    chan FileEvent
	logger    *slog.Logger

	// 文件快照：agentID -> relPath -> modTime
	snapshots map[string]map[string]time.Time
}

// NewWatcher 创建文件变更监控器
func NewWatcher(ws *Workspace, interval time.Duration, logger *slog.Logger) *Watcher {
	if interval == 0 {
		interval = 2 * time.Second
	}
	return &Watcher{
		workspace: ws,
		interval:  interval,
		events:    make(chan FileEvent, 256),
		snapshots: make(map[string]map[string]time.Time),
		logger:    logger,
	}
}

// Events 返回文件变更事件 channel
func (w *Watcher) Events() <-chan FileEvent {
	return w.events
}

// Start 启动文件监控（在后台轮询）
func (w *Watcher) Start(ctx context.Context) {
	go func() {
		ticker := time.NewTicker(w.interval)
		defer ticker.Stop()
		defer close(w.events)

		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				w.poll()
			}
		}
	}()
}

// poll 轮询所有 worktree 的文件变更
func (w *Watcher) poll() {
	w.workspace.mu.RLock()
	worktrees := make(map[string]*Worktree, len(w.workspace.worktrees))
	for id, wt := range w.workspace.worktrees {
		worktrees[id] = wt
	}
	w.workspace.mu.RUnlock()

	for agentID, wt := range worktrees {
		if !wt.Active {
			continue
		}
		w.pollWorktree(agentID, wt.Path)
	}
}

// pollWorktree 使用 git status 检测单个 worktree 的变更
func (w *Watcher) pollWorktree(agentID string, path string) {
	// 使用 git diff 检测变更，比遍历文件系统更高效
	files, err := w.workspace.ChangedFiles(agentID)
	if err != nil {
		return
	}

	w.mu.Lock()
	defer w.mu.Unlock()

	oldSnap := w.snapshots[agentID]
	if oldSnap == nil {
		oldSnap = make(map[string]time.Time)
	}

	newSnap := make(map[string]time.Time)
	now := time.Now()

	for _, file := range files {
		relPath := file
		if !filepath.IsAbs(file) {
			relPath = file
		}

		newSnap[relPath] = now

		if _, existed := oldSnap[relPath]; !existed {
			// 新文件或新变更
			op := "write"
			if !strings.Contains(file, "/") {
				op = "create"
			}
			select {
			case w.events <- FileEvent{
				Path:      relPath,
				Op:        op,
				AgentID:   agentID,
				Timestamp: now,
			}:
			default:
				w.logger.Warn("watcher event channel full, dropping event")
			}
		}
	}

	// 检测已删除的文件
	for relPath := range oldSnap {
		if _, exists := newSnap[relPath]; !exists {
			select {
			case w.events <- FileEvent{
				Path:      relPath,
				Op:        "remove",
				AgentID:   agentID,
				Timestamp: now,
			}:
			default:
			}
		}
	}

	w.snapshots[agentID] = newSnap
}

// ConflictDetector 冲突检测器
type ConflictDetector struct {
	logger *slog.Logger
}

// Conflict 文件冲突
type Conflict struct {
	File    string   `json:"file"`
	Agents  []string `json:"agents"`  // 涉及的 Agent
	Message string   `json:"message"`
}

// NewConflictDetector 创建冲突检测器
func NewConflictDetector(logger *slog.Logger) *ConflictDetector {
	return &ConflictDetector{logger: logger}
}

// Detect 从文件变更事件中检测冲突（多个 Agent 修改同一文件）
func (d *ConflictDetector) Detect(events []FileEvent) []Conflict {
	// 按文件分组
	fileAgents := make(map[string]map[string]bool)
	for _, e := range events {
		if e.Op == "write" || e.Op == "create" {
			if fileAgents[e.Path] == nil {
				fileAgents[e.Path] = make(map[string]bool)
			}
			fileAgents[e.Path][e.AgentID] = true
		}
	}

	var conflicts []Conflict
	for file, agents := range fileAgents {
		if len(agents) > 1 {
			agentList := make([]string, 0, len(agents))
			for a := range agents {
				agentList = append(agentList, a)
			}
			conflicts = append(conflicts, Conflict{
				File:    file,
				Agents:  agentList,
				Message: "multiple agents modified the same file",
			})
			d.logger.Warn("file conflict detected",
				"file", file,
				"agents", agentList,
			)
		}
	}

	return conflicts
}
