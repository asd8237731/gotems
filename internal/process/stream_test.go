package process

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestStreamParserJSONLines(t *testing.T) {
	input := `{"type":"assistant","message":{"content":[{"type":"text","text":"Hello World"}]}}
{"type":"result","result":"done","session_id":"sess-123","usage":{"input_tokens":50,"output_tokens":100}}
`
	parser := NewStreamParser(strings.NewReader(input), FormatJSONLines)
	ctx := context.Background()
	chunks := parser.Parse(ctx)

	var results []StreamChunk
	for chunk := range chunks {
		results = append(results, chunk)
	}

	if len(results) != 2 {
		t.Fatalf("expected 2 chunks, got %d", len(results))
	}

	if results[0].Type != "text" || results[0].Content != "Hello World" {
		t.Errorf("chunk 0: expected text='Hello World', got type=%s content=%s", results[0].Type, results[0].Content)
	}

	if results[1].Type != "result" || results[1].Content != "done" {
		t.Errorf("chunk 1: expected result='done', got type=%s content=%s", results[1].Type, results[1].Content)
	}

	if results[1].SessionID != "sess-123" {
		t.Errorf("expected session_id='sess-123', got '%s'", results[1].SessionID)
	}

	if results[1].Usage == nil || results[1].Usage.InputTokens != 50 || results[1].Usage.OutputTokens != 100 {
		t.Errorf("unexpected usage: %+v", results[1].Usage)
	}
}

func TestStreamParserPlainText(t *testing.T) {
	input := "line 1\nline 2\nline 3\n"
	parser := NewStreamParser(strings.NewReader(input), FormatPlainText)
	ctx := context.Background()

	var lines []string
	for chunk := range parser.Parse(ctx) {
		lines = append(lines, chunk.Content)
	}

	if len(lines) != 3 {
		t.Fatalf("expected 3 lines, got %d", len(lines))
	}

	if lines[0] != "line 1" || lines[2] != "line 3" {
		t.Errorf("unexpected lines: %v", lines)
	}
}

func TestStreamParserContextCancel(t *testing.T) {
	// 创建一个会持续输出的 reader
	input := strings.Repeat("line\n", 10000)
	parser := NewStreamParser(strings.NewReader(input), FormatPlainText)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()

	count := 0
	for range parser.Parse(ctx) {
		count++
	}

	// 应该在超时后停止，不会读完所有 10000 行
	// 但由于 string reader 很快，可能会读完
	// 关键是不会死锁
	t.Logf("read %d lines before context cancel", count)
}

func TestParseSingleJSONClaudeFormat(t *testing.T) {
	input := `{"result":"Hello from Claude","session_id":"abc-123","usage":{"input_tokens":10,"output_tokens":20}}`

	chunk, err := ParseSingleJSON([]byte(input))
	if err != nil {
		t.Fatalf("parse error: %v", err)
	}

	if chunk.Type != "result" {
		t.Errorf("expected type=result, got %s", chunk.Type)
	}
	if chunk.Content != "Hello from Claude" {
		t.Errorf("expected content='Hello from Claude', got '%s'", chunk.Content)
	}
	if chunk.SessionID != "abc-123" {
		t.Errorf("expected session_id='abc-123', got '%s'", chunk.SessionID)
	}
	if chunk.Usage.InputTokens != 10 || chunk.Usage.OutputTokens != 20 {
		t.Errorf("unexpected usage: %+v", chunk.Usage)
	}
}

func TestParseSingleJSONOpenAIFormat(t *testing.T) {
	input := `{"choices":[{"message":{"content":"Hello from GPT"}}],"usage":{"prompt_tokens":15,"completion_tokens":25}}`

	chunk, err := ParseSingleJSON([]byte(input))
	if err != nil {
		t.Fatalf("parse error: %v", err)
	}

	if chunk.Type != "result" {
		t.Errorf("expected type=result, got %s", chunk.Type)
	}
	if chunk.Content != "Hello from GPT" {
		t.Errorf("expected content='Hello from GPT', got '%s'", chunk.Content)
	}
	if chunk.Usage.InputTokens != 15 || chunk.Usage.OutputTokens != 25 {
		t.Errorf("unexpected usage: %+v", chunk.Usage)
	}
}

func TestParseSingleJSONGeminiFormat(t *testing.T) {
	input := `{"candidates":[{"content":{"parts":[{"text":"Hello from Gemini"}]}}],"usageMetadata":{"promptTokenCount":30,"candidatesTokenCount":40}}`

	chunk, err := ParseSingleJSON([]byte(input))
	if err != nil {
		t.Fatalf("parse error: %v", err)
	}

	if chunk.Type != "result" {
		t.Errorf("expected type=result, got %s", chunk.Type)
	}
	if chunk.Content != "Hello from Gemini" {
		t.Errorf("expected content='Hello from Gemini', got '%s'", chunk.Content)
	}
	if chunk.Usage.InputTokens != 30 || chunk.Usage.OutputTokens != 40 {
		t.Errorf("unexpected usage: %+v", chunk.Usage)
	}
}

func TestParseSingleJSONInvalid(t *testing.T) {
	_, err := ParseSingleJSON([]byte("not json"))
	if err == nil {
		t.Error("expected error for invalid JSON")
	}
}
