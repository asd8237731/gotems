package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os/exec"
	"time"

	"github.com/lyymini/gotems/internal/task"
	"github.com/lyymini/gotems/pkg/schema"
)

// ClaudeMode 定义 Claude 的运行模式
type ClaudeMode int

const (
	ClaudeModeAPI ClaudeMode = iota // 通过 Anthropic API 调用
	ClaudeModeCLI                   // 通过 claude -p CLI 调用
)

// ClaudeAgent 是 Claude 的适配器
type ClaudeAgent struct {
	BaseAgent
	apiKey     string
	mode       ClaudeMode
	cliPath    string
	httpClient *http.Client
	logger     *slog.Logger
}

// ClaudeOption 配置选项
type ClaudeOption func(*ClaudeAgent)

func WithClaudeAPIKey(key string) ClaudeOption  { return func(a *ClaudeAgent) { a.apiKey = key } }
func WithClaudeMode(m ClaudeMode) ClaudeOption   { return func(a *ClaudeAgent) { a.mode = m } }
func WithClaudeCLIPath(p string) ClaudeOption    { return func(a *ClaudeAgent) { a.cliPath = p } }
func WithClaudeModel(m string) ClaudeOption      { return func(a *ClaudeAgent) { a.ModelID = m } }

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
		cliPath:    "claude",
		httpClient: &http.Client{Timeout: 5 * time.Minute},
		logger:     logger,
	}
	for _, opt := range opts {
		opt(a)
	}
	return a
}

func (a *ClaudeAgent) Start(_ context.Context) error {
	a.StatusVal = StatusIdle
	a.logger.Info("claude agent started", "id", a.AgentID, "model", a.ModelID, "mode", a.modeString())
	return nil
}

func (a *ClaudeAgent) Stop(_ context.Context) error {
	a.StatusVal = StatusStopped
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

// executeCLI 通过 claude -p 命令行执行
func (a *ClaudeAgent) executeCLI(ctx context.Context, t *task.Task, start time.Time) (*schema.Result, error) {
	args := []string{"-p", t.Prompt, "--output-format", "json"}
	cmd := exec.CommandContext(ctx, a.cliPath, args...)
	if t.WorkDir != "" {
		cmd.Dir = t.WorkDir
	}

	output, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("claude cli: %w", err)
	}

	var cliResp cliResponse
	if err := json.Unmarshal(output, &cliResp); err != nil {
		// 非 JSON 输出，直接作为文本
		return &schema.Result{
			AgentID:  a.AgentID,
			Provider: string(ProviderClaude),
			Content:  string(output),
			Duration: time.Since(start),
		}, nil
	}

	return &schema.Result{
		AgentID:   a.AgentID,
		Provider:  string(ProviderClaude),
		Content:   cliResp.Result,
		TokensIn:  cliResp.Usage.InputTokens,
		TokensOut: cliResp.Usage.OutputTokens,
		Duration:  time.Since(start),
	}, nil
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

// Claude CLI JSON 响应结构
type cliResponse struct {
	Result string `json:"result"`
	Usage  struct {
		InputTokens  int `json:"input_tokens"`
		OutputTokens int `json:"output_tokens"`
	} `json:"usage"`
}
