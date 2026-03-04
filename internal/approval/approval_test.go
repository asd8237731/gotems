package approval

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

func TestRequestApproval_Approved(t *testing.T) {
	logger := testLogger()
	mgr := NewManager(logger)

	req := &Request{
		AgentID:     "agent-1",
		ActionType:  ActionGitPush,
		Description: "Push to main branch",
		Details:     map[string]interface{}{"branch": "main"},
		Timeout:     1 * time.Second,
	}

	// 在后台批准
	go func() {
		time.Sleep(100 * time.Millisecond)
		pending := mgr.PendingList()
		if len(pending) > 0 {
			_ = mgr.Respond(pending[0].ID, Decision{Approved: true, Reason: "OK"})
		}
	}()

	ctx := context.Background()
	decision, err := mgr.RequestApproval(ctx, req)
	if err != nil {
		t.Fatalf("RequestApproval failed: %v", err)
	}

	if !decision.Approved {
		t.Error("expected approval")
	}
	if decision.Reason != "OK" {
		t.Errorf("reason = %s, want OK", decision.Reason)
	}
}

func TestRequestApproval_Rejected(t *testing.T) {
	logger := testLogger()
	mgr := NewManager(logger)

	req := &Request{
		AgentID:     "agent-1",
		ActionType:  ActionFileDelete,
		Description: "Delete important file",
		Timeout:     1 * time.Second,
	}

	// 在后台拒绝
	go func() {
		time.Sleep(100 * time.Millisecond)
		pending := mgr.PendingList()
		if len(pending) > 0 {
			_ = mgr.Respond(pending[0].ID, Decision{Approved: false, Reason: "Too dangerous"})
		}
	}()

	ctx := context.Background()
	decision, err := mgr.RequestApproval(ctx, req)
	if err != nil {
		t.Fatalf("RequestApproval failed: %v", err)
	}

	if decision.Approved {
		t.Error("expected rejection")
	}
	if decision.Reason != "Too dangerous" {
		t.Errorf("reason = %s, want 'Too dangerous'", decision.Reason)
	}
}

func TestRequestApproval_Timeout(t *testing.T) {
	logger := testLogger()
	mgr := NewManager(logger)

	req := &Request{
		AgentID:     "agent-1",
		ActionType:  ActionShellExec,
		Description: "Execute dangerous command",
		Timeout:     200 * time.Millisecond,
	}

	ctx := context.Background()
	decision, err := mgr.RequestApproval(ctx, req)
	if err == nil {
		t.Fatal("expected timeout error")
	}

	if decision.Approved {
		t.Error("expected rejection on timeout")
	}
	if decision.Reason != "approval timeout" {
		t.Errorf("reason = %s, want 'approval timeout'", decision.Reason)
	}
}

