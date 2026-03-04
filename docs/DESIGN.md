# GoTems — Go 多智能体编排框架设计方案

> **GoTems** = **Go** + **Teams** — 用 Go 构建的跨厂商 AI 编码智能体协作框架

---

## 一、项目定位

一个用 **纯 Go** 编写的多 AI 编码智能体编排框架，能够统一控制 **Claude Code、Gemini CLI、OpenAI Codex** 等编码助手，让它们像一个团队一样协作完成复杂软件工程任务。

### 为什么需要 GoTems？

| 痛点 | GoTems 方案 |
|------|------------|
| 各 AI 编码助手各自为战，无法协作 | 统一调度，任务分发，结果聚合 |
| 不同模型擅长不同任务，无法取长补短 | 智能路由，按模型特长分配任务 |
| 现有多智能体框架全是 Python，性能差、部署重 | Go 单二进制，高并发，低内存 |
| 缺乏跨厂商 Agent 通信标准 | 桥接 MCP + A2A，统一通信层 |

---

## 二、Go 语言优势分析

### 2.1 并发模型对比

| 维度 | Python (AutoGen/CrewAI) | Go (GoTems) |
|------|------------------------|-------------|
| **并发模型** | asyncio 单线程 + GIL | goroutine M:N 多核调度 |
| **单协程内存** | ~8MB (线程) | ~2KB (goroutine) |
| **万级并发** | 吃力，需 uvloop 优化 | 原生支持，毫无压力 |
| **部署产物** | venv + pip + 几百MB 镜像 | 单二进制文件 ~15MB |
| **类型安全** | 运行时才爆炸 | 编译期拦截 |
| **启动速度** | 秒级 | 毫秒级 |
| **交叉编译** | 痛苦 | `GOOS=linux go build` 一行搞定 |

### 2.2 关键并发模式

| 模式 | 应用场景 |
|------|---------|
| **Fan-out / Fan-in** | 同一任务发给 Claude + Gemini + GPT，收集结果后择优 |
| **Pipeline** | 意图识别 → 代码生成 → Code Review → 测试生成 |
| **Worker Pool** | 限制 API 并发调用数，防止触发限流 |
| **errgroup** | 并行执行多个 Agent 任务，任一失败立即取消其余 |
| **context.Context** | 统一超时控制、优雅取消级联 |
| **channel** | 进程内 Agent 间消息传递，类型安全 |

---

## 三、系统架构

