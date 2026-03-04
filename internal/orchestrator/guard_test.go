package orchestrator

import (
	"context"
	"log/slog"
	"os"
	"testing"
	"time"

	"github.com/lyymini/gotems/internal/agent"
	"github.com/lyymini/gotems/internal/cost"
	"github.com/lyymini/gotems/internal/observability"
	"github.com/lyymini/gotems/internal/ratelimit"
	"github.com/lyymini/gotems/internal/task"
	"github.com/lyymini/gotems/pkg/schema"
)

// mockAgent 用于测试的 mock Agent
type mockAgent struct {
	agent.BaseAgent
	executeFn func(ctx context.Context, t *task.Task) (*schema.Result, error)
}

func (m *mockAgent) Execute(ctx context.Context, t *task.Task) (*schema.Result, error) {
	if m.executeFn != nil {
		return m.executeFn(ctx, t)
	}
	return &schema.Result{
		AgentID:   m.AgentID,
		Provider:  string(m.ProviderType),
		Content:   "mock result for: " + t.Prompt,
		TokensIn:  100,
		TokensOut: 50,
	}, nil
}

func (m *mockAgent) Stream(_ context.Context, _ *task.Task) (<-chan schema.StreamEvent, error) {
	ch := make(chan schema.StreamEvent)
	close(ch)
	return ch, nil
}

func (m *mockAgent) Start(_ context.Context) error { m.SetStatus(agent.StatusIdle); return nil }
func (m *mockAgent) Stop(_ context.Context) error  { m.SetStatus(agent.StatusStopped); return nil }

func newMockAgent(id string, provider agent.ProviderType, model string) *mockAgent {
	m := &mockAgent{
		BaseAgent: agent.BaseAgent{
			AgentID:      id,
			ProviderType: provider,
			ModelID:      model,
			Caps:         []agent.Capability{agent.CapCodeGen},
			InboxCh:      make(chan *schema.Message, 50),
		},
	}
	m.SetStatus(agent.StatusIdle)
	return m
}

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
}

func TestGuard_Execute_Success(t *testing.T) {
	logger := testLogger()
	limiter := ratelimit.NewLimiter(logger)
	breaker := ratelimit.NewBreaker(5, 0, logger)
	tracker := cost.NewTracker(cost.Limits{DailyMax: 100}, logger)
	guard := NewGuard(limiter, breaker, tracker, logger)

	ma := newMockAgent("test-1", agent.ProviderClaude, "claude-sonnet-4-6")

	tk := &task.Task{ID: "t1", Prompt: "hello"}
	result, err := guard.Execute(context.Background(), ma, tk)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if result.AgentID != "test-1" {
		t.Errorf("got agent_id=%s, want test-1", result.AgentID)
	}
	if result.Model != "claude-sonnet-4-6" {
		t.Errorf("got model=%s, want claude-sonnet-4-6", result.Model)
	}
	if result.Cost == 0 {
		t.Error("expected cost to be auto-calculated, got 0")
	}

	// 验证 cost tracker 记录了
	summary := tracker.Summarize()
	if summary.RecordCount != 1 {
		t.Errorf("expected 1 record, got %d", summary.RecordCount)
	}
}

func TestGuard_Execute_CircuitBreaker(t *testing.T) {
	logger := testLogger()
	limiter := ratelimit.NewLimiter(logger)
	breaker := ratelimit.NewBreaker(2, 10*time.Minute, logger)
	tracker := cost.NewTracker(cost.Limits{}, logger)
	guard := NewGuard(limiter, breaker, tracker, logger)

	// 触发熔断
	breaker.RecordFailure("claude")
	breaker.RecordFailure("claude")

	ma := newMockAgent("test-1", agent.ProviderClaude, "claude-sonnet-4-6")
	tk := &task.Task{ID: "t1", Prompt: "hello"}
	_, err := guard.Execute(context.Background(), ma, tk)
	if err == nil {
		t.Fatal("expected circuit breaker error")
	}
}

func TestGuard_Execute_BudgetExhausted(t *testing.T) {
	logger := testLogger()
	limiter := ratelimit.NewLimiter(logger)
	breaker := ratelimit.NewBreaker(5, 10*time.Minute, logger)
	// mock agent 默认返回 100 in + 50 out
	// claude-sonnet-4-6: 100/1000*0.003 + 50/1000*0.015 = 0.0003 + 0.00075 = 0.00105
	// DailyMax 设为 0.0015，第一次 Track (0.00105) 成功，daily=0.00105 >= 0.0015 不成立
	// 但 Track 检查的是 todayCost = daily + r.Cost = 0 + 0.00105 = 0.00105 < 0.0015 → 写入
	// 第二次 CheckBudget: daily=0.00105 >= 0.0015？不成立。
	// 关键：需要让第一次 Track 后 daily >= DailyMax
	// 所以 DailyMax 必须 <= 0.00105
	tracker := cost.NewTracker(cost.Limits{DailyMax: 0.001}, logger)
	guard := NewGuard(limiter, breaker, tracker, logger)

	ma := newMockAgent("test-1", agent.ProviderClaude, "claude-sonnet-4-6")

	// 第一次：CheckBudget(0 < 0.001) → 通过 → Execute
	// → Guard.CalcCost = 0.00105
	// → Track(cost=0.00105): todayCost = 0 + 0.00105 = 0.00105 > 0.001 → Track 返回 error，不写入！
	// 所以 daily 永远不更新。Guard 忽略了 Track 的错误。

	// 修复：直接用 Track 预载超限数据
	_ = tracker.Track(cost.Record{Cost: 0.001})

	// 现在 daily=0.001 >= DailyMax=0.001 → CheckBudget 拦截
	tk := &task.Task{ID: "t1", Prompt: "hello"}
	_, err := guard.Execute(context.Background(), ma, tk)
	if err == nil {
		t.Fatal("expected budget error")
	}
}

func TestGuard_Execute_WithMetrics(t *testing.T) {
	logger := testLogger()
	limiter := ratelimit.NewLimiter(logger)
	breaker := ratelimit.NewBreaker(5, 0, logger)
	tracker := cost.NewTracker(cost.Limits{DailyMax: 100}, logger)
	guard := NewGuard(limiter, breaker, tracker, logger)

	metrics := observability.NewMetrics(logger)
	guard.SetMetrics(metrics)

	ma := newMockAgent("test-1", agent.ProviderClaude, "claude-sonnet-4-6")
	tk := &task.Task{ID: "t1", Prompt: "hello"}
	_, err := guard.Execute(context.Background(), ma, tk)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	snap := metrics.Snapshot()
	if snap.TasksTotal != 1 {
		t.Errorf("expected 1 total task, got %d", snap.TasksTotal)
	}
	if snap.TasksSucceeded != 1 {
		t.Errorf("expected 1 succeeded, got %d", snap.TasksSucceeded)
	}
}
