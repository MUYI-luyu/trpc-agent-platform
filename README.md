# Research Agent

基于 [trpc-agent-go](https://github.com/trpc-group/trpc-agent-go) 的多节点研究 Agent：输入一个技术问题，先澄清意图，再执行多轮联网调研，最后生成结构化研究报告。

## 架构

3-Node GraphAgent 流水线：

```
Entry → Clarify ──reject──→ END
              ├──answer──→ END
              └──research→ Investigate → Synthesize → END
```

| 节点 | 职责 |
|---|---|
| **Clarify** | 置信度分类：拒答（超出领域）／直接答（简单问题）／进入研究（需调研） |
| **Investigate** | 多轮 ReAct 循环，调用 `web_search` / `web_fetch` 工具收集证据 |
| **Synthesize** | 聚合 findings，生成带引用的结构化报告 |

三个节点之间通过 LLM 结构化输出做条件路由；会话级锁（`LockManager`）保证同一 session 的串行执行。

## 快速开始

```bash
# 无 LLM（启发式分类，仅演示图路由）
go run ./cmd/research-demo/

# 有 LLM（真实调研，DeepSeek + Tavily 搜索）
export DEEPSEEK_API_KEY="sk-..."
export TAVILY_API_KEY="tvly-..."
export SEARCH_BACKEND=tavily
go run ./cmd/research-demo/
```

启动后提交查询：

```bash
curl -X POST http://localhost:8080/research \
  -H "Content-Type: application/json" \
  -d '{"query":"Raft和Paxos的区别"}'
```

响应为 SSE 流式（`GET /health` 健康检查）。

## 目录结构

```
internal/research/
├── graph/     # 图构建、条件路由、会话锁
├── nodes/     # Clarify / Investigate / Synthesize 三节点
├── infra/     # 搜索工具、SSRF 防护、流式写出、后处理校验
├── types/     # 状态、配置、Prompt、Stream 定义
└── service.go # 顶层 API（Service）
internal/telemetry/  # OpenTelemetry 埋点（span / token 用量）
```

## 环境变量

| 变量 | 说明 |
|---|---|
| `DEEPSEEK_API_KEY` | LLM API key（不设则走启发式模式） |
| `DEEPSEEK_BASE_URL` | API 地址，默认 `https://api.deepseek.com/v1` |
| `DEEPSEEK_MODEL` | 模型名，默认 `deepseek-v4-flash` |
| `SEARCH_BACKEND` | `tavily` / `bing` / `searxng` / `builtin` / 空（网页抓取降级） |
| `TAVILY_API_KEY` / `BING_API_KEY` | 对应搜索后端的 key |
| `PORT` | HTTP 端口，默认 `8080` |

## 测试

```bash
go test ./...
```
