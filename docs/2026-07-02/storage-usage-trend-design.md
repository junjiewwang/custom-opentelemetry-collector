# Storage Usage Daily Trend 设计方案

> 创建日期：2026-07-02
> 状态：设计阶段，待实施

## 背景

当前 Storage Admin 只能查看即时磁盘使用快照（`GetDiskUsage`），不支持按时间维度查看历史趋势。后端 `LifecycleScheduler` 已有 `TrendBuffer`（环形缓冲区，168 snapshots，~7天 @ 每小时）和 `GetTrend()` 方法，但**未通过任何 HTTP API 暴露**。

## 设计原则

遵循 SOLID + DIP（依赖倒置原则），核心设计原则：
- **高层模块不依赖低层**：Storage API 依赖抽象的 `UsageHistoryReader`，不依赖具体的 `TrendBuffer`
- **依赖抽象而非具体**：`TrendAggregator` 依赖接口，`TrendBuffer` 是实现细节
- **ISP（接口隔离）**：新增 `UsageTrendProvider` 独立于 `StorageAdmin`，消费者只依赖需要的接口
- **OCP（开闭原则）**：新增 trend 功能不修改现有 `StorageAdmin` 接口

## 架构设计

```
┌─────────────────────────────────────────────────────────────────┐
│  前端：StorageAdminPage                                           │
│    ↓ GET /api/.../disk-usage/trend?start&end&granularity=daily    │
├─────────────────────────────────────────────────────────────────┤
│  处理器：observability_handler_v2.go                              │
│    handleStorageUsageTrend() → calls UsageTrendProvider           │
├─────────────────────────────────────────────────────────────────┤
│  接口层：provider.go                                              │
│    UsageTrendProvider interface ← 新增接口                        │
│    StorageAdmin interface ← 不修改                                │
├─────────────────────────────────────────────────────────────────┤
│  聚合器：lifecycle/trend_aggregator.go                            │
│    TrendAggregator struct ← 新增                                  │
│      depends on UsageHistoryReader (abstraction)                  │
│      aggregates []UsageSnapshot → []UsageTrendPoint               │
├─────────────────────────────────────────────────────────────────┤
│  抽象层：lifecycle/interfaces.go                                  │
│    UsageHistoryReader interface ← 新增                            │
│      ReadSnapshots(since, until time.Time) []UsageSnapshot        │
│      ↑                                                            │
│  实现层：lifecycle/trend_buffer.go                                  │
│    TrendBuffer implements UsageHistoryReader ← 已有               │
│      ReadSnapshots(since, until) ← 新增方法                        │
├─────────────────────────────────────────────────────────────────┤
│  组装：extension.go                                                │
│    wires TrendBuffer → UsageHistoryReader → TrendAggregator       │
│      → impl of UsageTrendProvider                                 │
└─────────────────────────────────────────────────────────────────┘
```

## 类型设计

### Layer 1：Domain Types（`observabilitystorageext/types.go`）

```go
// UsageTrendRequest 查询参数
type UsageTrendRequest struct {
    Start       time.Time `json:"start"`
    End         time.Time `json:"end"`
    Granularity string    `json:"granularity"` // "hourly" | "daily"
}

// UsageTrendPoint 单个趋势数据点
type UsageTrendPoint struct {
    Timestamp  time.Time            `json:"timestamp"`
    TotalBytes int64                `json:"totalBytes"`
    UsedBytes  int64                `json:"usedBytes"`
    BySignal   map[SignalType]int64 `json:"bySignal,omitempty"`
    ByApp      map[string]int64     `json:"byApp,omitempty"`
}

// UsageTrendResponse 趋势查询响应
type UsageTrendResponse struct {
    Points      []UsageTrendPoint `json:"points"`
    Granularity string            `json:"granularity"`
    BucketCount int               `json:"bucketCount"`
}
```

### Layer 2：Abstraction（`provider.go` + `lifecycle/interfaces.go`）

