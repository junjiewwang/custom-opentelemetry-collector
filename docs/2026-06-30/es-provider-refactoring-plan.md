# ES Provider 重构方案

> 创建日期：2026-06-30
> 状态：设计阶段，待实施

## 背景

在 storage-format-unification（存储格式统一化）实施过程中，发现 ES provider 存在以下问题：
- ES 查询中的字段名硬编码分散在多个文件中，与 `StoredSpan` JSON tag 不一致导致新数据写入了但查不出来
- 修复了 `trace_reader.go` 中 10 处字段名不匹配（`trace_id`→`traceId`, `start_time`→`startTimeUnixNano`, `service_name`→`serviceName`, `operation_name`→`name`）
- 三个 reader（trace/log/metric）之间大量重复代码，维护成本高

## 现有代码结构

| 文件 | 大小 | 职责 |
|------|------|------|
| `trace_reader.go` | 19.9 KB | Trace 查询（两阶段搜索：聚合 + 批量获取） |
| `log_reader.go` | 12.89 KB | Log 查询、上下文、统计 |
| `metric_reader.go` | 11.76 KB | Metric 即时查询、范围查询、标签发现 |
| `types_reader.go` | 5.35 KB | 共享类型定义 |
| `client_search.go` | 3.71 KB | SearchRequest/SearchResponse 定义 |
| `admin.go` | 12.01 KB | Schema 初始化、ILM、Purge（含 mapping 定义） |
| `purger.go` | 12.32 KB | 生命周期数据清理 |
| `trace_writer.go` | 3.27 KB | Trace 写入 |
| `log_writer.go` | 3.16 KB | Log 写入 |
| `metric_writer.go` | 3.11 KB | Metric 写入 |
| `provider.go` | 6.99 KB | Provider 主入口 |
| `model.go` | 3.3 KB | 工具函数 |
| `config.go` | 1.44 KB | 配置结构 |
| `client.go` | 2.91 KB | ES HTTP 客户端核心 |

## 设计问题分析

### 1. 硬编码字段名分散（高耦合、低可维护性）

同一字段在多处硬编码，无中心化定义：

```
trace_reader.go: "traceId", "startTimeUnixNano", "serviceName", "name" ...
log_reader.go:   "trace_id", "service_name", "severity", "timestamp" ...
metric_reader.go: "@timestamp", "metric_name", "service_name" ...
purger.go:       "start_time" ← 与 trace_reader 实际用的 "startTimeUnixNano" 不一致！
admin.go:         mapping 定义是全量的（可作为 "真相源"），但 reader 未引用
```

**影响：字段改名需要 grep 所有文件，漏一处就是 bug。**

### 2. 重复代码（DRY 违反）

| 重复模式 | 出现位置 |
|----------|---------|
| `indexPattern(appID ...string)` | TraceReader / LogReader / MetricReader 各自实现 |
| `timeRangeQuery` / `timeRangeFilter` | TraceReader + LogReader 各自实现 |
| `calculateInterval` | LogReader + MetricReader 各自实现（仅阈值不同） |
| `GetTrace` vs `GetTraceSpans` | 几乎完全相同的查询，仅返回类型不同 |
| `hitsToSpans` vs `hitsToStoredSpans` | 共用一个 `hitsToStoredSpans` 即可 |

### 3. 职责混杂（单一职责违反）

`TraceReader` 一个 struct 承担了 5 种职责：
- 查询构建（`buildTraceSearchQuery`）
- ES 搜索执行（调用 `client.Search`）
- 聚合解析（`parseTraceAggregation` 等）
- 数据转换（`hitsToSpans`、`storedSpanToLocalSpan`）
- 依赖计算（`calculateDependencies`）

### 4. `map[string]any` 查询构建脆弱

- 字段名拼写错误编译期无法发现（刚修的就这个）
- 无法做单元测试（不连 ES 没法验证查询结构）
- 每个方法重复写相同的 DSL 骨架（`"bool":{ "must":[{ "term":{...`）

### 5. 死代码

`errMissingTraceAppID` / `errMissingLogAppID` / `errMissingMetricAppID` 定义了但从未使用。

### 6. purger.go 字段名错误

`timestampField(SignalTrace)` 返回 `"start_time"`，但索引模板和实际使用的是 `"startTimeUnixNano"`。

### 7. 字段命名风格不一致

| 信号 | 命名风格 | 示例 |
|------|---------|------|
| Trace | camelCase | `traceId`, `startTimeUnixNano`, `serviceName` |
| Log | snake_case | `trace_id`, `service_name`, `span_id` |
| Metric | snake_case + @前缀 | `@timestamp`, `metric_name`, `service_name` |

---

## 重构方案

### 整体架构

