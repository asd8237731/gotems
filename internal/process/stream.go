package process

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
)

// StreamParser 解析 CLI 工具的流式 JSON 输出
type StreamParser struct {
	reader  *bufio.Reader
	format  OutputFormat
}

// OutputFormat CLI 工具的输出格式
type OutputFormat int

const (
	FormatJSONLines  OutputFormat = iota // 每行一个 JSON（Claude Code）
	FormatStreamJSON                     // JSON streaming（Gemini CLI）
	FormatPlainText                      // 纯文本（fallback）
)

// StreamChunk 解析后的流式数据块
type StreamChunk struct {
	Type      string         `json:"type"`       // "text", "tool_use", "thinking", "result", "error", "system"
	Content   string         `json:"content"`
	ToolName  string         `json:"tool_name,omitempty"`
	ToolInput map[string]any `json:"tool_input,omitempty"`
	SessionID string         `json:"session_id,omitempty"`
	Usage     *UsageInfo     `json:"usage,omitempty"`
	Raw       json.RawMessage `json:"raw,omitempty"` // 原始 JSON
}

// UsageInfo Token 使用统计
type UsageInfo struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
}

// NewStreamParser 创建流解析器
func NewStreamParser(r io.Reader, format OutputFormat) *StreamParser {
	return &StreamParser{
		reader: bufio.NewReaderSize(r, 1024*1024),
		format: format,
	}
}

// Parse 流式解析输出，通过 channel 返回解析后的数据块
func (p *StreamParser) Parse(ctx context.Context) <-chan StreamChunk {
	ch := make(chan StreamChunk, 128)

	go func() {
		defer close(ch)

		switch p.format {
		case FormatJSONLines:
			p.parseJSONLines(ctx, ch)
		case FormatStreamJSON:
			p.parseStreamJSON(ctx, ch)
		case FormatPlainText:
			p.parsePlainText(ctx, ch)
		}
	}()

	return ch
}

// parseJSONLines 解析 Claude Code 的 JSON Lines 输出
// Claude Code --output-format stream-json 输出每行一个 JSON 对象
func (p *StreamParser) parseJSONLines(ctx context.Context, ch chan<- StreamChunk) {
	scanner := bufio.NewScanner(p.reader)
	scanner.Buffer(make([]byte, 0, 1024*1024), 1024*1024)

	for scanner.Scan() {
		select {
		case <-ctx.Done():
			return
		default:
		}

		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}

		// 尝试解析为 Claude Code JSON 格式
		var raw map[string]any
		if err := json.Unmarshal([]byte(line), &raw); err != nil {
			// 非 JSON 行，作为纯文本
			ch <- StreamChunk{Type: "text", Content: line}
			continue
		}

		chunk := p.parseClaudeJSON(raw, []byte(line))
		ch <- chunk
	}
}

// parseClaudeJSON 解析 Claude Code 的 JSON 输出
func (p *StreamParser) parseClaudeJSON(raw map[string]any, rawBytes []byte) StreamChunk {
	chunk := StreamChunk{Raw: rawBytes}

	// Claude Code stream-json 格式
	if msgType, ok := raw["type"].(string); ok {
		switch msgType {
		case "assistant":
			// 助手消息
			if content, ok := raw["message"].(map[string]any); ok {
				if blocks, ok := content["content"].([]any); ok {
					for _, block := range blocks {
						if b, ok := block.(map[string]any); ok {
							if b["type"] == "text" {
								chunk.Type = "text"
								chunk.Content, _ = b["text"].(string)
							} else if b["type"] == "tool_use" {
								chunk.Type = "tool_use"
								chunk.ToolName, _ = b["name"].(string)
								if input, ok := b["input"].(map[string]any); ok {
									chunk.ToolInput = input
								}
							}
						}
					}
				}
			}
		case "result":
			chunk.Type = "result"
			if result, ok := raw["result"].(string); ok {
				chunk.Content = result
			}
			// 提取 usage
			if usage, ok := raw["usage"].(map[string]any); ok {
				chunk.Usage = &UsageInfo{}
				if v, ok := usage["input_tokens"].(float64); ok {
					chunk.Usage.InputTokens = int(v)
				}
				if v, ok := usage["output_tokens"].(float64); ok {
					chunk.Usage.OutputTokens = int(v)
				}
			}
			// 提取 session_id
			if sid, ok := raw["session_id"].(string); ok {
				chunk.SessionID = sid
			}
		case "system":
			chunk.Type = "system"
			if msg, ok := raw["message"].(string); ok {
				chunk.Content = msg
			}
		case "error":
			chunk.Type = "error"
			if msg, ok := raw["error"].(map[string]any); ok {
				chunk.Content, _ = msg["message"].(string)
			} else if msg, ok := raw["message"].(string); ok {
				chunk.Content = msg
			}
		default:
			chunk.Type = msgType
			chunk.Content = string(rawBytes)
		}
	} else {
		// 简单 JSON 输出（claude -p --output-format json）
		if result, ok := raw["result"].(string); ok {
			chunk.Type = "result"
			chunk.Content = result
			if usage, ok := raw["usage"].(map[string]any); ok {
				chunk.Usage = &UsageInfo{}
				if v, ok := usage["input_tokens"].(float64); ok {
					chunk.Usage.InputTokens = int(v)
				}
				if v, ok := usage["output_tokens"].(float64); ok {
					chunk.Usage.OutputTokens = int(v)
				}
			}
			if sid, ok := raw["session_id"].(string); ok {
				chunk.SessionID = sid
			}
		} else {
			chunk.Type = "text"
			chunk.Content = string(rawBytes)
		}
	}

	return chunk
}