```go
// provider.go — 独立接口，不影响 StorageAdmin
type UsageTrendProvider interface {
    GetUsageTrend(ctx context.Context, req UsageTrendRequest) (*UsageTrendResponse, error)
}

// lifecycle/interfaces.go — 抽象历史数据读取
type UsageHistoryReader interface {
    ReadSnapshots(since, until time.Time) []UsageSnapshot
}
```

**为什么 `UsageTrendProvider` 不合并到 `StorageAdmin`？**
- `GetDiskUsage` 返回即时快照，`GetUsageTrend` 查询历史趋势 → 不同关注点
- 部分 provider（如轻量版）可能支持即时查询但不支持趋势 → ISP 避免强制实现
- 前端可通过 interface check 决定是否展示趋势图

### Layer 3：Aggregator（`lifecycle/trend_aggregator.go`）

```go
// TrendAggregator 将 []UsageSnapshot 聚合为 []UsageTrendPoint
// 依赖 UsageHistoryReader 抽象，不依赖 TrendBuffer 具体实现
type TrendAggregator struct {
    reader UsageHistoryReader
}

func NewTrendAggregator(reader UsageHistoryReader) *TrendAggregator

// Aggregate 按请求的 granularity 聚合快照
//   "hourly": 按小时取平均值或最新值
//   "daily":  按天取平均值或最新值
func (a *TrendAggregator) Aggregate(req UsageTrendRequest) UsageTrendResponse
```

**聚合策略：**
- **daily**：将同一天内的所有 hourly 快照取最新值（磁盘使用量是单调递增的，最新值更能反映当天状态）
- **hourly**：直接返回匹配时间范围内的快照
- 无数据的时间段填充为前一时刻的值（防止图表跳空）

**可测试性：** `TrendAggregator` 依赖 `UsageHistoryReader` 接口，可注入 mock 进行聚合逻辑的单元测试。

### Layer 4：TrendBuffer 适配（`lifecycle/trend_buffer.go`）

```go
// ReadSnapshots 实现 UsageHistoryReader 接口
func (b *TrendBuffer) ReadSnapshots(since, until time.Time) []UsageSnapshot {
    // 从环形缓冲区筛选时间范围内的快照
}
```

## API 设计

### 新增端点

```
GET /api/v2/observability/admin/disk-usage/trend
```

**Query Parameters：**

| 参数 | 类型 | 必填 | 默认值 | 说明 |
|------|------|------|--------|------|
| `start` | RFC3339/unix-ms | 否 | now - 7d | 起始时间 |
| `end` | RFC3339/unix-ms | 否 | now | 结束时间 |
| `granularity` | string | 否 | `daily` | `hourly` 或 `daily` |

**响应示例：**

```json
{
  "points": [
    {"timestamp": "2026-07-01T00:00:00Z", "totalBytes": 107374182400, "usedBytes": 25600000000, "bySignal": {"trace": 10000000000, "metric": 8000000000, "log": 7600000000}},
    {"timestamp": "2026-07-02T00:00:00Z", "totalBytes": 107374182400, "usedBytes": 31200000000, "bySignal": {"trace": 12000000000, "metric": 10000000000, "log": 9200000000}}
  ],
  "granularity": "daily",
  "bucketCount": 2
}
```

## 前端适配

### StorageAdminPage 修改点

1. **新增时间范围选择器**：默认最近 7 天
2. **新增趋势折线图**：在"磁盘使用"卡片下方，使用 recharts `AreaChart`
3. **新增 API 调用**：`apiClient.getStorageUsageTrend(start, end, granularity)`

### 新增 TypeScript 类型

```typescript
// types/storage.ts
interface UsageTrendRequest {
  start?: string;  // ISO 8601
  end?: string;
  granularity?: 'hourly' | 'daily';
}

interface UsageTrendResponse {
  points: UsageTrendPoint[];
  granularity: string;
  bucketCount: number;
}

interface UsageTrendPoint {
  timestamp: string;
  totalBytes: number;
  usedBytes: number;
  bySignal?: Record<string, number>;
}
```

