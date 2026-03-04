package orchestrator

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/lyymini/gotems/internal/agent"
	"github.com/lyymini/gotems/internal/cost"
	"github.com/lyymini/gotems/internal/ratelimit"
	"github.com/lyymini/gotems/internal/task"
	"github.com/lyymini/gotems/pkg/schema"
)

// Guard 统一的执行守卫，封装限流 + 熔断 + 成本追踪
type Guard struct {
	limiter     *ratelimit.Limiter
	breaker     *ratelimit.Breaker
	costTracker *cost.Tracker
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

// Execute 带守卫的 Agent 执行：限流 → 熔断检查 → 执行 → 成本记录 → 熔断反馈
func (g *Guard) Execute(ctx context.Context, a agent.Agent, t *task.Task) (*schema.Result, error) {
	provider := string(a.Provider())

	// 熔断检查
	if !g.breaker.Allow(provider) {
		return nil, fmt.Errorf("provider %s is circuit-broken, try later", provider)
	}

	// 限流等待
	if err := g.limiter.Wait(ctx, provider); err != nil {
		return nil, fmt.Errorf("rate limit: %w", err)
	}

	// 预算检查
	if err := g.costTracker.CheckBudget(); err != nil {
		return nil, fmt.Errorf("budget check: %w", err)
	}

	g.logger.Info("guard: executing task",
		"agent", a.ID(),
		"provider", provider,
		"task_id", t.ID,
	)

	// 执行
	result, err := a.Execute(ctx, t)
	if err != nil {
		g.breaker.RecordFailure(provider)
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

	// 记录成本
	_ = g.costTracker.Track(cost.Record{
		AgentID:   result.AgentID,
		Provider:  result.Provider,
		Model:     result.Model,
		TokensIn:  result.TokensIn,
		TokensOut: result.TokensOut,
		Cost:      result.Cost,
	})

	return result, nil
}
