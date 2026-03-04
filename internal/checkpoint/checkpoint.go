package checkpoint

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/lyymini/gotems/pkg/schema"
)

// Checkpoint 检查点数据结构
type Checkpoint struct {
	ID            string                 `json:"id"`
	AgentID       string                 `json:"agent_id"`
	TaskID        string                 `json:"task_id"`
	State         State                  `json:"state"`
	IntermediateResults []*schema.Result `json:"intermediate_results"`
	Context       map[string]interface{} `json:"context"` // 任务上下文
	Metadata      map[string]interface{} `json:"metadata"`
	Timestamp     time.Time              `json:"timestamp"`
	Version       int                    `json:"version"` // 检查点版本号
}

// State 任务执行状态
type State string

const (
	StateInitialized State = "initialized" // 已初始化
	StateRunning     State = "running"     // 运行中
	StatePartial     State = "partial"     // 部分完成
	StateCompleted   State = "completed"   // 已完成
	StateFailed      State = "failed"      // 失败
)

// Manager 检查点管理器
type Manager struct {
	mu             sync.RWMutex
	dir            string                    // 检查点存储目录
	checkpoints    map[string]*Checkpoint    // taskID -> latest checkpoint
	autoSaveCount  int                       // 每 N 个任务自动保存
	autoSaveInterval time.Duration           // 每 M 分钟自动保存
	taskCounters   map[string]int            // taskID -> 操作计数
	lastSave       map[string]time.Time      // taskID -> 上次保存时间
	retentionDays  int                       // 检查点保留天数
	logger         *slog.Logger
}

// NewManager 创建检查点管理器
func NewManager(dir string, logger *slog.Logger) (*Manager, error) {
	if err := os.MkdirAll(dir, 0755); err != nil {
		return nil, fmt.Errorf("create checkpoint dir: %w", err)
	}

	return &Manager{
		dir:              dir,
		checkpoints:      make(map[string]*Checkpoint),
		autoSaveCount:    10,  // 默认每 10 个操作保存一次
		autoSaveInterval: 5 * time.Minute, // 默认每 5 分钟保存一次
		taskCounters:     make(map[string]int),
		lastSave:         make(map[string]time.Time),
		retentionDays:    7, // 默认保留 7 天
		logger:           logger,
	}, nil
}

// SetAutoSave 设置自动保存策略
func (m *Manager) SetAutoSave(count int, interval time.Duration) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.autoSaveCount = count
	m.autoSaveInterval = interval
}

// SetRetention 设置检查点保留天数
func (m *Manager) SetRetention(days int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.retentionDays = days
}

// Save 保存检查点
func (m *Manager) Save(ctx context.Context, cp *Checkpoint) error {
	if cp.ID == "" {
		cp.ID = fmt.Sprintf("cp-%s-%d", cp.TaskID, time.Now().UnixNano())
	}
	if cp.Timestamp.IsZero() {
		cp.Timestamp = time.Now()
	}

	m.mu.Lock()
	// 增加版本号
	if existing, ok := m.checkpoints[cp.TaskID]; ok {
		cp.Version = existing.Version + 1
	} else {
		cp.Version = 1
	}
	m.checkpoints[cp.TaskID] = cp
	m.lastSave[cp.TaskID] = time.Now()
	m.mu.Unlock()

	// 持久化到磁盘
	filename := fmt.Sprintf("%s-v%d.json", cp.TaskID, cp.Version)
	path := filepath.Join(m.dir, filename)

	data, err := json.MarshalIndent(cp, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal checkpoint: %w", err)
	}

	if err := os.WriteFile(path, data, 0644); err != nil {
		return fmt.Errorf("write checkpoint: %w", err)
	}

	m.logger.Info("checkpoint saved",
		"id", cp.ID,
		"task_id", cp.TaskID,
		"agent_id", cp.AgentID,
		"state", cp.State,
		"version", cp.Version,
	)
	return nil
}

// AutoSave 根据策略自动保存检查点
func (m *Manager) AutoSave(ctx context.Context, cp *Checkpoint) error {
	m.mu.Lock()
	counter := m.taskCounters[cp.TaskID]
	counter++
	m.taskCounters[cp.TaskID] = counter
	lastSave := m.lastSave[cp.TaskID]
	m.mu.Unlock()

	// 检查是否需要保存
	shouldSave := false
	if m.autoSaveCount > 0 && counter >= m.autoSaveCount {
		shouldSave = true
		m.mu.Lock()
		m.taskCounters[cp.TaskID] = 0 // 重置计数器
		m.mu.Unlock()
	}
	if m.autoSaveInterval > 0 && time.Since(lastSave) >= m.autoSaveInterval {
		shouldSave = true
	}

	if shouldSave {
		return m.Save(ctx, cp)
	}
	return nil
}

