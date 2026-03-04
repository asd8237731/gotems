package orchestrator

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/lyymini/gotems/internal/agent"
	"github.com/lyymini/gotems/internal/cost"
	"github.com/lyymini/gotems/internal/observability"
	"github.com/lyymini/gotems/internal/ratelimit"
	"github.com/lyymini/gotems/internal/task"
	"github.com/lyymini/gotems/pkg/schema"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
)

// Guard 统一的执行守卫，封装限流 + 熔断 + 成本追踪 + 可观测性
type Guard struct {
	mu          sync.RWMutex // 保护 metrics 和 tracer
	limiter     *ratelimit.Limiter
	breaker     *ratelimit.Breaker
	costTracker *cost.Tracker
	metrics     *observability.Metrics // 可选
	tracer      *observability.Tracer  // 可选
	logger      *slog.Logger
}

// NewGuard 创建执行守卫
func NewGuard(limiter *ratelimit.Limiter, breaker *ratelimit.Breaker, costTracker *cost.Tracker, logger *slog.Logger) *Guard {
	return &Guard{
		limiter:     limiter,
		breaker:     breaker,
		costTracker: costTracker,
		logger:      logger,
	}
}

// SetMetrics 注入指标收集器（线程安全）
func (g *Guard) SetMetrics(m *observability.Metrics) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.metrics = m
}

// SetTracer 注入链路追踪器（线程安全）
func (g *Guard) SetTracer(t *observability.Tracer) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.tracer = t
}

// Execute 带守卫的 Agent 执行：限流 → 熔断检查 → 执行 → 成本记录 → 指标 → 追踪
func (g *Guard) Execute(ctx context.Context, a agent.Agent, t *task.Task) (*schema.Result, error) {
	provider := string(a.Provider())
	start := time.Now()

	// 链路追踪
	g.mu.RLock()
	tracer := g.tracer
	g.mu.RUnlock()
	if tracer != nil {
		var span trace.Span
		ctx, span = tracer.TaskSpan(ctx, t.ID, a.ID(), provider)
		defer span.End()
	}

	// 熔断检查
	if !g.breaker.Allow(provider) {
		g.recordMetrics(provider, false, 0, 0, time.Since(start))
		return nil, fmt.Errorf("provider %s is circuit-broken, try later", provider)
	}

	// 限流等待
	if err := g.limiter.Wait(ctx, provider); err != nil {
		g.recordMetrics(provider, false, 0, 0, time.Since(start))
		return nil, fmt.Errorf("rate limit: %w", err)
	}

	// 预算检查
	if err := g.costTracker.CheckBudget(); err != nil {
		g.recordMetrics(provider, false, 0, 0, time.Since(start))
		return nil, fmt.Errorf("budget check: %w", err)
	}

	g.logger.Info("guard: executing task",
		"agent", a.ID(),
		"provider", provider,
		"task_id", t.ID,
	)

	// 执行
	result, err := a.Execute(ctx, t)
	latency := time.Since(start)

	if err != nil {
		g.breaker.RecordFailure(provider)
		g.recordMetrics(provider, false, 0, 0, latency)
		if tracer != nil {
			span := trace.SpanFromContext(ctx)
			span.RecordError(err)
			span.SetStatus(codes.Error, err.Error())
		}
		return nil, fmt.Errorf("execute task: %w", err)
	}

	g.breaker.RecordSuccess(provider)

	// 自动填充 Model 字段
	if result.Model == "" {
		result.Model = a.Model()
	}

	// 自动计算成本（如果 Agent 没有自行填充）
	if result.Cost == 0 && result.Model != "" {
		result.Cost = g.costTracker.CalcCost(result.Model, result.TokensIn, result.TokensOut)
	}

	// 记录成本（不再静默丢弃错误）
	if err := g.costTracker.Track(cost.Record{
		AgentID:   result.AgentID,
		Provider:  result.Provider,
		Model:     result.Model,
		TokensIn:  result.TokensIn,
		TokensOut: result.TokensOut,
		Cost:      result.Cost,
	}); err != nil {
		g.logger.Warn("failed to track cost", "error", err)
	}

	// 记录指标
	g.recordMetrics(provider, true, result.TokensIn, result.TokensOut, latency)

	// 追踪 span 属性
	if tracer != nil {
		span := trace.SpanFromContext(ctx)
		span.SetAttributes(
			attribute.Int("tokens.in", result.TokensIn),
			attribute.Int("tokens.out", result.TokensOut),
			attribute.Float64("cost", result.Cost),
			attribute.String("model", result.Model),
		)
	}

	return result, nil
}

func (g *Guard) recordMetrics(provider string, success bool, tokensIn, tokensOut int, latency time.Duration) {
	g.mu.RLock()
	metrics := g.metrics
	g.mu.RUnlock()
	if metrics != nil {
		metrics.RecordTask(provider, success, tokensIn, tokensOut, latency)
	}
}
