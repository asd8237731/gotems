package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"time"

	"github.com/lyymini/gotems/internal/task"
	"github.com/lyymini/gotems/pkg/schema"
)

// OllamaAgent 是本地 Ollama 模型的适配器
type OllamaAgent struct {
	BaseAgent
	baseURL    string
	httpClient *http.Client
	logger     *slog.Logger
}

type OllamaOption func(*OllamaAgent)

func WithOllamaBaseURL(url string) OllamaOption { return func(a *OllamaAgent) { a.baseURL = url } }
func WithOllamaModel(m string) OllamaOption     { return func(a *OllamaAgent) { a.ModelID = m } }

// NewOllamaAgent 创建 Ollama 本地模型智能体
func NewOllamaAgent(id string, logger *slog.Logger, opts ...OllamaOption) *OllamaAgent {
	a := &OllamaAgent{
		BaseAgent: BaseAgent{
			AgentID:      id,
			ProviderType: ProviderOllama,
			ModelID:      "qwen3:32b",
			Caps: []Capability{
				CapQuickTask, CapCodeGen,
			},
			InboxCh:   make(chan *schema.Message, 50),
			StatusVal: StatusIdle,
		},
		baseURL:    "http://localhost:11434",
		httpClient: &http.Client{Timeout: 10 * time.Minute},
		logger:     logger,
	}
	for _, opt := range opts {
		opt(a)
	}
	return a
}

func (a *OllamaAgent) Start(_ context.Context) error {
	a.StatusVal = StatusIdle
	a.logger.Info("ollama agent started", "id", a.AgentID, "model", a.ModelID, "base_url", a.baseURL)
	return nil
}

func (a *OllamaAgent) Stop(_ context.Context) error {
	a.StatusVal = StatusStopped
	a.logger.Info("ollama agent stopped", "id", a.AgentID)
	return nil
}

func (a *OllamaAgent) Execute(ctx context.Context, t *task.Task) (*schema.Result, error) {
	a.StatusVal = StatusBusy
	defer func() { a.StatusVal = StatusIdle }()

	start := time.Now()

	reqBody := map[string]any{
		"model":  a.ModelID,
		"prompt": t.Prompt,
		"stream": false,
	}

	bodyBytes, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	url := a.baseURL + "/api/generate"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(bodyBytes))
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := a.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("ollama api call: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("ollama api error (status %d): %s", resp.StatusCode, string(respBody))
	}

	var ollamaResp ollamaResponse
	if err := json.Unmarshal(respBody, &ollamaResp); err != nil {
		return nil, fmt.Errorf("parse response: %w", err)
	}

	return &schema.Result{
		AgentID:   a.AgentID,
		Provider:  string(ProviderOllama),
		Content:   ollamaResp.Response,
		TokensIn:  ollamaResp.PromptEvalCount,
		TokensOut: ollamaResp.EvalCount,
		Duration:  time.Since(start),
		Cost:      0, // 本地模型免费
	}, nil
}

func (a *OllamaAgent) Stream(ctx context.Context, t *task.Task) (<-chan schema.StreamEvent, error) {
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

type ollamaResponse struct {
	Response        string `json:"response"`
	PromptEvalCount int    `json:"prompt_eval_count"`
	EvalCount       int    `json:"eval_count"`
}
