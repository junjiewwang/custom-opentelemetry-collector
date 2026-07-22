# Fix: Tempo Intrinsic Tags — statusMessage / rootName / rootServiceName

## 需求背景

Grafana Tempo 数据源在 `/api/v2/search/tags` 中列出了 7 个 intrinsic tags：

```
duration, kind, name, status, statusMessage, rootName, rootServiceName
```

其中前 4 个（`duration`、`kind`、`name`、`status`）已完整实现（搜索过滤 + Tag Values 列出），后 3 个存在 TODO：

```
❌ statusMessage   — nested ES field "status.message", not yet queryable (TODO)
❌ rootName        — not stored in ES; would require trace root span derivation (TODO)
❌ rootServiceName — not stored in ES; would require trace root span derivation (TODO)
```

## ES 实际验证环境

- **集群**: `http://9.134.106.132:9200` (user: `elastic`)
- **Trace index**: `otel-traces-CIKmanlvG3sfdnpr-2026.07.22`
- **Span 总量**: ~5M spans/day
- **status.message 数据**: ~5000 条，中文+英文混合（"配送认证失败"、"Send failed" 等）

---

## 1. status.message — ES 验证结果

### 1.1 Mapping 结构

```json
{
  "status": {
    "properties": {
      "code":    { "type": "keyword" },
      "message": { "type": "text" }     // ← 注意：无 .keyword 子字段！
    }
  }
}
```

### 1.2 可操作性验证

| 操作 | ES 查询 | 结果 | 说明 |
|------|---------|------|------|
| `term` query | `{"term": {"status.message": "Send failed"}}` | ❌ 返回 0 | text 字段不支持 term-level 全文匹配 |
| `match` query | `{"match": {"status.message": "Send failed"}}` | ✅ 1185 条 | text 字段可用 match 做全文搜索 |
| `terms agg` | `{"terms": {"field": "status.message"}}` | ❌ 报错 | text 字段需要 `fielddata=true`（内存开销大） |

**ES 报错原文**：
> Text fields are not optimised for operations that require per-document field data like aggregations and sorting, so these operations are disabled by default.

### 1.3 实际数据采样

```
status.message = "配送认证失败"  (2193 条)
status.message = "Send failed"   (1185 条)
status.message = "市场服务不可用"
status.message = "配送失败"
status.message = "追踪点错误"
status.message = "用户无效请求"
status.message = "库存更新失败"
```

### 1.4 当前代码 Bug

`attribute_resolver.go` 正确映射了 `status.message` → `"status.message"`，但 `resolveTagTermClauses` 中：

```go
// trace_reader.go:634
func resolveTagTermClauses(key, value string) []map[string]any {
    fields, val := resolveTagESFields(key, value)
    for _, f := range fields {
        clauses = append(clauses, esq.T(f, val))  // ← 生成 {"term": {"status.message": "xxx"}}
    }
}
```

`term` query 在 `text` 字段上永远返回 0，**当前搜索过滤实际上不可用**。

### 1.5 结论

| 路径 | 可行性 | 方案 |
|------|--------|------|
| Tag Values 端点 | 🚫 不可能 | `text` 字段无 `.keyword` 子字段，terms agg 不可用。继续返回空。 |
| 搜索过滤 | ✅ 可修复 | `term` → `match` query |

---

## 2. rootName — ES 验证结果

### 2.1 概念

`rootName` 是 trace 的 **root span 的 `name` 属性**（即 `parentSpanId` 不存在的 span 的 `name`）。

ES 中不直接存储 `rootName` 字段，但可通过 root span 的 `name` 推导：

```
root span = parentSpanId 不存在（或 parentSpanId="0000000000000000" 历史数据）
rootName  = root span 的 name 字段值
```

### 2.2 搜索过滤验证

**ES query**: `name=xxx` + `parentSpanId` 不存在

```json
{
  "bool": {
    "must": [
      {"term": {"name": "Create connection"}},
      {"bool": {"must_not": {"exists": {"field": "parentSpanId"}}}}
    ]
  }
}
```

| 验证项 | all spans | root-only | 排除量 |
|--------|-----------|-----------|--------|
| `Create connection` | 135 | 110 | ✅ 排除 25 条非 root |
| `POST` | 3965 | 3965 | 全为 root span |
| `test-java-order-service` service | 10000 | 52 | ✅ 正确 |

### 2.3 Tag Values 验证

**ES query**: root spans 的 `name` terms aggregation

```json
{
  "query": {"bool": {"must_not": {"exists": {"field": "parentSpanId"}}}},
  "aggs": {"root_names": {"terms": {"field": "name", "size": 1000}}}
}
```

✅ 正常工作，返回 10+ 个不同的 root span name。

