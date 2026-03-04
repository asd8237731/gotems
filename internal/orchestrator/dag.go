package orchestrator

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	"github.com/lyymini/gotems/internal/agent"
	"github.com/lyymini/gotems/internal/comm"
	"github.com/lyymini/gotems/internal/task"
	"github.com/lyymini/gotems/pkg/schema"
	"golang.org/x/sync/errgroup"
)

// DAG 表示一个有向无环图的任务执行计划
type DAG struct {
	nodes map[string]*DAGNode
	order [][]string // 拓扑排序后的分层 ID 列表（同层可并行）
}

// DAGNode 是 DAG 中的一个节点
type DAGNode struct {
	Task      *task.Task
	DependsOn []string
	Agent     agent.Agent // 路由分配的 Agent
}

// DAGExecutor 执行 DAG
type DAGExecutor struct {
	router  *Router
	guard   *Guard
	mailbox *comm.Mailbox
	logger  *slog.Logger
}

// NewDAGExecutor 创建 DAG 执行器
func NewDAGExecutor(router *Router, guard *Guard, mailbox *comm.Mailbox, logger *slog.Logger) *DAGExecutor {
	return &DAGExecutor{
		router:  router,
		guard:   guard,
		mailbox: mailbox,
		logger:  logger,
	}
}

// Build 从任务列表构建 DAG 并做拓扑排序
func (e *DAGExecutor) Build(tasks []*task.Task) (*DAG, error) {
	dag := &DAG{
		nodes: make(map[string]*DAGNode),
	}

	// 构建节点
	for _, t := range tasks {
		a := e.router.Route(t.Tags)
		dag.nodes[t.ID] = &DAGNode{
			Task:      t,
			DependsOn: t.DependsOn,
			Agent:     a,
		}
	}

	// 拓扑排序（Kahn 算法）
	inDegree := make(map[string]int)
	for id := range dag.nodes {
		inDegree[id] = len(dag.nodes[id].DependsOn)
	}

	var layers [][]string
	resolved := make(map[string]bool)

	for len(resolved) < len(dag.nodes) {
		var layer []string
		for id, deg := range inDegree {
			if deg == 0 && !resolved[id] {
				layer = append(layer, id)
			}
		}
		if len(layer) == 0 {
			return nil, fmt.Errorf("circular dependency detected in task graph")
		}
		for _, id := range layer {
			resolved[id] = true
			// 减少依赖此节点的其他节点的入度
			for otherID, node := range dag.nodes {
				for _, dep := range node.DependsOn {
					if dep == id {
						inDegree[otherID]--
					}
				}
			}
		}
		layers = append(layers, layer)
	}

	dag.order = layers
	return dag, nil
}

// Execute 按拓扑序执行 DAG，同层任务用 errgroup 并行
// 支持：Guard 中间件（限流/熔断/成本）+ 节点间上下文传递 + Mailbox 结果广播
func (e *DAGExecutor) Execute(ctx context.Context, dag *DAG) (map[string]*schema.Result, error) {
	results := make(map[string]*schema.Result)

	for layerIdx, layer := range dag.order {
		e.logger.Info("executing DAG layer", "layer", layerIdx, "tasks", len(layer))

		// 在执行前，将前序结果注入到当前层任务的上下文中
		for _, taskID := range layer {
			node := dag.nodes[taskID]
			e.injectDependencyContext(node, results)
		}

		g, gCtx := errgroup.WithContext(ctx)
		resultCh := make(chan *layerResult, len(layer))

		for _, taskID := range layer {
			node := dag.nodes[taskID]
			if node.Agent == nil {
				return nil, fmt.Errorf("no agent assigned for task %s", taskID)
			}

			g.Go(func() error {
				e.logger.Info("executing task",
					"task_id", node.Task.ID,
					"agent", node.Agent.ID(),
					"provider", node.Agent.Provider(),
				)

				// 通过 Guard 执行（统一限流/熔断/成本追踪）
				result, err := e.guard.Execute(gCtx, node.Agent, node.Task)
				if err != nil {
					resultCh <- &layerResult{taskID: node.Task.ID, err: err}
					return fmt.Errorf("task %s failed: %w", node.Task.ID, err)
				}

				resultCh <- &layerResult{taskID: node.Task.ID, result: result}

				// 通过 Mailbox 广播任务完成结果
				e.broadcastResult(node, result)

				return nil
			})
		}

		err := g.Wait()
		close(resultCh)

		for lr := range resultCh {
			if lr.result != nil {
				results[lr.taskID] = lr.result
			}
		}

		if err != nil {
			return results, err
		}
	}

	return results, nil
}

// injectDependencyContext 将前序任务的结果摘要注入到当前任务的 Prompt 中
func (e *DAGExecutor) injectDependencyContext(node *DAGNode, results map[string]*schema.Result) {
	if len(node.DependsOn) == 0 {
		return
	}

	var depContexts []string
	for _, depID := range node.DependsOn {
		if r, ok := results[depID]; ok && r.Content != "" {
			depContexts = append(depContexts, fmt.Sprintf("[来自任务 %s 的结果]\n%s", depID, r.Content))
		}
	}

	if len(depContexts) == 0 {
		return
	}

	contextPrefix := "以下是前序任务的执行结果，请在此基础上继续完成当前任务：\n\n" +
		strings.Join(depContexts, "\n\n---\n\n") +
		"\n\n---\n\n当前任务：\n"

	node.Task.Prompt = contextPrefix + node.Task.Prompt

	// 同时写入 Metadata 方便追溯
	if node.Task.Metadata == nil {
		node.Task.Metadata = make(map[string]any)
	}
	depResults := make(map[string]string)
	for _, depID := range node.DependsOn {
		if r, ok := results[depID]; ok {
			depResults[depID] = r.Content
		}
	}
	node.Task.Metadata["dep_results"] = depResults
}

// broadcastResult 通过 Mailbox 广播任务完成消息
func (e *DAGExecutor) broadcastResult(node *DAGNode, result *schema.Result) {
	if e.mailbox == nil {
		return
	}
	msg := &schema.Message{
		ID:   fmt.Sprintf("dag-result-%s", node.Task.ID),
		From: node.Agent.ID(),
		To:   "*",
		Type: schema.MsgTaskResult,
		Content: fmt.Sprintf("任务 %s 已完成", node.Task.ID),
		Metadata: map[string]any{
			"task_id":  node.Task.ID,
			"agent_id": node.Agent.ID(),
			"summary":  truncate(result.Content, 200),
		},
	}
	if err := e.mailbox.Broadcast(msg); err != nil {
		e.logger.Debug("broadcast result skipped", "task_id", node.Task.ID, "error", err)
	}
}

// Layers 返回分层列表（用于展示）
func (d *DAG) Layers() [][]string {
	return d.order
}

type layerResult struct {
	taskID string
	result *schema.Result
	err    error
}

// truncate 截断字符串
func truncate(s string, maxLen int) string {
	runes := []rune(s)
	if len(runes) <= maxLen {
		return s
	}
	return string(runes[:maxLen]) + "..."
}
