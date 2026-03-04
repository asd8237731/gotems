package orchestrator

import (
	"context"
	"os"
	"testing"

	"github.com/lyymini/gotems/internal/agent"
	"github.com/lyymini/gotems/internal/cost"
	"github.com/lyymini/gotems/internal/ratelimit"
	"github.com/lyymini/gotems/internal/task"
	"github.com/lyymini/gotems/pkg/schema"
)

func TestOrchestrator_RegisterAgent(t *testing.T) {
	logger := testLogger()
	cfg := OrchestratorConfig{
		Strategy: StrategyBestFit,
	}
	o := New(cfg, logger)

	a1 := newMockAgent("agent-1", agent.ProviderClaude, "claude-sonnet-4-6")
	o.RegisterAgent(a1)

	if len(o.agents) != 1 {
		t.Errorf("expected 1 agent, got %d", len(o.agents))
	}
	if o.agents["agent-1"] != a1 {
		t.Error("agent not registered correctly")
	}
}

func TestOrchestrator_StartStop(t *testing.T) {
	logger := testLogger()
	cfg := OrchestratorConfig{
		Strategy: StrategyBestFit,
	}
	o := New(cfg, logger)

	a1 := newMockAgent("agent-1", agent.ProviderClaude, "claude-sonnet-4-6")
	a2 := newMockAgent("agent-2", agent.ProviderOpenAI, "gpt-4o")
	o.RegisterAgent(a1)
	o.RegisterAgent(a2)

	ctx := context.Background()
	if err := o.Start(ctx); err != nil {
		t.Fatalf("Start failed: %v", err)
	}

	if a1.Status() != agent.StatusIdle {
		t.Errorf("agent-1 status = %v, want idle", a1.Status())
	}
	if a2.Status() != agent.StatusIdle {
		t.Errorf("agent-2 status = %v, want idle", a2.Status())
	}

	if err := o.Stop(ctx); err != nil {
		t.Fatalf("Stop failed: %v", err)
	}

	if a1.Status() != agent.StatusStopped {
		t.Errorf("agent-1 status = %v, want stopped", a1.Status())
	}
	if a2.Status() != agent.StatusStopped {
		t.Errorf("agent-2 status = %v, want stopped", a2.Status())
	}
}

func TestOrchestrator_StartRollback(t *testing.T) {
	logger := testLogger()
	cfg := OrchestratorConfig{
		Strategy: StrategyBestFit,
	}
	o := New(cfg, logger)

	a1 := newMockAgent("agent-1", agent.ProviderClaude, "claude-sonnet-4-6")

	// 创建一个会启动失败的 agent
	failAgent := &mockAgentWithFailStart{
		BaseAgent: agent.BaseAgent{
			AgentID:      "fail-agent",
			ProviderType: agent.ProviderGemini,
			ModelID:      "gemini-2.5-pro",
			InboxCh:      make(chan *schema.Message, 50),
		},
	}
	failAgent.SetStatus(agent.StatusIdle)

	o.RegisterAgent(a1)
	o.RegisterAgent(failAgent)

	ctx := context.Background()
	err := o.Start(ctx)
	if err == nil {
		t.Fatal("expected Start to fail")
	}

	// 验证 a1 被回滚停止
	if a1.Status() != agent.StatusStopped {
		t.Errorf("agent-1 should be stopped after rollback, got %v", a1.Status())
	}
}

// mockAgentWithFailStart 是一个启动会失败的 mock agent
type mockAgentWithFailStart struct {
	agent.BaseAgent
}

func (m *mockAgentWithFailStart) Execute(ctx context.Context, t *task.Task) (*schema.Result, error) {
	return &schema.Result{
		AgentID:   m.AgentID,
		Provider:  string(m.ProviderType),
		Content:   "mock",
		TokensIn:  100,
		TokensOut: 50,
	}, nil
}

func (m *mockAgentWithFailStart) Stream(_ context.Context, _ *task.Task) (<-chan schema.StreamEvent, error) {
	ch := make(chan schema.StreamEvent)
	close(ch)
	return ch, nil
}

func (m *mockAgentWithFailStart) Start(_ context.Context) error {
	return os.ErrInvalid // 启动失败
}

func (m *mockAgentWithFailStart) Stop(_ context.Context) error {
	m.SetStatus(agent.StatusStopped)
	return nil
}

func TestOrchestrator_Run_Single(t *testing.T) {
	logger := testLogger()
	cfg := OrchestratorConfig{
		Strategy: StrategyBestFit,
		CostLimits: cost.Limits{
			DailyMax: 10.0,
		},
	}
	o := New(cfg, logger)

	a1 := newMockAgent("agent-1", agent.ProviderClaude, "claude-sonnet-4-6")
	o.RegisterAgent(a1)

	ctx := context.Background()
	_ = o.Start(ctx)
	defer o.Stop(ctx)

	result, err := o.Run(ctx, "test prompt")
	if err != nil {
		t.Fatalf("Run failed: %v", err)
	}

	if result.Content == "" {
		t.Error("expected non-empty content")
	}
	if len(result.Results) != 1 {
		t.Errorf("1 result, got %d", len(result.Results))
	}
	if result.Strategy != "single" {
		t.Errorf("strategy = %s, want single", result.Strategy)
	}
}

