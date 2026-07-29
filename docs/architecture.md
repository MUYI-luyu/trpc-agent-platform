# trpc-agent-platform 架构设计

## 一、整体架构

```
┌─────────────────────────────────────────────────────────────┐
│                        外部入口                              │
│  企业微信 Webhook  │  微信公众号  │  HTTP API  │  gRPC API  │
└─────────┬──────────────────┬─────────────┬──────────┬───────┘
          │                  │             │          │
          ▼                  ▼             ▼          ▼
┌─────────────────────────────────────────────────────────────┐
│                     Gateway (无状态)                         │
│  ┌───────────┐  ┌───────────────┐  ┌─────────────────────┐  │
│  │ IM 验签    │  │ 租户识别       │  │ 请求路由 → Worker   │  │
│  │ 消息解密   │  │ ctx 注入       │  │ 限流/认证           │  │
│  └───────────┘  └───────────────┘  └─────────────────────┘  │
└─────────────────────────┬───────────────────────────────────┘
                          │
          ┌───────────────┼───────────────┐
          ▼               ▼               ▼
┌─────────────────────────────────────────────────────────────┐
│                   Worker Pool (无状态)                        │
│  ┌───────────────────────────────────────────────────────┐  │
│  │                 Tenant Plugin                          │  │
│  │  BeforeModel  │  BeforeTool  │  AfterTool  │  OnEvent  │  │
│  │  (注入租户提示) │  (工具白名单)  │  (审计记录)  │  (脱敏)   │  │
│  └───────────────────────────────────────────────────────┘  │
│  ┌───────────────────────────────────────────────────────┐  │
│  │              tRPC-Agent-Go Runner (未修改)              │  │
│  │  Runner.Run() → Agent.Run() → Tool.Call() → Session   │  │
│  └───────────────────────────────────────────────────────┘  │
└─────────────────────────┬───────────────────────────────────┘
                          │
          ┌───────────────┼───────────────┐
          ▼               ▼               ▼
┌─────────────────────────────────────────────────────────────┐
│                    数据层（共享后端）                          │
│  ┌──────────┐  ┌──────────┐  ┌──────────┐  ┌────────────┐  │
│  │ Session   │  │ Memory   │  │ Audit    │  │ Tenant     │  │
│  │ Backend   │  │ Backend   │  │ Log DB   │  │ Config DB  │  │
│  │(Redis/SQL)│  │(Redis/SQL)│  │(Postgres)│  │(Postgres)  │  │
│  └──────────┘  └──────────┘  └──────────┘  └────────────┘  │
└─────────────────────────────────────────────────────────────┘
```

## 二、租户身份传播路径

```
外部请求到达
  │
  ▼
Gateway: 识别租户 (API key / webhook URL / JWT claim)
  │
  ├─ ctx = tenant.WithTenantID(ctx, "tenant_001")
  ├─ 获取 Tenant 配置: mgr.Get(ctx, "tenant_001")
  ├─ 解析后端: router.Resolve("tenant_001")
  │
  ▼
Worker:
  │
  ├─ BeforeModel Hook: 注入租户 system prompt
  ├─ BeforeTool Hook: 检查工具白名单
  │   └─ scopedApp = tenant.BuildTenantAppName("tenant_001", "myapp")
  │       → "tenant_001|myapp"
  │
  ├─ session.GetSession(ctx, Key{AppName: "tenant_001|myapp", ...})
  ├─ memory.SearchMemories(ctx, UserKey{AppName: "tenant_001|myapp", ...})
  │
  ├─ AfterTool Hook: 写入审计日志
  ├─ OnEvent Hook: 脱敏 event 内容
  │
  ▼
IM Adapter: 封装回复 → 发送到 IM 通道
```

## 三、核心设计决策

### 决策 1: AppName 前缀编码 vs 修改 session.Key

| 方案 | 优点 | 缺点 |
|------|------|------|
| **AppName 前缀编码** ✅ | 零接口变更，完全兼容上游 | 解析时需要 split |
| 修改 session.Key | 语义清晰 | 破坏所有 7 种后端实现，需 fork |

**选择**: AppName 前缀编码。格式 `tenant_{id}|{appname}`，分隔符 `|` 避免与 AppName 中常见的 `:` 冲突。

### 决策 2: Wrapper vs Plugin 模式

| 场景 | 使用模式 |
|------|---------|
| Session/Memory 数据隔离 | Wrapper — 在调用前替换 AppName |
| 工具白名单/预算限制 | Plugin Hook — BeforeTool/AfterTool |
| 模型参数注入 | Plugin Hook — BeforeModel |
| 审计日志记录 | Plugin Hook — AfterTool + OnEvent |
| IM 消息转换 | Adapter — 独立转换层 |

### 决策 3: 每个租户独立的 DataBackend vs 共享后端 + tenant_id 列

| 方案 | 优点 | 缺点 |
|------|------|------|
| **独立 DataBackend** ✅ | 物理隔离，无数据泄露风险；可混合后端类型 | 连接数多 |
| 共享后端 + tenant_id 列 | 连接数少 | 需改造 SQL schema；跨租户查询风险 |

**选择**: 独立 DataBackend。租户 A 可用 Redis，租户 B 可用 Postgres，灵活且安全。连接池按需创建。

## 四、DataBackend 抽象

```go
type DataBackend interface {
    SessionService() session.Service  // 租户级 session 后端
    MemoryService()  memory.Service   // 租户级 memory 后端
    HealthCheck() error
    Close() error
}
```

`BackendRouter` 负责 `tenantID → DataBackend` 的路由映射。`Factory` 根据 `DataBackendConfig` 创建后端实例。

## 五、已实现 / 待实现

| 组件 | 状态 | 计划 |
|------|------|------|
| Tenant 数据模型 | ✅ 已实现 | v0.1 |
| Context 透传 | ✅ 已实现 | v0.1 |
| InMemoryTenantManager | ✅ 已实现 | v0.1 |
| DataBackend + Router + Factory | ✅ 已实现 | v0.1 |
| TenantSessionService wrapper | 第 2 期 | v0.2 |
| TenantMemoryService wrapper | 第 2 期 | v0.2 |
| SQLite/Redis/Postgres 后端 | 第 2 期 | v0.2 |
| 企业微信 Channel Adapter | 第 3 周 | v0.3 |
| 治理 Filter (5 个) | 第 4 周 | v0.4 |
| OTel Tracing + Metrics | 第 4 周 | v0.4 |
| 审计日志 | 第 4 周 | v0.4 |
| 故障恢复 + 部署方案 | 第 5 周 | v0.5 |
