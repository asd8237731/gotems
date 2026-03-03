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
	"strings"
	"time"

	"github.com/lyymini/gotems/internal/process"
	"github.com/lyymini/gotems/internal/session"
	"github.com/lyymini/gotems/internal/task"
	"github.com/lyymini/gotems/pkg/schema"
)

// GeminiMode 定义 Gemini 的运行模式
type GeminiMode int

const (
	GeminiModeAPI GeminiMode = iota // 通过 Google AI API 调用
	GeminiModeCLI                   // 通过 gemini CLI 子进程调用
)

// GeminiAgent 是 Google Gemini 的适配器，支持 API 和 CLI 双模式
type GeminiAgent struct {
	BaseAgent
	apiKey       string
	mode         GeminiMode
	cliPath      string     // gemini CLI 路径
	autoApprove  bool
	httpClient   *http.Client
	sessionStore *session.Store
	procManager  *process.Manager
	logger       *slog.Logger
}

type GeminiOption func(*GeminiAgent)

func WithGeminiAPIKey(key string) GeminiOption    { return func(a *GeminiAgent) { a.apiKey = key } }
func WithGeminiModel(m string) GeminiOption       { return func(a *GeminiAgent) { a.ModelID = m } }
func WithGeminiMode(m GeminiMode) GeminiOption    { return func(a *GeminiAgent) { a.mode = m } }
func WithGeminiCLIPath(p string) GeminiOption     { return func(a *GeminiAgent) { a.cliPath = p } }
func WithGeminiAutoApprove(v bool) GeminiOption   { return func(a *GeminiAgent) { a.autoApprove = v } }
func WithGeminiSessionStore(s *session.Store) GeminiOption {
	return func(a *GeminiAgent) { a.sessionStore = s }
}

