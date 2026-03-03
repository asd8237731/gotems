package orchestrator

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/lyymini/gotems/internal/agent"
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
	Task     *task.Task
	DependsOn []string
	Agent    agent.Agent // 路由分配的 Agent
}

// DAGExecutor 执行 DAG
type DAGExecutor struct {
	router *Router
	logger *slog.Logger
}

// NewDAGExecutor 创建 DAG 执行器
func NewDAGExecutor(router *Router, logger *slog.Logger) *DAGExecutor {
	return &DAGExecutor{
		router: router,
		logger: logger,
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
		inDegree[id] = 0
	}
	for _, node := range dag.nodes {
		for _, dep := range node.DependsOn {
			inDegree[node.Task.ID]++
			_ = dep
		}
	}

	// 重新计算入度
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
func (e *DAGExecutor) Execute(ctx context.Context, dag *DAG) (map[string]*schema.Result, error) {
	results := make(map[string]*schema.Result)

	for layerIdx, layer := range dag.order {
		e.logger.Info("executing DAG layer", "layer", layerIdx, "tasks", len(layer))

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
				result, err := node.Agent.Execute(gCtx, node.Task)
				if err != nil {
					resultCh <- &layerResult{taskID: node.Task.ID, err: err}
					return fmt.Errorf("task %s failed: %w", node.Task.ID, err)
				}
				resultCh <- &layerResult{taskID: node.Task.ID, result: result}
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

// Layers 返回分层列表（用于展示）
func (d *DAG) Layers() [][]string {
	return d.order
}

type layerResult struct {
	taskID string
	result *schema.Result
	err    error
}
