package orchestrator

import (
	"context"
	"testing"

	"github.com/lyymini/gotems/internal/agent"
	"github.com/lyymini/gotems/internal/task"
	"github.com/lyymini/gotems/pkg/schema"
)

func TestRouterBestFit(t *testing.T) {
	agents := map[string]agent.Agent{
		"claude-1": newTestAgent("claude-1", agent.ProviderClaude, []agent.Capability{agent.CapReasoning, agent.CapCodeReview}),
		"gemini-1": newTestAgent("gemini-1", agent.ProviderGemini, []agent.Capability{agent.CapMultimodal, agent.CapCodeGen}),
	}

	router := NewRouter(StrategyBestFit, agents)

	a := router.Route([]string{"deep_reasoning"})
	if a == nil {
		t.Fatal("expected an agent")
	}
	if a.ID() != "claude-1" {
		t.Fatalf("expected claude-1 for reasoning, got %s", a.ID())
	}
}

func TestRouterCostFirst(t *testing.T) {
	agents := map[string]agent.Agent{
		"expensive": newTestAgent("expensive", agent.ProviderGemini, []agent.Capability{agent.CapCodeGen}),
		"cheap":     newTestAgent("cheap", agent.ProviderOllama, []agent.Capability{agent.CapCodeGen}),
	}

	router := NewRouter(StrategyCostFirst, agents)
	router.SetProfile("expensive", &ModelProfile{CostPerKIn: 0.01, CostPerKOut: 0.03})
	router.SetProfile("cheap", &ModelProfile{CostPerKIn: 0.001, CostPerKOut: 0.004})

	a := router.Route(nil)
	if a == nil {
		t.Fatal("expected an agent")
	}
	if a.ID() != "cheap" {
		t.Fatalf("expected cheap agent for cost_first, got %s", a.ID())
	}
}

func TestParseStrategy(t *testing.T) {
	tests := []struct {
		input    string
		expected Strategy
	}{
		{"best_fit", StrategyBestFit},
		{"cost_first", StrategyCostFirst},
		{"consensus", StrategyConsensus},
		{"round_robin", StrategyRoundRobin},
		{"unknown", StrategyBestFit},
	}

	for _, tt := range tests {
		got := ParseStrategy(tt.input)
		if got != tt.expected {
			t.Errorf("ParseStrategy(%q) = %d, want %d", tt.input, got, tt.expected)
		}
	}
}

// testAgent 是完整实现 Agent 接口的 mock
type testAgent struct {
	id       string
	provider agent.ProviderType
	model    string
	caps     []agent.Capability
	status   agent.Status
	inbox    chan *schema.Message
}

func newTestAgent(id string, provider agent.ProviderType, caps []agent.Capability) *testAgent {
	return &testAgent{
		id:       id,
		provider: provider,
		model:    "mock-model",
		caps:     caps,
		status:   agent.StatusIdle,
		inbox:    make(chan *schema.Message, 10),
	}
}

func (a *testAgent) ID() string                            { return a.id }
func (a *testAgent) Provider() agent.ProviderType          { return a.provider }
func (a *testAgent) Model() string                         { return a.model }
func (a *testAgent) Capabilities() []agent.Capability      { return a.caps }
func (a *testAgent) Status() agent.Status                  { return a.status }
func (a *testAgent) Inbox() <-chan *schema.Message          { return a.inbox }
func (a *testAgent) Start(_ context.Context) error         { return nil }
func (a *testAgent) Stop(_ context.Context) error          { return nil }
func (a *testAgent) Send(_ context.Context, msg *schema.Message) error {
	a.inbox <- msg
	return nil
}
func (a *testAgent) Execute(_ context.Context, t *task.Task) (*schema.Result, error) {
	return &schema.Result{
		AgentID:  a.id,
		Provider: string(a.provider),
		Content:  "mock result for: " + t.Prompt,
	}, nil
}
func (a *testAgent) Stream(_ context.Context, _ *task.Task) (<-chan schema.StreamEvent, error) {
	ch := make(chan schema.StreamEvent, 1)
	close(ch)
	return ch, nil
}
