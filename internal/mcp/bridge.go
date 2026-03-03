package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sync"

	"github.com/lyymini/gotems/internal/agent"
	"github.com/lyymini/gotems/internal/cost"
	"github.com/lyymini/gotems/internal/task"
	"github.com/lyymini/gotems/pkg/schema"
)

// Bridge 将 GoTems 的能力暴露为 MCP Tools，
// 同时也能将外部 MCP Tools 引入给 GoTems Agent 使用。
// 本实现不依赖外部 MCP 库，提供独立的 JSON-RPC 2.0 协议层。
type Bridge struct {
	mu       sync.RWMutex
	tools    map[string]*Tool
	agents   map[string]agent.Agent
	tracker  *cost.Tracker
	taskPool *task.Pool
	logger   *slog.Logger
}

// Tool 描述一个 MCP 工具
type Tool struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"inputSchema"`
	Handler     ToolHandler     `json:"-"`
}

// ToolHandler 工具执行函数
type ToolHandler func(ctx context.Context, args map[string]any) (any, error)

// NewBridge 创建 MCP 桥接器
func NewBridge(agents map[string]agent.Agent, tracker *cost.Tracker, pool *task.Pool, logger *slog.Logger) *Bridge {
	b := &Bridge{
		tools:    make(map[string]*Tool),
		agents:   agents,
		tracker:  tracker,
		taskPool: pool,
		logger:   logger,
	}
	b.registerBuiltinTools()
	return b
}

// registerBuiltinTools 注册 GoTems 内置的 MCP 工具
func (b *Bridge) registerBuiltinTools() {
	b.Register(&Tool{
		Name:        "gotems_run_task",
		Description: "通过 GoTems 编排器执行一个编码任务，自动路由到最佳 AI Agent",
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"prompt": {"type": "string", "description": "任务描述"},
				"provider": {"type": "string", "description": "指定 Provider (可选): claude, gemini, openai, ollama"},
				"tags": {"type": "array", "items": {"type": "string"}, "description": "能力标签"}
			},
			"required": ["prompt"]
		}`),
		Handler: b.handleRunTask,
	})

	b.Register(&Tool{
		Name:        "gotems_list_agents",
		Description: "列出 GoTems 中所有注册的 AI Agent 及其状态",
		InputSchema: json.RawMessage(`{"type": "object", "properties": {}}`),
		Handler:     b.handleListAgents,
	})

	b.Register(&Tool{
		Name:        "gotems_cost_summary",
		Description: "查看 GoTems 的费用统计摘要",
		InputSchema: json.RawMessage(`{"type": "object", "properties": {}}`),
		Handler:     b.handleCostSummary,
	})

	b.Register(&Tool{
		Name:        "gotems_task_pool",
		Description: "查看当前任务池中的所有任务状态",
		InputSchema: json.RawMessage(`{"type": "object", "properties": {}}`),
		Handler:     b.handleTaskPool,
	})

	b.Register(&Tool{
		Name:        "gotems_consensus",
		Description: "对同一个任务使用多个 AI Agent 并行执行，返回所有结果",
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"prompt": {"type": "string", "description": "任务描述"}
			},
			"required": ["prompt"]
		}`),
		Handler: b.handleConsensus,
	})
}

// Register 注册自定义 MCP 工具
func (b *Bridge) Register(tool *Tool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.tools[tool.Name] = tool
	b.logger.Info("MCP tool registered", "name", tool.Name)
}

// ListTools 列出所有已注册的 MCP 工具（MCP tools/list 响应）
func (b *Bridge) ListTools() []ToolInfo {
	b.mu.RLock()
	defer b.mu.RUnlock()

	infos := make([]ToolInfo, 0, len(b.tools))
	for _, t := range b.tools {
		infos = append(infos, ToolInfo{
			Name:        t.Name,
			Description: t.Description,
			InputSchema: t.InputSchema,
		})
	}
	return infos
}

// CallTool 调用指定的 MCP 工具（MCP tools/call 响应）
func (b *Bridge) CallTool(ctx context.Context, name string, args map[string]any) (*ToolResult, error) {
	b.mu.RLock()
	tool, ok := b.tools[name]
	b.mu.RUnlock()

	if !ok {
		return nil, fmt.Errorf("MCP tool %s not found", name)
	}

	b.logger.Info("MCP tool called", "name", name)

	result, err := tool.Handler(ctx, args)
	if err != nil {
		return &ToolResult{
			IsError: true,
			Content: []ContentBlock{{Type: "text", Text: err.Error()}},
		}, nil
	}

	text, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		text = []byte(fmt.Sprintf("%v", result))
	}

	return &ToolResult{
		Content: []ContentBlock{{Type: "text", Text: string(text)}},
	}, nil
}

// --- MCP 协议类型 ---

// ToolInfo MCP tools/list 返回的工具描述
type ToolInfo struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"inputSchema"`
}

// ToolResult MCP tools/call 返回的执行结果
type ToolResult struct {
	Content []ContentBlock `json:"content"`
	IsError bool           `json:"isError,omitempty"`
}

// ContentBlock MCP 内容块
type ContentBlock struct {
	Type string `json:"type"`
	Text string `json:"text,omitempty"`
}

