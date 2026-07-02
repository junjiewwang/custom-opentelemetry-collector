# Storage 按天用量查询 — 架构重构设计

> 创建：2026-07-02
> 状态：设计阶段

## 1. 方案对比

### 原方案（TrendBuffer，Sprint 1-3 已实施）

```
Scheduler(hourly) → TrendBuffer(环形缓冲) → TrendAggregator → UsageTrendProvider
                                                    ↓
                                          hourly→daily 聚合（精度损失）
```

**问题：**
- 数据来源是调度器每小时快照，不是 ES 索引的真实数据
- 时间范围受 `TrendBufferSize` 限制（默认 168 = 7 天）
- 聚合为 daily 时有精度损失（内存中做平均/取最新）
- 无法覆盖任意历史时间范围

### 新方案（ES 索引直接查询）

```
ES indices:
  otel-traces-app001-2026.07.01 (100MB)
  otel-traces-app001-2026.07.02 (110MB)
         ↓ _stats API（直接查每个索引的 store.size_in_bytes）
         ↓ parseIndexName → 解析 date + appID + signal
         ↓ group by date
  → 精确到天的存储量（无需聚合，数据就在 ES 里）
```

**优势：**

| 维度 | TrendBuffer 方案 | 新方案 |
|------|-----------------|--------|
| 数据来源 | 调度器快照（间接） | ES `_stats`（源头） |
| 精度 | 小时级聚合（有损） | 索引级精确值 |
| 时间范围 | TrendBufferSize 限制 | ES retention 限制（天然对齐） |
| 时间粒度 | 快照精度=1h，聚合为天 | 天然天级（索引命名即天级） |
| appID 维度 | 快照中的 ByApp 聚合 | 从索引名直接解析 |
| 代码耦合 | 依赖 TrendBuffer + 调度器 | 直接查 ES，零耦合 |
| 可测试性 | 需 mock TrendBuffer | 只需 mock `_stats` 响应 |

### 结论：TrendBuffer 方案应该被替代

需要回滚的部分（Sprint 1-3 中的 trend pipeline）：
- ❌ `lifecycle/trend_aggregator.go` + `_test.go`
- ❌ `observabilitystorageext/provider.go` 中的 `UsageTrendProvider` 接口
- ❌ `reader_adapter.go` 中的 `usageTrendProviderAdapter`
- ❌ `extension.go` 中的 `trendAggregator` 字段 + `GetUsageTrendProvider()`
- ❌ `adminext` 中的 `handleStorageUsageTrend` + 路由 `/disk-usage/trend`
- ❌ 前端的 `getStorageUsageTrend()` + `/trend` 调用

保留的部分（Sprint 1-3 中仍有价值的）：
- ✅ `lifecycle/types.go`：`StorageUsage`/`UsageSnapshot.ByApp`
- ✅ `es/usage.go`：`parseAppID()`/`extractIndexSize()`（被新方案复用）
- ✅ `lifecycle/trend_buffer.go`：调度器内部监控仍需要
- ✅ `lifecycle/interfaces.go`：`UsageHistoryReader`（TrendBuffer 内部用途）

---

## 2. 新架构设计

### 抽象分层

```
┌──────────────────────────────────────────────────────────┐
│  前端：StorageAdminPage                                    │
│    GET /disk-usage/daily?start=...&end=...&appId=...     │
│    → 多条线（按信号分），y 轴为每日存储字节数                  │
├──────────────────────────────────────────────────────────┤
│  handler: handleStorageDailyUsage()    [adminext]        │
│    → DailyStorageProvider 接口（抽象）                    │
├──────────────────────────────────────────────────────────┤
│  provider.go 中的三个接口（ISP）                           │
│    StorageAdmin          → GetDiskUsage() 即时快照         │
│    DailyStorageProvider  → GetDailyStorage() 按天历史查询  │
├──────────────────────────────────────────────────────────┤
│  ES 实现: es/usage.go                                     │
│    GetDailyStorage(start, end, appID)                     │
│      → _stats API                                        │
│      → 遍历每个 index                                     │
│        → extractIndexSize()                              │
│        → classifyIndex() → signal                        │
│        → parseAppID() → appID                            │
│        → extractDate() → date string                     │
│      → group by date, return sorted points               │
├──────────────────────────────────────────────────────────┤
│  单元测试: es/usage_test.go                                │
│    mock _stats 响应 → 验证分组聚合逻辑正确                   │
└──────────────────────────────────────────────────────────┘
```