### 2.4 结论

| 路径 | 可行性 | 方案 |
|------|--------|------|
| Tag Values 端点 | ✅ 可行 | root spans 的 `name` terms agg |
| 搜索过滤 | ✅ 可行 | `name=xxx` + `parentSpanId` 不存在的复合 bool 查询 |

---

## 3. rootServiceName — ES 验证结果

### 3.1 概念

`rootServiceName` 是 trace 的 **root span 的 `serviceName` 属性**。

### 3.2 搜索过滤验证

| 验证项 | all spans | root-only |
|--------|-----------|-----------|
| `test-java-gateway-service` | 10000 | 10000 |
| `test-java-order-service` | 10000 | 52 |
| `load-generator` | 3969 | 3969 |

✅ `test-java-order-service` 大量 span 由该服务发出但 root span 极少，rootServiceName 过滤正确区分。

### 3.3 结论

| 路径 | 可行性 | 方案 |
|------|--------|------|
| Tag Values 端点 | ✅ 可行 | root spans 的 `serviceName` terms agg |
| 搜索过滤 | ✅ 可行 | `serviceName=xxx` + `parentSpanId` 不存在的复合 bool 查询 |

---

## 4. 实施方案

### 4.1 总体架构

```
TraceQL: { trace:rootName = "GET /api" }
    │
    ▼ planner.go
QueryPlan.RootName = "GET /api"    (从 Tags 中提取，不混入通用 Tags)
    │
    ▼ tempo_handler.go
TraceQuery.RootName = "GET /api"
    │
    ▼ trace_reader.go: buildTraceSearchQuery()
bool must: [name="GET /api" AND must_not: parentSpanId exists]
    │
    ▼ ES
root span 过滤查询
```

### 4.2 变更清单

#### Sprint 1: statusMessage 搜索过滤修正

| 文件 | 变更 | 说明 |
|------|------|------|
| `extension/observabilitystorageext/provider/elasticsearch/trace_reader.go` | 修改 `resolveTagTermClauses` | 对 `status.message` 字段用 `match` query 替代 `term` |

改动：

```go
func resolveTagTermClauses(key, value string) []map[string]any {
    fields, val := resolveTagESFields(key, value)
    clauses := make([]map[string]any, 0, len(fields))
    for _, f := range fields {
        if f == FieldStatus+".message" {
            // status.message 是 text 类型（无 .keyword 子字段），
            // 必须用 match query 而不能用 term query。
            clauses = append(clauses, map[string]any{
                "match": map[string]any{f: val},
            })
        } else {
            clauses = append(clauses, esq.T(f, val))
        }
    }
    return clauses
}
```

#### Sprint 2: rootName / rootServiceName 全链路支持

| 文件 | 变更类型 | 说明 |
|------|----------|------|
| `extension/observabilitystorageext/provider/elasticsearch/types_reader.go` | 修改 | `TraceQuery` 新增 `RootName`、`RootService` 字段 |
| `extension/observabilitystorageext/provider.go` | 修改 | `TraceQuery` 新增 `RootName`、`RootService` 字段 |
| `extension/observabilitystorageext/reader_adapter.go` | 修改 | `SearchTraceSummaries` 透传新字段 |
| `extension/observabilitystorageext/provider/elasticsearch/trace_reader.go` | 修改 | `buildTraceSearchQuery` + `resolveIntrinsicTagValues` |
| `extension/observabilitystorageext/provider.go` | 修改 | `TraceReader` 接口新增 `GetIntrinsicTagValues` |
| `extension/observabilitystorageext/reader_adapter.go` | 修改 | `traceReaderAdapter` 实现 `GetIntrinsicTagValues` |
| `extension/observabilitystorageext/provider/elasticsearch/trace_reader.go` | 新增方法 | `GetIntrinsicTagValues` (root span ES agg) |
| `extension/adminext/traceql/planner.go` | 修改 | `trace:rootName` → `RootName`，`trace:rootService` → `RootService` |
| `extension/adminext/tempo_handler.go` | 修改 | `resolveIntrinsicTagValuesWithFilter` 实现 rootName/rootServiceName |

**搜索过滤改动**（`buildTraceSearchQuery`）：

```go
// ── Intrinsic: rootName (root span name match) ──
if tq.RootName != "" {
    qb.Raw(map[string]any{
        "bool": map[string]any{"must": []map[string]any{
            esq.T(FieldName, tq.RootName),
            esq.MustNotQ(esq.ExistsQ(FieldParentSpanID)),
        }},
    })
}
// ── Intrinsic: rootService (root span service match) ──
if tq.RootService != "" {
    qb.Raw(map[string]any{
        "bool": map[string]any{"must": []map[string]any{
            esq.T(FieldServiceName, tq.RootService),
            esq.MustNotQ(esq.ExistsQ(FieldParentSpanID)),
        }},
    })
}
```