## 实施计划

| Sprint | 内容 | 改动文件 | 状态 |
|--------|------|---------|------|
| **Sprint 1** 后端核心 | UsageHistoryReader + TrendAggregator + ByApp + parseAppID + UsageTrendProvider | 10 个文件 | ✅ 已完成 |
| **Sprint 2** API 暴露 | HTTP handler + router 注册 + adapter 桥接 + extension 组装 | 5 个文件 | ✅ 已完成 |
| **Sprint 3** 前端展示 | API client 方法 + TypeScript 类型 + 趋势图表组件 | 3 个文件 | ⬜ 待实施 |

### Sprint 1 实施记录（2026-07-02）

| 文件 | 操作 | 说明 |
|------|------|------|
| `lifecycle/types.go` | 修改 | `StorageUsage`/`UsageSnapshot` 新增 `ByApp` |
| `lifecycle/interfaces.go` | 新增接口 | `UsageHistoryReader` + 接口 |
| `lifecycle/trend_buffer.go` | 新增方法 | `ReadSnapshots(since,until)` 时间范围筛选 + 接口校验 |
| `lifecycle/trend_aggregator.go` | **新增** | `TrendAggregator` + 类型。daily: 每日最新值, hourly: 直通 |
| `es/usage.go` | 修改 | `parseAppID()` + `signalPrefix()` + `ByApp` 聚合 |
| `observabilitystorageext/types.go` | 修改 | `DiskUsage` 新增 `ByApp` 字段 |
| `observabilitystorageext/provider.go` | 新增接口 | `UsageTrendProvider` |
| `reader_adapter.go` | 修改 | `DiskUsage{}` 初始化加 `ByApp` |
| `pg_reader_adapter.go` | 修改 | `ByApp: nil`（PG 暂不支持） |
| `scheduler.go` | 修改 | `collectUsageSnapshot` 传递 `ByApp` |

**验收：** ✅ 全量编译 ✅ 全量测试 ✅ DIP（依赖抽象） ✅ ISP（接口隔离）

### Sprint 2 实施记录（2026-07-02）

| 文件 | 操作 | 说明 |
|------|------|------|
| `lifecycle/scheduler.go` | 新增方法 | `TrendReader()` 返回 `UsageHistoryReader`（不暴露 TrendBuffer 具体类型） |
| `observabilitystorageext/extension.go` | 新增字段+方法 | `trendAggregator` + `GetUsageTrendProvider()`，在 Start() 中创建 TrendAggregator |
| `observabilitystorageext/reader_adapter.go` | 新增类型+导入 | `usageTrendProviderAdapter` 实现 `UsageTrendProvider`，桥接 `TrendAggregator` |
| `adminext/extension.go` | 新增字段+接线 | `storageTrendProvider` + `storage.GetUsageTrendProvider()` |
| `adminext/observability_handler_v2.go` | 新增 handler+导入 | `handleStorageUsageTrend`：解析 `start/end/granularity/appId` 参数 |
| `adminext/router.go` | 注册路由 | `GET /api/v2/observability/admin/disk-usage/trend` |

**API 端点：** `GET /api/v2/observability/admin/disk-usage/trend?start=xxx&end=xxx&granularity=daily&appId=xxx`

**验收：** ✅ 全量编译 ✅ 全量测试 ✅ 路由注册在 `storageAdmin != nil` 保护下

### Sprint 1 详细（后端核心，不依赖前端）

### Sprint 2 详细（API 暴露）

| 文件 | 操作 | 说明 |
|------|------|------|
| `observabilitystorageext/extension.go` | 新增字段+方法 | `trendProvider UsageTrendProvider` + `GetTrendProvider()` |
| `observabilitystorageext/reader_adapter.go` | 新增类型+方法 | adapter 桥接 `UsageTrendProvider` → handler |
| `adminext/observability_handler_v2.go` | 新增 handler | `handleStorageUsageTrend()` |
| `adminext/router.go` | 注册路由 | `GET /disk-usage/trend` |

