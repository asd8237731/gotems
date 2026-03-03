package task

import (
	"testing"
)

func TestFileLockAcquireRelease(t *testing.T) {
	fl := NewFileLock()

	// 获取锁
	if err := fl.Acquire("main.go", "agent-1"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// 同一 Agent 重复获取应成功
	if err := fl.Acquire("main.go", "agent-1"); err != nil {
		t.Fatalf("re-acquire by same agent should succeed: %v", err)
	}

	// 不同 Agent 获取应失败
	if err := fl.Acquire("main.go", "agent-2"); err == nil {
		t.Fatal("expected error when different agent acquires locked file")
	}

	// 释放锁
	if err := fl.Release("main.go", "agent-1"); err != nil {
		t.Fatalf("unexpected error on release: %v", err)
	}

	// 释放后其他 Agent 可以获取
	if err := fl.Acquire("main.go", "agent-2"); err != nil {
		t.Fatalf("should acquire after release: %v", err)
	}
}

func TestFileLockReleaseAll(t *testing.T) {
	fl := NewFileLock()
	fl.Acquire("a.go", "agent-1")
	fl.Acquire("b.go", "agent-1")
	fl.Acquire("c.go", "agent-2")

	fl.ReleaseAll("agent-1")

	held := fl.HeldBy("agent-1")
	if len(held) != 0 {
		t.Fatalf("expected 0 locks for agent-1 after ReleaseAll, got %d", len(held))
	}

	// agent-2 的锁应该还在
	locked, holder := fl.IsLocked("c.go")
	if !locked || holder != "agent-2" {
		t.Fatalf("expected c.go to still be locked by agent-2")
	}
}

func TestFileLockIsLocked(t *testing.T) {
	fl := NewFileLock()

	locked, _ := fl.IsLocked("nope.go")
	if locked {
		t.Fatal("expected unlocked for non-existent file")
	}

	fl.Acquire("yes.go", "agent-1")
	locked, holder := fl.IsLocked("yes.go")
	if !locked || holder != "agent-1" {
		t.Fatalf("expected locked by agent-1, got locked=%v holder=%s", locked, holder)
	}
}
