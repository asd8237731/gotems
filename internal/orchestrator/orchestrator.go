package orchestrator

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/lyymini/gotems/internal/agent"
	"github.com/lyymini/gotems/internal/comm"
	"github.com/lyymini/gotems/internal/cost"
	"github.com/lyymini/gotems/internal/ratelimit"
	"github.com/lyymini/gotems/internal/splitter"
	"github.com/lyymini/gotems/internal/task"
	"github.com/lyymini/gotems/internal/workspace"
	"github.com/lyymini/gotems/pkg/schema"
)

// Orchestrator 是 GoTems 的核心编排引擎
type Orchestrator struct {
	agents      map[string]agent.Agent
	taskPool    *task.Pool
	fileLock    *task.FileLock
	mailbox     *comm.Mailbox
	router      *Router
	dag         *DAGExecutor
	aggregator  *Aggregator
	guard       *Guard
	costTracker *cost.Tracker
	limiter     *ratelimit.Limiter
	breaker     *ratelimit.Breaker
	splitter    *splitter.Splitter
	workspace   *workspace.Workspace // 可选，需 git 仓库
	logger      *slog.Logger
}

// OrchestratorConfig 编排器配置
type OrchestratorConfig struct {
	Strategy         Strategy
	CostLimits       cost.Limits
	JudgeAgent       agent.Agent   // 裁判 Agent（可选，竞赛模式使用）
	SplitterAgent    agent.Agent   // 拆分 Agent（可选，自动拆分大任务）
	BreakerThreshold int           // 熔断阈值，默认 5
	BreakerCooldown  time.Duration // 熔断冷却，默认 30s
	WorkDir          string        // 工作目录（可选，用于 git worktree 隔离）
}

// New 创建编排器
func New(cfg OrchestratorConfig, logger *slog.Logger) *Orchestrator {
	agents := make(map[string]agent.Agent)
	router := NewRouter(cfg.Strategy, agents)

	threshold := cfg.BreakerThreshold
	if threshold == 0 {
		threshold = 5
	}
	cooldown := cfg.BreakerCooldown
	if cooldown == 0 {
		cooldown = 30 * time.Second
	}

	limiter := ratelimit.NewLimiter(logger)
	breaker := ratelimit.NewBreaker(threshold, cooldown, logger)
	costTracker := cost.NewTracker(cfg.CostLimits, logger)
	guard := NewGuard(limiter, breaker, costTracker, logger)
	mailbox := comm.NewMailbox(logger)
	dag := NewDAGExecutor(router, guard, mailbox, logger)

	o := &Orchestrator{
		agents:      agents,
		taskPool:    task.NewPool(),
		fileLock:    task.NewFileLock(),
		mailbox:     mailbox,
		router:      router,
		dag:         dag,
		aggregator:  NewAggregator(cfg.JudgeAgent, logger),
		guard:       guard,
		costTracker: costTracker,
		limiter:     limiter,
		breaker:     breaker,
		splitter:    splitter.NewSplitter(cfg.SplitterAgent, logger),
		logger:      logger,
	}

	// 如果指定了工作目录，初始化 Workspace
	if cfg.WorkDir != "" {
		o.workspace = workspace.NewWorkspace(cfg.WorkDir, logger)
	}

	return o
}

// ConfigureRateLimit 为指定 Provider 设置限流参数
func (o *Orchestrator) ConfigureRateLimit(cfg ratelimit.LimiterConfig) {
	o.limiter.Configure(cfg)
}

// RegisterAgent 注册一个 Agent 到编排器
func (o *Orchestrator) RegisterAgent(a agent.Agent) {
	o.agents[a.ID()] = a
	o.router.agents[a.ID()] = a
	o.mailbox.Register(a.ID())
	o.logger.Info("agent registered",
		"id", a.ID(),
		"provider", a.Provider(),
		"model", a.Model(),
		"capabilities", a.Capabilities(),
	)
}

// Start 启动所有已注册的 Agent
func (o *Orchestrator) Start(ctx context.Context) error {
	for _, a := range o.agents {
		if err := a.Start(ctx); err != nil {
			return fmt.Errorf("start agent %s: %w", a.ID(), err)
		}
	}
	o.logger.Info("orchestrator started", "agents", len(o.agents))
	return nil
}

// Stop 停止所有 Agent，清理 Workspace
func (o *Orchestrator) Stop(ctx context.Context) error {
	for _, a := range o.agents {
		if err := a.Stop(ctx); err != nil {
			o.logger.Warn("failed to stop agent", "id", a.ID(), "error", err)
		}
	}
	o.mailbox.Close()
	if o.workspace != nil {
		o.workspace.Cleanup()
	}
	o.logger.Info("orchestrator stopped")
	return nil
}

// Run 执行单个任务（自动路由到最佳 Agent）
func (o *Orchestrator) Run(ctx context.Context, prompt string) (*schema.FinalResult, error) {
	t := &task.Task{
		ID:     fmt.Sprintf("task-%d", time.Now().UnixNano()),
		Prompt: prompt,
	}

	// 根据策略决定执行方式
	if o.router.strategy == StrategyConsensus {
		return o.runConsensus(ctx, t)
	}

	return o.runSingle(ctx, t)
}

// RunSplit 自动拆分任务并以 DAG 方式执行
func (o *Orchestrator) RunSplit(ctx context.Context, prompt string) (*schema.FinalResult, error) {
	tasks, err := o.splitter.Split(ctx, prompt)
	if err != nil {
		return nil, fmt.Errorf("split task: %w", err)
	}
	o.logger.Info("task split result", "sub_tasks", len(tasks))
	return o.RunWithTasks(ctx, tasks)
}

