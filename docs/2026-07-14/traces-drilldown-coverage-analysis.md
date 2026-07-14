# Traces Drilldown 支持度与覆盖范围分析

> 基于 [grafana/traces-drilldown](https://github.com/grafana/traces-drilldown) v2.1.0 源码分析，对照当前 Collector 实现的 Tempo API 兼容度。

## 总体评估

Drilldown 是一个 Grafana App Plugin，**专门绑定 Tempo 数据源**（硬编码 `pluginId: 'tempo'`），通过 `SceneQueryRunner` 向 Tempo 发 TraceQL 查询。我们的 Collector **实现了 Tempo Search API 的基础响应格式**，前两次修复（durationMs、name/service.name）已解决部分问题，但 RED 指标面板完全不可用，因为依赖的 `rate()`/`quantile_over_time()`/`histogram_over_time()`/`compare()` 等 TraceQL 函数我们暂不支持。

**总体完成度：Search API 响应格式 ~75%，RED 指标查询 ~0%**

---

## 1. Drilldown 的核心能力与 API 依赖

Drilldown 有三大功能区：

| 功能区 | 实现方式 | 后端依赖 |
|--------|---------|---------|
| **RED 指标面板** (Rate/Errors/Duration) | TraceQL metrics query → 时间序列图表 | `rate()` / `quantile_over_time()` / `histogram_over_time()` |
| **Service Structure 树** | 主查询 + trace merge 算法 | `select(nestedSet*, service.name, name, status)` + 返回完整 spans |
| **属性过滤器/标签** | DataSource `getTagKeys()` | `/api/v2/search/tags` + `/api/v2/search/tag/{name}/values` |
| **比较分析** | `compare()` 函数 | TraceQL `compare()` |
| **异常分析** | TraceQL metrics query | `event:name=exception` + 聚合 |
| **Span 列表** | `select(columns)` | `select()` 投射 |

---

## 2. Search API 响应格式分析

### 2.1 Drilldown 期望的格式（来自 `src/types.ts`）

```typescript
type TraceSearchMetadata = {
  traceID: string;
  rootServiceName: string;
  rootTraceName: string;
  startTimeUnixNano?: string;
  durationMs?: number;
  spanSets?: Spanset[];        // ← 使用 spanSets（数组形式）
  spanSet?: Spanset;           // ← deprecated，但仍检查
};

type Span = {
  spanID: string;
  name?: string;               // ← 顶层 name 字段
  startTimeUnixNano: string;
  durationNanos: string;       // ← 字符串
  attributes?: Array<{
    key: string;
    value: {
      stringValue?: string;
      intValue?: string;       // ← 字符串，如 "19"
      boolValue?: boolean;
      Value?: { ... };         // ← 备用格式（proto 兼容）
    }
  }>;
};

type Spanset = {
  spans: Span[];
};
```

### 2.2 我们的输出格式（已有修复后）

| 字段 | 标准期望 | 我们输出 | 状态 |
|------|---------|---------|:---:|
| `traceID` | `string` | `string` | ✅ |
| `rootServiceName` | `string` | `string` | ✅ |
| `rootTraceName` | `string` | `string` | ✅ |
| `startTimeUnixNano` | `string` (nanos) | `string` | ✅ |
| `durationMs` | `number` | `int64` → JSON number | ✅ (已修复) |
| `spanSets[].spans` | `Span[]` | `tempoSearchSpan[]` | ✅ |
| `spanSets[].matched` | `number` | `number` | ✅ |
| `span.spanID` | `string` | `string` | ✅ |
| `span.name` | 顶层字段 | 顶层字段 | ✅ (已修复) |
| `span.startTimeUnixNano` | `string` | `string` | ✅ |
| `span.durationNanos` | `string` | `string` | ✅ |
| `span.attributes[].key` | `"service.name"` (无 scope 前缀) | `"service.name"` (已去前缀) | ✅ (已修复) |
| `span.attributes[].key` | `"nestedSetLeft"` | `"nestedSetLeft"` | ✅ |
| `span.attributes[].key` | `"nestedSetRight"` | `"nestedSetRight"` | ✅ |
| `span.attributes[].key` | `"nestedSetParent"` | `"nestedSetParent"` | ✅ |
| `span.attributes[].key` | `"status"` | `"status"` | ✅ |
| `span.attributes[].value.intValue` | `string` | `string` ✅ | ✅ |
| `span.attributes[].value.stringValue` | `string` | `string` ✅ | ✅ |
| `span.attributes[].value.Value.*` | proto 备用格式 | ❌ | ⚠️ 未提供 |

### 2.3 mergeTraces 的关键依赖

`src/utils/trace-merge/merge.ts` 构建 Service Structure 树时强制要求：

```ts
if (trace.spanSets?.length !== 1) {
  throw new Error('there should be only 1 spanset!');
}
```

**我们必须确保每个 trace 恰好有一个 `spanSets` 条目。**

---

## 3. RED 指标查询分析（完全不支持）

### 3.1 Drilldown 生成的 TraceQL 查询

`src/components/Explore/queries/generateMetricsQuery.ts` 生成的查询：

| 指标 | 查询 | 依赖 |
|------|------|------|
| Rate | `{filters} \| rate() [by(xxx)] [with(sample=true)]` | `rate()` 函数 |
| Errors | `{filters && status=error} \| rate() [by(xxx)]` | `rate()` 函数 |
| Duration | `{filters} \| quantile_over_time(duration, 0.5,0.95,0.99) [by(xxx)]` | `quantile_over_time()` |
| Duration Heatmap | `{filters} \| histogram_over_time(duration) with(sample=true)` | `histogram_over_time()` |
| Compare | `{filters} \| compare({duration>=X && duration<=Y})` | `compare()` 函数 |

### 3.2 我们对 TraceQL Functions 的支持度

| 函数 | 用途 | 支持状态 |
|------|------|:---:|
| `rate()` | RED 面板：Rate & Errors 时序 | ❌ 完全未实现 |
| `quantile_over_time()` | RED 面板：Duration 分位数 | ❌ 完全未实现 |
| `histogram_over_time()` | RED 面板：Duration 热力图 | ❌ 完全未实现 |
| `count_over_time()` | Duration 备选 | ❌ 完全未实现 |
| `compare()` | 比较分析 | ❌ 完全未实现 |
| `select()` | Span 列表投影 | ✅ 已实现 |
| `by()` 分组 | 按属性分组 | ❌ 语法可解析但不执行 |
| `with(sample=true)` | 采样提示 | ❌ 被忽略 |

### 3.3 Primary Signals 查询

`src/pages/Explore/primary-signals.ts` 定义了 5 种预设信号：

```traceql
1. Root spans:  { nestedSetParent<0 }
2. All spans:   { true }
3. Server:      { kind=server }
4. Consumer:    { kind=consumer }
5. Database:    { span.db.system.name!="" }
```

这些是作为 `{}` 内 filter 注入的。前 4 个我们**基本支持**（`nestedSetParent<0` → IS_ROOT, `kind=server` → SpanKind, `true` → TrueExpr），但 `span.db.system.name!=""` 的 `!=` 对字符串的语义需要验证。

---

## 4. Attribute Filters（标签探索）

Drilldown 使用 `ds.getTagKeys(options)` 获取所有可用 attributes。

**数据流**：
```
drilldown → Tempo DataSource → GET /api/v2/search/tags?start=...&end=...
                              → GET /api/v2/search/tag/{name}/values?start=...&end=...
```

我们的 `handleTempoV2SearchTagValues` 已实现 `GET /api/v2/search/tag/{tagName}/values`，但需要确认 `/api/v2/search/tags` 是否存在。

---

## 5. 异常分析（Exceptions）

Drilldown 在 `errors` 模式下展示 `ExceptionsScene`，查询格式：
```traceql
{ filters && status=error } | rate() by(event.exception.type?)
```

依赖 `event:*` 作用域（完全不支持）和 `rate()`。

---

## 6. 数据格式关键差异总结

### 6.1 Span 响应中的字段优先级

Drilldown 读 span 数据时的查找顺序：

| 数据 | 查找路径 | 我们输出 | 状态 |
|------|---------|---------|:---:|
| 操作名 | `span.name` (顶层) | `span.name` | ✅ (已修复) |
| 服务名 | `attributes[].key === 'service.name'` | `key = "service.name"` | ✅ (已修复) |
| 状态 | `attributes[].key === 'status'` | `key = "status"` | ✅ |
| 嵌套集左值 | `attributes[].key === 'nestedSetLeft'` → `intValue` | ✅ | ✅ |
| 嵌套集右值 | `attributes[].key === 'nestedSetRight'` → `intValue` | ✅ | ✅ |
| 嵌套集父值 | `attributes[].key === 'nestedSetParent'` → `intValue` | ✅ | ✅ |
| intValue 格式 | `"intValue": "19"` (string) | `"intValue": "19"` (string) | ✅ |
| 备用格式 | `value.Value.int_value` (proto 兼容) | ❌ | ⚠️ 不提供 |

### 6.2 只有一个 spanSets

测试文件显式断言 `spanSets.length === 1`，我们返回的就是单个 spanSet，格式一致 ✅。

---

## 7. 当前问题的诊断

### 当前已修复的问题

1. ✅ `durationMs = 0` → 已计算正确
2. ✅ `span.name` 在顶层 → 已重构
3. ✅ `service.name` key 带 scope 前缀 → 已去前缀
4. ✅ nestedSet 场景只返回部分 spans → 已返回全部 spans

### 仍存在的已知问题

| # | 问题 | 原因 | 影响 |
|---|------|------|------|
| 1 | **RED 面板 (Rate/Errors/Duration) 为空白** | `rate()`/`quantile_over_time()`/`histogram_over_time()` 完全未实现 | **全三大指标面板不可用** |
| 2 | **Duration 热力图无数据** | `histogram_over_time(duration)` 未实现 | Duration 面板不可用 |
| 3 | **比较分析无数据** | `compare()` 未实现 | 比较功能不可用 |
| 4 | **异常分析无数据** | `event:*` 作用域 + `rate()` 未实现 | Errors 面板详情不可用 |
| 5 | **属性过滤器可能不完整** | `/api/v2/search/tags` 需验证 | 过滤器下拉可能缺项 |
| 6 | **intValue 备用格式缺失** | 未提供 `Value.int_value` 备用字段 | 某些 edge case 解析失败 |

---

## 8. 优先级建议

### 🔴 P0 — 功能完全缺失

| 功能 | 工作量 | 状态 |
|------|:---:|:---:|
| `rate()` 函数支持（基础实现：按时间窗口计数） | 3-5 天 | ✅ 已实施 |
| `quantile_over_time(duration, ...)` 函数支持 | 2-3 天 | ✅ 已实施 |
| `histogram_over_time(duration)` 函数支持 | 2-3 天 | ✅ 已实施 |

> 这三个函数解锁全部三个 RED 面板（Rate / Errors / Duration）。

### 实施详情

**改动文件**：

| 文件 | 改动 |
|------|------|
| `traceql/ast.go` | 新增 `NodeMetrics` + `MetricsStage` + `MetricsFunc` 枚举 |
| `traceql/lexer.go` | 新增 `TokenRate/TokenQuantileOverTime/TokenHistogramOverTime/TokenBy/TokenWith` |
| `traceql/parser.go` | 新增 `parseMetricsStage()` 解析函数，支持 `rate()/quantile_over_time()/histogram_over_time()` + `by()/with()` 参数 |
| `traceql/planner.go` | `ExecutionPlan` 新增 `HasMetrics`/`MetricsStage` 字段 |
| `observabilitystorageext/trace_metrics.go` | 新增 `TraceMetricsQuery/Result/Series/Point` 公共类型 |
| `observabilitystorageext/provider.go` | `TraceReader` 接口新增 `QueryTraceMetrics()` |
| `provider/elasticsearch/types_reader.go` | 新增 ES 本地 metrics 类型 |
| `provider/elasticsearch/trace_metrics.go` | **核心引擎**：ES date_histogram 聚合 + rate/percentile/histogram 计算 |
| `observabilitystorageext/reader_adapter.go` | 新增 ES adapter `QueryTraceMetrics` |
| `observabilitystorageext/pg_reader_adapter.go` | PG stub（返回 unimplemented） |
| `adminext/tempo_handler.go` | `handleTempoSearch` 新增 metrics 检测 → `executeTempoMetricsQuery` |

**执行流程**：

```
用户查询: {filters} | rate() by(service.name)
  → Parser 识别 TokenRate + by() 
  → Planner 提取 HasMetrics=true, MetricsStage={rate, by:["service.name"]}
  → Handler 检测 HasMetrics → executeTempoMetricsQuery
  → 构建 ES date_histogram 聚合
       { "date_histogram": {"field":"startTimeUnixNano", "fixed_interval":"15s"},
         "aggs": { "by_service.name": { "terms": {...}, 
                    "aggs": { "buckets": { "date_histogram": {...}, 
                               "aggs": { "metric": { "value_count": {"field":"_id"} }}}}}}}
  → ES 返回按 (label, time_bucket) 分组的计数值
  → rate = count / bucket_interval_seconds
  → 返回 [{labels: {service.name: "X"}, values: [{t: 1721..., v: 15.3}, ...]}]
```

### 🟡 P1 — 增强功能

| 功能 | 工作量 | 状态 |
|------|:---:|:---:|
| `compare()` 函数支持 | 2-3 天 | ⏳ 待实施 |
| `event:*` 作用域基础支持 | 2-3 天 | ✅ 已实施 |
| `by()` 分组执行 | 2-3 天 | ✅ 已在 P0 实施 |
| Span response 增加 `Value.*` 备用字段 | 0.5 天 | ✅ 已实施 |

**P1 附加项（从覆盖率分析发现）**：

| 功能 | 状态 |
|------|:---:|
| `!~` NOT regex 操作符 | ✅ 已实施 |
| `nil` 比较 | ✅ 已实施 |
| `trace:` 内在字段 (trace:duration/rootName/rootService) | ✅ 已实施 |

### P1 实施详情

**Span `Value.*` 备用字段** (`tempo_handler.go`):
- `tempoAnyValue` 新增 `Value *tempoAnyValueAlt` 字段，提供 snake_case 备用字段名
- `publicAnyValueToTempo`、`strVal`、`intVal`、`anyToTempoValue` 同步填充

**`!~` NOT regex** (`lexer.go` + `parser.go`):
- 新增 `TokenNotRegex`，readBang 中添加 `!~` 解析
- `tokenToOperator` 新增 `!~` 映射

**`nil` 比较** (`parser.go`):
- `parseValue` 中 TokenIdent="nil" 返回 Go nil

**`event:*` 作用域** (`parser.go` + `planner.go` + `builder.go`):
- `parseScopeAndKey` 新增 `event:` 冒号前缀 + `event.` 点号前缀
- `ExecutionPlan` 新增 `EventTags`/`EventTagsOr`
- `extractCondition` 路由 event scope 条件到 EventTags
- `buildTraceSearchQuery` 用 `esq.NestedQuery` 生成 nested query
- `storedmodel.TraceQuery` + `types.TraceQuery` 新增 EventTags 字段

**`trace:*` 内在字段** (`planner.go`):
- `trace:duration` → 提取为 MinDuration/MaxDuration
- `trace:rootName` / `trace:rootService` → 提取为 Tags
- `resolveSelectField` 支持 trace:/event: 冒号前缀解析

### 🟢 P2 — 优化

| 功能 | 工作量 |
|------|:---:|
| `with(sample=true)` 采样优化 | 1 天 |
| `!=` 对字符串属性语义验证 | 0.5 天 |
