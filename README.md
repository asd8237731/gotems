# GoTems

**Go 多智能体编排框架** — 用 Go 构建的跨厂商 AI 编码智能体协作框架

GoTems 统一控制 Claude Code、Gemini CLI、OpenAI Codex、Ollama 等 AI 编码助手，让它们像一个团队一样协作。

## 特性

- **跨厂商支持** — Claude / Gemini / OpenAI / Ollama 四大 Provider
- **API + CLI 双模式** — 每个 Agent 支持 REST API 和 CLI 子进程两种运行模式
- **真正的 CLI 控制** — 通过子进程管理 claude/gemini/codex CLI，支持会话保持、流式输出、自动审批
- **智能路由** — 按任务类型自动匹配最佳模型（5 种策略）
- **多种执行模式** — 单 Agent、竞赛模式(Consensus)、DAG 流水线、自动拆分
- **Agent 间通信** — 邮箱系统 + 任务池 + 广播
- **工作空间隔离** — 基于 git worktree 的多 Agent 并行文件编辑
- **会话持久化** — 支持 CLI 会话 ID 跨调用保持上下文
- **成本感知** — 实时追踪 Token 消耗和费用，支持每日限额
- **限流 + 熔断** — 令牌桶限流 + 断路器保护
- **文件冲突检测** — 多 Agent 修改同一文件时自动告警
- **MCP 协议** — 内置 MCP 服务器，可被 Claude Code 等工具调用
- **Web 仪表盘** — HTTP API + SSE 实时推送
- **单二进制部署** — Go 编译，零依赖

## 快速开始

```bash
# 构建
make build

# 设置 API Key（至少一个）
export ANTHROPIC_API_KEY="your-key"
export GOOGLE_API_KEY="your-key"
export OPENAI_API_KEY="your-key"

# 单 Agent 执行
./bin/gotems run "用 Go 写一个 HTTP 服务器"

# 竞赛模式（多模型并行，择优）
./bin/gotems run --strategy consensus "实现快速排序算法"

# 指定 Provider
./bin/gotems run --provider claude "审查这段代码"

# DAG 模式（多任务有依赖关系）
./bin/gotems run --dag examples/dag-blog.json

# 自动拆分大任务
./bin/gotems split "实现一个完整的博客系统"

# 启动 Web 仪表盘
./bin/gotems serve --addr :8080

# 启动 MCP 服务器
./bin/gotems mcp

# JSON 输出
./bin/gotems run --json "写一个函数"

# 查看已注册 Agent
./bin/gotems agents
```

## CLI 模式配置

在 `configs/gotems.yaml` 中启用 CLI 模式：

```yaml
providers:
  claude:
    cli:
      enabled: true   # 启用 Claude Code CLI 子进程模式
      path: claude     # CLI 路径（默认 claude）
  gemini:
    cli:
      enabled: true   # 启用 Gemini CLI 子进程模式
      path: gemini
  openai:
    cli:
      enabled: true   # 启用 Codex CLI 子进程模式
      path: codex
```

CLI 模式特性：
- **Claude Code**: `--session-id` 多轮会话、`--dangerously-skip-permissions` 自动审批、`--output-format stream-json` 流式输出
- **Gemini CLI**: `-p` 非交互模式、自动审批
- **Codex CLI**: `-p` 非交互模式、`--approval-mode full-auto` 自动审批

## 项目结构

```
gotems/
├── cmd/gotems/          # CLI 入口
├── internal/
│   ├── agent/           # Agent 接口与各厂商适配器（API + CLI 双模式）
│   ├── orchestrator/    # 编排引擎（路由/DAG/聚合/竞赛）
│   ├── process/         # 子进程生命周期管理 + 流式输出解析
│   ├── workspace/       # git worktree 工作空间隔离 + 文件变更监控
│   ├── session/         # 会话持久化存储
│   ├── comm/            # 通信层（邮箱/总线）
│   ├── task/            # 任务管理与文件锁
│   ├── cost/            # 费用追踪
│   ├── ratelimit/       # 限流 + 熔断
│   ├── splitter/        # AI 任务拆分器
│   ├── mcp/             # MCP 协议桥接
│   ├── server/          # Web 仪表盘（HTTP + SSE）
│   ├── config/          # 配置管理
│   └── plugin/          # 插件系统
├── pkg/schema/          # 公共消息类型
├── configs/             # 默认配置文件
├── examples/            # 示例
└── docs/                # 设计文档
```

## 配置

编辑 `configs/gotems.yaml` 或通过环境变量配置：

| 环境变量 | 说明 |
|---------|------|
| `ANTHROPIC_API_KEY` | Claude API 密钥 |
| `GOOGLE_API_KEY` | Gemini API 密钥 |
| `OPENAI_API_KEY` | OpenAI API 密钥 |

## 路由策略

| 策略 | 说明 |
|------|------|
| `best_fit` | 按任务标签匹配最擅长的 Agent（默认） |
| `cost_first` | 选择最便宜的 Agent |
| `speed_first` | 选择最快的 Agent |
| `consensus` | 所有 Agent 并行执行，择优返回 |
| `round_robin` | 轮询分配 |

## 开发

```bash
make test     # 运行测试
make fmt      # 格式化代码
make lint     # 代码检查
make build    # 构建
make serve    # 启动 Web 仪表盘
make mcp      # 启动 MCP 服务器
```

## 设计文档

详见 [docs/DESIGN.md](docs/DESIGN.md)
