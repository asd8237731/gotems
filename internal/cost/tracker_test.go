package cost

import (
	"log/slog"
	"math"
	"os"
	"testing"
)

func TestTrackerTrackAndSummarize(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	tracker := NewTracker(Limits{DailyMax: 100.0}, logger)

	err := tracker.Track(Record{
		AgentID:   "claude-1",
		Provider:  "claude",
		Model:     "claude-sonnet-4-6",
		TokensIn:  1000,
		TokensOut: 500,
		Cost:      0.05,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	err = tracker.Track(Record{
		AgentID:   "gemini-1",
		Provider:  "gemini",
		Model:     "gemini-2.5-pro",
		TokensIn:  2000,
		TokensOut: 800,
		Cost:      0.03,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	s := tracker.Summarize()
	if s.RecordCount != 2 {
		t.Fatalf("expected 2 records, got %d", s.RecordCount)
	}
	if s.TotalTokensIn != 3000 {
		t.Fatalf("expected 3000 tokens in, got %d", s.TotalTokensIn)
	}
	if s.TotalTokensOut != 1300 {
		t.Fatalf("expected 1300 tokens out, got %d", s.TotalTokensOut)
	}
	if !floatEqual(s.TotalCost, 0.08) {
		t.Fatalf("expected cost ~0.08, got %f", s.TotalCost)
	}
}

func TestTrackerDailyLimit(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	tracker := NewTracker(Limits{DailyMax: 0.10}, logger)

	err := tracker.Track(Record{Cost: 0.08})
	if err != nil {
		t.Fatalf("first track should succeed: %v", err)
	}

	err = tracker.Track(Record{Cost: 0.05})
	if err == nil {
		t.Fatal("expected error when exceeding daily limit")
	}
}

func TestTrackerTodayCost(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	tracker := NewTracker(Limits{}, logger)

	tracker.Track(Record{Cost: 0.10})
	tracker.Track(Record{Cost: 0.20})

	today := tracker.TodayCost()
	if !floatEqual(today, 0.30) {
		t.Fatalf("expected today cost ~0.30, got %f", today)
	}
}

// floatEqual 浮点数近似比较
func floatEqual(a, b float64) bool {
	return math.Abs(a-b) < 1e-9
}
