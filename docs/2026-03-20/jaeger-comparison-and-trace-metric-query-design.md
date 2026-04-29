# Jaeger 对比分析 & Trace/Metric 数据查询设计

> **创建日期**: 2026-03-20
> **状态**: Sprint 1~3 已完成，UI 增强优化已完成
> **目标**: 分析 Jaeger 的 Trace 查询前后端实现，与当前项目对比，提出优化设计方案

---

## 一、Jaeger 架构分析

### 1.1 后端架构

Jaeger v2 基于 **OpenTelemetry Collector** 框架构建（与本项目相同），核心组件：

| 组件 | 说明 |
|------|------|
| `jaegerstorage` Extension | 管理所有存储后端工厂，支持 7 种 Trace 存储 + 3 种 Metric 存储 |
| `jaegerquery` Extension | 查询 API 服务，提供 HTTP + gRPC 双协议 |
| Web 框架 | gorilla/mux + 标准 net/http，端口 16686(HTTP) / 16685(gRPC) |

#### Trace 查询 API（双版本并行）

**HTTP v2 API**（当前主力，给 Jaeger UI 使用）：

| 方法 | 路由 | 说明 |
|------|------|------|
| GET | `/api/traces` | 搜索 Traces |
| GET | `/api/traces/{traceID}` | 获取单个 Trace |
| GET | `/api/services` | 获取服务列表 |
| GET | `/api/services/{service}/operations` | 获取操作列表 |
| GET | `/api/operations` | 获取操作列表（新 URL） |
| GET | `/api/dependencies` | 获取服务依赖关系 |
| POST | `/api/archive/{traceID}` | 归档 Trace |
| POST | `/api/transform` | OTLP → Jaeger 格式转换 |

**HTTP v3 API**（新版，使用 OTLP 原生格式）：

| 方法 | 路由 | 说明 |
|------|------|------|
| GET | `/api/v3/traces` | 搜索 Traces（OTLP 格式） |
| GET | `/api/v3/traces/{trace_id}` | 获取 Trace（OTLP 格式） |
| GET | `/api/v3/services` | 获取服务列表 |
| GET | `/api/v3/operations` | 获取操作列表 |

**Trace 搜索参数**：

| 参数 | 类型 | 说明 |
|------|------|------|
| `service` | string | 服务名（必填，除非提供 traceID） |
| `operation` | string | 操作名 |
| `start` | int64 | 开始时间（unix 微秒） |
| `end` | int64 | 结束时间（unix 微秒） |
| `minDuration` | string | 最小持续时间（如 "1ms", "500us"） |
| `maxDuration` | string | 最大持续时间 |
| `limit` | int | 结果限制（默认 100） |
| `tags` | JSON | 标签过滤（如 `{"error":"true"}` ）|
| `tag` | key:value | 标签过滤（可多个） |
| `traceID` | string | 直接按 ID 查询（可多个） |
| `raw` | bool | 是否返回原始 trace |

**gRPC API**（v2 + v3 双版本）：
- `GetTrace` / `FindTraces` / `GetServices` / `GetOperations` / `GetDependencies`
- v2 返回 Jaeger model，v3 返回 OTLP ptrace 格式

#### Metric 查询 API

| 方法 | 路由 | 说明 |
|------|------|------|
| GET | `/api/metrics/latencies` | P99 等延迟百分位 |
| GET | `/api/metrics/calls` | 调用率 |
| GET | `/api/metrics/errors` | 错误率 |
| GET | `/api/metrics/minstep` | 最小时间步长 |

#### 核心存储接口

```go
// tracestore.Reader — Jaeger 的 Trace 读取接口
type Reader interface {
    GetTraces(ctx, ...GetTraceParams) iter.Seq2[[]ptrace.Traces, error]
    GetServices(ctx) ([]string, error)
    GetOperations(ctx, OperationQueryParams) ([]Operation, error)
    FindTraces(ctx, TraceQueryParams) iter.Seq2[[]ptrace.Traces, error]
    FindTraceIDs(ctx, TraceQueryParams) iter.Seq2[[]FoundTraceID, error]
}

// metricstore.Reader — Jaeger 的 Metric 读取接口
type Reader interface {
    GetLatencies(ctx, *LatenciesQueryParameters) (*MetricFamily, error)
    GetCallRates(ctx, *CallRateQueryParameters) (*MetricFamily, error)
    GetErrorRates(ctx, *ErrorRateQueryParameters) (*MetricFamily, error)
    GetMinStepDuration(ctx, *MinStepDurationQueryParameters) (time.Duration, error)
}
```

