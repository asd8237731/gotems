package observability

import (
	"log/slog"
	"os"
	"strings"
	"testing"
	"time"
)

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
}

func TestMetrics_RecordTask(t *testing.T) {
	m := NewMetrics(testLogger())

	m.RecordTask("claude", true, 1000, 500, 100*time.Millisecond)
	m.RecordTask("claude", true, 2000, 1000, 200*time.Millisecond)
	m.RecordTask("gemini", false, 500, 0, 50*time.Millisecond)

	snap := m.Snapshot()
	if snap.TasksTotal != 3 {
		t.Errorf("TasksTotal = %d, want 3", snap.TasksTotal)
	}
	if snap.TasksSucceeded != 2 {
		t.Errorf("TasksSucceeded = %d, want 2", snap.TasksSucceeded)
	}
	if snap.TasksFailed != 1 {
		t.Errorf("TasksFailed = %d, want 1", snap.TasksFailed)
	}
	if snap.TokensIn != 3500 {
		t.Errorf("TokensIn = %d, want 3500", snap.TokensIn)
	}
	if snap.TokensOut != 1500 {
		t.Errorf("TokensOut = %d, want 1500", snap.TokensOut)
	}

	// 按 provider 验证
	claude, ok := snap.ByProvider["claude"]
	if !ok {
		t.Fatal("missing claude in ByProvider")
	}
	if claude.Requests != 2 {
		t.Errorf("claude requests = %d, want 2", claude.Requests)
	}
	if claude.Failures != 0 {
		t.Errorf("claude failures = %d, want 0", claude.Failures)
	}

	gemini, ok := snap.ByProvider["gemini"]
	if !ok {
		t.Fatal("missing gemini in ByProvider")
	}
	if gemini.Failures != 1 {
		t.Errorf("gemini failures = %d, want 1", gemini.Failures)
	}
}

func TestMetrics_AvgLatency(t *testing.T) {
	m := NewMetrics(testLogger())

	m.RecordTask("claude", true, 100, 50, 100*time.Millisecond)
	m.RecordTask("claude", true, 100, 50, 300*time.Millisecond)

	snap := m.Snapshot()
	claude := snap.ByProvider["claude"]
	if claude.AvgLatencyMs != 200 {
		t.Errorf("AvgLatencyMs = %f, want 200", claude.AvgLatencyMs)
	}
}

func TestMetrics_PrometheusText(t *testing.T) {
	m := NewMetrics(testLogger())
	m.RecordTask("claude", true, 1000, 500, 100*time.Millisecond)

	text := m.PrometheusText()

	expected := []string{
		"gotems_tasks_total 1",
		"gotems_tasks_succeeded_total 1",
		"gotems_tasks_failed_total 0",
		"gotems_tokens_in_total 1000",
		"gotems_tokens_out_total 500",
		`gotems_provider_requests_total{provider="claude"} 1`,
	}

	for _, e := range expected {
		if !strings.Contains(text, e) {
			t.Errorf("PrometheusText missing: %s", e)
		}
	}
}

func TestMetrics_Concurrent(t *testing.T) {
	m := NewMetrics(testLogger())

	done := make(chan struct{})
	for i := 0; i < 100; i++ {
		go func() {
			m.RecordTask("claude", true, 100, 50, time.Millisecond)
			done <- struct{}{}
		}()
	}
	for i := 0; i < 100; i++ {
		<-done
	}

	snap := m.Snapshot()
	if snap.TasksTotal != 100 {
		t.Errorf("TasksTotal = %d, want 100", snap.TasksTotal)
	}
}
