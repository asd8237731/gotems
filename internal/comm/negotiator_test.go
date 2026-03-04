package comm

import (
	"context"
	"log/slog"
	"os"
	"testing"
	"time"
)

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
}

func TestNegotiator_AskAnswer(t *testing.T) {
	logger := testLogger()
	mailbox := NewMailbox(logger)
	mailbox.Register("agent-a")
	mailbox.Register("agent-b")
	neg := NewNegotiator(mailbox, logger)
	neg.SetTimeout(2 * time.Second)

	// 模拟 agent-b 在后台回答
	go func() {
		time.Sleep(100 * time.Millisecond)
		// 获取 pending questions 的 ID
		neg.mu.RLock()
		var qid string
		for id := range neg.pending {
			qid = id
			break
		}
		neg.mu.RUnlock()

		if qid != "" {
			_ = neg.Answer("agent-b", qid, "API 返回 JSON 格式")
		}
	}()

	answer, err := neg.Ask(context.Background(), "agent-a", "agent-b", "API 格式是什么？")
	if err != nil {
		t.Fatalf("Ask: %v", err)
	}
	if answer != "API 返回 JSON 格式" {
		t.Errorf("got answer=%q, want 'API 返回 JSON 格式'", answer)
	}
}

func TestNegotiator_Timeout(t *testing.T) {
	logger := testLogger()
	mailbox := NewMailbox(logger)
	mailbox.Register("agent-a")
	mailbox.Register("agent-b")
	neg := NewNegotiator(mailbox, logger)
	neg.SetTimeout(100 * time.Millisecond)

	_, err := neg.Ask(context.Background(), "agent-a", "agent-b", "无人回答的问题")
	if err == nil {
		t.Fatal("expected timeout error")
	}
}

func TestNegotiator_AnswerNonExistent(t *testing.T) {
	logger := testLogger()
	mailbox := NewMailbox(logger)
	neg := NewNegotiator(mailbox, logger)

	err := neg.Answer("agent-b", "non-existent-question", "回答")
	if err == nil {
		t.Fatal("expected error for non-existent question")
	}
}

func TestNegotiator_PendingQuestions(t *testing.T) {
	logger := testLogger()
	mailbox := NewMailbox(logger)
	mailbox.Register("agent-a")
	mailbox.Register("agent-b")
	neg := NewNegotiator(mailbox, logger)
	neg.SetTimeout(500 * time.Millisecond)

	if neg.PendingQuestions() != 0 {
		t.Errorf("expected 0 pending, got %d", neg.PendingQuestions())
	}

	// 发起一个问题（会超时，但我们在超时前检查 pending 数）
	go func() {
		_, _ = neg.Ask(context.Background(), "agent-a", "agent-b", "test question")
	}()

	time.Sleep(50 * time.Millisecond)
	if neg.PendingQuestions() != 1 {
		t.Errorf("expected 1 pending, got %d", neg.PendingQuestions())
	}
}