```
┌─────────────────────────────────────────────────────────────────────┐
│                        GoTems CLI / Web UI                          │
│                     (用户交互层 - 任务输入/状态展示)                    │
├─────────────────────────────────────────────────────────────────────┤
│                                                                     │
│  ┌───────────────────────────────────────────────────────────────┐  │
│  │                    Orchestrator (编排引擎)                      │  │
│  │  ┌──────────┐  ┌──────────┐  ┌──────────┐  ┌──────────────┐  │  │
│  │  │ 任务拆分器 │  │ 智能路由器 │  │ 状态机/DAG│  │ 结果聚合/裁判 │  │  │
│  │  │ Splitter │  │  Router  │  │ Executor │  │  Aggregator  │  │  │
│  │  └──────────┘  └──────────┘  └──────────┘  └──────────────┘  │  │
│  └──────────────────────────┬────────────────────────────────────┘  │
│                             │                                       │
│  ┌──────────────────────────▼────────────────────────────────────┐  │
│  │                  Communication Bus (通信总线)                   │  │
│  │          MCP Protocol  |  Go Channels  |  Mailbox             │  │
│  └────┬──────────────┬────────────────┬──────────────┬──────────┘  │
│       │              │                │              │              │
│  ┌────▼────┐   ┌─────▼─────┐   ┌─────▼─────┐  ┌────▼──────────┐  │
│  │ Claude  │   │  Gemini   │   │  OpenAI   │  │  Local LLM   │  │
│  │ Adapter │   │  Adapter  │   │  Adapter  │  │  Adapter     │  │
│  │(双模式) │   │ (双模式)   │   │ (双模式)   │  │  (Ollama)    │  │
│  │┌───────┐│   │┌─────────┐│   │┌─────────┐│  │┌────────────┐│  │
│  ││ API   ││   ││  API    ││   ││  API    ││  ││ Ollama API ││  │
│  ││ CLI   ││   ││  CLI    ││   ││  CLI    ││  ││            ││  │
│  │└───────┘│   │└─────────┘│   │└─────────┘│  │└────────────┘│  │
│  └─────────┘   └───────────┘   └───────────┘  └──────────────┘  │
│                                                                     │
│  ┌───────────────────────────────────────────────────────────────┐  │
│  │                 Process & Workspace Layer                      │  │
│  │  ┌──────────────┐  ┌───────────────┐  ┌──────────────────┐   │  │
│  │  │ Process Mgr  │  │ Workspace Mgr │  │ Session Store   │   │  │
│  │  │ 子进程生命周期 │  │ git worktree  │  │ 会话持久化       │   │  │
│  │  │ 流式输出解析   │  │ 文件变更监控    │  │ CLI SessionID   │   │  │
│  │  └──────────────┘  └───────────────┘  └──────────────────┘   │  │
│  └───────────────────────────────────────────────────────────────┘  │
│                                                                     │
│  ┌───────────────────────────────────────────────────────────────┐  │
│  │                    Shared Infrastructure                       │  │
│  │  ┌────────┐  ┌──────────┐  ┌─────────┐  ┌─────────────────┐  │  │
│  │  │TaskPool│  │ Mailbox  │  │FileLocker│  │ Cost Tracker   │  │  │
│  │  │任务池   │  │ 邮箱系统  │  │ 文件锁   │  │ 成本追踪       │  │  │
│  │  └────────┘  └──────────┘  └─────────┘  └─────────────────┘  │  │
│  │  ┌──────────────────┐  ┌──────────────────────────────────┐   │  │
│  │  │ Rate Limiter     │  │ Circuit Breaker                 │   │  │
│  │  │ 令牌桶限流        │  │ 断路器熔断                       │   │  │
│  │  └──────────────────┘  └──────────────────────────────────┘   │  │
│  └───────────────────────────────────────────────────────────────┘  │
│                                                                     │
│  ┌───────────────────────────────────────────────────────────────┐  │
│  │                    External Interfaces                         │  │
│  │  ┌──────────┐  ┌────────────────┐  ┌─────────────────────┐   │  │
│  │  │ MCP 服务器 │  │ Web Dashboard  │  │ Plugin System      │   │  │
│  │  │ stdio    │  │ HTTP + SSE     │  │ CodeReview/TestGen  │   │  │
│  │  └──────────┘  └────────────────┘  └─────────────────────┘   │  │
│  └───────────────────────────────────────────────────────────────┘  │
└─────────────────────────────────────────────────────────────────────┘
```

---

## 四、核心工作流程

### 4.1 模型竞赛模式 (Consensus)

```
用户输入: "重构这个函数，提升性能"
        │
        ▼
   ┌────────────┐
   │ Orchestrator│
   └─────┬──────┘
         │ Fan-out (并行发送)
    ┌────┼────────────┐
    ▼    ▼            ▼
 Claude  Gemini     GPT
 重构方案A 重构方案B   重构方案C
    │    │            │
    └────┼────────────┘
         │ Fan-in (收集结果)
         ▼
   ┌────────────┐
   │ Aggregator │ ← 用另一个模型做裁判
   │ 评分+择优    │
   └─────┬──────┘
         ▼
   最优重构方案 → 用户
```

### 4.2 流水线模式 (Pipeline / DAG)

```
用户输入: "给这个项目添加用户认证功能"
        │
        ▼
   [Stage 1: 架构设计]  ← Claude (深度推理)
        │
        ▼
   [Stage 2: 代码生成]  ← Gemini Pro (大上下文+快速)
        │
        ▼
   [Stage 3: Code Review] ← Claude (逻辑审查)
        │
        ▼
   [Stage 4: 测试生成]   ← GPT-4o (测试生成)
        │
        ▼
   [Stage 5: 安全审计]   ← Claude (安全分析)
        │
        ▼
   最终交付 → 用户
```

