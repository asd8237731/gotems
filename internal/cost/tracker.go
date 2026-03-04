package cost

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
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

// 模型降级映射：昂贵模型 -> 便宜替代模型
var defaultDowngradeMap = map[string]string{
	// Claude 降级链：Opus → Sonnet → Haiku
	"claude-opus-4-6":   "claude-sonnet-4-6",
	"claude-sonnet-4-6": "claude-haiku-4-5",
	// Gemini 降级链：Pro → Flash
	"gemini-2.5-pro":    "gemini-2.5-flash",
	// OpenAI 降级链：GPT-4o → GPT-4o-mini
	"gpt-4o":            "gpt-4o-mini",
	"o3":                "gpt-4o",
}

// BudgetAlert 预算告警回调
type BudgetAlert func(level string, current, limit float64, message string)

// Tracker 追踪各 Agent 和模型的 Token 消耗与费用
type Tracker struct {
	mu              sync.RWMutex
	records         []Record
	daily           map[string]float64 // date -> cost
	pricing         map[string]ModelPricing
	downgradeMap    map[string]string // 模型降级映射
	limits          Limits
	maxRecords      int // records 最大容量，超过后清理旧记录
	alertThresholds []float64 // 告警阈值（如 0.7, 0.9）
	alertCallback   BudgetAlert
	logger          *slog.Logger
}

// Record 记录一次 API 调用的消耗
type Record struct {
	AgentID   string    `json:"agent_id"`
	Provider  string    `json:"provider"`
	Model     string    `json:"model"`
	TokensIn  int       `json:"tokens_in"`
	TokensOut int       `json:"tokens_out"`
	Cost      float64   `json:"cost"`
	Timestamp time.Time `json:"timestamp"`
}

// Limits 费用限制
type Limits struct {
	DailyMax   float64
	PerTaskMax float64
}

// Summary 汇总统计
type Summary struct {
	TotalCost      float64            `json:"total_cost"`
	TotalTokensIn  int                `json:"total_tokens_in"`
	TotalTokensOut int                `json:"total_tokens_out"`
	ByProvider     map[string]float64 `json:"by_provider"`
	ByModel        map[string]float64 `json:"by_model"`
	RecordCount    int                `json:"record_count"`
}

// NewTracker 创建费用追踪器
func NewTracker(limits Limits, logger *slog.Logger) *Tracker {
	// 复制默认定价表
	pricing := make(map[string]ModelPricing, len(defaultPricing))
	for k, v := range defaultPricing {
		pricing[k] = v
	}
	// 复制默认降级映射
	downgradeMap := make(map[string]string, len(defaultDowngradeMap))
	for k, v := range defaultDowngradeMap {
		downgradeMap[k] = v
	}
	return &Tracker{
		daily:           make(map[string]float64),
		pricing:         pricing,
		downgradeMap:    downgradeMap,
		limits:          limits,
		maxRecords:      10000, // 默认最多保留 10000 条记录
		alertThresholds: []float64{0.7, 0.9}, // 默认 70% 和 90% 告警
		logger:          logger,
	}
}

// SetMaxRecords 设置 records 最大容量
func (t *Tracker) SetMaxRecords(max int) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.maxRecords = max
}

// SetAlertThresholds 设置预算告警阈值（如 []float64{0.7, 0.9}）
func (t *Tracker) SetAlertThresholds(thresholds []float64) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.alertThresholds = thresholds
}

// SetAlertCallback 设置预算告警回调
func (t *Tracker) SetAlertCallback(callback BudgetAlert) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.alertCallback = callback
}

// RegisterPricing 注册或覆盖模型定价
func (t *Tracker) RegisterPricing(model string, p ModelPricing) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.pricing[model] = p
}

// RegisterDowngrade 注册模型降级映射
func (t *Tracker) RegisterDowngrade(from, to string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.downgradeMap[from] = to
}

