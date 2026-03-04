package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/lyymini/gotems/internal/process"
	"github.com/lyymini/gotems/internal/session"
	"github.com/lyymini/gotems/internal/task"
	"github.com/lyymini/gotems/pkg/schema"
)

// ClaudeMode 定义 Claude 的运行模式
type ClaudeMode int

const (
	ClaudeModeAPI ClaudeMode = iota // 通过 Anthropic API 调用
	ClaudeModeCLI                   // 通过 claude CLI 子进程调用
)

// ClaudeAgent 是 Claude 的适配器，支持 API 和 CLI 双模式
// CLI 模式支持：--session-id 多轮会话、-y 自动审批、流式输出解析
type ClaudeAgent struct {
	BaseAgent
	apiKey       string
	mode         ClaudeMode
	cliPath      string
	autoApprove  bool   // -y 自动审批工具调用
	maxTurns     int    // --max-turns 最大轮数
	systemPrompt string // --system-prompt 系统提示
	httpClient   *http.Client
	sessionStore *session.Store
	procManager  *process.Manager
	logger       *slog.Logger
}

// ClaudeOption 配置选项
type ClaudeOption func(*ClaudeAgent)

func WithClaudeAPIKey(key string) ClaudeOption     { return func(a *ClaudeAgent) { a.apiKey = key } }
func WithClaudeMode(m ClaudeMode) ClaudeOption      { return func(a *ClaudeAgent) { a.mode = m } }
func WithClaudeCLIPath(p string) ClaudeOption       { return func(a *ClaudeAgent) { a.cliPath = p } }
func WithClaudeModel(m string) ClaudeOption         { return func(a *ClaudeAgent) { a.ModelID = m } }
func WithClaudeAutoApprove(v bool) ClaudeOption     { return func(a *ClaudeAgent) { a.autoApprove = v } }
func WithClaudeMaxTurns(n int) ClaudeOption         { return func(a *ClaudeAgent) { a.maxTurns = n } }
func WithClaudeSystemPrompt(s string) ClaudeOption  { return func(a *ClaudeAgent) { a.systemPrompt = s } }
func WithClaudeSessionStore(s *session.Store) ClaudeOption {
	return func(a *ClaudeAgent) { a.sessionStore = s }
}

// NewClaudeAgent 创建 Claude 智能体
func NewClaudeAgent(id string, logger *slog.Logger, opts ...ClaudeOption) *ClaudeAgent {
	a := &ClaudeAgent{
		BaseAgent: BaseAgent{
			AgentID:      id,
			ProviderType: ProviderClaude,
			ModelID:      "claude-sonnet-4-6",
			Caps: []Capability{
				CapReasoning, CapCodeReview, CapRefactor, CapCodeGen, CapLargeContext,
			},
			InboxCh:   make(chan *schema.Message, 50),
			StatusVal: StatusIdle,
		},
		cliPath:     "claude",
		autoApprove: true, // 默认自动审批，编排器场景无人交互
		maxTurns:    10,
		httpClient:  &http.Client{Timeout: 5 * time.Minute},
		procManager: process.NewManager(logger),
		logger:      logger,
	}
	for _, opt := range opts {
		opt(a)
	}
	return a
}

func (a *ClaudeAgent) Start(_ context.Context) error {
	a.StatusVal = StatusIdle
	a.logger.Info("claude agent started",
		"id", a.AgentID,
		"model", a.ModelID,
		"mode", a.modeString(),
		"auto_approve", a.autoApprove,
	)
	return nil
}

func (a *ClaudeAgent) Stop(_ context.Context) error {
	a.StatusVal = StatusStopped
	a.procManager.StopAll()
	a.logger.Info("claude agent stopped", "id", a.AgentID)
	return nil
}

func (a *ClaudeAgent) Execute(ctx context.Context, t *task.Task) (*schema.Result, error) {
	a.StatusVal = StatusBusy
	defer func() { a.StatusVal = StatusIdle }()

	start := time.Now()
	switch a.mode {
	case ClaudeModeAPI:
		return a.executeAPI(ctx, t, start)
	case ClaudeModeCLI:
		return a.executeCLI(ctx, t, start)
	default:
		return nil, fmt.Errorf("unknown claude mode: %d", a.mode)
	}
}