#### 查询链路

```
HTTP/gRPC Handler
  → QueryService (querysvc)
    → tracestore.Reader / metricstore.Reader
      → 具体存储实现 (Memory/ES/Cassandra/ClickHouse/Badger/Prometheus)
```

#### 中间件链

```
Recovery Handler → OTel HTTP Instrumentation → Trace Response Handler → Tenant Extraction → Bearer Token Propagation
```

### 1.2 前端架构

#### 技术栈

| 类别 | 技术 |
|------|------|
| 框架 | React 19 + TypeScript |
| 状态管理 | Redux + React Query（混合，迁移中） |
| UI 组件库 | Ant Design 6 |
| 路由 | React Router v5 + v6 compat |
| 构建 | Vite 7 |
| 图表 | Recharts、@pyroscope/flamegraph |
| 虚拟滚动 | @tanstack/react-virtual |
| API 校验 | Zod |

#### 双 API 客户端架构

| 客户端 | 技术 | 用途 |
|--------|------|------|
| 旧版 v1/v2 | isomorphic-fetch + Redux (redux-promise-middleware) | Trace 搜索/获取、依赖关系、Metrics |
| 新版 v3 | fetch + Zod + React Query | 服务列表、操作列表 |

#### Trace 搜索页面

- **布局**：左右分栏（左 6:右 18），左侧搜索表单 + 右侧结果
- **搜索表单字段**：Service（必选）、Operation、Tags（logfmt 格式）、Lookback（预设时间范围）、Start/End Time（自定义）、Min/Max Duration、Limit
- **搜索结果**：散点图（X=时间, Y=耗时, 红点=有错误） + 排序 + 结果列表 + 下载 JSON

#### Trace 详情页面 — 5 种视图

| 视图 | 说明 |
|------|------|
| **Timeline/Waterfall** | 默认视图，虚拟滚动的 Span 瀑布图 + 详情面板 |
| **TraceGraph** | DAG 图（使用 plexus 库） |
| **TraceStatistics** | 统计视图 |
| **TraceSpanView** | 表格化 Span 视图 |
| **TraceFlamegraph** | 火焰图（@pyroscope/flamegraph） |

#### Timeline 视图组件层次

```
TraceTimelineViewer
├── TimelineHeaderRow (时间轴头部 + minimap)
└── VirtualizedTraceView (虚拟滚动 span 列表)
    ├── SpanBarRow (每个 span 的横条行)
    │   ├── SpanTreeOffset (树形缩进 + 展开/折叠)
    │   ├── SpanBar (时间横条)
    │   └── ReferencesButton
    └── SpanDetailRow (展开的 span 详情)
        └── SpanDetail
            ├── AccordionAttributes (Tags)
            ├── AccordionEvents (Logs)
            ├── AccordionLinks (Links)
            └── AccordionText (Warnings)
```

#### Trace 比较功能

- 路由 `/trace/:a...:b`，支持从搜索结果选择多个 trace 加入比较队列
- `TraceDiffGraph` 渲染差异视图

#### 数据模型

- **内部模型**: Trace/Span（Jaeger 原生格式）
- **OTel 模型**: IOtelTrace/IOtelSpan（通过 `asOtelTrace()` 懒转换）
- **Span 详情属性**: traceID, spanID, parentSpanID, name, kind, startTime, duration, attributes, events, links, status, resource, instrumentationScope, depth, hasChildren, childSpans, relativeStartTime, warnings

---

## 二、当前项目架构分析

### 2.1 后端架构

