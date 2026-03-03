package schema

import "time"

// MessageType 定义消息类型
type MessageType int

const (
	MsgTaskAssign  MessageType = iota // 任务分配
	MsgTaskResult                     // 任务结果
	MsgQuestion                       // 向其他 Agent 提问
	MsgAnswer                         // 回答
	MsgFileChanged                    // 文件变更通知
	MsgConflict                       // 冲突告警
	MsgBroadcast                      // 广播消息
	MsgHeartbeat                      // 心跳
)

func (m MessageType) String() string {
	switch m {
	case MsgTaskAssign:
		return "task_assign"
	case MsgTaskResult:
		return "task_result"
	case MsgQuestion:
		return "question"
	case MsgAnswer:
		return "answer"
	case MsgFileChanged:
		return "file_changed"
	case MsgConflict:
		return "conflict"
	case MsgBroadcast:
		return "broadcast"
	case MsgHeartbeat:
		return "heartbeat"
	default:
		return "unknown"
	}
}

// Message 是 Agent 之间通信的基本单元
type Message struct {
	ID        string         `json:"id"`
	From      string         `json:"from"`
	To        string         `json:"to"` // "*" 表示广播
	Type      MessageType    `json:"type"`
	Content   string         `json:"content"`
	Metadata  map[string]any `json:"metadata,omitempty"`
	Timestamp time.Time      `json:"timestamp"`
}

// StreamEvent 是流式输出事件
type StreamEvent struct {
	AgentID string `json:"agent_id"`
	Type    string `json:"type"` // "text", "tool_use", "thinking", "error", "done"
	Content string `json:"content"`
}

// Result 是 Agent 执行任务后的返回结果
type Result struct {
	AgentID    string         `json:"agent_id"`
	Provider   string         `json:"provider"`
	Content    string         `json:"content"`
	TokensIn   int            `json:"tokens_in"`
	TokensOut  int            `json:"tokens_out"`
	Cost       float64        `json:"cost"`
	Duration   time.Duration  `json:"duration"`
	Metadata   map[string]any `json:"metadata,omitempty"`
	FilesChanged []string     `json:"files_changed,omitempty"`
	Error      string         `json:"error,omitempty"`
}

// FinalResult 是编排器聚合后的最终结果
type FinalResult struct {
	Content    string    `json:"content"`
	Results    []*Result `json:"results"`
	TotalCost  float64   `json:"total_cost"`
	TotalTokensIn  int   `json:"total_tokens_in"`
	TotalTokensOut int   `json:"total_tokens_out"`
	Strategy   string    `json:"strategy"`
}