func (a *ClaudeAgent) Stream(ctx context.Context, t *task.Task) (<-chan schema.StreamEvent, error) {
	if a.mode == ClaudeModeCLI {
		return a.streamCLI(ctx, t)
	}

	// API 模式 fallback：执行后一次性返回
	ch := make(chan schema.StreamEvent, 100)
	go func() {
		defer close(ch)
		result, err := a.Execute(ctx, t)
		if err != nil {
			ch <- schema.StreamEvent{AgentID: a.AgentID, Type: "error", Content: err.Error()}
			return
		}
		ch <- schema.StreamEvent{AgentID: a.AgentID, Type: "text", Content: result.Content}
		ch <- schema.StreamEvent{AgentID: a.AgentID, Type: "done", Content: ""}
	}()
	return ch, nil
}

// executeAPI 通过 Anthropic Messages API 执行
func (a *ClaudeAgent) executeAPI(ctx context.Context, t *task.Task, start time.Time) (*schema.Result, error) {
	reqBody := map[string]any{
		"model":      a.ModelID,
		"max_tokens": 8192,
		"messages": []map[string]string{
			{"role": "user", "content": t.Prompt},
		},
	}
	bodyBytes, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://api.anthropic.com/v1/messages", bytes.NewReader(bodyBytes))
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-api-key", a.apiKey)
	req.Header.Set("anthropic-version", "2023-06-01")

	resp, err := a.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("api call: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("api error (status %d): %s", resp.StatusCode, string(respBody))
	}

	var apiResp anthropicResponse
	if err := json.Unmarshal(respBody, &apiResp); err != nil {
		return nil, fmt.Errorf("parse response: %w", err)
	}

	content := ""
	for _, block := range apiResp.Content {
		if block.Type == "text" {
			content += block.Text
		}
	}

	return &schema.Result{
		AgentID:   a.AgentID,
		Provider:  string(ProviderClaude),
		Content:   content,
		TokensIn:  apiResp.Usage.InputTokens,
		TokensOut: apiResp.Usage.OutputTokens,
		Duration:  time.Since(start),
	}, nil
}

// executeCLI 通过 claude CLI 子进程执行（完整会话支持）
// 使用 Process Manager 统一管理子进程生命周期
func (a *ClaudeAgent) executeCLI(ctx context.Context, t *task.Task, start time.Time) (*schema.Result, error) {
	args := a.buildCLIArgs(t)

	a.logger.Debug("executing claude cli",
		"args", strings.Join(args, " "),
		"work_dir", t.WorkDir,
	)

	procID := fmt.Sprintf("%s-%s-%d", a.AgentID, t.ID, time.Now().UnixNano())
	proc := a.procManager.Create(procID, process.Config{
		Binary:  a.cliPath,
		Args:    args,
		WorkDir: t.WorkDir,
	})
	defer a.procManager.Remove(procID)

	if err := proc.Start(ctx); err != nil {
		return nil, fmt.Errorf("claude cli start: %w", err)
	}

	stdoutStr, stderrStr, err := proc.CollectOutput(ctx)
	waitErr := proc.Wait()

	if waitErr != nil {
		errMsg := stderrStr
		if errMsg == "" {
			errMsg = waitErr.Error()
		}
		return nil, fmt.Errorf("claude cli: %s", errMsg)
	}
	if err != nil {
		return nil, fmt.Errorf("claude cli read output: %w", err)
	}

	// 解析 JSON 输出
	chunk, parseErr := process.ParseSingleJSON([]byte(stdoutStr))
	if parseErr != nil {
		// 非 JSON 输出，直接作为文本返回
		return &schema.Result{
			AgentID:  a.AgentID,
			Provider: string(ProviderClaude),
			Content:  stdoutStr,
			Duration: time.Since(start),
		}, nil
	}

	// 更新 session ID
	if chunk.SessionID != "" && a.sessionStore != nil {
		workDir := t.WorkDir
		if workDir == "" {
			workDir = "."
		}
		a.sessionStore.UpdateSessionID(a.AgentID, workDir, chunk.SessionID)
	}

	tokensIn, tokensOut := 0, 0
	if chunk.Usage != nil {
		tokensIn = chunk.Usage.InputTokens
		tokensOut = chunk.Usage.OutputTokens
	}

	return &schema.Result{
		AgentID:   a.AgentID,
		Provider:  string(ProviderClaude),
		Content:   chunk.Content,
		TokensIn:  tokensIn,
		TokensOut: tokensOut,
		Duration:  time.Since(start),
	}, nil
}