### 4.3 分治协作模式 (Divide & Conquer + git worktree)

```
用户输入: "实现一个完整的博客系统"
        │
        ▼
   Orchestrator (AI 自动拆分任务)
        │
   ┌────┼────────────┬──────────────┐
   ▼    ▼            ▼              ▼
Claude  Gemini      GPT           Claude
后端API  前端页面   数据库Schema    测试用例
(worktree-1) (worktree-2) (worktree-3) (worktree-4)
   │    │            │              │
   │    ├──Question──┤              │
   │    │ "API格式？" │              │  ← Mailbox 跨 Agent 通信
   │    ◄──Answer────┤              │
   │    │            │              │
   └────┼────────────┼──────────────┘
        ▼
   Workspace Merger (git merge 合并各分支)
        ▼
   Conflict Detector (冲突检测)
        ▼
   完整博客系统 → 用户
```

---

## 五、Agent 适配器：API + CLI 双模式

### 5.1 支持矩阵

| Provider | API 模式 | CLI 模式 | CLI 工具 | 会话保持 | 流式输出 | 自动审批 |
|----------|:-------:|:-------:|---------|:-------:|:-------:|:-------:|
| **Claude** | Anthropic Messages API | claude CLI | `claude` | `--session-id` | `stream-json` | `--dangerously-skip-permissions` |
| **Gemini** | Google AI generateContent | gemini CLI | `gemini` | - | 文本流 | `-y` |
| **OpenAI** | Chat Completions API | codex CLI | `codex` | - | 文本流 | `--approval-mode full-auto` |
| **Ollama** | Ollama generate API | - | - | - | - | - |

### 5.2 CLI 子进程管理

```
┌─────────────────────────────────────────────┐
│           Process Manager                     │
│                                               │
│  ┌──────────────┐   ┌──────────────────────┐ │
│  │   Process     │   │   Stream Parser      │ │
│  │ ┌──────────┐  │   │ ┌──────────────────┐ │ │
│  │ │ stdin    │  │   │ │ FormatJSONLines  │ │ │ ← Claude Code
│  │ │ stdout   │──┼──►│ │ FormatStreamJSON │ │ │ ← Gemini CLI
│  │ │ stderr   │  │   │ │ FormatPlainText  │ │ │ ← Codex CLI
│  │ └──────────┘  │   │ └──────────────────┘ │ │
│  │ Start/Stop    │   │ Parse → StreamChunk  │ │
│  │ Wait/PID      │   │ (type/content/usage) │ │
│  └──────────────┘   └──────────────────────┘ │
└─────────────────────────────────────────────┘
```

### 5.3 会话持久化

```
Agent 首次执行:
  claude -p "prompt" --output-format json
  → 返回 { session_id: "abc-123", result: "..." }
  → SessionStore.UpdateSessionID("claude-1", ".", "abc-123")

Agent 后续执行:
  → SessionStore.Get("claude-1", ".")  → session_id = "abc-123"
  claude -p "继续上次的任务" --session-id abc-123
  → Claude 自动恢复上下文
```

---

## 六、与现有框架对比

| 特性 | AutoGen (Python) | CrewAI (Python) | LangGraph (Python) | **GoTems (Go)** |
|------|:-:|:-:|:-:|:-:|
| **跨厂商 Agent** | 部分 | 部分 | 部分 | **原生 4 厂商** |
| **CLI 工具控制** | 无 | 无 | 无 | **claude/gemini/codex** |
| **Agent 间通信** | 对话式 | 角色式 | 图节点 | **邮箱+任务池+广播** |
| **工作空间隔离** | 无 | 无 | 无 | **git worktree** |
| **会话保持** | 无 | 无 | 无 | **session-id 持久化** |
| **并发性能** | asyncio | asyncio | asyncio | **goroutine 真并行** |
| **部署复杂度** | pip + venv | pip + venv | pip + venv | **单二进制** |
| **内存效率** | 高 | 高 | 高 | **极低 (2KB/协程)** |
| **类型安全** | 运行时 | 运行时 | 运行时 | **编译时** |
| **MCP 集成** | 无 | 无 | 无 | **内置 MCP 桥接** |
| **限流 / 熔断** | 无 | 无 | 无 | **令牌桶 + 断路器** |
| **成本感知调度** | 无 | 无 | 无 | **内置 Token 追踪** |
| **Web 仪表盘** | 无 | 无 | LangSmith | **内置 SSE 实时推送** |