// RunWithTasks 执行多个有依赖关系的任务（DAG 模式）
func (o *Orchestrator) RunWithTasks(ctx context.Context, tasks []*task.Task) (*schema.FinalResult, error) {
	if err := o.costTracker.CheckBudget(); err != nil {
		return nil, fmt.Errorf("budget check: %w", err)
	}

	// 如果有 Workspace，为每个 Agent 创建 worktree
	if o.workspace != nil {
		o.setupWorktrees(tasks)
		defer o.mergeAndCleanWorktrees(tasks)
	}

	// 构建 DAG
	dag, err := o.dag.Build(tasks)
	if err != nil {
		return nil, fmt.Errorf("build DAG: %w", err)
	}

	o.logger.Info("DAG built", "layers", len(dag.Layers()))
	for i, layer := range dag.Layers() {
		o.logger.Info("DAG layer", "index", i, "tasks", len(layer))
	}

	// 执行 DAG（Guard 已在 DAGExecutor 内部统一处理限流/熔断/成本）
	results, err := o.dag.Execute(ctx, dag)
	if err != nil {
		return nil, fmt.Errorf("execute DAG: %w", err)
	}

	return o.aggregator.MergeResults(results), nil
}

// runSingle 单 Agent 执行（通过 Guard 统一处理限流/熔断/成本）
func (o *Orchestrator) runSingle(ctx context.Context, t *task.Task) (*schema.FinalResult, error) {
	a := o.router.Route(t.Tags)
	if a == nil {
		return nil, fmt.Errorf("no available agent for task")
	}

	o.logger.Info("routing task to agent", "agent", a.ID(), "provider", a.Provider())

	result, err := o.guard.Execute(ctx, a, t)
	if err != nil {
		return nil, err
	}

	return &schema.FinalResult{
		Content:        result.Content,
		Results:        []*schema.Result{result},
		TotalCost:      result.Cost,
		TotalTokensIn:  result.TokensIn,
		TotalTokensOut: result.TokensOut,
		Strategy:       "single",
	}, nil
}

// runConsensus 竞赛模式：同一任务发给所有 Agent（通过 Guard 统一处理）
func (o *Orchestrator) runConsensus(ctx context.Context, t *task.Task) (*schema.FinalResult, error) {
	agents := o.router.RouteAll()
	if len(agents) == 0 {
		return nil, fmt.Errorf("no available agents")
	}

	o.logger.Info("consensus mode: dispatching to all agents", "count", len(agents))

	results, err := GuardedParallelExecute(ctx, o.guard, agents, t)
	if err != nil {
		o.logger.Warn("some agents failed in consensus mode", "error", err)
	}
	if len(results) == 0 {
		return nil, fmt.Errorf("all agents failed")
	}

	return o.aggregator.BestOf(ctx, t.Prompt, results)
}

// --- Workspace 辅助方法 ---

// setupWorktrees 为 DAG 中涉及的 Agent 创建 worktree
func (o *Orchestrator) setupWorktrees(tasks []*task.Task) {
	seen := make(map[string]bool)
	for _, t := range tasks {
		if t.AssignedTo != "" && !seen[t.AssignedTo] {
			seen[t.AssignedTo] = true
			wt, err := o.workspace.CreateWorktree(t.AssignedTo)
			if err != nil {
				o.logger.Warn("failed to create worktree", "agent", t.AssignedTo, "error", err)
				continue
			}
			// 将 worktree 路径设置到 Task
			t.WorkDir = wt.Path
		}
	}
}

// mergeAndCleanWorktrees 合并所有 worktree 并清理
func (o *Orchestrator) mergeAndCleanWorktrees(tasks []*task.Task) {
	errs := o.workspace.MergeAll()
	for _, err := range errs {
		o.logger.Warn("worktree merge error", "error", err)
	}
	o.workspace.Cleanup()
}

// --- 公共访问器 ---

// CostSummary 返回成本汇总
func (o *Orchestrator) CostSummary() cost.Summary {
	return o.costTracker.Summarize()
}

// CostTracker 返回成本追踪器（供外部 MCP/Server 使用）
func (o *Orchestrator) CostTracker() *cost.Tracker {
	return o.costTracker
}

// TaskPool 返回任务池
func (o *Orchestrator) TaskPool() *task.Pool {
	return o.taskPool
}

// Breaker 返回熔断器
func (o *Orchestrator) Breaker() *ratelimit.Breaker {
	return o.breaker
}

// Mailbox 返回邮箱系统
func (o *Orchestrator) Mailbox() *comm.Mailbox {
	return o.mailbox
}

// AgentsMap 返回 Agent 映射
func (o *Orchestrator) AgentsMap() map[string]agent.Agent {
	return o.agents
}

// Agents 返回所有已注册 Agent 的状态快照
func (o *Orchestrator) Agents() []AgentInfo {
	infos := make([]AgentInfo, 0, len(o.agents))
	for _, a := range o.agents {
		infos = append(infos, AgentInfo{
			ID:           a.ID(),
			Provider:     string(a.Provider()),
			Model:        a.Model(),
			Status:       a.Status().String(),
			Capabilities: capStrings(a.Capabilities()),
		})
	}
	return infos
}

// AgentInfo Agent 状态信息
type AgentInfo struct {
	ID           string   `json:"id"`
	Provider     string   `json:"provider"`
	Model        string   `json:"model"`
	Status       string   `json:"status"`
	Capabilities []string `json:"capabilities"`
}

func capStrings(caps []agent.Capability) []string {
	s := make([]string, len(caps))
	for i, c := range caps {
		s[i] = string(c)
	}
	return s
}
