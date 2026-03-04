package cost

import (
	"log/slog"
	"os"
	"testing"
)

func TestSuggestDowngrade_NoLimit(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	tracker := NewTracker(Limits{DailyMax: 0}, logger) // 无限制

	model := tracker.SuggestDowngrade("claude-opus-4-6")
	if model != "claude-opus-4-6" {
		t.Errorf("expected no downgrade when no limit, got %s", model)
	}
}

func TestSuggestDowngrade_LowUsage(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	tracker := NewTracker(Limits{DailyMax: 10.0}, logger)

	// 使用 50% 预算
	_ = tracker.Track(Record{
		AgentID:   "test",
		Provider:  "claude",
		Model:     "claude-opus-4-6",
		TokensIn:  100,
		TokensOut: 50,
		Cost:      5.0,
	})

	model := tracker.SuggestDowngrade("claude-opus-4-6")
	if model != "claude-opus-4-6" {
		t.Errorf("expected no downgrade at 50%% usage, got %s", model)
	}
}

func TestSuggestDowngrade_HighUsage(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	tracker := NewTracker(Limits{DailyMax: 10.0}, logger)

	// 使用 75% 预算
	_ = tracker.Track(Record{
		AgentID:   "test",
		Provider:  "claude",
		Model:     "claude-opus-4-6",
		TokensIn:  100,
		TokensOut: 50,
		Cost:      7.5,
	})

	model := tracker.SuggestDowngrade("claude-opus-4-6")
	if model != "claude-sonnet-4-6" {
		t.Errorf("expected downgrade to sonnet at 75%% usage, got %s", model)
	}
}

func TestSuggestDowngrade_VeryHighUsage(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	tracker := NewTracker(Limits{DailyMax: 10.0}, logger)

	// 使用 95% 预算
	_ = tracker.Track(Record{
		AgentID:   "test",
		Provider:  "claude",
		Model:     "claude-opus-4-6",
		TokensIn:  100,
		TokensOut: 50,
		Cost:      9.5,
	})

	model := tracker.SuggestDowngrade("claude-opus-4-6")
	if model != "claude-sonnet-4-6" {
		t.Errorf("expected downgrade to sonnet at 95%% usage, got %s", model)
	}
}

func TestSuggestDowngrade_Chain(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	tracker := NewTracker(Limits{DailyMax: 10.0}, logger)

	// 使用 80% 预算
	_ = tracker.Track(Record{
		AgentID:   "test",
		Provider:  "claude",
		Model:     "claude-sonnet-4-6",
		TokensIn:  100,
		TokensOut: 50,
		Cost:      8.0,
	})

	// Sonnet 应该降级到 Haiku
	model := tracker.SuggestDowngrade("claude-sonnet-4-6")
	if model != "claude-haiku-4-5" {
		t.Errorf("expected downgrade to haiku, got %s", model)
	}
}

func TestSuggestDowngrade_NoDowngradeAvailable(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	tracker := NewTracker(Limits{DailyMax: 10.0}, logger)

	// 使用 80% 预算
	_ = tracker.Track(Record{
		AgentID:   "test",
		Provider:  "claude",
		Model:     "claude-haiku-4-5",
		TokensIn:  100,
		TokensOut: 50,
		Cost:      8.0,
	})

	// Haiku 已经是最便宜的，无法再降级
	model := tracker.SuggestDowngrade("claude-haiku-4-5")
	if model != "claude-haiku-4-5" {
		t.Errorf("expected no downgrade for haiku, got %s", model)
	}
}

