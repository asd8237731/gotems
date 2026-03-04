package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/lyymini/gotems/internal/agent"
	"github.com/lyymini/gotems/internal/config"
	"github.com/lyymini/gotems/internal/cost"
	"github.com/lyymini/gotems/internal/mcp"
	"github.com/lyymini/gotems/internal/orchestrator"
	"github.com/lyymini/gotems/internal/ratelimit"
	"github.com/lyymini/gotems/internal/server"
	"github.com/lyymini/gotems/internal/task"
	"github.com/lyymini/gotems/pkg/schema"
)

const version = "0.5.0"

func main() {
	if len(os.Args) < 2 {
		printUsage()
		os.Exit(1)
	}

	cmd := os.Args[1]
	switch cmd {
	case "run":
		cmdRun()
	case "split":
		cmdSplit()
	case "serve":
		cmdServe()
	case "mcp":
		cmdMCP()
	case "agents":
		cmdAgents()
	case "cost":
		cmdCost()
	case "version":
		fmt.Printf("gotems v%s\n", version)
	case "help", "-h", "--help":
		printUsage()
	default:
		fmt.Fprintf(os.Stderr, "未知命令: %s\n", cmd)
		printUsage()
		os.Exit(1)
	}
}

func printUsage() {
	fmt.Println(`GoTems — Go 多智能体编排框架 v` + version + `

用法:
  gotems run [选项] "你的任务描述"
  gotems run --strategy consensus "任务描述"
  gotems run --dag tasks.json
  gotems split "大任务描述"
  gotems serve [--addr :8080]
  gotems mcp
  gotems agents
  gotems cost
  gotems version

命令:
  run       执行任务（支持单 Agent、竞赛、DAG 模式）
  split     用 AI 自动拆分大任务为子任务后执行
  serve     启动 Web 仪表盘（HTTP API + SSE 实时推送）
  mcp       启动 MCP 服务器（stdio 传输，供 Claude Code 等调用）
  agents    列出已注册的 Agent 及状态
  cost      查看费用统计
  version   显示版本号

选项:
  --config, -c     配置文件路径 (默认: configs/gotems.yaml)
  --strategy, -s   路由策略: best_fit, cost_first, consensus (默认: best_fit)
  --provider, -p   指定 Provider: claude, gemini, openai, ollama
  --model, -m      指定模型 ID
  --dag            DAG 任务文件 (JSON)
  --addr           Web 服务器监听地址 (默认: :8080)
  --json           JSON 格式输出

环境变量:
  ANTHROPIC_API_KEY  Claude API 密钥
  GOOGLE_API_KEY     Gemini API 密钥
  OPENAI_API_KEY     OpenAI API 密钥`)
}

// --- run 命令 ---

func cmdRun() {
	args := parseArgs(os.Args[2:])
	logger := setupLogger(args["log-level"])
	cfg := loadConfig(args["config"])

	strategy := orchestrator.StrategyBestFit
	if s, ok := args["strategy"]; ok {
		strategy = orchestrator.ParseStrategy(s)
	}

	orch := buildOrchestrator(cfg, strategy, logger)
	registerAgents(orch, cfg, args, logger)

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	if err := orch.Start(ctx); err != nil {
		logger.Error("failed to start orchestrator", "error", err)
		os.Exit(1)
	}
	defer orch.Stop(ctx)

	// DAG 模式
	if dagFile, ok := args["dag"]; ok {
		runDAG(ctx, orch, dagFile, args, logger)
		return
	}

	prompt := args["_positional"]
	if prompt == "" {
		fmt.Fprintln(os.Stderr, "错误: 请提供任务描述")
		os.Exit(1)
	}

	result, err := orch.Run(ctx, prompt)
	if err != nil {
		logger.Error("task execution failed", "error", err)
		os.Exit(1)
	}

	printResult(result, args["json"] == "true")
	printCostFooter(orch)
}

// --- split 命令 ---