// SuggestDowngrade 根据当前预算使用情况建议降级模型
// 返回建议的模型，如果不需要降级则返回原模型
func (t *Tracker) SuggestDowngrade(currentModel string) string {
	t.mu.RLock()
	defer t.mu.RUnlock()

	if t.limits.DailyMax == 0 {
		return currentModel // 无预算限制
	}

	dateKey := time.Now().Format("2006-01-02")
	usage := t.daily[dateKey] / t.limits.DailyMax

	// 如果使用率 < 70%，不降级
	if usage < 0.7 {
		return currentModel
	}

	// 如果使用率 >= 90%，强制降级
	// 如果使用率 >= 70%，建议降级
	if usage >= 0.7 {
		if downgrade, ok := t.downgradeMap[currentModel]; ok {
			t.logger.Info("budget-based model downgrade suggested",
				"from", currentModel,
				"to", downgrade,
				"usage", fmt.Sprintf("%.1f%%", usage*100),
			)
			return downgrade
		}
	}

	return currentModel
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

	// 检查单任务限额
	if t.limits.PerTaskMax > 0 && r.Cost > t.limits.PerTaskMax {
		return fmt.Errorf("per-task cost limit exceeded: %.2f / %.2f", r.Cost, t.limits.PerTaskMax)
	}

	t.records = append(t.records, r)
	t.daily[dateKey] = todayCost

	// 检查告警阈值
	if t.limits.DailyMax > 0 && t.alertCallback != nil {
		usage := todayCost / t.limits.DailyMax
		for _, threshold := range t.alertThresholds {
			// 检查是否刚刚跨过阈值
			prevUsage := (todayCost - r.Cost) / t.limits.DailyMax
			if prevUsage < threshold && usage >= threshold {
				level := fmt.Sprintf("%.0f%%", threshold*100)
				msg := fmt.Sprintf("Daily budget usage reached %s (%.2f / %.2f)", level, todayCost, t.limits.DailyMax)
				t.alertCallback(level, todayCost, t.limits.DailyMax, msg)
				t.logger.Warn("budget alert triggered", "level", level, "usage", todayCost, "limit", t.limits.DailyMax)
			}
		}
	}

	// 清理旧记录（保留最近的 maxRecords 条）
	if len(t.records) > t.maxRecords {
		// 保留后 80% 的记录
		keep := int(float64(t.maxRecords) * 0.8)
		t.records = t.records[len(t.records)-keep:]
		t.logger.Info("cost records trimmed", "kept", keep, "max", t.maxRecords)
	}

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

// BudgetUsage 返回今日预算使用率（0.0 - 1.0）
func (t *Tracker) BudgetUsage() float64 {
	t.mu.RLock()
	defer t.mu.RUnlock()
	if t.limits.DailyMax == 0 {
		return 0
	}
	dateKey := time.Now().Format("2006-01-02")
	return t.daily[dateKey] / t.limits.DailyMax
}

// FetchPricing 从远程 API 拉取最新定价并更新本地定价表
// endpoint 应返回 JSON: {"models": {"model-id": {"input": 0.003, "output": 0.015}, ...}}
func (t *Tracker) FetchPricing(ctx context.Context, endpoint string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return fmt.Errorf("create pricing request: %w", err)
	}

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("fetch pricing: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("pricing api returned status %d", resp.StatusCode)
	}

	var payload struct {
		Models map[string]struct {
			Input  float64 `json:"input"`
			Output float64 `json:"output"`
		} `json:"models"`
	}

	// 限制 Body 大小为 1MB
	limitedBody := io.LimitReader(resp.Body, 1024*1024)
	body, err := io.ReadAll(limitedBody)
	if err != nil {
		return fmt.Errorf("read pricing response: %w", err)
	}

	if err := json.Unmarshal(body, &payload); err != nil {
		return fmt.Errorf("parse pricing: %w", err)
	}

	t.mu.Lock()
	defer t.mu.Unlock()

	updated := 0
	for model, p := range payload.Models {
		t.pricing[model] = ModelPricing{
			CostPerKInput:  p.Input,
			CostPerKOutput: p.Output,
		}
		updated++
	}

	t.logger.Info("pricing updated from remote", "endpoint", endpoint, "models_updated", updated)
	return nil
}

// Pricing 返回当前定价表的快照
func (t *Tracker) Pricing() map[string]ModelPricing {
	t.mu.RLock()
	defer t.mu.RUnlock()
	result := make(map[string]ModelPricing, len(t.pricing))
	for k, v := range t.pricing {
		result[k] = v
	}
	return result
}

// Summarize 返回汇总统计
func (t *Tracker) Summarize() Summary {
	t.mu.RLock()
	defer t.mu.RUnlock()

	s := Summary{
		ByProvider:  make(map[string]float64),
		ByModel:     make(map[string]float64),
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
