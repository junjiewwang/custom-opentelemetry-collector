# TimeRangeFilter 重构设计方案

## 1. 背景与问题

### 1.1 当前状态

Sprint 4 将 metric 时间字段从 `long`（纳秒）改为 `date` + `epoch_millis`（毫秒），但通用时间范围查询函数 `TimeRangeFilter` / `TimeRangeQuery` 仍硬编码使用 `UnixNano()`：

```go
// query/time_range.go — 当前实现
func TimeRangeFilter(field string, tr storedmodel.TimeRange) map[string]any {
    filter["gte"] = tr.Start.UnixNano()  // ⚠️ 纳秒
    filter["lte"] = tr.End.UnixNano()    // ⚠️ 纳秒
}
```

**根因**：3 种 signal 的时间字段单位不统一：

| Signal | 字段名 | ES 类型 | 存储单位 |
|--------|--------|---------|---------|
| Trace  | `startTimeUnixNano` | `long` | 纳秒 |
| Log    | `timeUnixNano` | `long` | 纳秒 |
| Metric | `timeUnixMilli` | `date` (epoch_millis) | 毫秒 |

### 1.2 影响

- `ListMetricNames` → ES 收到纳秒值(19位)当 epoch_millis 解释 → 时间范围为公元 57000 年 → 返回空
- `ListLabelNames` → 同上
- `ListLabelValues` → 同上
- `QueryRange` → **不受影响**（手动用 `UnixMilli()` 构建，绕过了 `TimeRangeFilter`）

### 1.3 设计目标

1. **消除单位不匹配 BUG** — metric 查询使用毫秒
2. **统一 `QueryRange` 的时间范围构建** — 消除散落的手动构建逻辑
3. **向后兼容** — trace/log 的纳秒语义不受影响
4. **可扩展** — 未来如果 trace/log 也改为 `date` 类型，只需修改一处
5. **可单元测试** — 纯函数，无外部依赖

---

## 2. 设计方案

### 2.1 核心抽象：`TimeUnit` 枚举 + 多版本时间范围函数

引入 `TimeUnit` 类型作为时间精度的显式声明，遵循**开闭原则（OCP）**：新增时间单位只需扩展枚举和 converter，不修改已有函数。

```
┌──────────────────────────────────────────────────────────────┐
│                    query/time_range.go                         │
│                                                               │
│  TimeUnit (Nano | Milli)                                      │
│      │                                                        │
│      ▼                                                        │
│  timeConverter(unit) → func(time.Time) int64                  │
│      │                                                        │
│      ├─── TimeRangeFilterWithUnit(field, tr, unit)            │
│      ├─── TimeRangeQueryWithUnit(field, tr, unit)             │
│      │                                                        │
│      │  ┌─── TimeRangeFilter(field, tr)  [纳秒, 向后兼容]     │
│      └──┤                                                     │
│         └─── TimeRangeFilterMilli(field, tr) [毫秒, 新增]     │
│                                                               │
└──────────────────────────────────────────────────────────────┘
```

### 2.2 文件结构（改动范围）

```
extension/observabilitystorageext/provider/elasticsearch/query/
├── time_range.go          ← 重构：增加 TimeUnit + WithUnit 系列函数
├── time_range_test.go     ← 新增：完整单元测试
├── bucket_limit.go        ← 不变
├── bucket_limit_test.go   ← 不变
├── builder.go             ← 不变
├── aggregation.go         ← 不变
└── pattern.go             ← 不变

extension/observabilitystorageext/provider/elasticsearch/
├── metric_reader.go       ← 改动：timeRangeQuery 改用 TimeRangeFilterMilli；
│                              QueryRange 复用 timeRangeQuery 消除重复
├── trace_reader.go        ← 不变（已使用纳秒版 TimeRangeFilter）
└── log_reader.go          ← 不变（已使用纳秒版 TimeRangeFilter）
```

---

## 3. 详细设计

### 3.1 `query/time_range.go` — 重构后完整接口

