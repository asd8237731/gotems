package approval

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"
)

// ActionType 敏感操作类型
type ActionType string

const (
	ActionFileDelete    ActionType = "file_delete"     // 文件删除
	ActionGitPush       ActionType = "git_push"        // Git 推送
	ActionGitForce      ActionType = "git_force_push"  // Git 强制推送
	ActionExternalAPI   ActionType = "external_api"    // 外部 API 调用
	ActionDatabaseWrite ActionType = "database_write"  // 数据库写入
	ActionShellExec     ActionType = "shell_exec"      // Shell 命令执行
	ActionNetworkCall   ActionType = "network_call"    // 网络调用
)

// Request 审批请求
type Request struct {
	ID          string                 `json:"id"`
	AgentID     string                 `json:"agent_id"`
	ActionType  ActionType             `json:"action_type"`
	Description string                 `json:"description"`
	Details     map[string]interface{} `json:"details"` // 操作详情
	Timestamp   time.Time              `json:"timestamp"`
	Timeout     time.Duration          `json:"timeout"` // 审批超时时间
	Status      Status                 `json:"status"`
	Response    string                 `json:"response"` // 审批意见
	RespondedAt time.Time              `json:"responded_at"`
}

// Status 审批状态
type Status string

const (
	StatusPending  Status = "pending"  // 待审批
	StatusApproved Status = "approved" // 已批准
	StatusRejected Status = "rejected" // 已拒绝
	StatusTimeout  Status = "timeout"  // 超时
)

// Decision 审批决策
type Decision struct {
	Approved bool
	Reason   string
}

// Callback 审批回调函数
type Callback func(req *Request, decision Decision)

// Manager 审批管理器
type Manager struct {
	mu        sync.RWMutex
	requests  map[string]*Request // requestID -> Request
	pending   chan *Request       // 待审批队列
	responses map[string]chan Decision // requestID -> response channel
	callback  Callback            // 审批回调（可选）
	history   []*Request          // 审批历史
	maxHistory int                // 最大历史记录数
	logger    *slog.Logger
}

// NewManager 创建审批管理器
func NewManager(logger *slog.Logger) *Manager {
	return &Manager{
		requests:   make(map[string]*Request),
		pending:    make(chan *Request, 100),
		responses:  make(map[string]chan Decision),
		history:    make([]*Request, 0, 1000),
		maxHistory: 1000,
		logger:     logger,
	}
}

// SetCallback 设置审批回调
func (m *Manager) SetCallback(callback Callback) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.callback = callback
}

// SetMaxHistory 设置最大历史记录数
func (m *Manager) SetMaxHistory(max int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.maxHistory = max
}

// Request 提交审批请求（阻塞直到审批完成或超时）
func (m *Manager) RequestApproval(ctx context.Context, req *Request) (Decision, error) {
	if req.ID == "" {
		req.ID = fmt.Sprintf("approval-%d", time.Now().UnixNano())
	}
	if req.Timestamp.IsZero() {
		req.Timestamp = time.Now()
	}
	if req.Timeout == 0 {
		req.Timeout = 5 * time.Minute // 默认 5 分钟超时
	}
	req.Status = StatusPending

	// 创建响应 channel
	respCh := make(chan Decision, 1)
	m.mu.Lock()
	m.requests[req.ID] = req
	m.responses[req.ID] = respCh
	m.mu.Unlock()

	defer func() {
		m.mu.Lock()
		delete(m.responses, req.ID)
		m.mu.Unlock()
	}()

	// 发送到待审批队列
	select {
	case m.pending <- req:
		m.logger.Info("approval request submitted",
			"id", req.ID,
			"agent", req.AgentID,
			"action", req.ActionType,
			"description", req.Description,
		)
	case <-ctx.Done():
		return Decision{Approved: false, Reason: "context cancelled"}, ctx.Err()
	}

	// 等待审批或超时
	timeoutCtx, cancel := context.WithTimeout(ctx, req.Timeout)
	defer cancel()

	select {
	case decision := <-respCh:
		m.recordHistory(req, decision)
		return decision, nil
	case <-timeoutCtx.Done():
		// 超时自动拒绝
		decision := Decision{Approved: false, Reason: "approval timeout"}
		m.mu.Lock()
		req.Status = StatusTimeout
		req.Response = decision.Reason
		req.RespondedAt = time.Now()
		m.mu.Unlock()
		m.recordHistory(req, decision)
		m.logger.Warn("approval request timeout",
			"id", req.ID,
			"agent", req.AgentID,
			"action", req.ActionType,
		)
		return decision, fmt.Errorf("approval timeout after %v", req.Timeout)
	}
}