// streamCLI CLI 模式的流式输出
// 使用 Process Manager 统一管理子进程生命周期
func (a *ClaudeAgent) streamCLI(ctx context.Context, t *task.Task) (<-chan schema.StreamEvent, error) {
	args := a.buildCLIArgs(t)
	// 流式模式使用 stream-json 格式
	for i, arg := range args {
		if arg == "json" && i > 0 && args[i-1] == "--output-format" {
			args[i] = "stream-json"
		}
	}

	procID := fmt.Sprintf("%s-%s-stream-%d", a.AgentID, t.ID, time.Now().UnixNano())
	proc := a.procManager.Create(procID, process.Config{
		Binary:  a.cliPath,
		Args:    args,
		WorkDir: t.WorkDir,
	})

	if err := proc.Start(ctx); err != nil {
		a.procManager.Remove(procID)
		return nil, fmt.Errorf("claude cli stream start: %w", err)
	}

	ch := make(chan schema.StreamEvent, 256)

	go func() {
		defer close(ch)
		defer a.procManager.Remove(procID)

		// 通过 Process 的 StreamOutput 获取输出流
		for line := range proc.StreamOutput(ctx) {
			if line.Stream == "stderr" {
				continue
			}
			// 尝试解析为 JSON 流事件
			var chunk process.StreamChunk
			if err := json.Unmarshal([]byte(line.Content), &chunk); err != nil {
				ch <- schema.StreamEvent{AgentID: a.AgentID, Type: "text", Content: line.Content}
				continue
			}

			switch chunk.Type {
			case "text":
				ch <- schema.StreamEvent{AgentID: a.AgentID, Type: "text", Content: chunk.Content}
			case "tool_use":
				ch <- schema.StreamEvent{AgentID: a.AgentID, Type: "tool_use", Content: chunk.ToolName}
			case "thinking":
				ch <- schema.StreamEvent{AgentID: a.AgentID, Type: "thinking", Content: chunk.Content}
			case "result":
				ch <- schema.StreamEvent{AgentID: a.AgentID, Type: "text", Content: chunk.Content}
				if chunk.SessionID != "" && a.sessionStore != nil {
					workDir := t.WorkDir
					if workDir == "" {
						workDir = "."
					}
					a.sessionStore.UpdateSessionID(a.AgentID, workDir, chunk.SessionID)
				}
			case "error":
				ch <- schema.StreamEvent{AgentID: a.AgentID, Type: "error", Content: chunk.Content}
			}
		}

		_ = proc.Wait()
		ch <- schema.StreamEvent{AgentID: a.AgentID, Type: "done"}
	}()

	return ch, nil
}

// buildCLIArgs 构建 CLI 命令参数
func (a *ClaudeAgent) buildCLIArgs(t *task.Task) []string {
	args := []string{
		"-p", t.Prompt,
		"--output-format", "json",
	}

	// 自动审批
	if a.autoApprove {
		args = append(args, "--dangerously-skip-permissions")
	}

	// 最大轮数
	if a.maxTurns > 0 {
		args = append(args, "--max-turns", fmt.Sprintf("%d", a.maxTurns))
	}

	// 指定模型
	if a.ModelID != "" {
		args = append(args, "--model", a.ModelID)
	}

	// 系统提示
	if a.systemPrompt != "" {
		args = append(args, "--system-prompt", a.systemPrompt)
	}

	// 多轮会话：复用 session ID
	if a.sessionStore != nil {
		workDir := t.WorkDir
		if workDir == "" {
			workDir = "."
		}
		if sess := a.sessionStore.Get(a.AgentID, workDir); sess != nil && sess.SessionID != "" {
			args = append(args, "--session-id", sess.SessionID)
		}
	}

	return args
}

func (a *ClaudeAgent) modeString() string {
	if a.mode == ClaudeModeCLI {
		return "cli"
	}
	return "api"
}

// Anthropic API 响应结构
type anthropicResponse struct {
	Content []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"content"`
	Usage struct {
		InputTokens  int `json:"input_tokens"`
		OutputTokens int `json:"output_tokens"`
	} `json:"usage"`
}
