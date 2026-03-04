package agent

import (
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/lyymini/gotems/internal/session"
	"github.com/lyymini/gotems/internal/task"
)

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
}

// createMockCLI 创建一个 mock CLI 脚本，输出指定的 JSON 响应
func createMockCLI(t *testing.T, dir string, name string, output map[string]any) string {
	t.Helper()
	data, err := json.Marshal(output)
	if err != nil {
		t.Fatalf("marshal mock output: %v", err)
	}

	scriptPath := filepath.Join(dir, name)
	// 写一个简单的 shell 脚本，输出 JSON
	script := "#!/bin/sh\necho '" + string(data) + "'\n"
	if err := os.WriteFile(scriptPath, []byte(script), 0755); err != nil {
		t.Fatalf("write mock script: %v", err)
	}
	return scriptPath
}

// TestClaudeAgent_CLI_E2E 端到端测试：Claude Agent CLI 模式通过 Process Manager 执行
func TestClaudeAgent_CLI_E2E(t *testing.T) {
	tmpDir := t.TempDir()
	logger := testLogger()

	mockOutput := map[string]any{
		"result":     "Hello from mock Claude",
		"session_id": "sess-abc-123",
		"usage": map[string]any{
			"input_tokens":  150,
			"output_tokens": 80,
		},
	}
	cliPath := createMockCLI(t, tmpDir, "claude", mockOutput)

	store := session.NewStore(filepath.Join(tmpDir, "sessions"))
	// 预创建 session，UpdateSessionID 只更新已存在的 session
	store.GetOrCreate("claude-e2e", "claude", tmpDir)

	agent := NewClaudeAgent("claude-e2e", logger,
		WithClaudeMode(ClaudeModeCLI),
		WithClaudeCLIPath(cliPath),
		WithClaudeModel("claude-sonnet-4-6"),
		WithClaudeSessionStore(store),
	)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := agent.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer agent.Stop(ctx)

	tk := &task.Task{ID: "e2e-1", Prompt: "test prompt", WorkDir: tmpDir}
	result, err := agent.Execute(ctx, tk)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}

	if result.AgentID != "claude-e2e" {
		t.Errorf("AgentID = %s, want claude-e2e", result.AgentID)
	}
	if result.Provider != "claude" {
		t.Errorf("Provider = %s, want claude", result.Provider)
	}
	if result.Content != "Hello from mock Claude" {
		t.Errorf("Content = %q, want 'Hello from mock Claude'", result.Content)
	}
	if result.TokensIn != 150 {
		t.Errorf("TokensIn = %d, want 150", result.TokensIn)
	}
	if result.TokensOut != 80 {
		t.Errorf("TokensOut = %d, want 80", result.TokensOut)
	}

	// 验证 session ID 被保存
	sess := store.Get("claude-e2e", tmpDir)
	if sess == nil || sess.SessionID != "sess-abc-123" {
		t.Errorf("session not persisted, got %v", sess)
	}
}

// TestOpenAIAgent_CLI_E2E 端到端测试：OpenAI Agent CLI 模式通过 Process Manager 执行
func TestOpenAIAgent_CLI_E2E(t *testing.T) {
	tmpDir := t.TempDir()
	logger := testLogger()

	mockOutput := map[string]any{
		"result": "Hello from mock Codex",
		"usage": map[string]any{
			"input_tokens":  200,
			"output_tokens": 100,
		},
	}
	cliPath := createMockCLI(t, tmpDir, "codex", mockOutput)

	agent := NewOpenAIAgent("openai-e2e", logger,
		WithOpenAIMode(OpenAIModeCLI),
		WithOpenAICLIPath(cliPath),
		WithOpenAIModel("gpt-4o"),
	)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := agent.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer agent.Stop(ctx)

	tk := &task.Task{ID: "e2e-2", Prompt: "test prompt", WorkDir: tmpDir}
	result, err := agent.Execute(ctx, tk)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}

	if result.AgentID != "openai-e2e" {
		t.Errorf("AgentID = %s, want openai-e2e", result.AgentID)
	}
	if result.Provider != "openai" {
		t.Errorf("Provider = %s, want openai", result.Provider)
	}
	if result.Content != "Hello from mock Codex" {
		t.Errorf("Content = %q, want 'Hello from mock Codex'", result.Content)
	}
	if result.TokensIn != 200 {
		t.Errorf("TokensIn = %d, want 200", result.TokensIn)
	}
	if result.TokensOut != 100 {
		t.Errorf("TokensOut = %d, want 100", result.TokensOut)
	}
}

// TestGeminiAgent_CLI_E2E 端到端测试：Gemini Agent CLI 模式通过 Process Manager 执行
func TestGeminiAgent_CLI_E2E(t *testing.T) {
	tmpDir := t.TempDir()
	logger := testLogger()

	mockOutput := map[string]any{
		"result": "Hello from mock Gemini",
		"usage": map[string]any{
			"input_tokens":  300,
			"output_tokens": 120,
		},
	}
	cliPath := createMockCLI(t, tmpDir, "gemini", mockOutput)

	agent := NewGeminiAgent("gemini-e2e", logger,
		WithGeminiMode(GeminiModeCLI),
		WithGeminiCLIPath(cliPath),
		WithGeminiModel("gemini-2.5-pro"),
	)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := agent.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer agent.Stop(ctx)

	tk := &task.Task{ID: "e2e-3", Prompt: "test prompt", WorkDir: tmpDir}
	result, err := agent.Execute(ctx, tk)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}

	if result.AgentID != "gemini-e2e" {
		t.Errorf("AgentID = %s, want gemini-e2e", result.AgentID)
	}
	if result.Provider != "gemini" {
		t.Errorf("Provider = %s, want gemini", result.Provider)
	}
	if result.Content != "Hello from mock Gemini" {
		t.Errorf("Content = %q, want 'Hello from mock Gemini'", result.Content)
	}
	if result.TokensIn != 300 {
		t.Errorf("TokensIn = %d, want 300", result.TokensIn)
	}
	if result.TokensOut != 120 {
		t.Errorf("TokensOut = %d, want 120", result.TokensOut)
	}
}

