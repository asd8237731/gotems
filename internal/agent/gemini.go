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

// GeminiAgent 是 Google Gemini 的适配器
type GeminiAgent struct {
	BaseAgent
	apiKey     string
	httpClient *http.Client
	logger     *slog.Logger
}

type GeminiOption func(*GeminiAgent)

func WithGeminiAPIKey(key string) GeminiOption { return func(a *GeminiAgent) { a.apiKey = key } }
func WithGeminiModel(m string) GeminiOption    { return func(a *GeminiAgent) { a.ModelID = m } }

// NewGeminiAgent 创建 Gemini 智能体
func NewGeminiAgent(id string, logger *slog.Logger, opts ...GeminiOption) *GeminiAgent {
	a := &GeminiAgent{
		BaseAgent: BaseAgent{
			AgentID:      id,
			ProviderType: ProviderGemini,
			ModelID:      "gemini-2.5-pro",
			Caps: []Capability{
				CapMultimodal, CapLargeContext, CapCodeGen,
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

func (a *GeminiAgent) Start(_ context.Context) error {
	a.StatusVal = StatusIdle
	a.logger.Info("gemini agent started", "id", a.AgentID, "model", a.ModelID)
	return nil
}

func (a *GeminiAgent) Stop(_ context.Context) error {
	a.StatusVal = StatusStopped
	a.logger.Info("gemini agent stopped", "id", a.AgentID)
	return nil
}

func (a *GeminiAgent) Execute(ctx context.Context, t *task.Task) (*schema.Result, error) {
	a.StatusVal = StatusBusy
	defer func() { a.StatusVal = StatusIdle }()

	start := time.Now()

	// Gemini API: POST https://generativelanguage.googleapis.com/v1beta/models/{model}:generateContent
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

func (a *GeminiAgent) Stream(ctx context.Context, t *task.Task) (<-chan schema.StreamEvent, error) {
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
