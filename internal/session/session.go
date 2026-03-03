package session

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Session 表示一个 Agent 的会话状态
type Session struct {
	ID        string            `json:"id"`
	AgentID   string            `json:"agent_id"`
	Provider  string            `json:"provider"`
	SessionID string            `json:"session_id"` // CLI 工具返回的 session ID
	WorkDir   string            `json:"work_dir"`
	History   []Turn            `json:"history"`
	Metadata  map[string]any    `json:"metadata,omitempty"`
	CreatedAt time.Time         `json:"created_at"`
	UpdatedAt time.Time         `json:"updated_at"`
}

// Turn 一轮对话
type Turn struct {
	Role      string    `json:"role"`    // "user" 或 "assistant"
	Content   string    `json:"content"`
	TokensIn  int       `json:"tokens_in,omitempty"`
	TokensOut int       `json:"tokens_out,omitempty"`
	Cost      float64   `json:"cost,omitempty"`
	Timestamp time.Time `json:"timestamp"`
}

// Store 会话持久化存储
type Store struct {
	mu       sync.RWMutex
	sessions map[string]*Session // sessionKey -> Session
	dir      string             // 持久化目录
}

// NewStore 创建会话存储
func NewStore(dir string) *Store {
	if dir == "" {
		dir = filepath.Join(os.TempDir(), "gotems-sessions")
	}
	_ = os.MkdirAll(dir, 0755)
	return &Store{
		sessions: make(map[string]*Session),
		dir:      dir,
	}
}

// sessionKey 用 agentID + workDir 唯一标识一个会话
func sessionKey(agentID, workDir string) string {
	return agentID + ":" + workDir
}

// Create 创建新会话
func (s *Store) Create(agentID, provider, workDir string) *Session {
	s.mu.Lock()
	defer s.mu.Unlock()

	key := sessionKey(agentID, workDir)
	sess := &Session{
		ID:        fmt.Sprintf("sess-%d", time.Now().UnixNano()),
		AgentID:   agentID,
		Provider:  provider,
		WorkDir:   workDir,
		History:   make([]Turn, 0),
		Metadata:  make(map[string]any),
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}

	s.sessions[key] = sess
	return sess
}

// Get 获取会话（如果存在）
func (s *Store) Get(agentID, workDir string) *Session {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.sessions[sessionKey(agentID, workDir)]
}

// GetOrCreate 获取或创建会话
func (s *Store) GetOrCreate(agentID, provider, workDir string) *Session {
	if sess := s.Get(agentID, workDir); sess != nil {
		return sess
	}
	return s.Create(agentID, provider, workDir)
}

// UpdateSessionID 更新 CLI 工具返回的 session ID
func (s *Store) UpdateSessionID(agentID, workDir, sessionID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := sessionKey(agentID, workDir)
	if sess, ok := s.sessions[key]; ok {
		sess.SessionID = sessionID
		sess.UpdatedAt = time.Now()
	}
}

// AppendTurn 追加一轮对话
func (s *Store) AppendTurn(agentID, workDir string, turn Turn) {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := sessionKey(agentID, workDir)
	if sess, ok := s.sessions[key]; ok {
		turn.Timestamp = time.Now()
		sess.History = append(sess.History, turn)
		sess.UpdatedAt = time.Now()
	}
}

// Save 持久化所有会话到磁盘
func (s *Store) Save() error {
	s.mu.RLock()
	defer s.mu.RUnlock()

	for _, sess := range s.sessions {
		data, err := json.MarshalIndent(sess, "", "  ")
		if err != nil {
			return fmt.Errorf("marshal session %s: %w", sess.ID, err)
		}

		path := filepath.Join(s.dir, sess.ID+".json")
		if err := os.WriteFile(path, data, 0644); err != nil {
			return fmt.Errorf("write session %s: %w", sess.ID, err)
		}
	}
	return nil
}

// Load 从磁盘恢复所有会话
func (s *Store) Load() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	entries, err := os.ReadDir(s.dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("read session dir: %w", err)
	}

	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}

		data, err := os.ReadFile(filepath.Join(s.dir, entry.Name()))
		if err != nil {
			continue
		}

		var sess Session
		if err := json.Unmarshal(data, &sess); err != nil {
			continue
		}

		key := sessionKey(sess.AgentID, sess.WorkDir)
		s.sessions[key] = &sess
	}
	return nil
}

// Delete 删除会话
func (s *Store) Delete(agentID, workDir string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := sessionKey(agentID, workDir)
	if sess, ok := s.sessions[key]; ok {
		// 删除磁盘文件
		_ = os.Remove(filepath.Join(s.dir, sess.ID+".json"))
		delete(s.sessions, key)
	}
}

// All 返回所有会话
func (s *Store) All() []*Session {
	s.mu.RLock()
	defer s.mu.RUnlock()
	result := make([]*Session, 0, len(s.sessions))
	for _, sess := range s.sessions {
		result = append(result, sess)
	}
	return result
}

// Cleanup 清除过期会话（超过 maxAge）
func (s *Store) Cleanup(maxAge time.Duration) int {
	s.mu.Lock()
	defer s.mu.Unlock()

	cutoff := time.Now().Add(-maxAge)
	count := 0

	for key, sess := range s.sessions {
		if sess.UpdatedAt.Before(cutoff) {
			_ = os.Remove(filepath.Join(s.dir, sess.ID+".json"))
			delete(s.sessions, key)
			count++
		}
	}
	return count
}
