package comm

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/lyymini/gotems/pkg/schema"
)

// Negotiator 实现 Agent 间基于 session 的多轮协商协议
// 支持 Question/Answer 模式：一个 Agent 向另一个提问，等待回答后继续
type Negotiator struct {
	mu       sync.RWMutex
	mailbox  *Mailbox
	pending  map[string]chan *schema.Message // questionID -> answer channel
	timeout  time.Duration
	logger   *slog.Logger
}

// NewNegotiator 创建协商器
func NewNegotiator(mailbox *Mailbox, logger *slog.Logger) *Negotiator {
	return &Negotiator{
		mailbox: mailbox,
		pending: make(map[string]chan *schema.Message),
		timeout: 30 * time.Second,
		logger:  logger,
	}
}

// SetTimeout 设置协商超时时间
func (n *Negotiator) SetTimeout(d time.Duration) {
	n.timeout = d
}

// Ask 向目标 Agent 发送问题并等待回答
// 阻塞直到收到回答、超时或 context 取消
func (n *Negotiator) Ask(ctx context.Context, from, to, question string) (string, error) {
	questionID := fmt.Sprintf("q-%s-%s-%d", from, to, time.Now().UnixNano())

	// 创建回答等待 channel
	answerCh := make(chan *schema.Message, 1)
	n.mu.Lock()
	n.pending[questionID] = answerCh
	n.mu.Unlock()

	defer func() {
		n.mu.Lock()
		delete(n.pending, questionID)
		n.mu.Unlock()
	}()

	// 发送问题
	msg := &schema.Message{
		ID:      questionID,
		From:    from,
		To:      to,
		Type:    schema.MsgQuestion,
		Content: question,
		Metadata: map[string]any{
			"question_id": questionID,
		},
		Timestamp: time.Now(),
	}

	if err := n.mailbox.Send(msg); err != nil {
		return "", fmt.Errorf("send question: %w", err)
	}

	n.logger.Info("negotiation: question sent",
		"from", from,
		"to", to,
		"question_id", questionID,
	)

	// 等待回答
	timeoutCtx, cancel := context.WithTimeout(ctx, n.timeout)
	defer cancel()

	select {
	case answer := <-answerCh:
		n.logger.Info("negotiation: answer received",
			"from", answer.From,
			"question_id", questionID,
		)
		return answer.Content, nil
	case <-timeoutCtx.Done():
		return "", fmt.Errorf("negotiation timeout: %s -> %s (question_id=%s)", from, to, questionID)
	}
}

// Answer 回答一个问题
func (n *Negotiator) Answer(from, questionID, answer string) error {
	n.mu.RLock()
	ch, ok := n.pending[questionID]
	n.mu.RUnlock()

	if !ok {
		return fmt.Errorf("no pending question: %s", questionID)
	}

	msg := &schema.Message{
		ID:      fmt.Sprintf("a-%s-%d", questionID, time.Now().UnixNano()),
		From:    from,
		To:      "",
		Type:    schema.MsgAnswer,
		Content: answer,
		Metadata: map[string]any{
			"question_id": questionID,
		},
		Timestamp: time.Now(),
	}

	select {
	case ch <- msg:
		n.logger.Info("negotiation: answer delivered",
			"from", from,
			"question_id", questionID,
		)
		return nil
	default:
		return fmt.Errorf("answer channel full for question: %s", questionID)
	}
}

// PendingQuestions 返回待回答的问题数
func (n *Negotiator) PendingQuestions() int {
	n.mu.RLock()
	defer n.mu.RUnlock()
	return len(n.pending)
}
