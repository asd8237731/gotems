package mcp

import (
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"testing"

	"github.com/lyymini/gotems/internal/agent"
	"github.com/lyymini/gotems/internal/cost"
	"github.com/lyymini/gotems/internal/task"
	"github.com/lyymini/gotems/pkg/schema"
)

// testAgent 用于测试的 mock Agent
type testAgent struct {
	id string
}

func (a *testAgent) ID() string                                { return a.id }
func (a *testAgent) Provider() agent.ProviderType              { return agent.ProviderClaude }
func (a *testAgent) Model() string                             { return "mock" }
func (a *testAgent) Capabilities() []agent.Capability          { return nil }
func (a *testAgent) Status() agent.Status                      { return agent.StatusIdle }
func (a *testAgent) Inbox() <-chan *schema.Message              { return nil }
func (a *testAgent) Start(_ context.Context) error             { return nil }
func (a *testAgent) Stop(_ context.Context) error              { return nil }
func (a *testAgent) Send(_ context.Context, _ *schema.Message) error { return nil }
func (a *testAgent) Stream(_ context.Context, _ *task.Task) (<-chan schema.StreamEvent, error) {
	ch := make(chan schema.StreamEvent)
	close(ch)
	return ch, nil
}
func (a *testAgent) Execute(_ context.Context, t *task.Task) (*schema.Result, error) {
	return &schema.Result{AgentID: a.id, Provider: "claude", Content: "mock: " + t.Prompt}, nil
}

func newTestBridge() *Bridge {
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	agents := map[string]agent.Agent{"test-1": &testAgent{id: "test-1"}}
	tracker := cost.NewTracker(cost.Limits{}, logger)
	pool := task.NewPool()
	return NewBridge(agents, tracker, pool, logger)
}

func TestBridgeListTools(t *testing.T) {
	b := newTestBridge()
	tools := b.ListTools()
	if len(tools) < 4 {
		t.Fatalf("expected at least 4 builtin tools, got %d", len(tools))
	}
	names := make(map[string]bool)
	for _, tool := range tools {
		names[tool.Name] = true
	}
	for _, expected := range []string{"gotems_run_task", "gotems_list_agents", "gotems_cost_summary", "gotems_task_pool"} {
		if !names[expected] {
			t.Fatalf("missing tool: %s", expected)
		}
	}
}

func TestBridgeCallTool(t *testing.T) {
	b := newTestBridge()
	ctx := context.Background()

	result, err := b.CallTool(ctx, "gotems_list_agents", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.IsError {
		t.Fatalf("unexpected error result: %+v", result)
	}
	if len(result.Content) == 0 {
		t.Fatal("expected content")
	}
}

func TestBridgeCallUnknownTool(t *testing.T) {
	b := newTestBridge()
	_, err := b.CallTool(context.Background(), "nonexistent", nil)
	if err == nil {
		t.Fatal("expected error for unknown tool")
	}
}

func TestBridgeRunTask(t *testing.T) {
	b := newTestBridge()
	ctx := context.Background()

	result, err := b.CallTool(ctx, "gotems_run_task", map[string]any{
		"prompt": "say hello",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.IsError {
		t.Fatalf("unexpected error: %+v", result)
	}
}

func TestHandleJSONRPCInitialize(t *testing.T) {
	b := newTestBridge()
	ctx := context.Background()

	req := JSONRPCRequest{JSONRPC: "2.0", ID: 1, Method: "initialize"}
	reqBytes, _ := json.Marshal(req)

	respBytes, err := b.HandleJSONRPC(ctx, reqBytes)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var resp JSONRPCResponse
	json.Unmarshal(respBytes, &resp)
	if resp.Error != nil {
		t.Fatalf("unexpected RPC error: %+v", resp.Error)
	}
}

func TestHandleJSONRPCToolsList(t *testing.T) {
	b := newTestBridge()
	ctx := context.Background()

	req := JSONRPCRequest{JSONRPC: "2.0", ID: 2, Method: "tools/list"}
	reqBytes, _ := json.Marshal(req)

	respBytes, err := b.HandleJSONRPC(ctx, reqBytes)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var resp JSONRPCResponse
	json.Unmarshal(respBytes, &resp)
	if resp.Error != nil {
		t.Fatalf("unexpected RPC error: %+v", resp.Error)
	}
}

func TestHandleJSONRPCToolsCall(t *testing.T) {
	b := newTestBridge()
	ctx := context.Background()

	params, _ := json.Marshal(map[string]any{
		"name":      "gotems_cost_summary",
		"arguments": map[string]any{},
	})
	req := JSONRPCRequest{JSONRPC: "2.0", ID: 3, Method: "tools/call", Params: params}
	reqBytes, _ := json.Marshal(req)

	respBytes, err := b.HandleJSONRPC(ctx, reqBytes)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var resp JSONRPCResponse
	json.Unmarshal(respBytes, &resp)
	if resp.Error != nil {
		t.Fatalf("unexpected RPC error: %+v", resp.Error)
	}
}

func TestHandleJSONRPCUnknownMethod(t *testing.T) {
	b := newTestBridge()
	ctx := context.Background()

	req := JSONRPCRequest{JSONRPC: "2.0", ID: 4, Method: "unknown/method"}
	reqBytes, _ := json.Marshal(req)

	respBytes, err := b.HandleJSONRPC(ctx, reqBytes)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var resp JSONRPCResponse
	json.Unmarshal(respBytes, &resp)
	if resp.Error == nil {
		t.Fatal("expected error for unknown method")
	}
}