```go
package query

import (
    "time"
    "go.opentelemetry.io/collector/custom/extension/observabilitystorageext/storedmodel"
)

// ═══════════════════════════════════════════════════
// TimeUnit — 时间精度枚举
// ═══════════════════════════════════════════════════

// TimeUnit represents the time precision used in ES field storage.
type TimeUnit int

const (
    // UnitNano indicates nanosecond precision (long fields for trace/log).
    UnitNano TimeUnit = iota
    // UnitMilli indicates millisecond precision (date fields with epoch_millis for metric).
    UnitMilli
)

// ═══════════════════════════════════════════════════
// 核心通用函数（接受 TimeUnit 参数）
// ═══════════════════════════════════════════════════

// TimeRangeFilterWithUnit returns a time range filter using the specified unit.
// If both Start and End are zero, returns {"match_all": {}}.
//
//   ES: {"range": {field: {"gte": value, "lte": value}}}
func TimeRangeFilterWithUnit(field string, tr storedmodel.TimeRange, unit TimeUnit) map[string]any {
    conv := timeConverter(unit)
    filter := map[string]any{}
    if !tr.Start.IsZero() {
        filter["gte"] = conv(tr.Start)
    }
    if !tr.End.IsZero() {
        filter["lte"] = conv(tr.End)
    }
    if len(filter) == 0 {
        return map[string]any{"match_all": map[string]any{}}
    }
    return map[string]any{
        "range": map[string]any{field: filter},
    }
}

// TimeRangeQueryWithUnit returns a range query (always includes gte + lte)
// using the specified unit.
func TimeRangeQueryWithUnit(field string, tr storedmodel.TimeRange, unit TimeUnit) map[string]any {
    conv := timeConverter(unit)
    return map[string]any{
        "range": map[string]any{
            field: map[string]any{
                "gte": conv(tr.Start),
                "lte": conv(tr.End),
            },
        },
    }
}

// ═══════════════════════════════════════════════════
// 便捷函数（向后兼容 + 语义明确）
// ═══════════════════════════════════════════════════

// TimeRangeFilter returns a nanosecond-precision time range filter.
// For trace/log fields stored as long (epoch_nanos).
// 向后兼容：现有 trace_reader/log_reader 调用无需修改。
func TimeRangeFilter(field string, tr storedmodel.TimeRange) map[string]any {
    return TimeRangeFilterWithUnit(field, tr, UnitNano)
}

// TimeRangeQuery returns a nanosecond-precision range query.
// 向后兼容：现有调用无需修改。
func TimeRangeQuery(field string, tr storedmodel.TimeRange) map[string]any {
    return TimeRangeQueryWithUnit(field, tr, UnitNano)
}

// TimeRangeFilterMilli returns a millisecond-precision time range filter.
// For metric fields stored as ES date type with epoch_millis format.
func TimeRangeFilterMilli(field string, tr storedmodel.TimeRange) map[string]any {
    return TimeRangeFilterWithUnit(field, tr, UnitMilli)
}

// TimeRangeQueryMilli returns a millisecond-precision range query.
// For metric fields stored as ES date type with epoch_millis format.
func TimeRangeQueryMilli(field string, tr storedmodel.TimeRange) map[string]any {
    return TimeRangeQueryWithUnit(field, tr, UnitMilli)
}

// ═══════════════════════════════════════════════════
// 内部实现
// ═══════════════════════════════════════════════════

// timeConverter returns a function that converts time.Time to int64
// in the specified unit.
func timeConverter(unit TimeUnit) func(time.Time) int64 {
    switch unit {
    case UnitMilli:
        return func(t time.Time) int64 { return t.UnixMilli() }
    default: // UnitNano
        return func(t time.Time) int64 { return t.UnixNano() }
    }
}
```

### 3.2 设计原则映射

| 原则 | 体现 |
|------|------|
| **SRP（单一职责）** | `time_range.go` 只负责时间范围 → ES query 转换 |
| **OCP（开闭原则）** | 新增时间单位（如微秒）只需扩展 `TimeUnit` + `timeConverter`，无需修改已有函数 |
| **DRY（消除重复）** | 核心逻辑在 `TimeRangeFilterWithUnit` 一处实现，各便捷函数委托 |
| **高内聚** | 所有时间范围相关逻辑集中在 `time_range.go` |
| **低耦合** | 函数接受 `storedmodel.TimeRange` + 枚举，不依赖具体 reader |
| **可测试** | 纯函数，输入 → 输出，无副作用 |
| **向后兼容** | `TimeRangeFilter/TimeRangeQuery` 签名不变，trace/log 零改动 |