// NewGeminiAgent 创建 Gemini 智能体
func NewGeminiAgent(id string, logger *slog.Logger, opts ...GeminiOption) *GeminiAgent {
	a := &GeminiAgent{
		BaseAgent: BaseAgent{
			AgentID:      id,
			ProviderType: ProviderGemini,
			ModelID:      "gemini-2.5-pro",
			Caps: []Capability{
				CapMultimodal, CapLargeContext, CapCodeGen, CapReasoning,
			},
			InboxCh:   make(chan *schema.Message, 50),
			StatusVal: StatusIdle,
		},
		cliPath:     "gemini",
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

func (a *GeminiAgent) Start(_ context.Context) error {
	a.StatusVal = StatusIdle
	a.logger.Info("gemini agent started",
		"id", a.AgentID,
		"model", a.ModelID,
		"mode", a.modeString(),
	)
	return nil
}

func (a *GeminiAgent) Stop(_ context.Context) error {
	a.StatusVal = StatusStopped
	a.procManager.StopAll()
	a.logger.Info("gemini agent stopped", "id", a.AgentID)
	return nil
}

func (a *GeminiAgent) Execute(ctx context.Context, t *task.Task) (*schema.Result, error) {
	a.StatusVal = StatusBusy
	defer func() { a.StatusVal = StatusIdle }()

	start := time.Now()
	switch a.mode {
	case GeminiModeAPI:
		return a.executeAPI(ctx, t, start)
	case GeminiModeCLI:
		return a.executeCLI(ctx, t, start)
	default:
		return nil, fmt.Errorf("unknown gemini mode: %d", a.mode)
	}
}

func (a *GeminiAgent) Stream(ctx context.Context, t *task.Task) (<-chan schema.StreamEvent, error) {
	if a.mode == GeminiModeCLI {
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

// executeAPI 通过 Google AI API 执行
func (a *GeminiAgent) executeAPI(ctx context.Context, t *task.Task, start time.Time) (*schema.Result, error) {
	url := fmt.Sprintf("https://generativelanguage.googleapis.com/v1beta/models/%s:generateContent?key=%s", a.ModelID, a.apiKey)

	reqBody := map[string]any{
		"contents": []map[string]any{
			{
				"parts": []map[string]string{
					{"text": t.Prompt},
				},
			},
		},
		"generationConfig": map[string]any{
			"maxOutputTokens": 8192,
		},
	}

	bodyBytes, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(bodyBytes))
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

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
		return nil, fmt.Errorf("gemini api error (status %d): %s", resp.StatusCode, string(respBody))
	}

	var gemResp geminiResponse
	if err := json.Unmarshal(respBody, &gemResp); err != nil {
		return nil, fmt.Errorf("parse response: %w", err)
	}

	content := ""
	if len(gemResp.Candidates) > 0 && len(gemResp.Candidates[0].Content.Parts) > 0 {
		content = gemResp.Candidates[0].Content.Parts[0].Text
	}

	tokensIn, tokensOut := 0, 0
	if gemResp.UsageMetadata != nil {
		tokensIn = gemResp.UsageMetadata.PromptTokenCount
		tokensOut = gemResp.UsageMetadata.CandidatesTokenCount
	}

	return &schema.Result{
		AgentID:   a.AgentID,
		Provider:  string(ProviderGemini),
		Content:   content,
		TokensIn:  tokensIn,
		TokensOut: tokensOut,
		Duration:  time.Since(start),
	}, nil
}

// executeCLI 通过 gemini CLI 子进程执行
func (a *GeminiAgent) executeCLI(ctx context.Context, t *task.Task, start time.Time) (*schema.Result, error) {
	args := a.buildCLIArgs(t)

	a.logger.Debug("executing gemini cli",
		"args", strings.Join(args, " "),
		"work_dir", t.WorkDir,
	)

	cmd := exec.CommandContext(ctx, a.cliPath, args...)
	if t.WorkDir != "" {
		cmd.Dir = t.WorkDir
	}

	output, err := cmd.Output()
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			return nil, fmt.Errorf("gemini cli (exit %d): %s", exitErr.ExitCode(), string(exitErr.Stderr))
		}
		return nil, fmt.Errorf("gemini cli: %w", err)
	}

	// 尝试 JSON 解析
	chunk, parseErr := process.ParseSingleJSON(output)
	if parseErr != nil {
		return &schema.Result{
			AgentID:  a.AgentID,
			Provider: string(ProviderGemini),
			Content:  string(output),
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
		Provider:  string(ProviderGemini),
		Content:   chunk.Content,
		TokensIn:  tokensIn,
		TokensOut: tokensOut,
		Duration:  time.Since(start),
	}, nil
}

// streamCLI Gemini CLI 流式输出
func (a *GeminiAgent) streamCLI(ctx context.Context, t *task.Task) (<-chan schema.StreamEvent, error) {
	args := a.buildCLIArgs(t)

	ch := make(chan schema.StreamEvent, 256)

	go func() {
		defer close(ch)

		cmd := exec.CommandContext(ctx, a.cliPath, args...)
		if t.WorkDir != "" {
			cmd.Dir = t.WorkDir
		}

		stdout, err := cmd.StdoutPipe()
		if err != nil {
			ch <- schema.StreamEvent{AgentID: a.AgentID, Type: "error", Content: err.Error()}
			return
		}

		if err := cmd.Start(); err != nil {
			ch <- schema.StreamEvent{AgentID: a.AgentID, Type: "error", Content: err.Error()}
			return
		}

		parser := process.NewStreamParser(stdout, process.FormatPlainText)
		for chunk := range parser.Parse(ctx) {
			ch <- schema.StreamEvent{AgentID: a.AgentID, Type: "text", Content: chunk.Content}
		}

		_ = cmd.Wait()
		ch <- schema.StreamEvent{AgentID: a.AgentID, Type: "done"}
	}()

	return ch, nil
}

// buildCLIArgs 构建 Gemini CLI 参数
func (a *GeminiAgent) buildCLIArgs(t *task.Task) []string {
	// gemini CLI 的参数格式：gemini -p "prompt" [options]
	args := []string{"-p", t.Prompt}

	if a.autoApprove {
		args = append(args, "-y")
	}

	return args
}

func (a *GeminiAgent) modeString() string {
	if a.mode == GeminiModeCLI {
		return "cli"
	}
	return "api"
}

type geminiResponse struct {
	Candidates []struct {
		Content struct {
			Parts []struct {
				Text string `json:"text"`
			} `json:"parts"`
		} `json:"content"`
	} `json:"candidates"`
	UsageMetadata *struct {
		PromptTokenCount     int `json:"promptTokenCount"`
		CandidatesTokenCount int `json:"candidatesTokenCount"`
		TotalTokenCount      int `json:"totalTokenCount"`
	} `json:"usageMetadata"`
}
