package cost

import (
	"log/slog"
	"os"
	"testing"
)

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
}

func TestCalcCost_KnownModel(t *testing.T) {
	tracker := NewTracker(Limits{}, testLogger())

	// claude-sonnet-4-6: input=0.003/K, output=0.015/K
	cost := tracker.CalcCost("claude-sonnet-4-6", 1000, 1000)
	expected := 0.003 + 0.015
	if cost != expected {
		t.Errorf("CalcCost = %f, want %f", cost, expected)
	}
}

func TestCalcCost_UnknownModel(t *testing.T) {
	tracker := NewTracker(Limits{}, testLogger())
	cost := tracker.CalcCost("unknown-model", 1000, 1000)
	if cost != 0 {
		t.Errorf("CalcCost for unknown model = %f, want 0", cost)
	}
}

func TestCalcCost_ZeroTokens(t *testing.T) {
	tracker := NewTracker(Limits{}, testLogger())
	cost := tracker.CalcCost("claude-sonnet-4-6", 0, 0)
	if cost != 0 {
		t.Errorf("CalcCost with zero tokens = %f, want 0", cost)
	}
}

func TestRegisterPricing(t *testing.T) {
	tracker := NewTracker(Limits{}, testLogger())

	// 注册自定义模型
	tracker.RegisterPricing("custom-model", ModelPricing{
		CostPerKInput:  0.01,
		CostPerKOutput: 0.05,
	})

	cost := tracker.CalcCost("custom-model", 2000, 1000)
	expected := 0.01*2 + 0.05*1
	if cost != expected {
		t.Errorf("CalcCost = %f, want %f", cost, expected)
	}
}

func TestRegisterPricing_Override(t *testing.T) {
	tracker := NewTracker(Limits{}, testLogger())

	// 覆盖已有模型定价
	tracker.RegisterPricing("claude-sonnet-4-6", ModelPricing{
		CostPerKInput:  0.1,
		CostPerKOutput: 0.5,
	})

	cost := tracker.CalcCost("claude-sonnet-4-6", 1000, 1000)
	expected := 0.1 + 0.5
	if cost != expected {
		t.Errorf("CalcCost after override = %f, want %f", cost, expected)
	}
}

func TestTrack_AutoCalc(t *testing.T) {
	tracker := NewTracker(Limits{DailyMax: 100}, testLogger())

	err := tracker.Track(Record{
		AgentID:   "agent-1",
		Provider:  "claude",
		Model:     "claude-sonnet-4-6",
		TokensIn:  1000,
		TokensOut: 500,
		Cost:      0, // 应该自动计算
	})
	if err != nil {
		t.Fatalf("Track: %v", err)
	}

	summary := tracker.Summarize()
	if summary.TotalCost == 0 {
		t.Error("expected auto-calculated cost, got 0")
	}
	// 1000/1000*0.003 + 500/1000*0.015 = 0.003 + 0.0075 = 0.0105
	expectedCost := 0.003 + 0.0075
	if !floatEqual(summary.TotalCost, expectedCost) {
		t.Errorf("TotalCost = %f, want %f", summary.TotalCost, expectedCost)
	}
}

func TestPricing_Snapshot(t *testing.T) {
	tracker := NewTracker(Limits{}, testLogger())
	pricing := tracker.Pricing()

	if _, ok := pricing["claude-sonnet-4-6"]; !ok {
		t.Error("expected claude-sonnet-4-6 in pricing snapshot")
	}
	if _, ok := pricing["gpt-4o"]; !ok {
		t.Error("expected gpt-4o in pricing snapshot")
	}

	// 修改快照不影响原始
	pricing["test"] = ModelPricing{CostPerKInput: 999}
	original := tracker.Pricing()
	if _, ok := original["test"]; ok {
		t.Error("snapshot modification should not affect original")
	}
}