### 3.3 `metric_reader.go` — 统一时间范围构建

**改动 1**：`timeRangeQuery` 使用毫秒版本

```go
// timeRangeQuery returns a millisecond-precision time range query for metrics.
func (r *MetricReader) timeRangeQuery(tr TimeRange) map[string]any {
    return esq.TimeRangeFilterMilli(FieldMetricTimeUnixMilli, tr)
}
```

**改动 2**：`QueryRange` 复用 `timeRangeQuery`，消除手动构建逻辑

```go
func (r *MetricReader) QueryRange(ctx context.Context, query MetricRangeQuery) (*MetricRangeResult, error) {
    esQuery := r.buildMetricQuery(query.MetricName, query.Labels, query.ServiceName)

    // 统一使用 timeRangeQuery 构建时间范围 filter
    must := []map[string]any{esQuery}
    timeFilter := r.timeRangeQuery(query.TimeRange)
    // 仅当 timeFilter 不是 match_all 时才追加（避免无效条件）
    if _, isMatchAll := timeFilter["match_all"]; !isMatchAll {
        must = append(must, timeFilter)
    }

    // ... 后续 date_histogram 逻辑不变
}
```

**改动对比**（Before → After）：

```diff
 // Before: QueryRange 手动构建毫秒范围（与 timeRangeQuery 重复）
-    if !query.TimeRange.Start.IsZero() || !query.TimeRange.End.IsZero() {
-        timeFilter := map[string]any{}
-        if !query.TimeRange.Start.IsZero() {
-            timeFilter["gte"] = query.TimeRange.Start.UnixMilli()
-        }
-        if !query.TimeRange.End.IsZero() {
-            timeFilter["lte"] = query.TimeRange.End.UnixMilli()
-        }
-        must = append(must, map[string]any{
-            "range": map[string]any{FieldMetricTimeUnixMilli: timeFilter},
-        })
-    }

 // After: 复用 timeRangeQuery（单一来源）
+    timeFilter := r.timeRangeQuery(query.TimeRange)
+    if _, isMatchAll := timeFilter["match_all"]; !isMatchAll {
+        must = append(must, timeFilter)
+    }
```

---

## 4. 单元测试设计

### 4.1 `query/time_range_test.go`