func cmdSplit() {
	args := parseArgs(os.Args[2:])
	logger := setupLogger(args["log-level"])
	cfg := loadConfig(args["config"])

	orch := buildOrchestrator(cfg, orchestrator.StrategyBestFit, logger)
	registerAgents(orch, cfg, args, logger)

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	if err := orch.Start(ctx); err != nil {
		logger.Error("failed to start orchestrator", "error", err)
		os.Exit(1)
	}
	defer orch.Stop(ctx)

	prompt := args["_positional"]
	if prompt == "" {
		fmt.Fprintln(os.Stderr, "错误: 请提供任务描述")
		os.Exit(1)
	}

	result, err := orch.RunSplit(ctx, prompt)
	if err != nil {
		logger.Error("split execution failed", "error", err)
		os.Exit(1)
	}

	printResult(result, args["json"] == "true")
	printCostFooter(orch)
}

// --- serve 命令 ---

func cmdServe() {
	args := parseArgs(os.Args[2:])
	logger := setupLogger(args["log-level"])
	cfg := loadConfig(args["config"])

	orch := buildOrchestrator(cfg, orchestrator.StrategyBestFit, logger)
	registerAgents(orch, cfg, args, logger)

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	if err := orch.Start(ctx); err != nil {
		logger.Error("failed to start orchestrator", "error", err)
		os.Exit(1)
	}
	defer orch.Stop(ctx)

	addr := args["addr"]
	if addr == "" {
		addr = ":8080"
	}

	srv := server.New(server.Config{
		Addr:        addr,
		Agents:      orch.AgentsMap(),
		TaskPool:    orch.TaskPool(),
		CostTracker: orch.CostTracker(),
		Breaker:     orch.Breaker(),
	}, logger)

	fmt.Printf("GoTems Web Dashboard: http://localhost%s\n", addr)

	if err := srv.Start(ctx, addr); err != nil && err.Error() != "http: Server closed" {
		logger.Error("server error", "error", err)
		os.Exit(1)
	}
}

// --- mcp 命令 ---

func cmdMCP() {
	args := parseArgs(os.Args[2:])
	logger := setupLogger("error") // MCP 模式下减少日志噪音
	cfg := loadConfig(args["config"])

	orch := buildOrchestrator(cfg, orchestrator.StrategyBestFit, logger)
	registerAgents(orch, cfg, nil, logger)

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	if err := orch.Start(ctx); err != nil {
		logger.Error("failed to start orchestrator", "error", err)
		os.Exit(1)
	}
	defer orch.Stop(ctx)

	bridge := mcp.NewBridge(orch.AgentsMap(), orch.CostTracker(), orch.TaskPool(), logger)
	stdioSrv := mcp.NewStdioServer(bridge, logger)

	if err := stdioSrv.Run(ctx); err != nil && ctx.Err() == nil {
		logger.Error("MCP server error", "error", err)
		os.Exit(1)
	}
}

// --- agents / cost 命令 ---

func cmdAgents() {
	logger := setupLogger("")
	cfg := loadConfig("")
	orch := buildOrchestrator(cfg, orchestrator.StrategyBestFit, logger)
	registerAgents(orch, cfg, nil, logger)

	infos := orch.Agents()
	data, _ := json.MarshalIndent(infos, "", "  ")
	fmt.Println(string(data))
}

func cmdCost() {
	fmt.Println("费用统计功能需要在运行任务后查看。")
	fmt.Println("提示: 使用 gotems serve 启动仪表盘可实时查看费用。")
}

// --- 公共函数 ---

func buildOrchestrator(cfg *config.Config, strategy orchestrator.Strategy, logger *slog.Logger) *orchestrator.Orchestrator {
	dailyMax := 50.0
	if cfg.Routing.CostLimit != nil {
		dailyMax = cfg.Routing.CostLimit.Daily
	}

	orch := orchestrator.New(orchestrator.OrchestratorConfig{
		Strategy:         strategy,
		CostLimits:       cost.Limits{DailyMax: dailyMax},
		BreakerThreshold: 5,
	}, logger)

	// 默认限流配置
	orch.ConfigureRateLimit(ratelimit.LimiterConfig{Provider: "claude", RPS: 10, Burst: 20})
	orch.ConfigureRateLimit(ratelimit.LimiterConfig{Provider: "gemini", RPS: 15, Burst: 30})
	orch.ConfigureRateLimit(ratelimit.LimiterConfig{Provider: "openai", RPS: 10, Burst: 20})
	orch.ConfigureRateLimit(ratelimit.LimiterConfig{Provider: "ollama", RPS: 100, Burst: 100})

	return orch
}

