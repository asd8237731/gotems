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

// OpenAIAgent 是 OpenAI 的适配器
type OpenAIAgent struct {
	BaseAgent
	apiKey     string
	httpClient *http.Client
	logger     *slog.Logger
}

type OpenAIOption func(*OpenAIAgent)

func WithOpenAIAPIKey(key string) OpenAIOption { return func(a *OpenAIAgent) { a.apiKey = key } }
func WithOpenAIModel(m string) OpenAIOption    { return func(a *OpenAIAgent) { a.ModelID = m } }

// NewOpenAIAgent 创建 OpenAI 智能体
func NewOpenAIAgent(id string, logger *slog.Logger, opts ...OpenAIOption) *OpenAIAgent {
	a := &OpenAIAgent{
		BaseAgent: BaseAgent{
			AgentID:      id,
			ProviderType: ProviderOpenAI,
			ModelID:      "gpt-4.1",
			Caps: []Capability{
				CapCodeGen, CapTestGen, CapQuickTask,
			},
			InboxCh:   make(chan *schema.Message, 50),
			StatusVal: StatusIdle,
		},
		httpClient: &http.Client{Timeout: 5 * time.Minute},
		logger:     logger,
	}
	for _, opt := range opts {
		opt(a)
	}
	return a
}

func (a *OpenAIAgent) Start(_ context.Context) error {
	a.StatusVal = StatusIdle
	a.logger.Info("openai agent started", "id", a.AgentID, "model", a.ModelID)
	return nil
}

func (a *OpenAIAgent) Stop(_ context.Context) error {
	a.StatusVal = StatusStopped
	a.logger.Info("openai agent stopped", "id", a.AgentID)
	return nil
}

func (a *OpenAIAgent) Execute(ctx context.Context, t *task.Task) (*schema.Result, error) {
	a.StatusVal = StatusBusy
	defer func() { a.StatusVal = StatusIdle }()

	start := time.Now()

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

func (a *OpenAIAgent) Stream(ctx context.Context, t *task.Task) (<-chan schema.StreamEvent, error) {
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