### 类型定义（`observabilitystorageext/types.go`）

```go
// DailyStorageRequest 按天存储量查询参数
type DailyStorageRequest struct {
    StartDate time.Time  // 起始日期（只取日期部分）
    EndDate   time.Time  // 结束日期
    AppID     string     // 可选：按 appID 过滤
}

// DailyStoragePoint 单日聚合的存储用量
type DailyStoragePoint struct {
    Date     string              `json:"date"`      // "2026-07-01"
    BySignal map[SignalType]int64 `json:"bySignal"` // 当天各信号总量
    ByApp    map[string]int64     `json:"byApp"`    // 当天各 app 总量
}

// DailyStorageResponse 按天存储量查询响应
type DailyStorageResponse struct {
    Points []DailyStoragePoint `json:"points"`
}
```

### 接口（`provider.go`）

```go
// DailyStorageProvider provides storage usage broken down by calender day.
// Data comes directly from storage backend (e.g., ES index stats), not from
// in-memory trend buffers.
//
// Separated from StorageAdmin (ISP): PG provider may not support this yet.
type DailyStorageProvider interface {
    GetDailyStorage(ctx context.Context, req DailyStorageRequest) (*DailyStorageResponse, error)
}
```

### ES 实现（`elasticsearch/usage.go`）

核心逻辑：

```go
func (r *UsageReporter) GetDailyStorage(ctx context.Context, req DailyStorageRequest) (*DailyStorageResponse, error) {
    // 1. 构造 _stats 查询的索引 pattern
    pattern := fmt.Sprintf("%s-*,%s-*,%s-*",
        r.config.Traces.IndexPrefix, r.config.Metrics.IndexPrefix, r.config.Logs.IndexPrefix)
    
    // 2. 调用 ES _stats API
    stats, err := r.client.GetIndicesStats(ctx, pattern)
    
    // 3. 遍历每个索引 → 按天分组聚合
    daily := make(map[string]*DailyStoragePoint)
    for indexName, indexData := range ... {
        size := r.extractIndexSize(indexData)
        signal := r.classifyIndex(indexName)
        prefix := r.signalPrefix(signal)
        
        dateStr := r.extractDate(indexName, prefix)    // "2026.07.01"
        appID := r.parseAppID(indexName, prefix)         // "app001"
        
        // 按日期范围过滤
        if dateOutOfRange(dateStr, req.StartDate, req.EndDate) { continue }
        // 按 appID 过滤
        if req.AppID != "" && appID != req.AppID { continue }
        
        // 聚合
        dp := getOrCreatePoint(daily, dateStr)
        dp.BySignal[signal] += size
        dp.ByApp[appID] += size
    }
    
    // 4. 按日期排序返回
    return sortAndReturn(daily), nil
}
```

### 可测试性

```go
func TestGetDailyStorage_BasicGrouping(t *testing.T) {
    r := &UsageReporter{config: testConfig(), client: mockClient(statsJSON)}
    resp, _ := r.GetDailyStorage(ctx, DailyStorageRequest{
        StartDate: date("2026-07-01"),
        EndDate:   date("2026-07-02"),
    })
    // 验证
    assert.Equal(t, 2, len(resp.Points))  // 2 天
    assert.Equal(t, int64(100MB), resp.Points[0].BySignal[lifecycle.SignalTrace])
    assert.Equal(t, "app001", keyOf(resp.Points[0].ByApp))
}

func TestGetDailyStorage_EmptyResponse(t *testing.T)     { ... }
func TestGetDailyStorage_AppIDFilter(t *testing.T)       { ... }
func TestGetDailyStorage_DateRangePartial(t *testing.T)  { ... }
```

