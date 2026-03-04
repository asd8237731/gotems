package orchestrator

import (
	"context"
	"testing"

	"github.com/lyymini/gotems/internal/agent"
	"github.com/lyymini/gotems/internal/task"
	"github.com/lyymini/gotems/pkg/schema"
)

func TestDAG_InjectDependencyContext(t *testing.T) {
	logger := testLogger()
	exec := NewDAGExecutor(nil, nil, nil, logger)

	results := map[string]*schema.Result{
		"task-1": {Content: "第一步完成"},
		"task-2": {Content: "第二步完成"},
	}

	node := &DAGNode{
		Task: &task.Task{
			ID:     "task-3",
			Prompt: "继续完成第三步",
		},
		DependsOn: []string{"task-1", "task-2"},
	}

	exec.injectDependencyContext(node, results)

	// 检查 Prompt 是否被注入了前序结果
	if node.Task.Prompt == "继续完成第三步" {
		t.Error("prompt was not modified with dependency context")
	}

	// 检查 Metadata 是否包含 dep_results
	if node.Task.Metadata == nil {
		t.Fatal("metadata should not be nil")
	}
	depResults, ok := node.Task.Metadata["dep_results"].(map[string]string)
	if !ok {
		t.Fatal("dep_results should be map[string]string")
	}
	if depResults["task-1"] != "第一步完成" {
		t.Errorf("dep_results[task-1] = %q, want '第一步完成'", depResults["task-1"])
	}
	if depResults["task-2"] != "第二步完成" {
		t.Errorf("dep_results[task-2] = %q, want '第二步完成'", depResults["task-2"])
	}
}

func TestDAG_InjectDependencyContext_NoDeps(t *testing.T) {
	logger := testLogger()
	exec := NewDAGExecutor(nil, nil, nil, logger)

	node := &DAGNode{
		Task: &task.Task{
			ID:     "task-1",
			Prompt: "原始提示",
		},
		DependsOn: nil,
	}

	exec.injectDependencyContext(node, nil)

	if node.Task.Prompt != "原始提示" {
		t.Errorf("prompt should not be modified for task with no deps, got: %s", node.Task.Prompt)
	}
}

func TestDAG_BuildAndExecute(t *testing.T) {
	logger := testLogger()
	orch := New(OrchestratorConfig{}, logger)

	ma := newMockAgent("agent-1", agent.ProviderClaude, "claude-sonnet-4-6")
	orch.RegisterAgent(ma)

	if err := orch.Start(context.Background()); err != nil {
		t.Fatalf("start: %v", err)
	}
	defer orch.Stop(context.Background())

	tasks := []*task.Task{
		{ID: "step-1", Prompt: "设计架构", Tags: []string{"code_generation"}},
		{ID: "step-2", Prompt: "实现代码", DependsOn: []string{"step-1"}, Tags: []string{"code_generation"}},
	}

	result, err := orch.RunWithTasks(context.Background(), tasks)
	if err != nil {
		t.Fatalf("RunWithTasks: %v", err)
	}

	if result == nil {
		t.Fatal("result should not be nil")
	}
	if len(result.Results) != 2 {
		t.Errorf("expected 2 results, got %d", len(result.Results))
	}
}

func TestGuardedParallelExecute_Independent(t *testing.T) {
	logger := testLogger()
	orch := New(OrchestratorConfig{Strategy: StrategyConsensus}, logger)

	ma1 := newMockAgent("a1", agent.ProviderClaude, "claude-sonnet-4-6")
	ma2 := newMockAgent("a2", agent.ProviderGemini, "gemini-2.5-pro")
	// 让 a2 失败
	ma2.executeFn = func(ctx context.Context, t *task.Task) (*schema.Result, error) {
		return nil, context.DeadlineExceeded
	}
	orch.RegisterAgent(ma1)
	orch.RegisterAgent(ma2)

	if err := orch.Start(context.Background()); err != nil {
		t.Fatalf("start: %v", err)
	}
	defer orch.Stop(context.Background())

	// 竞赛模式：a2 失败不应影响 a1
	result, err := orch.Run(context.Background(), "测试共识")
	if err != nil {
		t.Fatalf("Run consensus: %v", err)
	}
	if result == nil {
		t.Fatal("result should not be nil")
	}
	if result.Content == "" {
		t.Error("result content should not be empty, a1 should succeed")
	}
}

func TestTruncate(t *testing.T) {
	tests := []struct {
		input  string
		maxLen int
		want   string
	}{
		{"hello", 10, "hello"},
		{"hello world", 5, "hello..."},
		{"你好世界测试", 3, "你好世..."},
		{"", 5, ""},
	}

	for _, tt := range tests {
		got := truncate(tt.input, tt.maxLen)
		if got != tt.want {
			t.Errorf("truncate(%q, %d) = %q, want %q", tt.input, tt.maxLen, got, tt.want)
		}
	}
}