func registerAgents(orch *orchestrator.Orchestrator, cfg *config.Config, args map[string]string, logger *slog.Logger) {
	specified := ""
	if args != nil {
		specified = args["provider"]
	}

	if cfg.Providers.Claude != nil && (specified == "" || specified == "claude") {
		model := "claude-sonnet-4-6"
		if len(cfg.Providers.Claude.Models) > 0 {
			model = cfg.Providers.Claude.Models[0].ID
		}
		opts := []agent.ClaudeOption{
			agent.WithClaudeAPIKey(cfg.Providers.Claude.APIKey),
			agent.WithClaudeModel(model),
		}
		if cfg.Providers.Claude.CLI != nil && cfg.Providers.Claude.CLI.Enabled {
			opts = append(opts, agent.WithClaudeMode(agent.ClaudeModeCLI))
			if cfg.Providers.Claude.CLI.Path != "" {
				opts = append(opts, agent.WithClaudeCLIPath(cfg.Providers.Claude.CLI.Path))
			}
		}
		orch.RegisterAgent(agent.NewClaudeAgent("claude-1", logger, opts...))
	}

	if cfg.Providers.Gemini != nil && (specified == "" || specified == "gemini") {
		model := "gemini-2.5-pro"
		if len(cfg.Providers.Gemini.Models) > 0 {
			model = cfg.Providers.Gemini.Models[0].ID
		}
		geminiOpts := []agent.GeminiOption{
			agent.WithGeminiAPIKey(cfg.Providers.Gemini.APIKey),
			agent.WithGeminiModel(model),
		}
		if cfg.Providers.Gemini.CLI != nil && cfg.Providers.Gemini.CLI.Enabled {
			geminiOpts = append(geminiOpts, agent.WithGeminiMode(agent.GeminiModeCLI))
			if cfg.Providers.Gemini.CLI.Path != "" {
				geminiOpts = append(geminiOpts, agent.WithGeminiCLIPath(cfg.Providers.Gemini.CLI.Path))
			}
		}
		orch.RegisterAgent(agent.NewGeminiAgent("gemini-1", logger, geminiOpts...))
	}

	if cfg.Providers.OpenAI != nil && (specified == "" || specified == "openai") {
		model := "gpt-4o"
		if len(cfg.Providers.OpenAI.Models) > 0 {
			model = cfg.Providers.OpenAI.Models[0].ID
		}
		openaiOpts := []agent.OpenAIOption{
			agent.WithOpenAIAPIKey(cfg.Providers.OpenAI.APIKey),
			agent.WithOpenAIModel(model),
		}
		if cfg.Providers.OpenAI.CLI != nil && cfg.Providers.OpenAI.CLI.Enabled {
			openaiOpts = append(openaiOpts, agent.WithOpenAIMode(agent.OpenAIModeCLI))
			if cfg.Providers.OpenAI.CLI.Path != "" {
				openaiOpts = append(openaiOpts, agent.WithOpenAICLIPath(cfg.Providers.OpenAI.CLI.Path))
			}
		}
		orch.RegisterAgent(agent.NewOpenAIAgent("openai-1", logger, openaiOpts...))
	}

	if cfg.Providers.Ollama != nil && (specified == "" || specified == "ollama") {
		model := "qwen3:32b"
		if len(cfg.Providers.Ollama.Models) > 0 {
			model = cfg.Providers.Ollama.Models[0].ID
		}
		orch.RegisterAgent(agent.NewOllamaAgent("ollama-1", logger,
			agent.WithOllamaBaseURL(cfg.Providers.Ollama.BaseURL),
			agent.WithOllamaModel(model),
		))
	}
}

