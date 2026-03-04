package checkpoint

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/lyymini/gotems/pkg/schema"
)

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
}

func TestSaveAndLoad(t *testing.T) {
	tmpDir := t.TempDir()
	logger := testLogger()
	mgr, err := NewManager(tmpDir, logger)
	if err != nil {
		t.Fatalf("NewManager failed: %v", err)
	}

	cp := &Checkpoint{
		AgentID: "agent-1",
		TaskID:  "task-1",
		State:   StateRunning,
		IntermediateResults: []*schema.Result{
			{AgentID: "agent-1", Content: "partial result"},
		},
		Context: map[string]interface{}{"step": 1},
	}

	ctx := context.Background()
	if err := mgr.Save(ctx, cp); err != nil {
		t.Fatalf("Save failed: %v", err)
	}

	loaded, err := mgr.Load("task-1")
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}

	if loaded.AgentID != "agent-1" {
		t.Errorf("agent_id = %s, want agent-1", loaded.AgentID)
	}
	if loaded.State != StateRunning {
		t.Errorf("state = %s, want running", loaded.State)
	}
	if loaded.Version != 1 {
		t.Errorf("version = %d, want 1", loaded.Version)
	}
}

func TestAutoSave_Count(t *testing.T) {
	tmpDir := t.TempDir()
	logger := testLogger()
	mgr, err := NewManager(tmpDir, logger)
	if err != nil {
		t.Fatalf("NewManager failed: %v", err)
	}

	mgr.SetAutoSave(3, 0) // 每 3 次操作保存一次

	cp := &Checkpoint{
		AgentID: "agent-1",
		TaskID:  "task-1",
		State:   StateRunning,
	}

	ctx := context.Background()

	// 前 2 次不应保存
	_ = mgr.AutoSave(ctx, cp)
	_ = mgr.AutoSave(ctx, cp)

	_, err = mgr.Load("task-1")
	if err == nil {
		t.Error("checkpoint should not exist after 2 operations")
	}

	// 第 3 次应该保存
	_ = mgr.AutoSave(ctx, cp)

	loaded, err := mgr.Load("task-1")
	if err != nil {
		t.Fatalf("Load failed after 3 operations: %v", err)
	}
	if loaded.Version != 1 {
		t.Errorf("version = %d, want 1", loaded.Version)
	}
}

func TestAutoSave_Interval(t *testing.T) {
	tmpDir := t.TempDir()
	logger := testLogger()
	mgr, err := NewManager(tmpDir, logger)
	if err != nil {
		t.Fatalf("NewManager failed: %v", err)
	}

	mgr.SetAutoSave(0, 200*time.Millisecond) // 每 200ms 保存一次

	cp := &Checkpoint{
		AgentID: "agent-1",
		TaskID:  "task-1",
		State:   StateRunning,
	}

	ctx := context.Background()

	// 第一次调用应该保存（lastSave 为零值）
	_ = mgr.AutoSave(ctx, cp)

	// 立即再次调用不应保存
	_ = mgr.AutoSave(ctx, cp)

	loaded, _ := mgr.Load("task-1")
	if loaded.Version != 1 {
		t.Errorf("version = %d, want 1 (only first save)", loaded.Version)
	}

	// 等待超过间隔时间
	time.Sleep(250 * time.Millisecond)
	_ = mgr.AutoSave(ctx, cp)

	loaded, _ = mgr.Load("task-1")
	if loaded.Version != 2 {
		t.Errorf("version = %d, want 2 (after interval)", loaded.Version)
	}
}