---

## 七、技术选型

| 层级 | 选型 | 理由 |
|------|------|------|
| **HTTP Server** | `net/http` | 标准库，零依赖 |
| **日志** | `log/slog` | 标准库结构化日志 |
| **限流** | `golang.org/x/time/rate` | 官方令牌桶实现 |
| **并发** | `golang.org/x/sync/errgroup` | 官方并发控制 |
| **配置** | `gopkg.in/yaml.v3` | 轻量 YAML 解析 |
| **流式解析** | 自研 `process.StreamParser` | 支持 JSON Lines / Stream JSON / PlainText |
| **工作空间** | `git worktree` (exec) | 原生 git 隔离，无需额外依赖 |
| **会话存储** | 自研 `session.Store` (JSON) | 文件系统持久化，零外部依赖 |

---

## 八、模块依赖关系

```
cmd/gotems/main.go
  ├── internal/orchestrator  (编排引擎)
  │     ├── Guard            (执行守卫：限流+熔断+成本+Metrics+Tracer)
  │     ├── DAGExecutor      (DAG 执行：Guard+上下文传递+Mailbox广播)
  │     ├── Aggregator       (结果聚合：GuardedParallelExecute 容错模式)
  │     ├── internal/agent   (Agent 适配器，CLI 通过 Process Manager 执行)
  │     │     ├── internal/process   (子进程管理：Manager + Process 生命周期)
  │     │     └── internal/session   (会话持久化)
  │     ├── internal/comm    (通信层：Mailbox + Negotiator 协商协议)
  │     ├── internal/task    (任务管理)
  │     ├── internal/cost    (成本追踪+定价表+动态远程更新)
  │     ├── internal/ratelimit (限流/熔断)
  │     ├── internal/observability (Metrics + Tracer：Prometheus + OpenTelemetry)
  │     ├── internal/workspace (git worktree 隔离)
  │     └── internal/splitter (任务拆分)
  ├── internal/mcp           (MCP 协议桥接)
  ├── internal/server        (Web 仪表盘)
  ├── internal/config        (配置管理)
  └── internal/plugin        (插件系统)
```

---

## 九、实施路线图与完成状态

### Phase 1 — 基础骨架 ✅
- [x] 项目初始化、目录结构、Makefile
- [x] Agent 接口定义 (`internal/agent/agent.go`)
- [x] Claude Adapter（API + CLI 双模式）
- [x] Gemini Adapter（API + CLI 双模式）
- [x] 简单编排器（串行执行）
- [x] CLI 入口

### Phase 2 — 通信与路由 ✅
- [x] 邮箱系统 (`internal/comm/mailbox.go`)
- [x] 共享任务池 (`internal/task/task.go`)
- [x] 智能路由器（5 种策略）
- [x] DAG 执行引擎（Kahn 拓扑排序 + errgroup 并行）
- [x] OpenAI Adapter（API + CLI 双模式）
- [x] 成本追踪

### Phase 3 — 高级特性 ✅
- [x] MCP 桥接（JSON-RPC 2.0 + stdio）
- [x] 插件系统
- [x] 模型竞赛模式
- [x] 文件锁 / 冲突检测
- [x] 限流 / 熔断
- [x] Ollama 支持

### Phase 4 — 生产化 ✅
- [x] Web UI 仪表盘（HTTP API + SSE 实时推送）
- [x] Docker 多阶段构建
- [x] CI/CD（GitHub Actions）
- [x] 文档 + 示例

