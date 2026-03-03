package splitter

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"

	"github.com/lyymini/gotems/internal/agent"
	"github.com/lyymini/gotems/internal/task"
)

// Splitter 使用 AI Agent 将用户的大任务自动拆分为多个子任务
type Splitter struct {
	agent  agent.Agent // 用于拆分的 Agent（通常选推理能力强的）
	logger *slog.Logger
}

// NewSplitter 创建任务拆分器
func NewSplitter(a agent.Agent, logger *slog.Logger) *Splitter {
	return &Splitter{agent: a, logger: logger}
}

const splitPrompt = `你是一个任务规划专家。请将以下用户需求拆分为多个独立的子任务，用于分配给不同的 AI 编码助手并行完成。

要求：
1. 每个子任务应该是独立可执行的
2. 明确标注子任务之间的依赖关系
3. 为每个子任务标注所需能力标签（从以下选择）：
   - code_generation: 代码生成
   - code_review: 代码审查
   - deep_reasoning: 深度推理/架构设计
   - test_generation: 测试生成
   - refactoring: 重构
   - multimodal: 多模态（涉及图片等）
   - quick_task: 简单快速任务

请严格按以下 JSON 格式输出，不要输出其他内容：
[
  {
    "id": "t1",
    "prompt": "具体任务描述",
    "tags": ["能力标签"],
    "depends_on": []
  }
]

用户需求：
%s`

// Split 将用户提示词拆分为多个子任务
func (s *Splitter) Split(ctx context.Context, prompt string) ([]*task.Task, error) {
	if s.agent == nil {
		// 没有拆分 Agent，返回单个任务
		return []*task.Task{{
			ID:     "t1",
			Prompt: prompt,
			Tags:   []string{"code_generation"},
		}}, nil
	}

	s.logger.Info("splitting task with AI", "prompt_len", len(prompt))

	splitTask := &task.Task{
		ID:     "splitter",
		Prompt: fmt.Sprintf(splitPrompt, prompt),
	}

	result, err := s.agent.Execute(ctx, splitTask)
	if err != nil {
		s.logger.Warn("split failed, using single task fallback", "error", err)
		return []*task.Task{{
			ID:     "t1",
			Prompt: prompt,
			Tags:   []string{"code_generation"},
		}}, nil
	}

	tasks, err := parseSplitResult(result.Content)
	if err != nil {
		s.logger.Warn("failed to parse split result, using single task", "error", err)
		return []*task.Task{{
			ID:     "t1",
			Prompt: prompt,
			Tags:   []string{"code_generation"},
		}}, nil
	}

	s.logger.Info("task split complete", "sub_tasks", len(tasks))
	return tasks, nil
}

// parseSplitResult 从 AI 返回的文本中解析 JSON 任务列表
func parseSplitResult(content string) ([]*task.Task, error) {
	// 尝试从内容中提取 JSON 数组
	content = strings.TrimSpace(content)

	// 查找 JSON 数组的起止位置
	start := strings.Index(content, "[")
	end := strings.LastIndex(content, "]")
	if start == -1 || end == -1 || end <= start {
		return nil, fmt.Errorf("no JSON array found in response")
	}
	jsonStr := content[start : end+1]

	var tasks []*task.Task
	if err := json.Unmarshal([]byte(jsonStr), &tasks); err != nil {
		return nil, fmt.Errorf("parse JSON: %w", err)
	}

	if len(tasks) == 0 {
		return nil, fmt.Errorf("empty task list")
	}

	return tasks, nil
}