### Sprint 3 详细（前端展示）

| 文件 | 操作 | 说明 |
|------|------|------|
| `webui-react/src/types/storage.ts` | 新增类型 | `UsageTrendRequest/Response/Point` |
| `webui-react/src/api/client.ts` | 新增方法 | `getStorageUsageTrend()` |
| `webui-react/src/pages/StorageAdminPage.tsx` | 新增组件 | 时间选择器 + 趋势折线图 |

## 验收标准

- [ ] `TrendBuffer.ReadSnapshots()` 可按时间范围筛选 → 单元测试
- [ ] `TrendAggregator.Aggregate()` daily/hourly 聚合正确 → 单元测试（mock UsageHistoryReader）
- [ ] `GET /disk-usage/trend?granularity=daily` 返回按天聚合的数据 → 集成测试
- [ ] 前端趋势图正确展示 7 天/30 天趋势
- [ ] `GetDiskUsage()` 现有功能不受影响（不修改 StorageAdmin 接口）

## 遗留问题

### 1. TrendBuffer 容量与 Retention 对齐

**决策：** 不使用固定 720 容量，而是根据实际 retention 配置动态计算：

```go
// extension.go 初始化时
maxRetention := max(config.Traces.Retention, config.Metrics.Retention, config.Logs.Retention)
trendBufSize := int(maxRetention.Hours()) + 1 // e.g. 7d = 168, 30d = 720
```

与调度间隔一致（默认 1h），保证覆盖整个 retention 窗口。`TrendBufferSize` 配置项仍然保留用于覆盖。

### 2. appID 维度支持 ✅ 已确认需要

**决策：** 需要按 appID 维度的存储用量统计。

**索引命名规则：** `{prefix}-{appID}-{date}` → appID 可从索引名解析：

```
otel-traces-app001-2026.07.02  → appID = "app001"
otel-traces-my-app-2026.07.02  → appID = "my-app"  (支持含连字符的 appID)
```

**类型变更：**

```go
// StorageUsage 和 UsageSnapshot 都要加 ByApp 维度
type StorageUsage struct {
    TotalBytes     int64
    UsedBytes      int64
    AvailableBytes int64
    BySignal       map[SignalType]int64
    ByApp          map[string]int64     // 新增：按 appID 分组
    UsageRatio     float64
}

type UsageSnapshot struct {
    Timestamp  time.Time
    TotalBytes int64
    UsedBytes  int64
    BySignal   map[SignalType]int64
    ByApp      map[string]int64        // 新增：按 appID 分组
}
```

**UsageTrendRequest 需要 appID 参数用于过滤：**
```go
type UsageTrendRequest struct {
    Start       time.Time
    End         time.Time
    Granularity string
    AppID       string // 可选：按 appID 过滤趋势
}
```

**ES 实现：** `UsageReporter.GetUsage()` 中新增 `parseAppID(indexName, prefix)` 方法，从索引名提取 appID，聚合 `ByApp`。

### 3. PG provider trend 采集

**决策：** 暂不需要支持。

## 依赖关系图（验证 DIP）

```
TrendAggregator → UsageHistoryReader (abstraction)
                       ↑
                  TrendBuffer (implements)

handler → UsageTrendProvider (abstraction)
              ↑
         TrendAggregator (implements via adapter)
```

- ✅ `TrendAggregator` 不依赖 `TrendBuffer`（依赖接口）
- ✅ `handler` 不依赖具体实现（依赖 `UsageTrendProvider` 接口）
- ✅ `TrendBuffer` 不依赖高层模块（纯数据容器 + 接口实现）
- ✅ 现有 `StorageAdmin` 接口不修改（OCP）
