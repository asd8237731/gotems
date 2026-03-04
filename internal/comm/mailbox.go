package comm

import (
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"

	"github.com/lyymini/gotems/pkg/schema"
)

// Mailbox 实现 Agent 之间的消息通信系统
type Mailbox struct {
	mu        sync.RWMutex
	boxes     map[string]chan *schema.Message // agentID -> inbox
	broadcast chan *schema.Message
	subs      []chan *schema.Message // 广播订阅者
	closed    atomic.Bool
	logger    *slog.Logger
}

// NewMailbox 创建邮箱系统
func NewMailbox(logger *slog.Logger) *Mailbox {
	m := &Mailbox{
		boxes:     make(map[string]chan *schema.Message),
		broadcast: make(chan *schema.Message, 100),
		logger:    logger,
	}
	go m.broadcastLoop()
	return m
}

// Register 为指定 Agent 创建收件箱
func (m *Mailbox) Register(agentID string) <-chan *schema.Message {
	m.mu.Lock()
	defer m.mu.Unlock()
	ch := make(chan *schema.Message, 50)
	m.boxes[agentID] = ch
	m.subs = append(m.subs, ch)
	m.logger.Info("mailbox registered", "agent_id", agentID)
	return ch
}

// Unregister 移除指定 Agent 的收件箱
func (m *Mailbox) Unregister(agentID string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if ch, ok := m.boxes[agentID]; ok {
		close(ch)
		delete(m.boxes, agentID)
		// 从订阅列表移除
		newSubs := make([]chan *schema.Message, 0, len(m.subs)-1)
		for _, s := range m.subs {
			if s != ch {
				newSubs = append(newSubs, s)
			}
		}
		m.subs = newSubs
	}
}

// Send 向指定 Agent 发送消息
func (m *Mailbox) Send(msg *schema.Message) error {
	if m.closed.Load() {
		return fmt.Errorf("mailbox is closed")
	}
	if msg.To == "*" {
		return m.Broadcast(msg)
	}
	m.mu.RLock()
	box, ok := m.boxes[msg.To]
	m.mu.RUnlock()
	if !ok {
		return fmt.Errorf("agent %s not found in mailbox", msg.To)
	}
	select {
	case box <- msg:
		m.logger.Debug("message sent", "from", msg.From, "to", msg.To, "type", msg.Type)
	default:
		return fmt.Errorf("agent %s inbox full", msg.To)
	}
	return nil
}

// Broadcast 向所有 Agent 广播消息
func (m *Mailbox) Broadcast(msg *schema.Message) error {
	if m.closed.Load() {
		return fmt.Errorf("mailbox is closed")
	}
	msg.To = "*"
	select {
	case m.broadcast <- msg:
		return nil
	default:
		return fmt.Errorf("broadcast channel full")
	}
}

// broadcastLoop 持续将广播消息分发到所有订阅者
func (m *Mailbox) broadcastLoop() {
	for msg := range m.broadcast {
		m.mu.RLock()
		for agentID, ch := range m.boxes {
			if agentID == msg.From {
				continue // 不发给自己
			}
			select {
			case ch <- msg:
			default:
				m.logger.Warn("broadcast dropped for full inbox", "agent_id", agentID)
			}
		}
		m.mu.RUnlock()
	}
}

// Close 关闭邮箱系统
func (m *Mailbox) Close() {
	if m.closed.Swap(true) {
		return // 已经关闭过
	}
	close(m.broadcast)
	m.mu.Lock()
	defer m.mu.Unlock()
	for id, ch := range m.boxes {
		close(ch)
		delete(m.boxes, id)
	}
}