**Tag Values 改动**（新增 `GetIntrinsicTagValues`）：

```go
func (r *TraceReader) GetIntrinsicTagValues(ctx context.Context, field string, timeRange TimeRange, appID string) ([]string, error) {
    searchReq := &SearchRequest{
        Query: map[string]any{
            "bool": map[string]any{
                "must":     []map[string]any{r.timeRangeQuery(timeRange)},
                "must_not": []map[string]any{esq.ExistsQ(FieldParentSpanID)},
            },
        },
        Size: 0,
        Aggregations: map[string]any{
            "values": map[string]any{
                "terms": map[string]any{
                    "field": field,
                    "size":  1000,
                },
            },
        },
    }
    // ... parse terms agg buckets and return values
}
```

**Planner 改动**：

```go
// traceql/planner.go: 当前 trace:rootName → p.Tags["rootName"]
// 改为独立字段，不混入通用 Tags：
case key == IntrinsicRootName && cond.Scope == "trace":
    if cond.Operator == "=" && valStr != "" {
        p.RootName = valStr
    }
case key == IntrinsicRootServiceName && cond.Scope == "trace":
    if cond.Operator == "=" && valStr != "" {
        p.RootService = valStr
    }
```

### 4.3 Tag Values 响应预期

| intrinsic tag | 响应 |
|---------------|------|
| `statusMessage` | `[]` (空，ES text 字段限制) |
| `rootName` | `["GET order-service-route", "Create connection", ...]` |
| `rootServiceName` | `["test-java-gateway-service", "load-generator", ...]` |

### 4.4 搜索过滤响应预期

| TraceQL | ES 查询 | 行为 |
|---------|---------|------|
| `{ trace:rootName = "GET /api" }` | `name="GET /api" AND must_not: parentSpanId exists` | 按 root span 过滤 |
| `{ trace:rootService = "gateway" }` | `serviceName="gateway" AND must_not: parentSpanId exists` | 按 root span 过滤 |
| `{ status.message = "timeout" }` | `match: status.message="timeout"` | 全文搜索 status message |

---

## 5. 状态

- [x] ES mapping 已确认：`status.message=text`, `parentSpanId=keyword`, `name=keyword`, `serviceName=keyword`
- [x] `status.message` term query → 0 结果（bug 已证实）
- [x] `status.message` match query → 正确结果
- [x] `status.message` terms aggregation → 不可行（text 字段限制）
- [x] `rootName` 搜索过滤 → ES 验证通过（name + parentSpanId 不存在）
- [x] `rootServiceName` 搜索过滤 → ES 验证通过（serviceName + parentSpanId 不存在）
- [x] `rootName` Tag Values → ES 验证通过（root span name terms agg）
- [x] `rootServiceName` Tag Values → ES 验证通过（root span serviceName terms agg）
- [x] **Sprint 1 实施完成**: `resolveTagTermClauses` 中 `status.message` 字段 `term` → `match` query
- [x] **Sprint 2 实施完成**: rootName/rootServiceName 全链路支持
- [x] **设计评审 + 重构**: 接口消除 ES 细节泄漏 + Handler DRY 消除
- [x] **Sprint 2.5 实施完成**: TraceMetrics 查询路径（`TraceMetricsQuery` + `buildMetricsFilter` + `by(rootName)`）补全
- [x] **11 个新单元测试**: ES builder (4) + Planner != nil (2) + Metrics filter (4) + AttributeResolver (4) +
- [x] ES 集成测试全部通过

### 设计评审发现的问题与修复

| # | 问题 | 严重度 | 修复 |
|---|------|--------|------|
| 1 | `GetIntrinsicTagValues(esField)` 接口暴露 ES 字段名给调用方（违反 provider-agnostic 抽象） | 🔴 | 拆为 `ListRootSpanNames()` / `ListRootSpanServices()`，ES 字段名内部化 |
| 2 | Handler `resolveIntrinsicTagValuesWithFilter` 中 rootName/rootServiceName 两个 case 代码完全重复（违反 DRY） | 🟡 | 抽取 `fetchRootSpanTagValues` 统一 helper，通过函数参数化消除重复 |
| 3 | 新代码零单元测试覆盖（违反可测试性） | 🔴 | 新增 15 个测试：query builder 6 个 + planner 9 个 |

### 单元测试覆盖