| 组件 | 说明 |
|------|------|
| `adminext` Extension | Admin HTTP API + React WebUI（chi/v5 路由器） |
| `storageext` Extension | Redis + Nacos + BlobStore（制品存储） |
| `mcpext` Extension | MCP AI Agent 工具（12 个工具） |
| `agentgatewayreceiver` | OTLP + 控制面 HTTP/gRPC 接收器 |
| `tokenauthprocessor` | Token 鉴权过滤器 |

#### Trace/Metric 查询方式 — 纯代理模式

**⚠️ 核心发现：项目不直接存储和查询 Trace/Metric 数据，而是作为代理转发到外部后端**

**Trace 查询代理** → Jaeger Query API (`/api/v2/observability/traces/*`)：

| Admin API | Jaeger 目标 |
|-----------|------------|
| `GET /api/v2/observability/traces/services` | `/api/services` |
| `GET /api/v2/observability/traces/services/{svc}/operations` | `/api/services/{svc}/operations` |
| `GET /api/v2/observability/traces` | `/api/traces` |
| `GET /api/v2/observability/traces/{traceID}` | `/api/traces/{id}` |
| `GET /api/v2/observability/dependencies` | `/api/dependencies` |

**Metric 查询代理** → Prometheus HTTP API (`/api/v2/observability/metrics/*`)：

| Admin API | Prometheus 目标 |
|-----------|----------------|
| `GET .../metrics/query` | `/api/v1/query` |
| `GET .../metrics/query_range` | `/api/v1/query_range` |
| `GET .../metrics/labels` | `/api/v1/labels` |
| `GET .../metrics/labels/{name}/values` | `/api/v1/label/{name}/values` |
| `GET .../metrics/series` | `/api/v1/series` |
| `GET .../metrics/metadata` | `/api/v1/metadata` |

**代理实现**: `proxyGET` 方法，50MB 响应限制，透传查询参数。

### 2.2 前端架构

| 类别 | 技术 |
|------|------|
| 框架 | React 19 + TypeScript 5.7 |
| 样式 | Tailwind CSS |
| 路由 | React Router v7 |
| 图表 | ECharts 6 |
| 终端 | xterm.js 5 |
| 构建 | Vite 6 |

#### 9 个页面

| 页面 | 功能 |
|------|------|
| DashboardPage | 统计卡片 + Quick Actions + Health Overview |
| AppsPage | App CRUD + Token 管理 |
| ServicesPage | 服务卡片网格 |
| InstancesPage | 实例管理 + Arthas 终端 |
| TasksPage | 任务三级导航 + CRUD + 动态表单 |
| ConfigsPage | JSON 编辑器 + 模板推荐 |
| **TracesPage** | Trace 搜索 + 结果列表 + Span 时间轴瀑布图 |
| **MetricsPage** | PromQL 查询 + RED Dashboard |
| **ServiceMapPage** | ECharts 力导向拓扑图 |

---

## 三、对比分析

### 3.1 后端对比

| 维度 | Jaeger | 当前项目 | 差距 |
|------|--------|---------|------|
| **存储模式** | 内置存储层，7 种 Trace 后端 + 3 种 Metric 后端 | 纯代理模式，转发到 Jaeger + Prometheus | 当前项目依赖外部系统，无独立存储能力 |
| **查询抽象** | QueryService 统一查询入口 + tracestore.Reader/metricstore.Reader 抽象接口 | proxyGET 直接透传 HTTP 请求 | 当前项目缺少查询抽象层，无法做数据加工 |
| **API 版本** | v2 + v3 双版本，gRPC + HTTP 双协议 | 单版本 HTTP only | 当前项目无 gRPC 查询接口 |
| **数据格式** | v2(Jaeger model) + v3(OTLP ptrace) | 透传 Jaeger v2 格式 | 当前项目不做格式转换 |
| **中间件** | Recovery + OTel + Trace Response + Tenancy + Bearer Token | Recoverer + Logging + CORS + Auth | 架构类似，当前项目缺少多租户和 trace response header |
| **Trace 归档** | 支持 Archive 存储 | 不支持 | — |
| **Trace 时钟偏移修正** | 自动 Adjuster | 不支持 | — |
| **多租户** | 支持（x-tenant header） | 不支持 | — |

