package agent

import (
	"sync/atomic"
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

// OpenAIMode 定义 OpenAI 的运行模式
type OpenAIMode int

const (
	OpenAIModeAPI OpenAIMode = iota // 通过 OpenAI API 调用
	OpenAIModeCLI                   // 通过 codex / opencode CLI 子进程调用
)

// OpenAIAgent 是 OpenAI 的适配器，支持 API 和 CLI 双模式
type OpenAIAgent struct {
	BaseAgent
	apiKey       string
	mode         OpenAIMode
	cliPath      string     // codex / opencode CLI 路径
	autoApprove  bool
	httpClient   *http.Client
	sessionStore *session.Store
	procManager  *process.Manager
	logger       *slog.Logger
}

type OpenAIOption func(*OpenAIAgent)

func WithOpenAIAPIKey(key string) OpenAIOption    { return func(a *OpenAIAgent) { a.apiKey = key } }
func WithOpenAIModel(m string) OpenAIOption       { return func(a *OpenAIAgent) { a.ModelID = m } }
func WithOpenAIMode(m OpenAIMode) OpenAIOption    { return func(a *OpenAIAgent) { a.mode = m } }
func WithOpenAICLIPath(p string) OpenAIOption     { return func(a *OpenAIAgent) { a.cliPath = p } }
func WithOpenAIAutoApprove(v bool) OpenAIOption   { return func(a *OpenAIAgent) { a.autoApprove = v } }
func WithOpenAISessionStore(s *session.Store) OpenAIOption {
	return func(a *OpenAIAgent) { a.sessionStore = s }
}

// NewOpenAIAgent 创建 OpenAI 智能体
func NewOpenAIAgent(id string, logger *slog.Logger, opts ...OpenAIOption) *OpenAIAgent {
	a := &OpenAIAgent{
		BaseAgent: BaseAgent{
			AgentID:      id,
			ProviderType: ProviderOpenAI,
			ModelID:      "gpt-4o",
			Caps: []Capability{
				CapCodeGen, CapTestGen, CapQuickTask,
			},
			InboxCh:   make(chan *schema.Message, 50),
			StatusVal: atomic.Int32{},
		},
		cliPath:     "codex",
		autoApprove: true,
		httpClient:  &http.Client{Timeout: 5 * time.Minute},
		procManager: process.NewManager(logger),
		logger:      logger,
	}
	for _, opt := range opts {
		opt(a)
	}
	return a
}

func (a *OpenAIAgent) Start(_ context.Context) error {
	a.SetStatus(StatusIdle)
	a.logger.Info("openai agent started",
		"id", a.AgentID,
		"model", a.ModelID,
		"mode", a.modeString(),
	)
	return nil
}

func (a *OpenAIAgent) Stop(_ context.Context) error {
	a.SetStatus(StatusStopped)
	a.procManager.StopAll()
	a.logger.Info("openai agent stopped", "id", a.AgentID)
	return nil
}

func (a *OpenAIAgent) Execute(ctx context.Context, t *task.Task) (*schema.Result, error) {
	a.SetStatus(StatusBusy)
	defer func() { a.SetStatus(StatusIdle) }()

	start := time.Now()
	switch a.mode {
	case OpenAIModeAPI:
		return a.executeAPI(ctx, t, start)
	case OpenAIModeCLI:
		return a.executeCLI(ctx, t, start)
	default:
		return nil, fmt.Errorf("unknown openai mode: %d", a.mode)
	}
}

func (a *OpenAIAgent) Stream(ctx context.Context, t *task.Task) (<-chan schema.StreamEvent, error) {
	if a.mode == OpenAIModeCLI {
		return a.streamCLI(ctx, t)
	}

	ch := make(chan schema.StreamEvent, 100)
	go func() {
		defer close(ch)
		result, err := a.Execute(ctx, t)
		if err != nil {
			ch <- schema.StreamEvent{AgentID: a.AgentID, Type: "error", Content: err.Error()}
			return
		}
		ch <- schema.StreamEvent{AgentID: a.AgentID, Type: "text", Content: result.Content}
		ch <- schema.StreamEvent{AgentID: a.AgentID, Type: "done"}
	}()
	return ch, nil
}

// executeAPI 通过 OpenAI Chat Completions API 执行
func (a *OpenAIAgent) executeAPI(ctx context.Context, t *task.Task, start time.Time) (*schema.Result, error) {
	reqBody := map[string]any{
		"model": a.ModelID,
		"messages": []map[string]string{
			{"role": "user", "content": t.Prompt},
		},
		"max_tokens": 8192,
	}

	bodyBytes, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://api.openai.com/v1/chat/completions", bytes.NewReader(bodyBytes))
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+a.apiKey)

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
		return nil, fmt.Errorf("openai api error (status %d): %s", resp.StatusCode, string(respBody))
	}

	var oaiResp openaiResponse
	if err := json.Unmarshal(respBody, &oaiResp); err != nil {
		return nil, fmt.Errorf("parse response: %w", err)
	}

	content := ""
	if len(oaiResp.Choices) > 0 {
		content = oaiResp.Choices[0].Message.Content
	}

	return &schema.Result{
		AgentID:   a.AgentID,
		Provider:  string(ProviderOpenAI),
		Content:   content,
		TokensIn:  oaiResp.Usage.PromptTokens,
		TokensOut: oaiResp.Usage.CompletionTokens,
		Duration:  time.Since(start),
	}, nil
}