func TestRequestApproval_ContextCancelled(t *testing.T) {
	logger := testLogger()
	mgr := NewManager(logger)

	req := &Request{
		AgentID:     "agent-1",
		ActionType:  ActionExternalAPI,
		Description: "Call external API",
		Timeout:     5 * time.Second,
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // 立即取消

	_, err := mgr.RequestApproval(ctx, req)
	if err == nil {
		t.Fatal("expected context cancelled error")
	}
}

func TestPendingList(t *testing.T) {
	logger := testLogger()
	mgr := NewManager(logger)

	// 提交 3 个审批请求（不等待响应）
	for i := 0; i < 3; i++ {
		req := &Request{
			AgentID:     "agent-1",
			ActionType:  ActionGitPush,
			Description: "Test",
			Timeout:     5 * time.Second,
		}
		go mgr.RequestApproval(context.Background(), req)
	}

	time.Sleep(100 * time.Millisecond) // 等待请求提交

	pending := mgr.PendingList()
	if len(pending) != 3 {
		t.Errorf("pending count = %d, want 3", len(pending))
	}
}

func TestHistory(t *testing.T) {
	logger := testLogger()
	mgr := NewManager(logger)

	req := &Request{
		AgentID:     "agent-1",
		ActionType:  ActionGitPush,
		Description: "Test",
		Timeout:     1 * time.Second,
	}

	// 批准请求
	go func() {
		time.Sleep(100 * time.Millisecond)
		pending := mgr.PendingList()
		if len(pending) > 0 {
			_ = mgr.Respond(pending[0].ID, Decision{Approved: true, Reason: "OK"})
		}
	}()

	ctx := context.Background()
	_, _ = mgr.RequestApproval(ctx, req)

	history := mgr.History(10)
	if len(history) != 1 {
		t.Errorf("history count = %d, want 1", len(history))
	}
	if history[0].Status != StatusApproved {
		t.Errorf("status = %s, want approved", history[0].Status)
	}
}

func TestStats(t *testing.T) {
	logger := testLogger()
	mgr := NewManager(logger)

	// 批准 1 个
	req1 := &Request{
		AgentID:     "agent-1",
		ActionType:  ActionGitPush,
		Description: "Test 1",
		Timeout:     1 * time.Second,
	}
	go func() {
		time.Sleep(50 * time.Millisecond)
		pending := mgr.PendingList()
		if len(pending) > 0 {
			_ = mgr.Respond(pending[0].ID, Decision{Approved: true, Reason: "OK"})
		}
	}()
	_, _ = mgr.RequestApproval(context.Background(), req1)

	// 拒绝 1 个
	req2 := &Request{
		AgentID:     "agent-2",
		ActionType:  ActionFileDelete,
		Description: "Test 2",
		Timeout:     1 * time.Second,
	}
	go func() {
		time.Sleep(50 * time.Millisecond)
		pending := mgr.PendingList()
		if len(pending) > 0 {
			_ = mgr.Respond(pending[0].ID, Decision{Approved: false, Reason: "No"})
		}
	}()
	_, _ = mgr.RequestApproval(context.Background(), req2)

	// 超时 1 个
	req3 := &Request{
		AgentID:     "agent-3",
		ActionType:  ActionShellExec,
		Description: "Test 3",
		Timeout:     100 * time.Millisecond,
	}
	_, _ = mgr.RequestApproval(context.Background(), req3)

	stats := mgr.Stats()
	if stats.TotalRequests != 3 {
		t.Errorf("total = %d, want 3", stats.TotalRequests)
	}
	if stats.Approved != 1 {
		t.Errorf("approved = %d, want 1", stats.Approved)
	}
	if stats.Rejected != 1 {
		t.Errorf("rejected = %d, want 1", stats.Rejected)
	}
	if stats.Timeout != 1 {
		t.Errorf("timeout = %d, want 1", stats.Timeout)
	}
}

func TestCallback(t *testing.T) {
	logger := testLogger()
	mgr := NewManager(logger)

	callbackTriggered := false
	var callbackDecision Decision

	mgr.SetCallback(func(req *Request, decision Decision) {
		callbackTriggered = true
		callbackDecision = decision
	})

	req := &Request{
		AgentID:     "agent-1",
		ActionType:  ActionGitPush,
		Description: "Test",
		Timeout:     1 * time.Second,
	}

	go func() {
		time.Sleep(100 * time.Millisecond)
		pending := mgr.PendingList()
		if len(pending) > 0 {
			_ = mgr.Respond(pending[0].ID, Decision{Approved: true, Reason: "OK"})
		}
	}()

	ctx := context.Background()
	_, _ = mgr.RequestApproval(ctx, req)

	time.Sleep(200 * time.Millisecond) // 等待回调执行

	if !callbackTriggered {
		t.Error("callback not triggered")
	}
	if !callbackDecision.Approved {
		t.Error("callback decision should be approved")
	}
}

func TestRespondNonExistent(t *testing.T) {
	logger := testLogger()
	mgr := NewManager(logger)

	err := mgr.Respond("non-existent-id", Decision{Approved: true, Reason: "OK"})
	if err == nil {
		t.Error("expected error for non-existent request")
	}
}

func TestGet(t *testing.T) {
	logger := testLogger()
	mgr := NewManager(logger)

	req := &Request{
		ID:          "test-id",
		AgentID:     "agent-1",
		ActionType:  ActionGitPush,
		Description: "Test",
		Timeout:     5 * time.Second,
	}

	go mgr.RequestApproval(context.Background(), req)
	time.Sleep(100 * time.Millisecond)

	retrieved, ok := mgr.Get("test-id")
	if !ok {
		t.Fatal("request not found")
	}
	if retrieved.AgentID != "agent-1" {
		t.Errorf("agent_id = %s, want agent-1", retrieved.AgentID)
	}
}

func TestMaxHistory(t *testing.T) {
	logger := testLogger()
	mgr := NewManager(logger)
	mgr.SetMaxHistory(5)

	// 提交 10 个请求并批准
	for i := 0; i < 10; i++ {
		req := &Request{
			AgentID:     "agent-1",
			ActionType:  ActionGitPush,
			Description: "Test",
			Timeout:     1 * time.Second,
		}
		go func() {
			time.Sleep(50 * time.Millisecond)
			pending := mgr.PendingList()
			if len(pending) > 0 {
				_ = mgr.Respond(pending[0].ID, Decision{Approved: true, Reason: "OK"})
			}
		}()
		_, _ = mgr.RequestApproval(context.Background(), req)
	}

	history := mgr.History(100)
	// 应该只保留 80% * 5 = 4 条
	if len(history) > 5 {
		t.Errorf("history count = %d, should be <= 5", len(history))
	}
}