### 3.2 前端对比

| 维度 | Jaeger UI | 当前项目 | 差距 |
|------|-----------|---------|------|
| **搜索表单** | Service(必选) + Operation + Tags(logfmt) + Lookback(预设) + Duration + Limit + Adjust Time | Service + Operation + Tags + Lookback + Duration（基本齐全） | 当前项目基本对齐，缺少 Adjust Time 功能 |
| **搜索结果** | 散点图 + 排序(5种) + 下载 JSON + 结果列表(服务标签带颜色) | 结果列表 | ⚠️ 缺少散点图、排序、下载功能 |
| **Trace 详情** | 5 种视图（Timeline/Graph/Statistics/SpanView/Flamegraph） | 仅 Timeline（Span 瀑布图） | ⚠️ 缺少 Graph/Statistics/SpanView/Flamegraph 视图 |
| **Timeline 实现** | VirtualizedTraceView（虚拟滚动）+ 键盘快捷键 + Span 搜索 | 基础瀑布图 | ⚠️ 缺少虚拟滚动（大 trace 性能问题）、键盘导航、Span 搜索 |
| **Span 详情** | Attributes + Events + Links + Warnings + Debug Info | 基础展示 | 需要对齐完整性 |
| **Trace 比较** | 支持（选择多个 trace 进行 diff） | 不支持 | ⚠️ 缺失 |
| **UI 组件库** | Ant Design | Tailwind CSS（自定义组件） | 风格差异，各有优劣 |
| **状态管理** | Redux + React Query（混合） | 无全局状态管理 | 当前项目轻量化，复杂度上来后可能需要 |
| **API 校验** | Zod 运行时校验 | 无 | — |
| **数据模型** | Jaeger → OTel 双模型 + 懒转换 | 直接使用 Jaeger API 返回格式 | — |

### 3.3 Metric 页面对比

| 维度 | Jaeger Monitor 页面 | 当前项目 MetricsPage | 差距 |
|------|-------|---------|------|
| **数据来源** | 内置 metricstore.Reader（Prometheus/ES） | 代理 Prometheus API | 架构差异，功能类似 |
| **展示内容** | Latencies(P99) + Error Rate + Call Rate（SPM 面板） | PromQL 查询 + RED Dashboard | 当前项目 RED Dashboard 已基本对齐 |
| **图表库** | Recharts | ECharts | ECharts 功能更丰富 |
| **自定义查询** | 不支持自定义 PromQL | 支持（PromQL Query Tab） | ✅ 当前项目更灵活 |

---

## 四、优化建议

### 4.1 可参考的 Jaeger 设计（按优先级排序）

#### 🔴 P0 — 必做（核心体验提升）

**1. Trace 搜索结果增强**
- **散点图（ScatterPlot）**：X轴=startTime, Y轴=duration, 点大小=span数量, 红色=有错误
  - 用 ECharts scatter 即可实现，与现有技术栈一致
- **排序功能**：Most Recent / Longest First / Shortest First / Most Spans / Least Spans
- **下载 JSON**：将搜索结果导出
- **结果列表增强**：每条显示 trace name、span数量、错误数、各服务标签（带颜色标记）、相对时间

**2. Trace 详情虚拟滚动**
- 当前基础瀑布图在大 Trace（1000+ spans）时会有性能问题
- 参考 Jaeger 的 `VirtualizedTraceView`，使用 `@tanstack/react-virtual` 实现虚拟滚动
- 只渲染可视区域的 spans，大幅提升渲染性能

**3. Span 详情面板完善**
- 完整展示：Attributes/Tags、Events/Logs、Links、Resource/Process、Warnings
- 支持可折叠面板（Accordion 模式）
- 支持 Span 搜索过滤（通过 URL 参数 `uiFind`）

#### 🟡 P1 — 推荐做（显著提升体验）

**4. 多视图支持**
- **Statistics 视图**：统计各 Service/Operation 的 span 数量、平均耗时、错误率
- **SpanView（表格视图）**：以表格形式展示所有 spans，支持排序和过滤
- **Flamegraph 火焰图**：集成 `@pyroscope/flamegraph` 或使用 ECharts treemap
- **TraceGraph（DAG 图）**：使用 ECharts graph 或 D3 实现服务调用关系 DAG

