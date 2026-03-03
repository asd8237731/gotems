package plugin

import (
	"context"
	"fmt"
	"log/slog"
	"sync"

	"github.com/lyymini/gotems/internal/task"
	"github.com/lyymini/gotems/pkg/schema"
)

// Plugin 是插件的统一接口
type Plugin interface {
	// Name 返回插件名称
	Name() string
	// Description 返回插件描述
	Description() string
	// Execute 执行插件逻辑
	Execute(ctx context.Context, input *PluginInput) (*PluginOutput, error)
}

// PluginInput 插件输入
type PluginInput struct {
	Task     *task.Task        `json:"task"`
	Files    map[string]string `json:"files,omitempty"`    // filePath -> content
	Metadata map[string]any    `json:"metadata,omitempty"`
}

// PluginOutput 插件输出
type PluginOutput struct {
	Content  string            `json:"content"`
	Files    map[string]string `json:"files,omitempty"`    // filePath -> newContent
	Metadata map[string]any    `json:"metadata,omitempty"`
}

// Manager 管理所有已注册的插件
type Manager struct {
	mu      sync.RWMutex
	plugins map[string]Plugin
	logger  *slog.Logger
}

// NewManager 创建插件管理器
func NewManager(logger *slog.Logger) *Manager {
	return &Manager{
		plugins: make(map[string]Plugin),
		logger:  logger,
	}
}

// Register 注册插件
func (m *Manager) Register(p Plugin) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.plugins[p.Name()] = p
	m.logger.Info("plugin registered", "name", p.Name(), "description", p.Description())
}

// Get 获取插件
func (m *Manager) Get(name string) (Plugin, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	p, ok := m.plugins[name]
	if !ok {
		return nil, fmt.Errorf("plugin %s not found", name)
	}
	return p, nil
}

// List 列出所有插件
func (m *Manager) List() []PluginInfo {
	m.mu.RLock()
	defer m.mu.RUnlock()
	infos := make([]PluginInfo, 0, len(m.plugins))
	for _, p := range m.plugins {
		infos = append(infos, PluginInfo{
			Name:        p.Name(),
			Description: p.Description(),
		})
	}
	return infos
}

// Execute 执行指定插件
func (m *Manager) Execute(ctx context.Context, name string, input *PluginInput) (*PluginOutput, error) {
	p, err := m.Get(name)
	if err != nil {
		return nil, err
	}
	return p.Execute(ctx, input)
}

// PluginInfo 插件信息
type PluginInfo struct {
	Name        string `json:"name"`
	Description string `json:"description"`
}

// --- 内置插件示例 ---

// CodeReviewPlugin 代码审查插件
type CodeReviewPlugin struct{}

func (p *CodeReviewPlugin) Name() string        { return "code-review" }
func (p *CodeReviewPlugin) Description() string { return "AI 驱动的代码审查" }
func (p *CodeReviewPlugin) Execute(_ context.Context, input *PluginInput) (*PluginOutput, error) {
	// 构造代码审查提示词，交给外部 Agent 处理
	prompt := fmt.Sprintf("请对以下代码进行代码审查，指出问题并给出改进建议:\n\n%s", input.Task.Prompt)
	return &PluginOutput{
		Content: prompt,
		Metadata: map[string]any{
			"type": "code_review_prompt",
		},
	}, nil
}

// TestGenPlugin 测试生成插件
type TestGenPlugin struct{}

func (p *TestGenPlugin) Name() string        { return "test-gen" }
func (p *TestGenPlugin) Description() string { return "自动生成单元测试" }
func (p *TestGenPlugin) Execute(_ context.Context, input *PluginInput) (*PluginOutput, error) {
	prompt := fmt.Sprintf("请为以下代码生成全面的单元测试:\n\n%s", input.Task.Prompt)
	return &PluginOutput{
		Content: prompt,
		Metadata: map[string]any{
			"type": "test_gen_prompt",
		},
	}, nil
}

// RegisterBuiltins 注册所有内置插件
func RegisterBuiltins(m *Manager) {
	m.Register(&CodeReviewPlugin{})
	m.Register(&TestGenPlugin{})
}

// --- schema 包的 Result 类型引用用于对齐 ---
var _ = schema.Result{}