func TestVersionIncrement(t *testing.T) {
	tmpDir := t.TempDir()
	logger := testLogger()
	mgr, err := NewManager(tmpDir, logger)
	if err != nil {
		t.Fatalf("NewManager failed: %v", err)
	}

	cp := &Checkpoint{
		AgentID: "agent-1",
		TaskID:  "task-1",
		State:   StateRunning,
	}

	ctx := context.Background()

	// 保存 3 次
	for i := 0; i < 3; i++ {
		if err := mgr.Save(ctx, cp); err != nil {
			t.Fatalf("Save %d failed: %v", i+1, err)
		}
	}

	loaded, err := mgr.Load("task-1")
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}
	if loaded.Version != 3 {
		t.Errorf("version = %d, want 3", loaded.Version)
	}
}

func TestDelete(t *testing.T) {
	tmpDir := t.TempDir()
	logger := testLogger()
	mgr, err := NewManager(tmpDir, logger)
	if err != nil {
		t.Fatalf("NewManager failed: %v", err)
	}

	cp := &Checkpoint{
		AgentID: "agent-1",
		TaskID:  "task-1",
		State:   StateCompleted,
	}

	ctx := context.Background()
	_ = mgr.Save(ctx, cp)

	if err := mgr.Delete("task-1"); err != nil {
		t.Fatalf("Delete failed: %v", err)
	}

	_, err = mgr.Load("task-1")
	if err == nil {
		t.Error("checkpoint should not exist after delete")
	}

	// 验证磁盘文件也被删除
	pattern := filepath.Join(tmpDir, "task-1-v*.json")
	matches, _ := filepath.Glob(pattern)
	if len(matches) > 0 {
		t.Errorf("found %d checkpoint files after delete", len(matches))
	}
}

func TestLoadAll(t *testing.T) {
	tmpDir := t.TempDir()
	logger := testLogger()
	mgr, err := NewManager(tmpDir, logger)
	if err != nil {
		t.Fatalf("NewManager failed: %v", err)
	}

	ctx := context.Background()

	// 保存 3 个不同任务的检查点
	for i := 1; i <= 3; i++ {
		cp := &Checkpoint{
			AgentID: "agent-1",
			TaskID:  fmt.Sprintf("task-%d", i),
			State:   StateRunning,
		}
		_ = mgr.Save(ctx, cp)
	}

	// 创建新的 manager 并加载所有检查点
	mgr2, _ := NewManager(tmpDir, logger)
	if err := mgr2.LoadAll(); err != nil {
		t.Fatalf("LoadAll failed: %v", err)
	}

	list := mgr2.List()
	if len(list) != 3 {
		t.Errorf("loaded %d checkpoints, want 3", len(list))
	}
}

func TestCleanup(t *testing.T) {
	tmpDir := t.TempDir()
	logger := testLogger()
	mgr, err := NewManager(tmpDir, logger)
	if err != nil {
		t.Fatalf("NewManager failed: %v", err)
	}

	mgr.SetRetention(0) // 立即过期

	cp := &Checkpoint{
		AgentID: "agent-1",
		TaskID:  "task-1",
		State:   StateCompleted,
	}

	ctx := context.Background()
	_ = mgr.Save(ctx, cp)

	// 等待文件时间戳稳定
	time.Sleep(10 * time.Millisecond)

	if err := mgr.Cleanup(); err != nil {
		t.Fatalf("Cleanup failed: %v", err)
	}

	// 验证文件被删除
	pattern := filepath.Join(tmpDir, "*.json")
	matches, _ := filepath.Glob(pattern)
	if len(matches) > 0 {
		t.Errorf("found %d checkpoint files after cleanup", len(matches))
	}
}

func TestList(t *testing.T) {
	tmpDir := t.TempDir()
	logger := testLogger()
	mgr, err := NewManager(tmpDir, logger)
	if err != nil {
		t.Fatalf("NewManager failed: %v", err)
	}

	ctx := context.Background()

	// 保存 2 个检查点
	for i := 1; i <= 2; i++ {
		cp := &Checkpoint{
			AgentID: "agent-1",
			TaskID:  fmt.Sprintf("task-%d", i),
			State:   StateRunning,
		}
		_ = mgr.Save(ctx, cp)
	}

	list := mgr.List()
	if len(list) != 2 {
		t.Errorf("list count = %d, want 2", len(list))
	}
}