### API 端点

```
GET /api/v2/observability/admin/disk-usage/daily
  ?start=2026-06-24    // 仅日期部分生效（默认 7 天前）
  &end=2026-07-02      // 默认今天
  &appId=app001        // 可选
```

响应：
```json
{
  "points": [
    {"date": "2026-07-01", "bySignal": {"trace": 104857600}, "byApp": {"app001": 104857600}},
    {"date": "2026-07-02", "bySignal": {"trace": 115343360, "metric": 52428800}, "byApp": {"app001": 115343360, "app002": 52428800}}
  ]
}
```

### 前端改造

```
/api/disk-usage/daily → 每个日期有 BySignal/ByApp
  → 多线图：3 条线（trace/metric/log）
  → 每条线的 y 值 = 各 app 的当日索引大小之和
```

---

## 3. 需要回滚的代码（删掉替代的部分）

| 文件 | 操作 |
|------|------|
| `lifecycle/trend_aggregator.go` | 删除（被 DailyStorageProvider 替代） |
| `lifecycle/trend_aggregator_test.go` | 删除 |
| `provider.go` → `UsageTrendProvider` 接口 | 替换为 `DailyStorageProvider` |
| `reader_adapter.go` → `usageTrendProviderAdapter` | 替换为 `dailyStorageProviderAdapter` |
| `extension.go` → `trendAggregator` 字段 + `GetUsageTrendProvider()` | 替换为 `GetDailyStorageProvider()` |
| `adminext/observability_handler_v2.go` → `handleStorageUsageTrend` | 替换为 `handleStorageDailyUsage` |
| `adminext/router.go` → `/disk-usage/trend` | 替换为 `/disk-usage/daily` |
| `webui-react` 中 `/trend` 调用 + `UsageTrend*` 类型 | 替换为 `/daily` + `DailyStorage*` |
| `scheduler.go` → `TrendReader()` | 保留（内部监控用途） |

## 4. 新增文件

| 文件 | 说明 |
|------|------|
| `es/usage.go` | 新增 `GetDailyStorage()` + `extractDate()` 方法 |
| `es/usage_test.go` | 新增 `TestGetDailyStorage_*` 用例（mock `_stats`） |
| `provider.go` | 新增 `DailyStorageProvider` 接口 |
| `reader_adapter.go` | 新增 `dailyStorageProviderAdapter` |

## 5. 保留（不删）的 Sprint 1-3 产物

| 保留项 | 原因 |
|--------|------|
| `lifecycle/types.go` 中 `ByApp` 字段 | `GetDailyStorage` 也需要 ByApp |
| `es/usage.go` 中 `parseAppID()` / `classifyIndex()` / `extractIndexSize()` | `GetDailyStorage` 直接复用 |
| `lifecycle/trend_buffer.go` (TrendBuffer) | 调度器内部监控快照仍需要 |
| `lifecycle/interfaces.go` 中 `UsageHistoryReader` | TrendBuffer 的内部接口 |
| `lifecycle/scheduler.go` 中 `TrendReader()` | 调度器内部调用 |
| `es/usage_test.go` 中 `TestParseAppID` / `TestSignalPrefix` | 验证工具方法 |
| 所有 Phase 1-3 的 `query/` 包和 `fields.go` | 与 trend 无关 |

## 6. 实施计划

| 步骤 | 内容 |
|------|------|
| 1 | 回滚 trend pipeline（TrendAggregator/UsageTrendProvider/trend API/trend handler） |
| 2 | 新增 `DailyStorageRequest/Response/Point` 类型 |
| 3 | 新增 `DailyStorageProvider` 接口 |
| 4 | ES `GetDailyStorage()` 实现 + `extractDate()` 工具方法 |
| 5 | ES 单元测试 |
| 6 | Adapter + handler + route + extension wiring |
| 7 | 前端改用 `/daily` + 多线图 |
