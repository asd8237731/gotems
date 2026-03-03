package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
)

// StdioServer 通过标准输入/输出运行 MCP 服务器
// 每行一个 JSON-RPC 消息，兼容 Claude Code / Cursor 等 MCP 客户端
type StdioServer struct {
	bridge *Bridge
	logger *slog.Logger
}

// NewStdioServer 创建 stdio 传输的 MCP 服务器
func NewStdioServer(bridge *Bridge, logger *slog.Logger) *StdioServer {
	return &StdioServer{bridge: bridge, logger: logger}
}

// Run 启动 stdio 循环，阻塞读取 stdin 直到 EOF 或 context 取消
func (s *StdioServer) Run(ctx context.Context) error {
	s.logger.Info("MCP stdio server started")

	reader := bufio.NewReader(os.Stdin)
	writer := os.Stdout

	for {
		select {
		case <-ctx.Done():
			s.logger.Info("MCP stdio server shutting down")
			return ctx.Err()
		default:
		}

		line, err := reader.ReadBytes('\n')
		if err != nil {
			if err == io.EOF {
				s.logger.Info("MCP stdio: EOF received")
				return nil
			}
			return fmt.Errorf("read stdin: %w", err)
		}

		if len(line) == 0 || (len(line) == 1 && line[0] == '\n') {
			continue
		}

		resp, err := s.bridge.HandleJSONRPC(ctx, line)
		if err != nil {
			s.logger.Error("MCP handle error", "error", err)
			errResp := JSONRPCResponse{
				JSONRPC: "2.0",
				Error:   &JSONRPCError{Code: -32603, Message: err.Error()},
			}
			resp, _ = json.Marshal(errResp)
		}

		// 通知类消息不需要响应
		if resp == nil {
			continue
		}

		resp = append(resp, '\n')
		if _, err := writer.Write(resp); err != nil {
			return fmt.Errorf("write stdout: %w", err)
		}
	}
}