// Load 加载最新的检查点
func (m *Manager) Load(taskID string) (*Checkpoint, error) {
	m.mu.RLock()
	if cp, ok := m.checkpoints[taskID]; ok {
		m.mu.RUnlock()
		return cp, nil
	}
	m.mu.RUnlock()

	// 从磁盘加载
	pattern := filepath.Join(m.dir, fmt.Sprintf("%s-v*.json", taskID))
	matches, err := filepath.Glob(pattern)
	if err != nil {
		return nil, fmt.Errorf("glob checkpoints: %w", err)
	}
	if len(matches) == 0 {
		return nil, fmt.Errorf("no checkpoint found for task: %s", taskID)
	}

	// 加载最新版本（文件名按版本号排序，取最后一个）
	latestFile := matches[len(matches)-1]
	data, err := os.ReadFile(latestFile)
	if err != nil {
		return nil, fmt.Errorf("read checkpoint: %w", err)
	}

	var cp Checkpoint
	if err := json.Unmarshal(data, &cp); err != nil {
		return nil, fmt.Errorf("unmarshal checkpoint: %w", err)
	}

	m.mu.Lock()
	m.checkpoints[taskID] = &cp
	m.mu.Unlock()

	m.logger.Info("checkpoint loaded",
		"id", cp.ID,
		"task_id", cp.TaskID,
		"version", cp.Version,
	)
	return &cp, nil
}

// LoadAll 加载所有检查点
func (m *Manager) LoadAll() error {
	pattern := filepath.Join(m.dir, "*.json")
	matches, err := filepath.Glob(pattern)
	if err != nil {
		return fmt.Errorf("glob checkpoints: %w", err)
	}

	// 按 taskID 分组，只保留最新版本
	taskFiles := make(map[string]string) // taskID -> latest file
	for _, file := range matches {
		data, err := os.ReadFile(file)
		if err != nil {
			continue
		}
		var cp Checkpoint
		if err := json.Unmarshal(data, &cp); err != nil {
			continue
		}

		// 检查是否是更新的版本
		if existing, ok := taskFiles[cp.TaskID]; ok {
			var existingCP Checkpoint
			existingData, _ := os.ReadFile(existing)
			json.Unmarshal(existingData, &existingCP)
			if cp.Version > existingCP.Version {
				taskFiles[cp.TaskID] = file
			}
		} else {
			taskFiles[cp.TaskID] = file
		}
	}

	// 加载最新版本
	m.mu.Lock()
	defer m.mu.Unlock()
	for taskID, file := range taskFiles {
		data, err := os.ReadFile(file)
		if err != nil {
			continue
		}
		var cp Checkpoint
		if err := json.Unmarshal(data, &cp); err != nil {
			continue
		}
		m.checkpoints[taskID] = &cp
	}

	m.logger.Info("checkpoints loaded", "count", len(m.checkpoints))
	return nil
}

// Delete 删除指定任务的检查点
func (m *Manager) Delete(taskID string) error {
	m.mu.Lock()
	delete(m.checkpoints, taskID)
	delete(m.taskCounters, taskID)
	delete(m.lastSave, taskID)
	m.mu.Unlock()

	// 删除磁盘文件
	pattern := filepath.Join(m.dir, fmt.Sprintf("%s-v*.json", taskID))
	matches, err := filepath.Glob(pattern)
	if err != nil {
		return fmt.Errorf("glob checkpoints: %w", err)
	}

	for _, file := range matches {
		if err := os.Remove(file); err != nil {
			m.logger.Warn("failed to remove checkpoint file", "file", file, "error", err)
		}
	}

	m.logger.Info("checkpoint deleted", "task_id", taskID)
	return nil
}

// Cleanup 清理过期的检查点
func (m *Manager) Cleanup() error {
	m.mu.RLock()
	retentionDays := m.retentionDays
	m.mu.RUnlock()

	cutoff := time.Now().AddDate(0, 0, -retentionDays)
	pattern := filepath.Join(m.dir, "*.json")
	matches, err := filepath.Glob(pattern)
	if err != nil {
		return fmt.Errorf("glob checkpoints: %w", err)
	}

	removed := 0
	for _, file := range matches {
		info, err := os.Stat(file)
		if err != nil {
			continue
		}
		if info.ModTime().Before(cutoff) {
			if err := os.Remove(file); err != nil {
				m.logger.Warn("failed to remove old checkpoint", "file", file, "error", err)
			} else {
				removed++
			}
		}
	}

	m.logger.Info("checkpoint cleanup completed", "removed", removed)
	return nil
}

// List 列出所有检查点
func (m *Manager) List() []*Checkpoint {
	m.mu.RLock()
	defer m.mu.RUnlock()
	result := make([]*Checkpoint, 0, len(m.checkpoints))
	for _, cp := range m.checkpoints {
		result = append(result, cp)
	}
	return result
}

// Get 获取指定任务的检查点
func (m *Manager) Get(taskID string) (*Checkpoint, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	cp, ok := m.checkpoints[taskID]
	return cp, ok
}

// Stats 返回检查点统计信息
type Stats struct {
	TotalCheckpoints int            `json:"total_checkpoints"`
	ByState          map[State]int  `json:"by_state"`
	OldestTimestamp  time.Time      `json:"oldest_timestamp"`
	NewestTimestamp  time.Time      `json:"newest_timestamp"`
}

// Stats 返回检查点统计
func (m *Manager) Stats() Stats {
	m.mu.RLock()
	defer m.mu.RUnlock()

	stats := Stats{
		TotalCheckpoints: len(m.checkpoints),
		ByState:          make(map[State]int),
	}

	for _, cp := range m.checkpoints {
		stats.ByState[cp.State]++
		if stats.OldestTimestamp.IsZero() || cp.Timestamp.Before(stats.OldestTimestamp) {
			stats.OldestTimestamp = cp.Timestamp
		}
		if stats.NewestTimestamp.IsZero() || cp.Timestamp.After(stats.NewestTimestamp) {
			stats.NewestTimestamp = cp.Timestamp
		}
	}

	return stats
}