func TestGet(t *testing.T) {
	tmpDir := t.TempDir()
	logger := testLogger()
	mgr, err := NewManager(tmpDir, logger)
	if err != nil {
		t.Fatalf("NewManager failed: %v", err)
	}

	cp := &Checkpoint{
		AgentID: "agent-1",
		TaskID:  "task-1",
		State:   StateRunning,
	}

	ctx := context.Background()
	_ = mgr.Save(ctx, cp)

	retrieved, ok := mgr.Get("task-1")
	if !ok {
		t.Fatal("checkpoint not found")
	}
	if retrieved.AgentID != "agent-1" {
		t.Errorf("agent_id = %s, want agent-1", retrieved.AgentID)
	}
}

func TestStats(t *testing.T) {
	tmpDir := t.TempDir()
	logger := testLogger()
	mgr, err := NewManager(tmpDir, logger)
	if err != nil {
		t.Fatalf("NewManager failed: %v", err)
	}

	ctx := context.Background()

	// 保存不同状态的检查点
	states := []State{StateRunning, StateCompleted, StateFailed}
	for i, state := range states {
		cp := &Checkpoint{
			AgentID: "agent-1",
			TaskID:  fmt.Sprintf("task-%d", i+1),
			State:   state,
		}
		_ = mgr.Save(ctx, cp)
	}

	stats := mgr.Stats()
	if stats.TotalCheckpoints != 3 {
		t.Errorf("total = %d, want 3", stats.TotalCheckpoints)
	}
	if stats.ByState[StateRunning] != 1 {
		t.Errorf("running count = %d, want 1", stats.ByState[StateRunning])
	}
	if stats.ByState[StateCompleted] != 1 {
		t.Errorf("completed count = %d, want 1", stats.ByState[StateCompleted])
	}
	if stats.ByState[StateFailed] != 1 {
		t.Errorf("failed count = %d, want 1", stats.ByState[StateFailed])
	}
}

func TestLoadNonExistent(t *testing.T) {
	tmpDir := t.TempDir()
	logger := testLogger()
	mgr, err := NewManager(tmpDir, logger)
	if err != nil {
		t.Fatalf("NewManager failed: %v", err)
	}

	_, err = mgr.Load("non-existent-task")
	if err == nil {
		t.Error("expected error for non-existent checkpoint")
	}
}

func TestIntermediateResults(t *testing.T) {
	tmpDir := t.TempDir()
	logger := testLogger()
	mgr, err := NewManager(tmpDir, logger)
	if err != nil {
		t.Fatalf("NewManager failed: %v", err)
	}

	cp := &Checkpoint{
		AgentID: "agent-1",
		TaskID:  "task-1",
		State:   StatePartial,
		IntermediateResults: []*schema.Result{
			{AgentID: "agent-1", Content: "step 1 result", TokensIn: 100, TokensOut: 50},
			{AgentID: "agent-1", Content: "step 2 result", TokensIn: 150, TokensOut: 75},
		},
		Context: map[string]interface{}{
			"current_step": 2,
			"total_steps":  5,
		},
	}

	ctx := context.Background()
	_ = mgr.Save(ctx, cp)

	loaded, err := mgr.Load("task-1")
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}

	if len(loaded.IntermediateResults) != 2 {
		t.Errorf("intermediate results count = %d, want 2", len(loaded.IntermediateResults))
	}
	// Context 中的值可能是 int 或 float64（取决于 JSON 编码/解码）
	switch v := loaded.Context["current_step"].(type) {
	case int:
		if v != 2 {
			t.Errorf("current_step = %d, want 2", v)
		}
	case float64:
		if v != 2.0 {
			t.Errorf("current_step = %f, want 2.0", v)
		}
	default:
		t.Errorf("current_step has unexpected type %T", v)
	}
}