// JSONRPCRequest JSON-RPC 2.0 请求
type JSONRPCRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      any             `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

// JSONRPCResponse JSON-RPC 2.0 响应
type JSONRPCResponse struct {
	JSONRPC string         `json:"jsonrpc"`
	ID      any            `json:"id"`
	Result  any            `json:"result,omitempty"`
	Error   *JSONRPCError  `json:"error,omitempty"`
}

// JSONRPCError JSON-RPC 错误
type JSONRPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// HandleJSONRPC 处理 MCP JSON-RPC 请求（支持 stdio 和 HTTP 传输）
func (b *Bridge) HandleJSONRPC(ctx context.Context, reqBytes []byte) ([]byte, error) {
	var req JSONRPCRequest
	if err := json.Unmarshal(reqBytes, &req); err != nil {
		return marshalResponse(JSONRPCResponse{
			JSONRPC: "2.0",
			Error:   &JSONRPCError{Code: -32700, Message: "Parse error"},
		})
	}

	var result any
	var rpcErr *JSONRPCError

	switch req.Method {
	case "initialize":
		result = map[string]any{
			"protocolVersion": "2024-11-05",
			"serverInfo": map[string]string{
				"name":    "gotems",
				"version": "0.1.0",
			},
			"capabilities": map[string]any{
				"tools": map[string]bool{"listChanged": false},
			},
		}
	case "tools/list":
		result = map[string]any{
			"tools": b.ListTools(),
		}
	case "tools/call":
		var params struct {
			Name      string         `json:"name"`
			Arguments map[string]any `json:"arguments"`
		}
		if err := json.Unmarshal(req.Params, &params); err != nil {
			rpcErr = &JSONRPCError{Code: -32602, Message: "Invalid params"}
		} else {
			toolResult, err := b.CallTool(ctx, params.Name, params.Arguments)
			if err != nil {
				rpcErr = &JSONRPCError{Code: -32603, Message: err.Error()}
			} else {
				result = toolResult
			}
		}
	case "notifications/initialized":
		// 客户端初始化通知，无需响应
		return nil, nil
	default:
		rpcErr = &JSONRPCError{Code: -32601, Message: fmt.Sprintf("Method not found: %s", req.Method)}
	}

	return marshalResponse(JSONRPCResponse{
		JSONRPC: "2.0",
		ID:      req.ID,
		Result:  result,
		Error:   rpcErr,
	})
}

func marshalResponse(resp JSONRPCResponse) ([]byte, error) {
	return json.Marshal(resp)
}

// --- 内置工具处理函数 ---

func (b *Bridge) handleRunTask(ctx context.Context, args map[string]any) (any, error) {
	prompt, _ := args["prompt"].(string)
	if prompt == "" {
		return nil, fmt.Errorf("prompt is required")
	}

	provider, _ := args["provider"].(string)
	tags, _ := args["tags"].([]any)

	var tagStrs []string
	for _, t := range tags {
		if s, ok := t.(string); ok {
			tagStrs = append(tagStrs, s)
		}
	}

	// 选择 Agent
	var selected agent.Agent
	if provider != "" {
		for _, a := range b.agents {
			if string(a.Provider()) == provider && a.Status() != agent.StatusStopped {
				selected = a
				break
			}
		}
	} else {
		// 选择第一个可用的
		for _, a := range b.agents {
			if a.Status() == agent.StatusIdle {
				selected = a
				break
			}
		}
	}

	if selected == nil {
		return nil, fmt.Errorf("no available agent")
	}

	t := &task.Task{
		ID:     "mcp-task",
		Prompt: prompt,
		Tags:   tagStrs,
	}

	result, err := selected.Execute(ctx, t)
	if err != nil {
		return nil, err
	}
	return result, nil
}

func (b *Bridge) handleListAgents(_ context.Context, _ map[string]any) (any, error) {
	infos := make([]map[string]any, 0, len(b.agents))
	for _, a := range b.agents {
		infos = append(infos, map[string]any{
			"id":           a.ID(),
			"provider":     a.Provider(),
			"model":        a.Model(),
			"status":       a.Status().String(),
			"capabilities": a.Capabilities(),
		})
	}
	return infos, nil
}

func (b *Bridge) handleCostSummary(_ context.Context, _ map[string]any) (any, error) {
	if b.tracker == nil {
		return map[string]string{"message": "cost tracker not configured"}, nil
	}
	return b.tracker.Summarize(), nil
}

func (b *Bridge) handleTaskPool(_ context.Context, _ map[string]any) (any, error) {
	if b.taskPool == nil {
		return []any{}, nil
	}
	return b.taskPool.All(), nil
}

func (b *Bridge) handleConsensus(ctx context.Context, args map[string]any) (any, error) {
	prompt, _ := args["prompt"].(string)
	if prompt == "" {
		return nil, fmt.Errorf("prompt is required")
	}

	t := &task.Task{
		ID:     "mcp-consensus",
		Prompt: prompt,
	}

	var results []*schema.Result
	for _, a := range b.agents {
		if a.Status() == agent.StatusStopped {
			continue
		}
		result, err := a.Execute(ctx, t)
		if err != nil {
			b.logger.Warn("consensus agent failed", "agent", a.ID(), "error", err)
			continue
		}
		results = append(results, result)
	}

	return results, nil
}