// Respond 响应审批请求
func (m *Manager) Respond(requestID string, decision Decision) error {
	m.mu.RLock()
	req, ok := m.requests[requestID]
	respCh, hasCh := m.responses[requestID]
	m.mu.RUnlock()

	if !ok {
		return fmt.Errorf("approval request not found: %s", requestID)
	}
	if !hasCh {
		return fmt.Errorf("approval request already responded or expired: %s", requestID)
	}

	m.mu.Lock()
	if decision.Approved {
		req.Status = StatusApproved
	} else {
		req.Status = StatusRejected
	}
	req.Response = decision.Reason
	req.RespondedAt = time.Now()
	m.mu.Unlock()

	// 发送决策
	select {
	case respCh <- decision:
		m.logger.Info("approval responded",
			"id", requestID,
			"approved", decision.Approved,
			"reason", decision.Reason,
		)
		// 触发回调
		m.mu.RLock()
		callback := m.callback
		m.mu.RUnlock()
		if callback != nil {
			go callback(req, decision)
		}
		return nil
	default:
		return fmt.Errorf("failed to send decision to request: %s", requestID)
	}
}

// Pending 返回待审批请求的 channel（用于 UI 轮询）
func (m *Manager) Pending() <-chan *Request {
	return m.pending
}

// PendingList 返回当前所有待审批请求
func (m *Manager) PendingList() []*Request {
	m.mu.RLock()
	defer m.mu.RUnlock()
	var pending []*Request
	for _, req := range m.requests {
		if req.Status == StatusPending {
			pending = append(pending, req)
		}
	}
	return pending
}

// History 返回审批历史（最近 N 条）
func (m *Manager) History(limit int) []*Request {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if limit == 0 || limit > len(m.history) {
		limit = len(m.history)
	}
	// 返回最近的记录
	start := len(m.history) - limit
	result := make([]*Request, limit)
	copy(result, m.history[start:])
	return result
}

// Get 获取指定审批请求
func (m *Manager) Get(requestID string) (*Request, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	req, ok := m.requests[requestID]
	return req, ok
}

// recordHistory 记录审批历史
func (m *Manager) recordHistory(req *Request, decision Decision) {
	m.mu.Lock()
	defer m.mu.Unlock()

	// 创建副本
	historyCopy := *req
	m.history = append(m.history, &historyCopy)

	// 清理旧历史
	if len(m.history) > m.maxHistory {
		keep := int(float64(m.maxHistory) * 0.8)
		m.history = m.history[len(m.history)-keep:]
	}

	// 从 requests 中移除（已完成）
	delete(m.requests, req.ID)
}

// Stats 返回审批统计
type Stats struct {
	TotalRequests int `json:"total_requests"`
	Approved      int `json:"approved"`
	Rejected      int `json:"rejected"`
	Timeout       int `json:"timeout"`
	Pending       int `json:"pending"`
}

// Stats 返回审批统计信息
func (m *Manager) Stats() Stats {
	m.mu.RLock()
	defer m.mu.RUnlock()

	stats := Stats{}
	for _, req := range m.history {
		stats.TotalRequests++
		switch req.Status {
		case StatusApproved:
			stats.Approved++
		case StatusRejected:
			stats.Rejected++
		case StatusTimeout:
			stats.Timeout++
		}
	}
	for _, req := range m.requests {
		if req.Status == StatusPending {
			stats.Pending++
			stats.TotalRequests++
		}
	}
	return stats
}