func TestOrchestrator_Run_Consensus(t *testing.T) {
	logger := testLogger()
	cfg := OrchestratorConfig{
		Strategy: StrategyConsensus,
		CostLimits: cost.Limits{
			DailyMax: 10.0,
		},
	}
	o := New(cfg, logger)

	a1 := newMockAgent("agent-1", agent.ProviderClaude, "claude-sonnet-4-6")
	a2 := newMockAgent("agent-2", agent.ProviderOpenAI, "gpt-4o")
	o.RegisterAgent(a1)
	o.RegisterAgent(a2)

	ctx := context.Background()
	_ = o.Start(ctx)
	defer o.Stop(ctx)

	result, err := o.Run(ctx, "test prompt")
	if err != nil {
		t.Fatalf("Run failed: %v", err)
	}

	if result.Content == "" {
		t.Error("expected non-empty content")
	}
	if len(result.Results) != 2 {
		t.Errorf("expected 2 results, got %d", len(result.Results))
	}
	if result.Strategy != "consensus" {
		t.Errorf("strategy = %s, want consensus", result.Strategy)
	}
}

func TestOrchestrator_RunWithTasks_DAG(t *testing.T) {
	logger := testLogger()
	cfg := OrchestratorConfig{
		Strategy: StrategyBestFit,
		CostLimits: cost.Limits{
			DailyMax: 10.0,
		},
	}
	o := New(cfg, logger)

	a1 := newMockAgent("agent-1", agent.ProviderClaude, "claude-sonnet-4-6")
	o.RegisterAgent(a1)

	ctx := context.Background()
	_ = o.Start(ctx)
	defer o.Stop(ctx)

	tasks := []*task.Task{
		{ID: "task-1", Prompt: "step 1"},
		{ID: "task-2", Prompt: "step 2", DependsOn: []string{"task-1"}},
		{ID: "task-3", Prompt: "step 3", DependsOn: []string{"task-1"}},
		{ID: "task-4", Prompt: "step 4", DependsOn: []string{"task-2", "task-3"}},
	}

	result, err := o.RunWithTasks(ctx, tasks)
	if err != nil {
		t.Fatalf("RunWithTasks failed: %v", err)
	}

	if result.Content == "" {
		t.Error("expected non-empty content")
	}
	if len(result.Results) != 4 {
		t.Errorf("expected 4 results, got %d", len(result.Results))
	}
}

func TestOrchestrator_AgentsMap_IsolatedCopy(t *testing.T) {
	logger := testLogger()
	cfg := OrchestratorConfig{
		Strategy: StrategyBestFit,
	}
	o := New(cfg, logger)

	a1 := newMockAgent("agent-1", agent.ProviderClaude, "claude-sonnet-4-6")
	o.RegisterAgent(a1)

	// 获取副本
	copy := o.AgentsMap()
	if len(copy) != 1 {
		t.Errorf("expected 1 agent in copy, got %d", len(copy))
	}

	// 修改副本不应影响原始 map
	delete(copy, "agent-1")
	if len(o.agents) != 1 {
		t.Error("modifying copy affected original map")
	}
}

func TestOrchestrator_CostSummary(t *testing.T) {
	logger := testLogger()
	cfg := OrchestratorConfig{
		Strategy: StrategyBestFit,
		CostLimits: cost.Limits{
			DailyMax: 10.0,
		},
	}
	o := New(cfg, logger)

	a1 := newMockAgent("agent-1", agent.ProviderClaude, "claude-sonnet-4-6")
	o.RegisterAgent(a1)

	ctx := context.Background()
	_ = o.Start(ctx)
	defer o.Stop(ctx)

	_, _ = o.Run(ctx, "test prompt")

	summary := o.CostSummary()
	if summary.TotalCost == 0 {
		t.Error("expected non-zero total cost")
	}
	if summary.RecordCount == 0 {
		t.Error("expected non-zero record count")
	}
}

func TestOrchestrator_ConfigureRateLimit(t *testing.T) {
	logger := testLogger()
	cfg := OrchestratorConfig{
		Strategy: StrategyBestFit,
	}
	o := New(cfg, logger)

	o.ConfigureRateLimit(ratelimit.LimiterConfig{
		Provider: "claude",
		RPS:      10,
		Burst:    20,
	})

	// 验证配置生效（通过执行任务观察是否限流）
	a1 := newMockAgent("agent-1", agent.ProviderClaude, "claude-sonnet-4-6")
	o.RegisterAgent(a1)

	ctx := context.Background()
	_ = o.Start(ctx)
	defer o.Stop(ctx)

	// 快速执行多个任务，不应该因为限流失败
	for i := 0; i < 5; i++ {
		_, err := o.Run(ctx, "test")
		if err != nil {
			t.Fatalf("Run %d failed: %v", i, err)
		}
	}
}
