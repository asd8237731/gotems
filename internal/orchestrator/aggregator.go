package orchestrator

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	"github.com/lyymini/gotems/internal/agent"
	"github.com/lyymini/gotems/internal/task"
	"github.com/lyymini/gotems/pkg/schema"
	"golang.org/x/sync/errgroup"
)

// Aggregator 聚合多个 Agent 的执行结果
type Aggregator struct {
	judge  agent.Agent // 用于裁判的 Agent（可选）
	logger *slog.Logger
}

// NewAggregator 创建聚合器
func NewAggregator(judge agent.Agent, logger *slog.Logger) *Aggregator {
	return &Aggregator{
		judge:  judge,
		logger: logger,
	}
}

// MergeResults 合并多个结果（流水线/分治模式）
func (a *Aggregator) MergeResults(results map[string]*schema.Result) *schema.FinalResult {
	final := &schema.FinalResult{
		Results:  make([]*schema.Result, 0, len(results)),
		Strategy: "merge",
	}

	var parts []string
	for _, r := range results {
		final.Results = append(final.Results, r)
		final.TotalCost += r.Cost
		final.TotalTokensIn += r.TokensIn
		final.TotalTokensOut += r.TokensOut
		if r.Content != "" {
			parts = append(parts, r.Content)
		}
	}

	final.Content = strings.Join(parts, "\n\n---\n\n")
	return final
}

// BestOf 从多个结果中选择最优（竞赛模式）
func (a *Aggregator) BestOf(ctx context.Context, prompt string, results []*schema.Result) (*schema.FinalResult, error) {
	final := &schema.FinalResult{
		Results:  results,
		Strategy: "consensus",
	}

	for _, r := range results {
		final.TotalCost += r.Cost
		final.TotalTokensIn += r.TokensIn
		final.TotalTokensOut += r.TokensOut
	}

	if len(results) == 1 {
		final.Content = results[0].Content
		return final, nil
	}

	if a.judge != nil {
		return a.judgeResults(ctx, prompt, results, final)
	}

	// 没有裁判，选内容最长的（启发式）
	best := results[0]
	for _, r := range results[1:] {
		if len(r.Content) > len(best.Content) {
			best = r
		}
	}
	final.Content = best.Content
	return final, nil
}

// judgeResults 使用裁判 Agent 评判结果
func (a *Aggregator) judgeResults(ctx context.Context, prompt string, results []*schema.Result, final *schema.FinalResult) (*schema.FinalResult, error) {
	var sb strings.Builder
	sb.WriteString("以下是多个 AI 对同一个任务的回答，请评判哪个最好并给出最终答案。\n\n")
	sb.WriteString(fmt.Sprintf("原始任务: %s\n\n", prompt))

	for i, r := range results {
		sb.WriteString(fmt.Sprintf("=== 方案 %d (来自 %s/%s) ===\n%s\n\n", i+1, r.Provider, r.AgentID, r.Content))
	}
	sb.WriteString("请综合以上方案，给出最优的最终答案：")

	judgeT := &task.Task{
		ID:     "judge",
		Prompt: sb.String(),
	}
	result, err := a.judge.Execute(ctx, judgeT)
	if err != nil {
		a.logger.Warn("judge failed, falling back to first result", "error", err)
		final.Content = results[0].Content
		return final, nil
	}

	final.Content = result.Content
	final.TotalCost += result.Cost
	final.TotalTokensIn += result.TokensIn
	final.TotalTokensOut += result.TokensOut
	return final, nil
}

// ParallelExecute 并行执行同一任务到多个 Agent（竞赛模式，无守卫）
func ParallelExecute(ctx context.Context, agents []agent.Agent, t *task.Task) ([]*schema.Result, error) {
	g, gCtx := errgroup.WithContext(ctx)
	resultCh := make(chan *schema.Result, len(agents))

	for _, a := range agents {
		g.Go(func() error {
			result, err := a.Execute(gCtx, t)
			if err != nil {
				return err
			}
			resultCh <- result
			return nil
		})
	}

	err := g.Wait()
	close(resultCh)

	var results []*schema.Result
	for r := range resultCh {
		results = append(results, r)
	}
	return results, err
}

// GuardedParallelExecute 带守卫的并行执行（竞赛模式，统一限流/熔断/成本追踪）
func GuardedParallelExecute(ctx context.Context, guard *Guard, agents []agent.Agent, t *task.Task) ([]*schema.Result, error) {
	g, gCtx := errgroup.WithContext(ctx)
	resultCh := make(chan *schema.Result, len(agents))

	for _, a := range agents {
		g.Go(func() error {
			result, err := guard.Execute(gCtx, a, t)
			if err != nil {
				return err
			}
			resultCh <- result
			return nil
		})
	}

	err := g.Wait()
	close(resultCh)

	var results []*schema.Result
	for r := range resultCh {
		results = append(results, r)
	}
	return results, err
}