// TestClaudeAgent_CLI_PlainText 测试 CLI 返回非 JSON 纯文本时的降级处理
func TestClaudeAgent_CLI_PlainText(t *testing.T) {
	tmpDir := t.TempDir()
	logger := testLogger()

	// 直接输出纯文本
	scriptPath := filepath.Join(tmpDir, "claude-plain")
	script := "#!/bin/sh\necho 'This is plain text output'\n"
	if err := os.WriteFile(scriptPath, []byte(script), 0755); err != nil {
		t.Fatalf("write script: %v", err)
	}

	agent := NewClaudeAgent("claude-plain", logger,
		WithClaudeMode(ClaudeModeCLI),
		WithClaudeCLIPath(scriptPath),
	)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	_ = agent.Start(ctx)
	defer agent.Stop(ctx)

	tk := &task.Task{ID: "plain-1", Prompt: "test"}
	result, err := agent.Execute(ctx, tk)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}

	if result.Content != "This is plain text output\n" {
		t.Errorf("Content = %q, want 'This is plain text output\\n'", result.Content)
	}
	if result.TokensIn != 0 {
		t.Errorf("TokensIn = %d, want 0 for plain text", result.TokensIn)
	}
}

// TestClaudeAgent_CLI_ExitError 测试 CLI 非零退出码的错误处理
func TestClaudeAgent_CLI_ExitError(t *testing.T) {
	tmpDir := t.TempDir()
	logger := testLogger()

	scriptPath := filepath.Join(tmpDir, "claude-fail")
	script := "#!/bin/sh\necho 'something went wrong' >&2\nexit 1\n"
	if err := os.WriteFile(scriptPath, []byte(script), 0755); err != nil {
		t.Fatalf("write script: %v", err)
	}

	agent := NewClaudeAgent("claude-fail", logger,
		WithClaudeMode(ClaudeModeCLI),
		WithClaudeCLIPath(scriptPath),
	)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	_ = agent.Start(ctx)
	defer agent.Stop(ctx)

	tk := &task.Task{ID: "fail-1", Prompt: "test"}
	_, err := agent.Execute(ctx, tk)
	if err == nil {
		t.Fatal("expected error for exit code 1")
	}
}

// TestClaudeAgent_CLI_WorkDir 测试 CLI 工作目录切换
func TestClaudeAgent_CLI_WorkDir(t *testing.T) {
	tmpDir := t.TempDir()
	workDir := t.TempDir()
	logger := testLogger()

	// 输出当前工作目录的脚本
	scriptPath := filepath.Join(tmpDir, "claude-pwd")
	script := "#!/bin/sh\npwd\n"
	if err := os.WriteFile(scriptPath, []byte(script), 0755); err != nil {
		t.Fatalf("write script: %v", err)
	}

	agent := NewClaudeAgent("claude-pwd", logger,
		WithClaudeMode(ClaudeModeCLI),
		WithClaudeCLIPath(scriptPath),
	)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	_ = agent.Start(ctx)
	defer agent.Stop(ctx)

	tk := &task.Task{ID: "wd-1", Prompt: "test", WorkDir: workDir}
	result, err := agent.Execute(ctx, tk)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}

	// macOS /private/tmp 映射
	got := result.Content
	if got != workDir+"\n" && got != "/private"+workDir+"\n" {
		t.Errorf("WorkDir not applied, got %q, want %q", got, workDir)
	}
}

// TestClaudeAgent_Stream_E2E 端到端测试：Claude Agent CLI 流式输出
func TestClaudeAgent_Stream_E2E(t *testing.T) {
	tmpDir := t.TempDir()
	logger := testLogger()

	// 模拟流式输出（每行一个 JSON）
	scriptPath := filepath.Join(tmpDir, "claude-stream")
	script := `#!/bin/sh
echo '{"type":"text","content":"Hello "}'
echo '{"type":"text","content":"World"}'
echo '{"type":"result","content":"Hello World","session_id":"stream-sess-1"}'
`
	if err := os.WriteFile(scriptPath, []byte(script), 0755); err != nil {
		t.Fatalf("write script: %v", err)
	}

	store := session.NewStore(filepath.Join(tmpDir, "sessions"))

	agent := NewClaudeAgent("claude-stream", logger,
		WithClaudeMode(ClaudeModeCLI),
		WithClaudeCLIPath(scriptPath),
		WithClaudeSessionStore(store),
	)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	_ = agent.Start(ctx)
	defer agent.Stop(ctx)

	tk := &task.Task{ID: "stream-1", Prompt: "test", WorkDir: tmpDir}
	ch, err := agent.Stream(ctx, tk)
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}

	var events []string
	for ev := range ch {
		events = append(events, ev.Type+":"+ev.Content)
	}

	// 应该有 text + text + text(result) + done
	hasDone := false
	hasText := false
	for _, e := range events {
		if e == "done:" {
			hasDone = true
		}
		if len(e) > 5 && e[:5] == "text:" {
			hasText = true
		}
	}

	if !hasDone {
		t.Errorf("missing 'done' event, got %v", events)
	}
	if !hasText {
		t.Errorf("missing 'text' event, got %v", events)
	}
}