```
┌─────────────────────────────────────────┐
│  trace_reader.go / log_reader.go / ...  │  ← 业务层（薄）
├─────────────────────────────────────────┤
│         query/  查询抽象层                 │
│  - ESFieldNames (字段名常量)             │
│  - QueryBuilder (流式构建 bool/must/...) │
│  - AggBuilder  (聚合构建)                │
│  - TimeRangeHelper (时间范围查询)        │
│  - IndexPattern (索引模式)               │
│  - HitsParser (泛型文档解析)             │
├─────────────────────────────────────────┤
│         storedmodel (已存在，不修改)       │  ← 数据模型层
└─────────────────────────────────────────┘
```

**设计原则：**
- **高内聚**：ES DSL 构建逻辑集中在 `query` 包，reader 层只关心"查什么结果"不关心"怎么查"
- **低耦合**：reader 通过 `QueryBuilder` 与 ES 查询解耦，字段名变更只需改常量定义
- **可扩展**：新增查询类型只需组合现有 Builder，无需重复写 DSL 骨架
- **健壮性**：常量引用替代字符串字面量，编译期可抓拼写错误；Builder 可单独单元测试

---

### Phase 1：字段名常量化（低风险，高收益）

**目标：** 所有 ES 文档字段名集中定义，reader 通过常量引用而非硬编码字符串。

```go
// query/fields.go

// Trace document field names (aligned with StoredSpan JSON tags)
const (
    FieldTraceID           = "traceId"
    FieldSpanID            = "spanId"
    FieldParentSpanID      = "parentSpanId"
    FieldName              = "name"
    FieldKind              = "kind"
    FieldStartTimeUnixNano = "startTimeUnixNano"
    FieldEndTimeUnixNano   = "endTimeUnixNano"
    FieldDurationNano      = "durationNano"
    FieldStatus            = "status"
    FieldServiceName       = "serviceName"
    FieldAppID             = "appId"
    FieldAttributes        = "attributes"
    FieldResource          = "resource"
    FieldEvents            = "events"
    FieldLinks             = "links"
)

// Log document field names (aligned with StoredLogRecord JSON tags)
const (
    FieldLogTimeUnixNano    = "timeUnixNano"
    FieldLogObservedTime    = "observedTimeUnixNano"
    FieldLogTraceID         = "traceId"
    FieldLogSpanID          = "spanId"
    FieldLogSeverityText    = "severityText"
    FieldLogSeverityNumber  = "severityNumber"
    FieldLogBody            = "body"
    FieldLogServiceName     = "serviceName"
    FieldLogAppID           = "appId"
)

// Metric document field names (aligned with StoredMetricDataPoint JSON tags)
const (
    FieldMetricTimeUnixNano = "timeUnixNano"
    FieldMetricName         = "name"
    FieldMetricType         = "type"
    FieldMetricValue        = "value"
    FieldMetricServiceName  = "serviceName"
    FieldMetricAppID        = "appId"
    FieldMetricLabels       = "labels"
)
```

**改动范围：** `trace_reader.go`、`log_reader.go`、`metric_reader.go`、`admin.go`、`purger.go`

**验收标准：**
- `grep -r '"traceId"'` / `grep -r '"serviceName"'` 等搜索结果仅出现在 `query/fields.go` 和 `storedmodel` 中
- `grep -r '"trace_id"'` / `grep -r '"service_name"'` 仅 `compat*` 函数中出现（向后兼容旧索引）
- 编译通过，现有功能不变

---

### Phase 2：提取公共查询构建器（中风险，高收益）

**目标：** 将重复的 ES DSL 构建模式抽象为可复用的 Builder。

```go
// query/builder.go

// QueryBuilder provides a fluent API for composing ES bool queries.
type QueryBuilder struct {
    mustClauses   []map[string]any
    filterClauses []map[string]any
    shouldClauses []map[string]any
    mustNot       []map[string]any
    minShould     int
}

func NewQueryBuilder() *QueryBuilder { ... }

// Term adds a term query (exact match).
func (b *QueryBuilder) Term(field, value string) *QueryBuilder { ... }

// Terms adds a terms query (match any of the values).
func (b *QueryBuilder) Terms(field string, values []string) *QueryBuilder { ... }

// Range adds a range query for numeric/date fields.
func (b *QueryBuilder) Range(field string, gte, lte, gt, lt any) *QueryBuilder { ... }

// Match adds a match query for attributes/resources with key-value pair.
func (b *QueryBuilder) MatchAttribute(key, value string) *QueryBuilder { ... }

// Build returns the final ES query map.
func (b *QueryBuilder) Build() map[string]any { ... }
```

**使用对比：**

```go
// Before（脆弱）
must = append(must, map[string]any{
    "term": map[string]any{"service_name": query.ServiceName},
})

// After（健壮）
qb := query.NewQueryBuilder().
    Term(query.FieldServiceName, query.ServiceName).
    Term(query.FieldName, query.OperationName).
    Range(query.FieldStartTimeUnixNano,
        tr.Start.UnixNano(), tr.End.UnixNano(), nil, nil)
```

