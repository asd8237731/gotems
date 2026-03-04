package cost

import (
	"fmt"
	"log/slog"
	"sync"
	"time"
)

// ModelPricing 模型定价（每 1K tokens）
type ModelPricing struct {
	CostPerKInput  float64
	CostPerKOutput float64
}

// 内置定价表（可通过 RegisterPricing 覆盖）
var defaultPricing = map[string]ModelPricing{
	// Claude
	"claude-sonnet-4-6":  {CostPerKInput: 0.003, CostPerKOutput: 0.015},
	"claude-haiku-4-5":   {CostPerKInput: 0.0008, CostPerKOutput: 0.004},
	"claude-opus-4-6":    {CostPerKInput: 0.015, CostPerKOutput: 0.075},
	// Gemini
	"gemini-2.5-pro":     {CostPerKInput: 0.00125, CostPerKOutput: 0.005},
	"gemini-2.5-flash":   {CostPerKInput: 0.00015, CostPerKOutput: 0.0006},
	// OpenAI
	"gpt-4o":             {CostPerKInput: 0.002, CostPerKOutput: 0.008},
	"gpt-4o-mini":        {CostPerKInput: 0.00015, CostPerKOutput: 0.0006},
	"o3":                 {CostPerKInput: 0.01, CostPerKOutput: 0.04},
	// Ollama（本地免费）
	"qwen3:32b":          {CostPerKInput: 0, CostPerKOutput: 0},
}

// Tracker 追踪各 Agent 和模型的 Token 消耗与费用
type Tracker struct {
	mu      sync.RWMutex
	records []Record
	daily   map[string]float64 // date -> cost
	pricing map[string]ModelPricing
	limits  Limits
	logger  *slog.Logger
}

// Record 记录一次 API 调用的消耗
type Record struct {
	AgentID    string    `json:"agent_id"`
	Provider   string    `json:"provider"`
	Model      string    `json:"model"`
	TokensIn   int       `json:"tokens_in"`
	TokensOut  int       `json:"tokens_out"`
	Cost       float64   `json:"cost"`
	Timestamp  time.Time `json:"timestamp"`
}

// Limits 费用限制
type Limits struct {
	DailyMax  float64
	PerTaskMax float64
}

// Summary 汇总统计
type Summary struct {
	TotalCost     float64            `json:"total_cost"`
	TotalTokensIn  int              `json:"total_tokens_in"`
	TotalTokensOut int              `json:"total_tokens_out"`
	ByProvider    map[string]float64 `json:"by_provider"`
	ByModel       map[string]float64 `json:"by_model"`
	RecordCount   int                `json:"record_count"`
}

// NewTracker 创建费用追踪器
func NewTracker(limits Limits, logger *slog.Logger) *Tracker {
	// 复制默认定价表
	pricing := make(map[string]ModelPricing, len(defaultPricing))
	for k, v := range defaultPricing {
		pricing[k] = v
	}
	return &Tracker{
		daily:   make(map[string]float64),
		pricing: pricing,
		limits:  limits,
		logger:  logger,
	}
}

// RegisterPricing 注册或覆盖模型定价
func (t *Tracker) RegisterPricing(model string, p ModelPricing) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.pricing[model] = p
}

// CalcCost 根据模型和 token 数自动计算费用
func (t *Tracker) CalcCost(model string, tokensIn, tokensOut int) float64 {
	t.mu.RLock()
	p, ok := t.pricing[model]
	t.mu.RUnlock()
	if !ok {
		return 0
	}
	return float64(tokensIn)/1000*p.CostPerKInput + float64(tokensOut)/1000*p.CostPerKOutput
}

// Track 记录一次消耗（Cost 为 0 时自动从定价表计算）
func (t *Tracker) Track(r Record) error {
	r.Timestamp = time.Now()

	t.mu.Lock()
	defer t.mu.Unlock()

	// 自动计算费用
	if r.Cost == 0 && r.Model != "" {
		if p, ok := t.pricing[r.Model]; ok {
			r.Cost = float64(r.TokensIn)/1000*p.CostPerKInput + float64(r.TokensOut)/1000*p.CostPerKOutput
		}
	}

	// 检查每日限额
	dateKey := r.Timestamp.Format("2006-01-02")
	todayCost := t.daily[dateKey] + r.Cost
	if t.limits.DailyMax > 0 && todayCost > t.limits.DailyMax {
		return fmt.Errorf("daily cost limit exceeded: %.2f / %.2f", todayCost, t.limits.DailyMax)
	}

	t.records = append(t.records, r)
	t.daily[dateKey] = todayCost

	t.logger.Info("cost tracked",
		"agent_id", r.AgentID,
		"provider", r.Provider,
		"model", r.Model,
		"tokens_in", r.TokensIn,
		"tokens_out", r.TokensOut,
		"cost", r.Cost,
		"daily_total", todayCost,
	)
	return nil
}

// CheckBudget 检查是否还有预算
func (t *Tracker) CheckBudget() error {
	t.mu.RLock()
	defer t.mu.RUnlock()
	dateKey := time.Now().Format("2006-01-02")
	if t.limits.DailyMax > 0 && t.daily[dateKey] >= t.limits.DailyMax {
		return fmt.Errorf("daily budget exhausted: %.2f", t.daily[dateKey])
	}
	return nil
}

// TodayCost 返回今日累计花费
func (t *Tracker) TodayCost() float64 {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.daily[time.Now().Format("2006-01-02")]
}

// Summarize 返回汇总统计
func (t *Tracker) Summarize() Summary {
	t.mu.RLock()
	defer t.mu.RUnlock()

	s := Summary{
		ByProvider: make(map[string]float64),
		ByModel:    make(map[string]float64),
		RecordCount: len(t.records),
	}
	for _, r := range t.records {
		s.TotalCost += r.Cost
		s.TotalTokensIn += r.TokensIn
		s.TotalTokensOut += r.TokensOut
		s.ByProvider[r.Provider] += r.Cost
		s.ByModel[r.Model] += r.Cost
	}
	return s
}