```go
package query

import (
    "testing"
    "time"
    "go.opentelemetry.io/collector/custom/extension/observabilitystorageext/storedmodel"
)

func TestTimeRangeFilterWithUnit_Nano(t *testing.T) {
    tr := storedmodel.TimeRange{
        Start: time.Unix(1000, 500),
        End:   time.Unix(2000, 600),
    }
    got := TimeRangeFilterWithUnit("startTimeUnixNano", tr, UnitNano)
    rangeClause := got["range"].(map[string]any)["startTimeUnixNano"].(map[string]any)
    
    if rangeClause["gte"] != tr.Start.UnixNano() {
        t.Errorf("gte: got %v, want %v", rangeClause["gte"], tr.Start.UnixNano())
    }
    if rangeClause["lte"] != tr.End.UnixNano() {
        t.Errorf("lte: got %v, want %v", rangeClause["lte"], tr.End.UnixNano())
    }
}

func TestTimeRangeFilterWithUnit_Milli(t *testing.T) {
    tr := storedmodel.TimeRange{
        Start: time.Unix(1000, 0),
        End:   time.Unix(2000, 0),
    }
    got := TimeRangeFilterWithUnit("timeUnixMilli", tr, UnitMilli)
    rangeClause := got["range"].(map[string]any)["timeUnixMilli"].(map[string]any)
    
    if rangeClause["gte"] != tr.Start.UnixMilli() {
        t.Errorf("gte: got %v, want %v", rangeClause["gte"], tr.Start.UnixMilli())
    }
    if rangeClause["lte"] != tr.End.UnixMilli() {
        t.Errorf("lte: got %v, want %v", rangeClause["lte"], tr.End.UnixMilli())
    }
}

func TestTimeRangeFilter_ZeroValues(t *testing.T) {
    tests := []struct {
        name     string
        tr       storedmodel.TimeRange
        wantKey  string // "match_all" or "range"
    }{
        {"both zero", storedmodel.TimeRange{}, "match_all"},
        {"only start", storedmodel.TimeRange{Start: time.Unix(1000, 0)}, "range"},
        {"only end", storedmodel.TimeRange{End: time.Unix(2000, 0)}, "range"},
        {"both set", storedmodel.TimeRange{Start: time.Unix(1000, 0), End: time.Unix(2000, 0)}, "range"},
    }
    for _, tt := range tests {
        t.Run(tt.name, func(t *testing.T) {
            got := TimeRangeFilterWithUnit("field", tt.tr, UnitMilli)
            if _, ok := got[tt.wantKey]; !ok {
                t.Errorf("expected key %q in result, got %v", tt.wantKey, got)
            }
        })
    }
}

func TestTimeRangeFilter_OnlyGte(t *testing.T) {
    tr := storedmodel.TimeRange{Start: time.Unix(1000, 0)}
    got := TimeRangeFilterMilli("timeUnixMilli", tr)
    rangeClause := got["range"].(map[string]any)["timeUnixMilli"].(map[string]any)
    
    if _, ok := rangeClause["gte"]; !ok {
        t.Error("expected gte to be present")
    }
    if _, ok := rangeClause["lte"]; ok {
        t.Error("expected lte to be absent when End is zero")
    }
}

func TestTimeRangeFilter_OnlyLte(t *testing.T) {
    tr := storedmodel.TimeRange{End: time.Unix(2000, 0)}
    got := TimeRangeFilterMilli("timeUnixMilli", tr)
    rangeClause := got["range"].(map[string]any)["timeUnixMilli"].(map[string]any)
    
    if _, ok := rangeClause["lte"]; !ok {
        t.Error("expected lte to be present")
    }
    if _, ok := rangeClause["gte"]; ok {
        t.Error("expected gte to be absent when Start is zero")
    }
}

func TestTimeRangeQuery_AlwaysHasBothBounds(t *testing.T) {
    tr := storedmodel.TimeRange{
        Start: time.Unix(1000, 0),
        End:   time.Unix(2000, 0),
    }
    got := TimeRangeQueryMilli("timeUnixMilli", tr)
    rangeClause := got["range"].(map[string]any)["timeUnixMilli"].(map[string]any)
    
    if _, ok := rangeClause["gte"]; !ok {
        t.Error("TimeRangeQuery must always have gte")
    }
    if _, ok := rangeClause["lte"]; !ok {
        t.Error("TimeRangeQuery must always have lte")
    }
}

// TestBackwardCompatibility 确保旧函数签名行为不变
func TestBackwardCompatibility(t *testing.T) {
    tr := storedmodel.TimeRange{
        Start: time.Unix(1000, 123456789),
        End:   time.Unix(2000, 987654321),
    }
    
    // TimeRangeFilter（旧签名）应使用纳秒
    got := TimeRangeFilter("startTimeUnixNano", tr)
    rangeClause := got["range"].(map[string]any)["startTimeUnixNano"].(map[string]any)
    
    if rangeClause["gte"] != tr.Start.UnixNano() {
        t.Errorf("backward compat broken: gte got %v, want %v", rangeClause["gte"], tr.Start.UnixNano())
    }
}

// TestTimeConverterPrecision 验证不同单位转换的精度
func TestTimeConverterPrecision(t *testing.T) {
    ts := time.Date(2026, 7, 10, 12, 0, 0, 123456789, time.UTC)
    
    nanoConv := timeConverter(UnitNano)
    milliConv := timeConverter(UnitMilli)
    
    // 纳秒保留完整精度
    if nanoConv(ts) != ts.UnixNano() {
        t.Errorf("nano converter: got %v, want %v", nanoConv(ts), ts.UnixNano())
    }
    
    // 毫秒截断纳秒部分
    if milliConv(ts) != ts.UnixMilli() {
        t.Errorf("milli converter: got %v, want %v", milliConv(ts), ts.UnixMilli())
    }
    
    // 毫秒值应该比纳秒值短 6 位
    nanoStr := fmt.Sprintf("%d", nanoConv(ts))
    milliStr := fmt.Sprintf("%d", milliConv(ts))
    if len(nanoStr)-len(milliStr) != 6 {
        t.Errorf("precision diff: nano=%d digits, milli=%d digits", len(nanoStr), len(milliStr))
    }
}
```