**改动范围：** `trace_reader.go` 中 `buildTraceSearchQuery`、`timeRangeQuery`、`timeRangeFilter` 等

---

### Phase 3：提取公共组件（低风险，中收益）

**3.1 indexPattern 统一**

```go
// query/index_pattern.go
func IndexPattern(prefix, appID string) string {
    if appID != "" {
        return prefix + "-" + appID + "-*"
    }
    return prefix + "-*"
}
```

**3.2 timeRange 辅助函数**

```go
// query/time_range.go
func TimeRangeQuery(field string, tr TimeRange) map[string]any { ... }
func TimeRangeFilter(field string, tr TimeRange) map[string]any { ... }
```

**3.3 泛型文档解析**

```go
// query/hits.go
func ParseHits[T any](hits []SearchHit, logger *zap.Logger) []T {
    result := make([]T, 0, len(hits))
    for _, hit := range hits {
        var doc T
        if err := json.Unmarshal(hit.Source, &doc); err != nil {
            logger.Warn("Failed to unmarshal", zap.Error(err))
            continue
        }
        result = append(result, doc)
    }
    return result
}
```

---

### Phase 4：清理死代码 + 修复 bug

- 删除 `errMissingTraceAppID` / `errMissingLogAppID` / `errMissingMetricAppID`
- 修复 `purger.go` 中 `timestampField(SignalTrace)` 返回值 `"start_time"` → `"startTimeUnixNano"`

---

## 实施优先级

| Phase | 风险 | 收益 | 改动量 | 建议 |
|-------|------|------|--------|------|
| Phase 1 (常量化) | 低 | ⭐⭐⭐ | 中 | 立即做 |
| Phase 4 (清理+修复) | 低 | ⭐⭐⭐ | 小 | 立即做 |
| Phase 2 (查询构建器) | 中 | ⭐⭐⭐ | 大 | 需要测试覆盖 |
| Phase 3 (公共组件) | 低 | ⭐⭐ | 中 | 可随后做 |

---

## 未完成任务

- [x] Phase 1：字段名常量化
- [x] Phase 2：提取查询构建器
- [x] Phase 3：提取公共组件
- [x] Phase 4：清理死代码 + 修复 purger.go

## 实施进展

### 2026-06-30：Phase 1 + Phase 4 已完成

**变更文件清单：**

| 文件 | 变更类型 | 说明 |
|------|---------|------|
| `fields.go` | 新增 | ES 字段名常量定义（Trace/Log/Metric/Shared/Legacy） |
| `trace_reader.go` | 修改 | 全部 28 处硬编码字段名替换为常量 |
| `log_reader.go` | 修改 | 字段名常量化 + 时间戳格式从 ISO 字符串改为 int64 纳秒 |
| `metric_reader.go` | 修改 | 字段名常量化 + 时间戳格式从 ISO 字符串改为 int64 纳秒 + hitToDataPoint 结构体更新 |
| `admin.go` | 修改 | 三个 index template 的 mapping 属性名替换为常量 |
| `purger.go` | 修改 | timestampField 修复 + app_id 常量化 |

**关键修复：**

1. **purger.go 时间戳字段名修复：**
   - `SignalTrace`: `"start_time"` → `"startTimeUnixNano"`
   - `SignalMetric`: `"@timestamp"` → `"timeUnixNano"`
   - `SignalLog`: `"timestamp"` → `"timeUnixNano"`

2. **log_reader.go 时间戳格式修复：**
   - 所有 `formatTimestamp(ts)` → `ts.UnixNano()`（对齐 `timeUnixNano` 的 long 类型）
   - `"timestamp"` → `FieldLogTimeUnixNano`（对齐 StoredLogRecord）

3. **metric_reader.go 时间戳格式修复：**
   - `"@timestamp"` + `formatTimestamp()` → `FieldMetricTimeUnixNano` + `UnixNano()`
   - `hitToDataPoint` 反序列化从 `string` + `time.Parse` 改为 `int64` + `time.Unix(0, ...)`

4. **死代码清理：**
   - 删除 `errMissingTraceAppID`、`errMissingLogAppID`、`errMissingMetricAppID`

**验收结果：**
- ✅ `grep -r '"traceId"'` / `grep -r '"serviceName"'` 等搜索结果仅出现在 `fields.go` 常量定义中
- ✅ `grep -r '"trace_id"'` / `grep -r '"service_name"'` 仅 `compat*()` 函数中出现（JSON 结构体 tag）
- ✅ 全量编译通过
- ✅ 全量测试通过（`go test ./extension/observabilitystorageext/...`）

## 遗留问题

