# GoTems

**Go 多智能体编排框架** — 用 Go 构建的跨厂商 AI 编码智能体协作框架

GoTems 统一控制 Claude Code、Gemini、OpenAI、Ollama 等 AI 编码助手，让它们像一个团队一样协作。

## 特性

- **跨厂商支持** — Claude / Gemini / OpenAI / Ollama 四大 Provider
- **智能路由** — 按任务类型自动匹配最佳模型
- **多种执行模式** — 单 Agent、竞赛模式(Consensus)、DAG 流水线
- **Agent 间通信** — 邮箱系统 + 任务池 + 广播
- **成本感知** — 实时追踪 Token 消耗和费用，支持每日限额
- **文件锁** — 多 Agent 并行修改时自动检测冲突
- **插件系统** — 可扩展的插件架构
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

# JSON 输出
./bin/gotems run --json "写一个函数"

# 查看已注册 Agent
./bin/gotems agents
```

## 项目结构

```
gotems/
├── cmd/gotems/          # CLI 入口
├── internal/
│   ├── agent/           # Agent 接口与各厂商适配器
│   ├── orchestrator/    # 编排引擎（路由/DAG/聚合）
│   ├── comm/            # 通信层（邮箱/总线）
│   ├── task/            # 任务管理与文件锁
│   ├── cost/            # 费用追踪
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
| `consensus` | 所有 Agent 并行执行，择优返回 |
| `round_robin` | 轮询分配 |

## 开发

```bash
make test     # 运行测试
make fmt      # 格式化代码
make lint     # 代码检查
make build    # 构建
```

## 设计文档

详见 [docs/DESIGN.md](docs/DESIGN.md)