### 4.2 测试覆盖维度

| 维度 | 测试用例 |
|------|---------|
| 单位正确性 | Nano 输出 19 位纳秒值；Milli 输出 13 位毫秒值 |
| 边界处理 | Start 为零（省略 gte）；End 为零（省略 lte）；双零（match_all）|
| 向后兼容 | `TimeRangeFilter` 旧签名行为不变 |
| 精度验证 | 纳秒完整保留；毫秒正确截断 |
| Query vs Filter | Query 总是双边；Filter 可单边或 match_all |

---

## 5. 改动影响分析

### 5.1 改动范围（最小化）

| 文件 | 改动类型 | 说明 |
|------|---------|------|
| `query/time_range.go` | **重构** | 新增 TimeUnit + WithUnit 系列；旧函数委托新函数 |
| `query/time_range_test.go` | **新增** | 完整单元测试 |
| `metric_reader.go` | **修改** | `timeRangeQuery` 调用 `TimeRangeFilterMilli`；`QueryRange` 复用 `timeRangeQuery` |
| `trace_reader.go` | **不变** | 已使用 `TimeRangeFilter`（纳秒），无需修改 |
| `log_reader.go` | **不变** | 已使用 `TimeRangeFilter`（纳秒），无需修改 |

### 5.2 风险评估

| 风险 | 级别 | 缓解措施 |
|------|------|---------|
| 旧函数行为变化 | 低 | 旧函数 100% 委托 WithUnit + UnitNano，行为等价 |
| metric 查询语义变化 | 中 | `timeRangeQuery` 从纳秒改为毫秒，正是要修的 BUG |
| QueryRange 行为变化 | 低 | 只是抽取重复代码，输出等价 |
| 编译错误 | 低 | 只修改内部实现，公开 API 签名不变 |

---

## 6. 实施计划

### Sprint 5：TimeRangeFilter 重构

| 步骤 | 内容 | 验收标准 |
|------|------|---------|
| 1 | 重构 `query/time_range.go`：新增 `TimeUnit`、`TimeRangeFilterWithUnit`、`TimeRangeQueryWithUnit`、`TimeRangeFilterMilli`、`TimeRangeQueryMilli`；旧函数改为委托 | 编译通过；旧函数签名不变 |
| 2 | 新增 `query/time_range_test.go`：覆盖所有维度 | `go test ./...` 全绿 |
| 3 | 修改 `metric_reader.go`：`timeRangeQuery` 调用 `TimeRangeFilterMilli`；`QueryRange` 复用 `timeRangeQuery` | 编译通过；`ListMetricNames` 正常返回数据 |
| 4 | 端到端验证 | `/metrics/names`、`/metrics/labels`、`/metrics/query_range` 均正常 |
| 5 | 更新文档 | 标记本任务完成 |

---

## 7. 类图（关系总览）

```
┌─────────────────────────────────────────────────────────────────┐
│                        query package                             │
│                                                                  │
│  ┌─────────────┐    ┌──────────────────────────────────────┐    │
│  │  TimeUnit   │    │       TimeRangeFilterWithUnit         │    │
│  │  ─────────  │◄───│  (field, tr, unit) → map[string]any   │    │
│  │  UnitNano   │    └──────────────────────────────────────┘    │
│  │  UnitMilli  │              ▲            ▲                     │
│  └─────────────┘              │            │                     │
│                               │            │                     │
│  ┌────────────────────┐  ┌───┴────────┐  ┌┴────────────────┐   │
│  │ TimeRangeFilter    │  │ TimeRange  │  │ TimeRangeFilter │   │
│  │ (向后兼容/纳秒)    │  │ Query      │  │ Milli (新增)   │   │
│  └────────────────────┘  └────────────┘  └─────────────────┘   │
└─────────────────────────────────────────────────────────────────┘
         ▲                                        ▲
         │                                        │
    ┌────┴────────┐                    ┌──────────┴──────────┐
    │ TraceReader │                    │    MetricReader     │
    │ LogReader   │                    │  ───────────────    │
    │ (纳秒,不变) │                    │  timeRangeQuery()  │
    └─────────────┘                    │  → FilterMilli     │
                                       └────────────────────┘
```