// parseStreamJSON 解析 Gemini CLI 的流式 JSON 输出
func (p *StreamParser) parseStreamJSON(ctx context.Context, ch chan<- StreamChunk) {
	scanner := bufio.NewScanner(p.reader)
	scanner.Buffer(make([]byte, 0, 1024*1024), 1024*1024)

	for scanner.Scan() {
		select {
		case <-ctx.Done():
			return
		default:
		}

		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}

		var raw map[string]any
		if err := json.Unmarshal([]byte(line), &raw); err != nil {
			ch <- StreamChunk{Type: "text", Content: line}
			continue
		}

		chunk := StreamChunk{Raw: []byte(line)}

		// Gemini CLI 输出格式解析
		if parts, ok := raw["candidates"].([]any); ok && len(parts) > 0 {
			if cand, ok := parts[0].(map[string]any); ok {
				if content, ok := cand["content"].(map[string]any); ok {
					if textParts, ok := content["parts"].([]any); ok {
						for _, part := range textParts {
							if p, ok := part.(map[string]any); ok {
								if text, ok := p["text"].(string); ok {
									chunk.Type = "text"
									chunk.Content = text
								}
							}
						}
					}
				}
			}
		}

		if chunk.Type == "" {
			chunk.Type = "text"
			chunk.Content = line
		}

		ch <- chunk
	}
}

// parsePlainText 纯文本模式
func (p *StreamParser) parsePlainText(ctx context.Context, ch chan<- StreamChunk) {
	scanner := bufio.NewScanner(p.reader)
	scanner.Buffer(make([]byte, 0, 1024*1024), 1024*1024)

	for scanner.Scan() {
		select {
		case <-ctx.Done():
			return
		default:
		}

		ch <- StreamChunk{
			Type:    "text",
			Content: scanner.Text(),
		}
	}
}

// ParseSingleJSON 解析单次执行的完整 JSON 输出
func ParseSingleJSON(data []byte) (*StreamChunk, error) {
	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("parse JSON output: %w", err)
	}

	chunk := &StreamChunk{Raw: data}

	// Claude Code JSON 输出
	if result, ok := raw["result"].(string); ok {
		chunk.Type = "result"
		chunk.Content = result
		if usage, ok := raw["usage"].(map[string]any); ok {
			chunk.Usage = &UsageInfo{}
			if v, ok := usage["input_tokens"].(float64); ok {
				chunk.Usage.InputTokens = int(v)
			}
			if v, ok := usage["output_tokens"].(float64); ok {
				chunk.Usage.OutputTokens = int(v)
			}
		}
		if sid, ok := raw["session_id"].(string); ok {
			chunk.SessionID = sid
		}
		return chunk, nil
	}

	// OpenAI Chat Completions 格式
	if choices, ok := raw["choices"].([]any); ok && len(choices) > 0 {
		if choice, ok := choices[0].(map[string]any); ok {
			if msg, ok := choice["message"].(map[string]any); ok {
				chunk.Type = "result"
				chunk.Content, _ = msg["content"].(string)
			}
		}
		if usage, ok := raw["usage"].(map[string]any); ok {
			chunk.Usage = &UsageInfo{}
			if v, ok := usage["prompt_tokens"].(float64); ok {
				chunk.Usage.InputTokens = int(v)
			}
			if v, ok := usage["completion_tokens"].(float64); ok {
				chunk.Usage.OutputTokens = int(v)
			}
		}
		return chunk, nil
	}

	// Gemini 格式
	if candidates, ok := raw["candidates"].([]any); ok && len(candidates) > 0 {
		if cand, ok := candidates[0].(map[string]any); ok {
			if content, ok := cand["content"].(map[string]any); ok {
				if parts, ok := content["parts"].([]any); ok && len(parts) > 0 {
					if p, ok := parts[0].(map[string]any); ok {
						chunk.Type = "result"
						chunk.Content, _ = p["text"].(string)
					}
				}
			}
		}
		if meta, ok := raw["usageMetadata"].(map[string]any); ok {
			chunk.Usage = &UsageInfo{}
			if v, ok := meta["promptTokenCount"].(float64); ok {
				chunk.Usage.InputTokens = int(v)
			}
			if v, ok := meta["candidatesTokenCount"].(float64); ok {
				chunk.Usage.OutputTokens = int(v)
			}
		}
		return chunk, nil
	}

	chunk.Type = "text"
	chunk.Content = string(data)
	return chunk, nil
}