**5. Trace 比较（Diff）功能**
- 从搜索结果选择多个 trace 加入比较队列（cohort）
- 选择 A/B 两个 trace 进行 diff
- 展示差异视图（新增/删除/变化的 spans）

**6. 后端查询抽象层**
- 将 `proxyGET` 重构为 QueryService + 存储接口模式
- 定义 `TraceReader` / `MetricReader` 接口
- 当前实现可以是 "JaegerBackend" 和 "PrometheusBackend"
- 为将来支持其他存储后端（如直接查询 ClickHouse）打好基础

```go
// 建议的接口设计
type TraceReader interface {
    SearchTraces(ctx context.Context, query TraceSearchQuery) (*TraceSearchResult, error)
    GetTrace(ctx context.Context, traceID string) (*Trace, error)
    GetServices(ctx context.Context) ([]string, error)
    GetOperations(ctx context.Context, service string) ([]Operation, error)
    GetDependencies(ctx context.Context, endTs time.Time, lookback time.Duration) ([]DependencyLink, error)
}

type MetricReader interface {
    QueryInstant(ctx context.Context, query string, time time.Time) (*QueryResult, error)
    QueryRange(ctx context.Context, query string, start, end time.Time, step time.Duration) (*QueryResult, error)
    GetLabels(ctx context.Context) ([]string, error)
    GetLabelValues(ctx context.Context, label string) ([]string, error)
    GetSeries(ctx context.Context, matchers []string, start, end time.Time) ([]Series, error)
}
```

#### 🟢 P2 — 可选做（锦上添花）

**7. Trace Timeline 键盘快捷键**
- 左右平移、缩放
- 上下翻页
- 跳转到上/下一个可见 span

**8. API 数据校验**
- 参考 Jaeger v3 使用 Zod 做运行时 API 响应校验
- 增强前端容错能力

**9. 前端状态管理升级**
- 当查询功能复杂度增加后，考虑引入 React Query 或 Zustand
- Trace 缓存：已查看的 trace 缓存在内存中避免重复请求

**10. 多租户支持**
- 参考 Jaeger 的 `x-tenant` header 机制
- 在代理请求时注入租户标识

### 4.2 不建议参考的部分

| Jaeger 特性 | 原因 |
|-------------|------|
| Redux + redux-actions 状态管理 | Jaeger 自己也在迁移，且当前项目轻量化更合适 |
| Ant Design UI 组件库 | 当前项目用 Tailwind CSS 自定义组件，风格统一，无需切换 |
| 双版本 API (v2/v3) | 当前项目无历史包袱，保持单版本即可 |
| gRPC 查询接口 | 当前场景 HTTP 足够，gRPC 增加复杂度 |
| 内置存储层 | 代理模式更轻量，当前架构合理，除非有特殊需求 |

---

## 五、实施路线图（建议）

### Sprint 1：Trace 搜索结果增强 + Span 详情完善 ✅

**前端改动**：
- [x] TracesPage 搜索结果新增散点图（ECharts scatter）
- [x] 搜索结果增加排序功能（5种排序方式）
- [x] 搜索结果增加下载 JSON 功能
- [x] 结果列表增强（span数量、错误数、服务标签带颜色）
- [x] Span 详情面板：完善 Attributes/Events/Links/Warnings 的 Accordion 展示

**后端改动**：
- [x] 无（复用现有代理 API）

### Sprint 2：虚拟滚动 + 多视图 ✅

**前端改动**：
- [x] Trace Timeline 引入虚拟滚动（@tanstack/react-virtual）
- [x] 新增 Statistics 视图（统计 Service/Operation 维度）— `TraceStatisticsView.tsx`
- [x] 新增 SpanView 表格视图 — `TraceSpanTableView.tsx`
- [x] Span 搜索过滤功能

**后端改动**：
- [x] 无

### Sprint 3：Trace 比较 + 后端重构 ✅

