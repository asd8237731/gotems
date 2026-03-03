package comm

import (
	"log/slog"
	"os"
	"testing"
	"time"

	"github.com/lyymini/gotems/pkg/schema"
)

func TestMailboxSendReceive(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	mb := NewMailbox(logger)
	defer mb.Close()

	inbox := mb.Register("agent-1")

	msg := &schema.Message{
		ID:   "m1",
		From: "agent-2",
		To:   "agent-1",
		Type: schema.MsgQuestion,
		Content: "hello",
		Timestamp: time.Now(),
	}

	if err := mb.Send(msg); err != nil {
		t.Fatalf("send failed: %v", err)
	}

	select {
	case received := <-inbox:
		if received.Content != "hello" {
			t.Fatalf("expected 'hello', got %s", received.Content)
		}
	case <-time.After(time.Second):
		t.Fatal("timeout waiting for message")
	}
}

func TestMailboxBroadcast(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	mb := NewMailbox(logger)
	defer mb.Close()

	inbox1 := mb.Register("agent-1")
	inbox2 := mb.Register("agent-2")

	msg := &schema.Message{
		ID:      "b1",
		From:    "leader",
		Content: "broadcast message",
		Timestamp: time.Now(),
	}

	// leader 不在 boxes 中，广播不会因为不发给自己而有问题
	if err := mb.Broadcast(msg); err != nil {
		t.Fatalf("broadcast failed: %v", err)
	}

	// 两个 inbox 都应该收到
	for _, inbox := range []<-chan *schema.Message{inbox1, inbox2} {
		select {
		case received := <-inbox:
			if received.Content != "broadcast message" {
				t.Fatalf("unexpected content: %s", received.Content)
			}
		case <-time.After(time.Second):
			t.Fatal("timeout waiting for broadcast")
		}
	}
}

func TestMailboxSendToUnknown(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	mb := NewMailbox(logger)
	defer mb.Close()

	msg := &schema.Message{To: "nobody", Content: "test"}
	if err := mb.Send(msg); err == nil {
		t.Fatal("expected error when sending to unregistered agent")
	}
}