// executeCLI 通过 codex/opencode CLI 子进程执行
// 使用 Process Manager 统一管理子进程生命周期
func (a *OpenAIAgent) executeCLI(ctx context.Context, t *task.Task, start time.Time) (*schema.Result, error) {
	args := a.buildCLIArgs(t)

	a.logger.Debug("executing openai cli",
		"binary", a.cliPath,
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
		return nil, fmt.Errorf("%s cli start: %w", a.cliPath, err)
	}

	stdoutStr, stderrStr, err := proc.CollectOutput(ctx)
	waitErr := proc.Wait()

	if waitErr != nil {
		errMsg := stderrStr
		if errMsg == "" {
			errMsg = waitErr.Error()
		}
		return nil, fmt.Errorf("%s cli: %s", a.cliPath, errMsg)
	}
	if err != nil {
		return nil, fmt.Errorf("%s cli read output: %w", a.cliPath, err)
	}

	// 尝试 JSON 解析
	chunk, parseErr := process.ParseSingleJSON([]byte(stdoutStr))
	if parseErr != nil {
		return &schema.Result{
			AgentID:  a.AgentID,
			Provider: string(ProviderOpenAI),
			Content:  stdoutStr,
			Duration: time.Since(start),
		}, nil
	}

	tokensIn, tokensOut := 0, 0
	if chunk.Usage != nil {
		tokensIn = chunk.Usage.InputTokens
		tokensOut = chunk.Usage.OutputTokens
	}

	return &schema.Result{
		AgentID:   a.AgentID,
		Provider:  string(ProviderOpenAI),
		Content:   chunk.Content,
		TokensIn:  tokensIn,
		TokensOut: tokensOut,
		Duration:  time.Since(start),
	}, nil
}

// streamCLI codex/opencode CLI 流式输出
// 使用 Process Manager 统一管理子进程生命周期
func (a *OpenAIAgent) streamCLI(ctx context.Context, t *task.Task) (<-chan schema.StreamEvent, error) {
	args := a.buildCLIArgs(t)

	procID := fmt.Sprintf("%s-%s-stream-%d", a.AgentID, t.ID, time.Now().UnixNano())
	proc := a.procManager.Create(procID, process.Config{
		Binary:  a.cliPath,
		Args:    args,
		WorkDir: t.WorkDir,
	})

	if err := proc.Start(ctx); err != nil {
		a.procManager.Remove(procID)
		return nil, fmt.Errorf("%s cli stream start: %w", a.cliPath, err)
	}

	ch := make(chan schema.StreamEvent, 256)

	go func() {
		defer close(ch)
		defer a.procManager.Remove(procID)

		for line := range proc.StreamOutput(ctx) {
			if line.Stream == "stderr" {
				continue
			}
			ch <- schema.StreamEvent{AgentID: a.AgentID, Type: "text", Content: line.Content}
		}

		_ = proc.Wait()
		ch <- schema.StreamEvent{AgentID: a.AgentID, Type: "done"}
	}()

	return ch, nil
}

// buildCLIArgs 构建 codex CLI 参数
func (a *OpenAIAgent) buildCLIArgs(t *task.Task) []string {
	// codex CLI: codex -p "prompt" --approval-mode full-auto
	args := []string{"-p", t.Prompt}

	if a.autoApprove {
		args = append(args, "--approval-mode", "full-auto")
	}

	return args
}

func (a *OpenAIAgent) modeString() string {
	if a.mode == OpenAIModeCLI {
		return "cli"
	}
	return "api"
}

type openaiResponse struct {
	Choices []struct {
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
	} `json:"choices"`
	Usage struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
	} `json:"usage"`
}