**前端改动**：
- [x] Trace 比较（Diff）功能（cohort 选择 + A/B diff 视图）— `TraceDiffView.tsx` + `TraceComparePage.tsx`
- [x] Flamegraph 火焰图视图 — `TraceFlamegraphView.tsx`（div 绝对定位实现，支持 1x-20x 缩放）
- [x] TraceGraph DAG 图视图 — `TraceGraphView.tsx`（ECharts force-directed graph）

**后端改动**：
- [x] 重构 proxyGET 为 TraceReader/MetricReader 接口 — `observability/interfaces.go`
- [x] 实现 JaegerTraceReader — `observability/jaeger_reader.go`（30s 超时，50MB 响应限制）
- [x] 实现 PrometheusMetricReader — `observability/prometheus_reader.go`

### Sprint 4：UI 增强优化 ✅

**1. ECharts 动态导入 + Code Splitting**
- [x] 4 个页面改为 `React.lazy()` 懒加载：TracesPage、TraceComparePage、MetricsPage、ServiceMapPage
- [x] 新增 `LazyLoadFallback.tsx` 加载动画组件
- [x] 移除 `vendor-echarts` 手动分割，由 Rollup 自动 code split
- [x] 效果：主应用 chunk 311KB/gzip 85KB，ECharts 延迟加载，首屏减少约 380KB gzip

**2. 四视图组件集成到 TraceDetail Tab**
- [x] TraceDetail.tsx 的 Tab 切换替换 "Coming soon" 为实际组件渲染
- [x] Statistics → `TraceStatisticsView`、Table → `TraceSpanTableView`、Flamegraph → `TraceFlamegraphView`、Graph → `TraceGraphView`

**3. Jaeger 配色风格复刻**
- [x] 服务颜色调色板：15 色鲜艳方案 → Jaeger 经典 20 色柔和方案（Teal、Light Gold、Brown 等）
- [x] TraceDetail.tsx 微调：hover 更微妙、错误行更柔和、timeline bar 透明度调整
- [x] TracesPage.tsx 散点图：正常点颜色 `#3b82f6` → `#17B8BE`（Jaeger Teal）

**4. Trace 详情 Modal 弹窗**
- [x] 新增通用 `Modal.tsx` 组件：createPortal 渲染、Escape 关闭、遮罩层、body 滚动锁定、弹入动画
- [x] 尺寸支持：md / lg / xl / full
- [x] TracesPage.tsx 从内联展开改为 Modal 弹窗显示 TraceDetail

**构建产物分析（Sprint 4 后）**：

| Chunk | 大小 | gzip |
|-------|------|------|
| `TracesPage-*.js` | 590 KB | ~191 KB |
| `index-*.js` (echarts core) | 513 KB | ~174 KB |
| `vendor-xterm-*.js` | 370 KB | ~96 KB |
| `index-*.js` (主应用) | 311 KB | ~85 KB |
| `vendor-react-*.js` | 50 KB | ~18 KB |
| `TraceComparePage-*.js` | 19 KB | ~5 KB |
| `MetricsPage-*.js` | 15 KB | ~5 KB |
| `ServiceMapPage-*.js` | 6 KB | ~3 KB |

---

## 六、遗留问题

1. ~~**虚拟滚动方案选型**~~：已选用 `@tanstack/react-virtual`，与 Tailwind CSS 兼容良好 ✅
2. ~~**Flamegraph 方案**~~：使用 div 绝对定位自实现，支持 1x-20x 缩放 ✅
3. ~~**DAG 图方案**~~：使用 ECharts force-directed graph 实现 ✅
4. ~~**后端抽象层的必要性**~~：已实现 TraceReader/MetricReader 接口抽象层 ✅
5. **状态管理时机**：何时引入全局状态管理？等到 Trace 缓存需求明确后再决定
6. ~~**Trace 比较的交互设计**~~：已实现双输入框 + Swap 按钮 + URL 参数联动 ✅
7. **DiffSpanRow 递归渲染优化**：TraceDiffView 中的 DiffSpanRow 使用递归渲染，当比较大型 Trace 时可能有性能问题，后续可考虑虚拟滚动优化