### Phase 5 — 真正的 CLI 编排 (v0.3.0) ✅
- [x] 子进程生命周期管理 (`internal/process/`)
- [x] 流式输出解析器（JSON Lines / Stream JSON / PlainText）
- [x] Claude CLI 完整支持（session-id / 自动审批 / 流式输出）
- [x] Gemini CLI 支持
- [x] OpenAI Codex CLI 支持
- [x] git worktree 工作空间隔离 (`internal/workspace/`)
- [x] 文件变更监控 + 冲突检测
- [x] 会话持久化 (`internal/session/`)
- [x] 修复模型 ID（gpt-4.1 → gpt-4o）

### Phase 6 — 深度集成 (v0.4.0) ✅
- [x] Guard 中间件：统一封装限流→熔断→预算→执行→成本记录的完整链路
- [x] Guard 覆盖所有执行路径：runSingle / DAG / Consensus 均通过 Guard 执行
- [x] Mailbox 通信接入 DAG：节点完成后通过 Mailbox 广播 `MsgTaskResult`
- [x] DAG 节点间上下文传递：前序任务结果自动注入后续任务 Prompt + Metadata
- [x] Workspace Manager 接入 Orchestrator：DAG 模式自动创建/合并/清理 worktree
- [x] 成本自动计算：内置 9 个模型定价表，根据 model + tokens 自动计算费用
- [x] BaseAgent.Send 防死锁：`select` + `ctx.Done()` 替代阻塞 channel 写入
- [x] Result 增加 Model 字段：支持定价查表
- [x] GuardedParallelExecute：竞赛模式并行执行也经过 Guard 守卫

### Phase 7 — 生产打磨 (v0.5.0) ✅
- [x] Agent 间基于 session 的多轮协商协议（Question/Answer）— `internal/comm/negotiator.go`
- [x] Process Manager 接入 Agent CLI 执行路径 — claude/openai/gemini 均通过 `procManager.Create()` 统一管理
- [x] 端到端集成测试（mock CLI 脚本验证完整执行路径）— `internal/agent/agent_integration_test.go` (7 tests)
- [x] OpenTelemetry 链路追踪接入 — `internal/observability/observability.go` Tracer
- [x] Prometheus 指标导出 — `internal/observability/observability.go` Metrics + PrometheusText()
- [x] 动态定价表更新（从远程 API 拉取最新价格）— `cost.Tracker.FetchPricing()`
- [x] Guard 集成 Metrics + Tracer：每次执行自动记录指标和链路追踪
- [x] GuardedParallelExecute 容错改造：独立 goroutine 替代 errgroup，单 Agent 失败不影响其他
- [x] 版本号更新至 v0.5.0

### Phase 8 — 未来规划
- [ ] Web UI 实时 Metrics 仪表盘（Prometheus + Grafana）
- [ ] OTLP 远程 Trace 导出（替换 stdout exporter）
- [ ] Agent 热重载（运行时动态增删 Agent）
- [ ] 分布式多节点编排（跨机器 Agent 协作）
- [ ] 自然语言任务拆分（LLM 驱动的 DAG 生成）

---

## 十、测试覆盖

| 模块 | 测试文件 | 测试用例数 |
|------|---------|:---------:|
| agent | `agent_integration_test.go` | 7 |
| comm | `mailbox_test.go`, `negotiator_test.go` | 7 |
| cost | `tracker_test.go`, `pricing_test.go` | 10 |
| mcp | `bridge_test.go`, `stdio_test.go` | 8 |
| observability | `observability_test.go` | 4 |
| orchestrator | `router_test.go`, `guard_test.go`, `dag_test.go` | 12 |
| process | `stream_test.go` | 7 |
| ratelimit | `ratelimit_test.go` | 5 |
| session | `session_test.go` | 8 |
| splitter | `splitter_test.go` | 5 |
| task | `task_test.go`, `filelock_test.go` | 8 |
| **总计** | | **81** |
