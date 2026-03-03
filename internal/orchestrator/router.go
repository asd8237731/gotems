package orchestrator

import (
	"github.com/lyymini/gotems/internal/agent"
)

// ModelProfile 描述模型的特长和成本信息
type ModelProfile struct {
	Provider     agent.ProviderType
	Model        string
	Strengths    []agent.Capability
	CostPerKIn   float64
	CostPerKOut  float64
	MaxContext   int
}

// Router 智能路由器，根据策略选择最合适的 Agent
type Router struct {
	strategy Strategy
	agents   map[string]agent.Agent
	profiles map[string]*ModelProfile // agentID -> profile
}

// Strategy 路由策略
type Strategy int

const (
	StrategyBestFit    Strategy = iota // 按能力匹配
	StrategyCostFirst                  // 成本优先
	StrategySpeedFirst                 // 速度优先
	StrategyConsensus                  // 多模型共识（全部执行）
	StrategyRoundRobin                 // 轮询
)

// ParseStrategy 从字符串解析策略
func ParseStrategy(s string) Strategy {
	switch s {
	case "cost_first":
		return StrategyCostFirst
	case "speed_first":
		return StrategySpeedFirst
	case "consensus":
		return StrategyConsensus
	case "round_robin":
		return StrategyRoundRobin
	default:
		return StrategyBestFit
	}
}

// NewRouter 创建路由器
func NewRouter(strategy Strategy, agents map[string]agent.Agent) *Router {
	return &Router{
		strategy: strategy,
		agents:   agents,
		profiles: make(map[string]*ModelProfile),
	}
}

// SetProfile 设置 Agent 的模型画像
func (r *Router) SetProfile(agentID string, p *ModelProfile) {
	r.profiles[agentID] = p
}

// Route 根据任务标签选择最佳 Agent
func (r *Router) Route(tags []string) agent.Agent {
	switch r.strategy {
	case StrategyConsensus:
		return nil // 共识模式不选单个，由编排层处理
	case StrategyCostFirst:
		return r.routeByCost()
	default:
		return r.routeByCapability(tags)
	}
}

// RouteAll 返回所有可用 Agent（共识模式使用）
func (r *Router) RouteAll() []agent.Agent {
	agents := make([]agent.Agent, 0, len(r.agents))
	for _, a := range r.agents {
		if a.Status() == agent.StatusIdle || a.Status() == agent.StatusBusy {
			agents = append(agents, a)
		}
	}
	return agents
}

// routeByCapability 按能力匹配
func (r *Router) routeByCapability(tags []string) agent.Agent {
	required := make(map[agent.Capability]bool)
	for _, t := range tags {
		required[agent.Capability(t)] = true
	}

	var best agent.Agent
	bestScore := -1

	for _, a := range r.agents {
		if a.Status() == agent.StatusStopped || a.Status() == agent.StatusError {
			continue
		}
		score := 0
		for _, cap := range a.Capabilities() {
			if required[cap] {
				score++
			}
		}
		// 空闲的优先
		if a.Status() == agent.StatusIdle {
			score += 1
		}
		if score > bestScore {
			bestScore = score
			best = a
		}
	}
	return best
}

// routeByCost 成本最低优先
func (r *Router) routeByCost() agent.Agent {
	var best agent.Agent
	bestCost := float64(999999)

	for id, a := range r.agents {
		if a.Status() == agent.StatusStopped || a.Status() == agent.StatusError {
			continue
		}
		p, ok := r.profiles[id]
		if !ok {
			continue
		}
		totalCost := p.CostPerKIn + p.CostPerKOut
		if totalCost < bestCost {
			bestCost = totalCost
			best = a
		}
	}
	// 如果没有 profile，返回第一个可用的
	if best == nil {
		for _, a := range r.agents {
			if a.Status() != agent.StatusStopped {
				return a
			}
		}
	}
	return best
}