---

## 8. 遗留问题与后续优化

| 问题 | 优先级 | 说明 |
|------|--------|------|
| 前端 `getMetricNames()` 不传时间范围 | P2 | 导致后端使用默认范围，可优化为传递当前页面时间范围 |
| trace/log 未来是否也改为 `date` 类型 | P3 | 如改则只需调用 `TimeRangeFilterMilli`，零代码新增 |
| `Query`（instant）方法也手动用 `UnixMilli()` | P2 | 可进一步统一，但当前语义正确无紧急性 |

---

## 9. 变更日志

| 日期 | 版本 | 说明 |
|------|------|------|
| 2026-07-10 | v1.0 | 初始设计 |
| 2026-07-10 | v1.1 | Sprint 5 实施完成 |

---

## 10. 实施记录（Sprint 5）

### 10.1 改动文件清单

| 文件 | 改动 | 行数变化 |
|------|------|---------|
| `query/time_range.go` | 重构：新增 `TimeUnit` 枚举、`TimeRangeFilterWithUnit`、`TimeRangeQueryWithUnit`、`TimeRangeFilterMilli`、`TimeRangeQueryMilli`、`timeConverter`；旧函数改为委托 | +80 / -20 |
| `query/time_range_test.go` | **新增**：12 个测试用例覆盖全部维度 | +210 |
| `metric_reader.go` | 修改：`timeRangeQuery` 改用 `TimeRangeFilterMilli`；`QueryRange` 复用 `timeRangeQuery` | +5 / -12 |

### 10.2 测试结果

```
=== 全部 12 个新增测试通过 ===
  ✓ TimeRangeFilterWithUnit_Nano       — 纳秒精度 gte/lte
  ✓ TimeRangeFilterWithUnit_Milli      — 毫秒精度 gte/lte
  ✓ TimeRangeFilter_ZeroValues         — 零值边界（match_all / 单边）
  ✓ TimeRangeFilter_OnlyGte            — 仅 Start → 省略 lte
  ✓ TimeRangeFilter_OnlyLte            — 仅 End → 省略 gte
  ✓ TimeRangeQuery_AlwaysHasBothBounds — Query 总是双边
  ✓ BackwardCompatibility_TimeRangeFilter — 向后兼容（纳秒）
  ✓ BackwardCompatibility_TimeRangeQuery  — 向后兼容（纳秒）
  ✓ TimeConverter_NanoPrecision         — 纳秒转换精确
  ✓ TimeConverter_MilliPrecision        — 毫秒转换精确
  ✓ MilliVsNanoValueRange               — 毫秒值 vs 纳秒值量级验证
  ✓ TimeRangeFilterMilli_DoesNotLeakNano — 回归测试：毫秒值不泄露纳秒

=== 所有已有测试保持通过 ===
  - bucket_limit_test.go: 12 tests PASS
  - query 包: go test ./... PASS
  - elasticsearch 包: go build ./... 零错误
```

### 10.3 验收确认

| 验收标准 | 状态 |
|---------|------|
| 编译通过 | ✅ `go build ./...` 零错误 |
| 新增测试全绿 | ✅ 12/12 PASS |
| 旧测试无回归 | ✅ 全部 PASS |
| trace/log 零改动 | ✅ `TimeRangeFilter` 签名行为不变 |
| `timeRangeQuery` 使用毫秒 | ✅ `TimeRangeFilterMilli` |
| `QueryRange` 消除手动构建 | ✅ 复用 `timeRangeQuery` |
| Lint 无新增问题 | ✅ 仅 1 个 pre-existing warning |
