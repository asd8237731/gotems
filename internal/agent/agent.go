package agent

import (
	"context"

	"github.com/lyymini/gotems/internal/task"
	"github.com/lyymini/gotems/pkg/schema"
)

// ProviderType 标识 AI 提供商
type ProviderType string

const (
	ProviderClaude ProviderType = "claude"
	ProviderGemini ProviderType = "gemini"
	ProviderOpenAI ProviderType = "openai"
	ProviderOllama ProviderType = "ollama"
)

// Capability 描述 Agent 的能力标签
type Capability string

const (
	CapCodeGen      Capability = "code_generation"
	CapCodeReview   Capability = "code_review"
	CapReasoning    Capability = "deep_reasoning"
	CapMultimodal   Capability = "multimodal"
	CapLargeContext Capability = "large_context"
	CapTestGen      Capability = "test_generation"
	CapRefactor     Capability = "refactoring"
	CapQuickTask    Capability = "quick_task"
)

// Status 是 Agent 的运行状态
type Status int

const (
	StatusIdle    Status = iota // 空闲
	StatusBusy                 // 忙碌
	StatusError                // 出错
	StatusStopped              // 已停止
)

func (s Status) String() string {
	switch s {
	case StatusIdle:
		return "idle"
	case StatusBusy:
		return "busy"
	case StatusError:
		return "error"
	case StatusStopped:
		return "stopped"
	default:
		return "unknown"
	}
}

// Agent 是所有 AI 智能体的统一接口
type Agent interface {
	// 基础信息
	ID() string
	Provider() ProviderType
	Model() string

	// 执行任务
	Execute(ctx context.Context, t *task.Task) (*schema.Result, error)

	// 流式执行
	Stream(ctx context.Context, t *task.Task) (<-chan schema.StreamEvent, error)

	// 通信
	Send(ctx context.Context, msg *schema.Message) error
	Inbox() <-chan *schema.Message

	// 生命周期
	Start(ctx context.Context) error
	Stop(ctx context.Context) error
	Status() Status

	// 能力声明
	Capabilities() []Capability
}

// BaseAgent 提供 Agent 通用字段，具体实现嵌入此结构体
type BaseAgent struct {
	AgentID      string
	ProviderType ProviderType
	ModelID      string
	Caps         []Capability
	InboxCh      chan *schema.Message
	StatusVal    Status
}

func (b *BaseAgent) ID() string              { return b.AgentID }
func (b *BaseAgent) Provider() ProviderType   { return b.ProviderType }
func (b *BaseAgent) Model() string            { return b.ModelID }
func (b *BaseAgent) Capabilities() []Capability { return b.Caps }
func (b *BaseAgent) Inbox() <-chan *schema.Message { return b.InboxCh }
func (b *BaseAgent) Status() Status           { return b.StatusVal }

func (b *BaseAgent) Send(_ context.Context, msg *schema.Message) error {
	b.InboxCh <- msg
	return nil
}