1. ~~三种信号的时间戳字段不统一问题已在 Phase 1 中间接修复：全部统一为 `timeUnixNano`（Log/Metric）和 `startTimeUnixNano`（Trace）~~  ✅ 已修复
2. ~~三种信号的字段命名风格统一为 camelCase（对齐 StoredSpan/StoredLogRecord/StoredMetricDataPoint）~~ ✅ 已统一
3. Log 和 Metric reader 中时间戳格式已从 ISO 字符串改为 int64 纳秒，需要确认旧索引数据的兼容性（`compatLogRecord` 应处理）

### 2026-06-30：Phase 2 已完成

**变更文件清单：**

| 文件 | 变更类型 | 说明 |
|------|---------|------|
| `query/builder.go` | 新增 | 流式 QueryBuilder + `TermQ`/`TermsQ`/`T` 便利函数 |
| `query/time_range.go` | 新增 | 共享 `TimeRangeQuery`/`TimeRangeFilter`（消除 TraceReader/LogReader 重复） |
| `query/pattern.go` | 新增 | 共享 `IndexPattern`（消除三 reader 重复） |
| `trace_reader.go` | 修改 | `buildTraceSearchQuery` 用 Builder 重写，内联查询用 `TermQ`/`TermsQ` |
| `log_reader.go` | 修改 | `buildLogSearchQuery` 用 Builder 重写 |
| `metric_reader.go` | 修改 | `buildMetricQuery` 用 Builder 重写 |

**代码行数变化：**

| 指标 | 变化 |
|------|------|
| 消除重复的 `indexPattern` 实现 | 3 处 → 1 处 (`query.IndexPattern`) |
| 消除重复的 `timeRangeQuery` 实现 | 3 处 → 1 处 (`query.TimeRangeQuery`) |
| 消除重复的 `timeRangeFilter` 实现 | 2 处 → 1 处 (`query.TimeRangeFilter`) |
| 消除 `[]map[string]any` boilerplate | ~60 行使手写 DSL → ~30 行 Builder 调用 |
| `buildXxxSearchQuery` 方法行数 | Trace: 53→26, Log: 55→24, Metric: 27→14 |

**验收结果：**
- ✅ 全量编译通过
- ✅ 全量测试通过
- ✅ `query` 包零依赖 `elasticsearch` 包（避免循环引用），仅依赖 `storedmodel`
- ✅ 使用包别名 `esq` 避免与业务变量名 `query` 冲突

### 2026-06-30：Phase 3 已完成

**变更文件清单：**

| 文件 | 变更类型 | 说明 |
|------|---------|------|
| `query/aggregation.go` | 新增 | `ParseTermsAgg` / `ParseTermsAggWithCount` 共享聚合解析 |
| `trace_reader.go` | 修改 | 删除重复的 `hitsToSpans`（与 `hitsToStoredSpans` 完全相同）+ 删除无用的 `timeRangeFilter` 包装器 + 使用 `ParseTermsAgg` |
| `metric_reader.go` | 修改 | `ListMetricNames` / `ListLabelValues` 使用 `ParseTermsAgg` |
| `purger.go` | 修改 | `timestampField` 使用 `fields.go` 常量替代硬编码字符串 |
| `provider.go` | 修改 | 硬编码 `"resource.app_id"` → `FieldResource + ".app_id"` |

**消除的重复：**

| 重复模式 | Before | After |
|----------|--------|-------|
| `hitsToSpans` vs `hitsToStoredSpans` | 2 个完全相同的函数 (30 行) | 1 个 (`hitsToStoredSpans`) |
| terms aggregation 解析匿名结构体 | 4 处各自定义相同的匿名 struct | 统一使用 `esq.ParseTermsAgg()` |
| `timestampField` 硬编码字段名 | 3 处硬编码字符串（有不同步风险） | 3 处引用 `fields.go` 常量 |
| `timeRangeFilter` 包装器 | trace_reader.go 中不再被调用 | 已删除 |

**验收结果：**
- ✅ 全量编译通过
- ✅ 全量测试通过
- ✅ 无新增 lint 错误

---

## 全部 Phase 总结

| Phase | 变更文件数 | 核心收益 |
|-------|-----------|---------|
| Phase 1 | 6 | 30 个字段名常量，消除全部硬编码 ES 字段名 |
| Phase 2 | 7 | 流式 QueryBuilder，消除 3 处 indexPattern/timeRange 重复 |
| Phase 3 | 5 | 删除重复函数，统一聚合解析，修复 residual 硬编码 |
| Phase 4 | 3 | 清理死代码，修复 purger 时间戳字段名 bug |

**累计收益：** ES 字段名从分散硬编码（5+ 文件，30+ 处）→ 集中在 `fields.go`，改名一处全局生效；查询构建从手写 `map[string]any` DSL（~150 行 boilerplate）→ 流式 Builder API（~80 行）。