func TestBudgetAlert_Threshold(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	tracker := NewTracker(Limits{DailyMax: 10.0}, logger)

	alertTriggered := false
	var alertLevel string
	tracker.SetAlertCallback(func(level string, current, limit float64, message string) {
		alertTriggered = true
		alertLevel = level
	})

	// 第一次记录：50% 使用，不应触发告警
	_ = tracker.Track(Record{
		AgentID:   "test",
		Provider:  "claude",
		Model:     "claude-opus-4-6",
		TokensIn:  100,
		TokensOut: 50,
		Cost:      5.0,
	})

	if alertTriggered {
		t.Error("alert should not trigger at 50% usage")
	}

	// 第二次记录：跨过 70% 阈值，应触发告警
	_ = tracker.Track(Record{
		AgentID:   "test",
		Provider:  "claude",
		Model:     "claude-opus-4-6",
		TokensIn:  100,
		TokensOut: 50,
		Cost:      2.5, // 总计 7.5，75%
	})

	if !alertTriggered {
		t.Error("alert should trigger at 70% threshold")
	}
	if alertLevel != "70%" {
		t.Errorf("alert level = %s, want 70%%", alertLevel)
	}

	// 重置
	alertTriggered = false
	alertLevel = ""

	// 第三次记录：跨过 90% 阈值，应再次触发告警
	_ = tracker.Track(Record{
		AgentID:   "test",
		Provider:  "claude",
		Model:     "claude-opus-4-6",
		TokensIn:  100,
		TokensOut: 50,
		Cost:      1.6, // 总计 9.1，91%
	})

	if !alertTriggered {
		t.Error("alert should trigger at 90% threshold")
	}
	if alertLevel != "90%" {
		t.Errorf("alert level = %s, want 90%%", alertLevel)
	}
}

func TestBudgetUsage(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	tracker := NewTracker(Limits{DailyMax: 10.0}, logger)

	usage := tracker.BudgetUsage()
	if usage != 0 {
		t.Errorf("initial usage = %.2f, want 0", usage)
	}

	_ = tracker.Track(Record{
		AgentID:   "test",
		Provider:  "claude",
		Model:     "claude-opus-4-6",
		TokensIn:  100,
		TokensOut: 50,
		Cost:      7.5,
	})

	usage = tracker.BudgetUsage()
	if usage != 0.75 {
		t.Errorf("usage = %.2f, want 0.75", usage)
	}
}

func TestRegisterDowngrade(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	tracker := NewTracker(Limits{DailyMax: 10.0}, logger)

	// 注册自定义降级映射
	tracker.RegisterDowngrade("custom-model-pro", "custom-model-lite")

	// 使用 80% 预算
	_ = tracker.Track(Record{
		AgentID:   "test",
		Provider:  "custom",
		Model:     "custom-model-pro",
		TokensIn:  100,
		TokensOut: 50,
		Cost:      8.0,
	})

	model := tracker.SuggestDowngrade("custom-model-pro")
	if model != "custom-model-lite" {
		t.Errorf("expected custom downgrade, got %s", model)
	}
}

func TestSetAlertThresholds(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	tracker := NewTracker(Limits{DailyMax: 10.0}, logger)

	// 设置自定义阈值：50%, 80%
	tracker.SetAlertThresholds([]float64{0.5, 0.8})

	alertCount := 0
	tracker.SetAlertCallback(func(level string, current, limit float64, message string) {
		alertCount++
	})

	// 跨过 50% 阈值
	_ = tracker.Track(Record{
		AgentID:   "test",
		Provider:  "claude",
		Model:     "claude-opus-4-6",
		TokensIn:  100,
		TokensOut: 50,
		Cost:      5.5, // 55%
	})

	if alertCount != 1 {
		t.Errorf("alert count = %d, want 1", alertCount)
	}

	// 跨过 80% 阈值
	_ = tracker.Track(Record{
		AgentID:   "test",
		Provider:  "claude",
		Model:     "claude-opus-4-6",
		TokensIn:  100,
		TokensOut: 50,
		Cost:      3.0, // 总计 8.5，85%
	})

	if alertCount != 2 {
		t.Errorf("alert count = %d, want 2", alertCount)
	}
}

func TestDowngradeMap_AllProviders(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	tracker := NewTracker(Limits{DailyMax: 10.0}, logger)

	// 使用 80% 预算
	_ = tracker.Track(Record{Cost: 8.0})

	tests := []struct {
		from string
		to   string
	}{
		{"claude-opus-4-6", "claude-sonnet-4-6"},
		{"claude-sonnet-4-6", "claude-haiku-4-5"},
		{"gemini-2.5-pro", "gemini-2.5-flash"},
		{"gpt-4o", "gpt-4o-mini"},
		{"o3", "gpt-4o"},
	}

	for _, tt := range tests {
		got := tracker.SuggestDowngrade(tt.from)
		if got != tt.to {
			t.Errorf("SuggestDowngrade(%s) = %s, want %s", tt.from, got, tt.to)
		}
	}
}
