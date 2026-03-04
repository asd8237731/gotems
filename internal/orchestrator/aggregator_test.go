package orchestrator

import (
	"context"
	"testing"
	"time"

	"github.com/lyymini/gotems/internal/agent"
	"github.com/lyymini/gotems/internal/cost"
	"github.com/lyymini/gotems/internal/ratelimit"
	"github.com/lyymini/gotems/internal/task"
	"github.com/lyymini/gotems/pkg/schema"
)

func TestAggregator_MergeResults(t *testing.T) {
	logger := testLogger()
	agg := NewAggregator(nil, logger)

	results := map[string]*schema.Result{
		"task-1": {
			AgentID:   "agent-1",
			Provider:  "claude",
			Content:   "Part 1",
			TokensIn:  100,
			TokensOut: 50,
			Cost:      0.01,
		},
		"task-2": {
			AgentID:   "agent-2",
			Provider:  "openai",
			Content:   "Part 2",
			TokensIn:  150,
			TokensOut: 75,
			Cost:      0.02,
		},
	}

	final := agg.MergeResults(results)

	if final.Strategy != "merge" {
		t.Errorf("strategy = %s, want merge", final.Strategy)
	}
	if len(final.Results) != 2 {
		t.Errorf("results count = %d, want 2", len(final.Results))
	}
	if final.TotalCost != 0.03 {
		t.Errorf("total cost = %.2f, want 0.03", final.TotalCost)
	}
	if final.TotalTokensIn != 250 {
		t.Errorf("total tokens in = %d, want 250", final.TotalTokensIn)
	}
	if final.TotalTokensOut != 125 {
		t.Errorf("total tokens out = %d, want 125", final.TotalTokensOut)
	}
	if final.Content == "" {
		t.Error("expected non-empty merged content")
	}
}

func TestAggregator_BestOf_NoJudge(t *testing.T) {
	logger := testLogger()
	agg := NewAggregator(nil, logger)

	results := []*schema.Result{
		{
			AgentID:   "agent-1",
			Provider:  "claude",
			Content:   "Short",
			TokensIn:  100,
			TokensOut: 50,
			Cost:      0.01,
		},
		{
			AgentID:   "agent-2",
			Provider:  "openai",
			Content:   "This is a much longer response with more details",
			TokensIn:  150,
			TokensOut: 75,
			Cost:      0.02,
		},
	}

	ctx := context.Background()
	final, err := agg.BestOf(ctx, "test prompt", results)
	if err != nil {
		t.Fatalf("BestOf failed: %v", err)
	}

	if final.Strategy != "consensus" {
		t.Errorf("strategy = %s, want consensus", final.Strategy)
	}
	// 应该选择更长的内容
	if final.Content != results[1].Content {
		t.Errorf("expected longer content to be selected")
	}
	if final.TotalCost != 0.03 {
		t.Errorf("total cost = %.2f, want 0.03", final.TotalCost)
	}
}

func TestAggregator_BestOf_WithJudge(t *testing.T) {
	logger := testLogger()
	judge := newMockAgent("judge", agent.ProviderClaude, "claude-sonnet-4-6")
	judge.executeFn = func(ctx context.Context, t *task.Task) (*schema.Result, error) {
		return &schema.Result{
			AgentID:   "judge",
			Provider:  "claude",
			Content:   "Final judged answer",
			TokensIn:  200,
			TokensOut: 100,
			Cost:      0.03,
		}, nil
	}
	agg := NewAggregator(judge, logger)

	results := []*schema.Result{
		{
			AgentID:   "agent-1",
			Provider:  "claude",
			Content:   "Answer 1",
			TokensIn:  100,
			TokensOut: 50,
			Cost:      0.01,
		},
		{
			AgentID:   "agent-2",
			Provider:  "openai",
			Content:   "Answer 2",
			TokensIn:  150,
			TokensOut: 75,
			Cost:      0.02,
		},
	}

	ctx := context.Background()
	final, err := agg.BestOf(ctx, "test prompt", results)
	if err != nil {
		t.Fatalf("BestOf failed: %v", err)
	}

	if final.Content != "Final judged answer" {
		t.Errorf("content = %q, want judge's answer", final.Content)
	}
	// 总成本应包含裁判的成本
	if final.TotalCost != 0.06 {
		t.Errorf("total cost = %.2f, want 0.06", final.TotalCost)
	}
}

func TestAggregator_BestOf_SingleResult(t *testing.T) {
	logger := testLogger()
	agg := NewAggregator(nil, logger)

	results := []*schema.Result{
		{
			AgentID:   "agent-1",
			Provider:  "claude",
			Content:   "Only result",
			TokensIn:  100,
			TokensOut: 50,
			Cost:      0.01,
		},
	}

	ctx := context.Background()
	final, err := agg.BestOf(ctx, "test prompt", results)
	if err != nil {
		t.Fatalf("BestOf failed: %v", err)
	}

	if final.Content != "Only result" {
		t.Errorf("content = %q, want 'Only result'", final.Content)
	}
}

func TestParallelExecute(t *testing.T) {
	a1 := newMockAgent("agent-1", agent.ProviderClaude, "claude-sonnet-4-6")
	a2 := newMockAgent("agent-2", agent.ProviderOpenAI, "gpt-4o")

	agents := []agent.Agent{a1, a2}
	tk := &task.Task{ID: "test", Prompt: "test prompt"}

	ctx := context.Background()
	results, err := ParallelExecute(ctx, agents, tk)
	if err != nil {
		t.Fatalf("ParallelExecute failed: %v", err)
	}

	if len(results) != 2 {
		t.Errorf("expected 2 results, got %d", len(results))
	}
}

func TestGuardedParallelExecute_PartialFailure(t *testing.T) {
	logger := testLogger()
	limiter := ratelimit.NewLimiter(logger)
	breaker := ratelimit.NewBreaker(5, 30*time.Second, logger)
	tracker := cost.NewTracker(cost.Limits{DailyMax: 10.0}, logger)
	guard := NewGuard(limiter, breaker, tracker, logger)

	a1 := newMockAgent("agent-1", agent.ProviderClaude, "claude-sonnet-4-6")
	a2 := newMockAgent("agent-2", agent.ProviderOpenAI, "gpt-4o")
	// 让 a2 失败
	a2.executeFn = func(ctx context.Context, t *task.Task) (*schema.Result, error) {
		return nil, context.Canceled
	}

	agents := []agent.Agent{a1, a2}
	tk := &task.Task{ID: "test", Prompt: "test prompt"}

	ctx := context.Background()
	results, err := GuardedParallelExecute(ctx, guard, agents, tk)

	// 应该有部分失败错误
	if err == nil {
		t.Error("expected partial failure error")
	}
	// 但应该有一个成功的结果
	if len(results) != 1 {
		t.Errorf("expected 1 successful result, got %d", len(results))
	}
	if results[0].AgentID != "agent-1" {
		t.Errorf("expected agent-1 result, got %s", results[0].AgentID)
	}
}