func runDAG(ctx context.Context, orch *orchestrator.Orchestrator, dagFile string, args map[string]string, logger *slog.Logger) {
	data, err := os.ReadFile(dagFile)
	if err != nil {
		logger.Error("failed to read DAG file", "path", dagFile, "error", err)
		os.Exit(1)
	}

	var tasks []*task.Task
	if err := json.Unmarshal(data, &tasks); err != nil {
		logger.Error("failed to parse DAG file", "error", err)
		os.Exit(1)
	}

	result, err := orch.RunWithTasks(ctx, tasks)
	if err != nil {
		logger.Error("DAG execution failed", "error", err)
		os.Exit(1)
	}

	printResult(result, args["json"] == "true")
	printCostFooter(orch)
}

func printResult(result *schema.FinalResult, jsonOutput bool) {
	if jsonOutput {
		data, _ := json.MarshalIndent(result, "", "  ")
		fmt.Println(string(data))
		return
	}

	fmt.Println("━━━ GoTems 执行结果 ━━━")
	fmt.Printf("策略: %s\n", result.Strategy)
	fmt.Printf("参与 Agent: %d 个\n", len(result.Results))
	fmt.Println()
	fmt.Println(result.Content)
	fmt.Println()
	fmt.Println("━━━ 统计 ━━━")
	fmt.Printf("Token 消耗: 输入 %d / 输出 %d\n", result.TotalTokensIn, result.TotalTokensOut)
	fmt.Printf("总费用: $%.4f\n", result.TotalCost)

	if len(result.Results) > 1 {
		fmt.Println()
		fmt.Println("━━━ 各 Agent 明细 ━━━")
		for _, r := range result.Results {
			fmt.Printf("  [%s] %s — tokens: %d+%d, $%.4f, %s\n",
				r.AgentID, r.Provider,
				r.TokensIn, r.TokensOut, r.Cost, r.Duration)
		}
	}
}

func printCostFooter(orch *orchestrator.Orchestrator) {
	summary := orch.CostSummary()
	if summary.RecordCount > 0 {
		fmt.Printf("\n━━━ 本次会话累计 ━━━\n")
		fmt.Printf("总请求: %d 次, 总费用: $%.4f\n", summary.RecordCount, summary.TotalCost)
	}
}

func loadConfig(path string) *config.Config {
	if path != "" {
		cfg, err := config.Load(path)
		if err != nil {
			fmt.Fprintf(os.Stderr, "警告: 加载配置失败 (%s), 使用默认配置\n", err)
			return config.Default()
		}
		return cfg
	}
	if cfg, err := config.Load("configs/gotems.yaml"); err == nil {
		return cfg
	}
	return config.Default()
}

func setupLogger(level string) *slog.Logger {
	var logLevel slog.Level
	switch level {
	case "debug":
		logLevel = slog.LevelDebug
	case "warn":
		logLevel = slog.LevelWarn
	case "error":
		logLevel = slog.LevelError
	default:
		logLevel = slog.LevelInfo
	}
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: logLevel}))
}

func parseArgs(args []string) map[string]string {
	result := make(map[string]string)
	var positional []string

	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch {
		case arg == "--json":
			result["json"] = "true"
		case strings.HasPrefix(arg, "--"):
			key := strings.TrimPrefix(arg, "--")
			if i+1 < len(args) && !strings.HasPrefix(args[i+1], "--") {
				result[key] = args[i+1]
				i++
			} else {
				result[key] = "true"
			}
		case strings.HasPrefix(arg, "-") && len(arg) == 2:
			shortMap := map[string]string{"c": "config", "s": "strategy", "p": "provider", "m": "model"}
			if full, ok := shortMap[arg[1:]]; ok {
				if i+1 < len(args) {
					result[full] = args[i+1]
					i++
				}
			}
		default:
			positional = append(positional, arg)
		}
	}

	result["_positional"] = strings.Join(positional, " ")
	return result
}
