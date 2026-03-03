package session

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestStoreCreateAndGet(t *testing.T) {
	dir := t.TempDir()
	store := NewStore(dir)

	sess := store.Create("agent-1", "claude", "/tmp/work")

	if sess.AgentID != "agent-1" {
		t.Errorf("expected agent_id=agent-1, got %s", sess.AgentID)
	}
	if sess.Provider != "claude" {
		t.Errorf("expected provider=claude, got %s", sess.Provider)
	}

	got := store.Get("agent-1", "/tmp/work")
	if got == nil {
		t.Fatal("expected to find session")
	}
	if got.ID != sess.ID {
		t.Errorf("expected ID=%s, got %s", sess.ID, got.ID)
	}
}

func TestStoreGetOrCreate(t *testing.T) {
	dir := t.TempDir()
	store := NewStore(dir)

	sess1 := store.GetOrCreate("agent-1", "claude", "/work")
	sess2 := store.GetOrCreate("agent-1", "claude", "/work")

	if sess1.ID != sess2.ID {
		t.Error("GetOrCreate should return same session")
	}

	sess3 := store.GetOrCreate("agent-2", "gemini", "/work")
	if sess3.ID == sess1.ID {
		t.Error("different agent should get different session")
	}
}

func TestStoreUpdateSessionID(t *testing.T) {
	dir := t.TempDir()
	store := NewStore(dir)

	store.Create("agent-1", "claude", "/work")
	store.UpdateSessionID("agent-1", "/work", "cli-sess-42")

	sess := store.Get("agent-1", "/work")
	if sess.SessionID != "cli-sess-42" {
		t.Errorf("expected session_id=cli-sess-42, got %s", sess.SessionID)
	}
}

func TestStoreAppendTurn(t *testing.T) {
	dir := t.TempDir()
	store := NewStore(dir)

	store.Create("agent-1", "claude", "/work")
	store.AppendTurn("agent-1", "/work", Turn{
		Role:    "user",
		Content: "Hello",
	})
	store.AppendTurn("agent-1", "/work", Turn{
		Role:    "assistant",
		Content: "Hi there!",
	})

	sess := store.Get("agent-1", "/work")
	if len(sess.History) != 2 {
		t.Fatalf("expected 2 turns, got %d", len(sess.History))
	}
	if sess.History[0].Role != "user" || sess.History[0].Content != "Hello" {
		t.Errorf("unexpected turn 0: %+v", sess.History[0])
	}
}

func TestStoreSaveAndLoad(t *testing.T) {
	dir := t.TempDir()
	store := NewStore(dir)

	store.Create("agent-1", "claude", "/work")
	store.UpdateSessionID("agent-1", "/work", "saved-sess")
	store.AppendTurn("agent-1", "/work", Turn{
		Role:    "user",
		Content: "test",
	})

	if err := store.Save(); err != nil {
		t.Fatalf("save error: %v", err)
	}

	// 验证文件存在
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 {
		t.Fatalf("expected 1 session file, got %d", len(entries))
	}

	// 用新 store 加载
	store2 := NewStore(dir)
	if err := store2.Load(); err != nil {
		t.Fatalf("load error: %v", err)
	}

	sess := store2.Get("agent-1", "/work")
	if sess == nil {
		t.Fatal("expected to find loaded session")
	}
	if sess.SessionID != "saved-sess" {
		t.Errorf("expected session_id=saved-sess, got %s", sess.SessionID)
	}
	if len(sess.History) != 1 {
		t.Errorf("expected 1 history turn, got %d", len(sess.History))
	}
}

func TestStoreDelete(t *testing.T) {
	dir := t.TempDir()
	store := NewStore(dir)

	sess := store.Create("agent-1", "claude", "/work")
	_ = store.Save()

	store.Delete("agent-1", "/work")

	if store.Get("agent-1", "/work") != nil {
		t.Error("session should be deleted from memory")
	}

	// 验证文件已删除
	path := filepath.Join(dir, sess.ID+".json")
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Error("session file should be deleted")
	}
}

func TestStoreAll(t *testing.T) {
	dir := t.TempDir()
	store := NewStore(dir)

	store.Create("agent-1", "claude", "/work1")
	store.Create("agent-2", "gemini", "/work2")

	all := store.All()
	if len(all) != 2 {
		t.Errorf("expected 2 sessions, got %d", len(all))
	}
}

func TestStoreCleanup(t *testing.T) {
	dir := t.TempDir()
	store := NewStore(dir)

	sess := store.Create("old-agent", "claude", "/work")
	sess.UpdatedAt = time.Now().Add(-48 * time.Hour)

	store.Create("new-agent", "gemini", "/work")

	cleaned := store.Cleanup(24 * time.Hour)
	if cleaned != 1 {
		t.Errorf("expected 1 cleaned, got %d", cleaned)
	}

	all := store.All()
	if len(all) != 1 {
		t.Errorf("expected 1 remaining, got %d", len(all))
	}
}