| 测试文件 | 新增测试（15 个） | 覆盖内容 |
|----------|-------------------|----------|
| `trace_reader_query_test.go` | `TestBuildTraceSearchQuery_RootName` | root span name 复合 bool query |
| | `TestBuildTraceSearchQuery_RootService` | root span service 复合 bool query |
| | `TestBuildTraceSearchQuery_RootName_NotMixedIntoTags` | RootName 不污染 Tags 路径 |
| | `TestBuildTraceSearchQuery_StatusMessage` | status.message match query |
| | `TestBuildTraceSearchQuery_StatusMessageNot` | TagsNot + match query |
| | `TestBuildTraceSearchQuery_StatusMessageExists` | TagsExists + status.message |
| `traceql_test.go` | `TestPlanRootName_FromTags` | Planner RootName 提取 |
| | `TestPlanRootService_FromTags` | Planner RootService 提取 |
| | `TestPlanRootName_NotMixedIntoTags` | RootName 不混入 Tags |
| | `TestPlanRootService_NotMixedIntoTags` | RootService 不混入 Tags |
| | `TestPlanRootName_StructuralRelaxPreserved` | structural OR 公共 rootName 保留 |
| | `TestPlanRootService_StructuralRelaxCleared` | structural OR 非公共 rootService 清除 |
| | `TestPlanRootName_StructuralRelaxDifferentValue` | structural OR 不同值 rootName 清除 |

### Sprint 2 完成摘要

**变更文件（10 个）**：

| 文件 | 变更 |
|------|------|
| `storedmodel/trace_query.go` | +`RootName`, `RootService` |
| `types.go` | 公共 `TraceQuery` +`RootName`, `RootService` |
| `provider.go` | 接口 +`ListRootSpanNames` / `ListRootSpanServices` |
| `reader_adapter.go` | 透传 + adapter 实现 + `fetchRootSpanTagValues` |
| `pg_reader_adapter.go` | PG 空实现（接口兼容） |
| `trace_reader.go` | `buildTraceSearchQuery` + `listIntrinsicTagValues` (private) |
| `trace_reader_query_test.go` | +6 个单元测试 |
| `planner.go` | `ExecutionPlan` + extract + `safeConditions` + structural relaxation |
| `traceql_test.go` | +9 个单元测试 |
| `tempo_handler.go` | 搜索通路 + tag values + `fetchRootSpanTagValues` helper |

**ES 验证**：

| 验证项 | 结果 |
|--------|------|
| rootName Tag Values (root span `name` terms agg) | ✅ 10 个 distinct root names |
| rootServiceName Tag Values (root span `serviceName` terms agg) | ✅ 7 个 distinct root services |
| rootName 搜索过滤 (`name=X` + `parentSpanId` 不存在) | ✅ 8418 个 root spans |
| rootService 搜索过滤 (`serviceName=X` + `parentSpanId` 不存在) | ✅ 10000 个 root spans |

### Sprint 2.5: TraceMetrics 查询路径补全

**发现**：Sprint 2 只修复了**搜索路径**（`TraceQuery` + `buildTraceSearchQuery`），但**metrics 查询路径**（`TraceMetricsQuery` + `buildMetricsFilter` + `buildMetricsAggTree`）完全遗漏，导致查询 `{rootName != nil} | rate() by(rootName)` 返回空。

**根因**：
- `rootName != nil` → Planner 推入 `TagsExists=["rootName"]` → `AttributeResolver` → `"attributes.rootName"` → ES 不存在
- `by(rootName)` → `AttributeResolver` → `"attributes.rootName"` → terms agg 空 buckets

**修复**：

| 文件 | 变更 |
|------|------|
| `trace_metrics.go` (public) | +`RootName`, `RootService` |
| `types_reader.go` (ES) | +`RootName`, `RootService` |
| `reader_adapter.go` | adapter 透传 |
| `trace_metrics.go` (ES) | `buildMetricsFilter` 加 root span 复合 bool 查询 |
| `attribute_resolver.go` | `rootName` → `name`, `rootServiceName` → `serviceName` |
| `planner.go` | `rootName != nil` / `rootServiceName != nil` → `IsRoot=true`（不推 TagsExists） |
| `tempo_handler.go` | `executeTempoMetricsQuery` + `executeTempoMetricsQueryRange` 透传 + 补 TagsExists/TagsNot/TagsRegex |
| `trace_metrics_test.go` (新) | 8 个单元测试 |
| `traceql_test.go` | +2 个 != nil planner 测试 |

**查询修复前后对比**：

```
查询: {nestedSetParent<0 && true && rootName != nil} | rate() by(rootName)

修复前 planner:
  TagsExists = ["rootName"]  → ES: exists attributes.rootName  → 0 结果

修复后 planner:
  IsRoot = true              → ES: parentSpanId 不存在
  by(rootName)               → ES: terms agg on name 字段
```
