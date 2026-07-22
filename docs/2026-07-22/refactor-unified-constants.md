# 统一常量重构

> 状态：已实施  
> 日期：2025-07-21  
> 关联文件：`extension/adminext/constants.go` (新增), `extension/adminext/traceql/ast.go`, 以及所有 handler/test 文件

---

## 1. 背景

经过 PromQL topk/bottomk 和 Grafana Tempo 搜索标签验证后，代码中硬编码字符串散落在多处：

| 硬编码类型 | 分散位置 | 大致数量 |
|-----------|---------|---------|
| `"__name__"` | `prometheus_handler.go` | 15+ 处 |
| `"le"` | `prometheus_handler.go` | 10+ 处 |
| `"histogram_quantile"` | `prometheus_handler.go` | 6+ 处 |
| `"vector"` / `"matrix"` | `prometheus_handler.go` | 8+ 处 |
| aggregation funcs (`"sum"`, `"avg"`, ...) | `prometheus_handler.go` | 10+ 处 |
| intrinsic tag names | `tempo_handler.go`, `traceql/ast.go`, `traceql/evaluator.go`, `traceql/planner.go` | 30+ 处 |
| span kind / status values | `tempo_handler.go`, `traceql/ast.go` | 多处 |
| common attribute keys (fallback) | `tempo_handler.go` (2处重复) | 2 处 |
| scope names (`"resource"`, `"span"`, `"intrinsic"`) | `tempo_handler.go` | 多处 |
| OTel span attribute keys (`"promql.*"`, `"es.*"`, `"error.type"`) | `prometheus_handler.go` | 15+ 处 |

问题：
- 拼写错误难以发现
- 修改需要全局搜索替换，容易遗漏
- 同一值（如 intrinsic tag names）在 `tempo_handler.go` 和 `traceql` 包中独立维护，可能不一致

## 2. 方案

### 2.1 新增：`extension/adminext/constants.go`

统一管理 adminext 包内所有字符串常量，按功能域分组：

- **Prometheus Labels**: `PromLabelName`, `PromLabelIgnoreUsage`, `PromLabelLe`, `PromInternalLabelPrefix`
- **PromQL Functions**: `AggHistogramQuantile`, `AggTopK`, `AggBottomK`, `AggSum`, `AggAvg`, `AggMax`, `AggMin`, `AggCount`, `FnRate`, `FnIncrease`, `FnIrate`
- **PromQL Aggregation List**: `AggFuncs` (slice, 用于 parseAggWrapper)
- **Histogram Sub-Series**: `HistogramSubSum`, `HistogramSubBucket`, `HistogramSuffixSum`, `HistogramSuffixBucket`, `HistogramSuffixTotal`
- **Response Types**: `ResultTypeVector`, `ResultTypeMatrix`
- **OTel Span Attributes**: `SpanAttrPromQLExpr`, `SpanAttrPromQLMetric`, ... (15个)
- **Tempo Intrinsic Tags**: `TempoIntrinsicName`, `TempoIntrinsicKind`, `TempoIntrinsicStatus`, `TempoIntrinsicDuration`, `TempoIntrinsicStatusMessage`, `TempoIntrinsicRootName`, `TempoIntrinsicRootServiceName`
- **Tempo NestedSet Fields**: `TempoIntrinsicNestedSetParent`, `TempoIntrinsicNestedSetLeft`, `TempoIntrinsicNestedSetRight`, `TempoIntrinsicTraceDuration`
- **Tempo Intrinsic Tag List**: `TempoIntrinsicTags` (slice)
- **Tempo Span Kind Values**: `SpanKindUnspecifiedStr`, ... + `TempoSpanKindValues`
- **Tempo Status Code Values**: `StatusCodeUnsetStr`, ... + `TempoStatusCodeValues`
- **Tempo Scope Names**: `TempoScopeResource`, `TempoScopeSpan`, `TempoScopeIntrinsic`, `TempoScopeEvent`, `TempoScopeTrace`
- **Tempo Common Attribute Keys**: `TempoCommonSpanAttributeKeys`, `TempoCommonResourceAttributeKeys`, `TempoV1CommonSpanAttributeKeys` (slice)
- **OTel Semantic Convention Keys**: `OTelAttrServiceName`, `OTelAttrSpanKind`

### 2.2 新增：`extension/adminext/traceql/ast.go` Intrinsic 常量

因为 `traceql` 是 `adminext` 的子包，不能直接引用父包常量（循环依赖），所以在 `traceql/ast.go` 中新增独立常量（值相同）：

```go
const (
    IntrinsicName            = "name"
    IntrinsicStatus          = "status"
    IntrinsicKind            = "kind"
    IntrinsicDuration        = "duration"
    IntrinsicNestedSetParent = "nestedSetParent"
    IntrinsicNestedSetLeft   = "nestedSetLeft"
    IntrinsicNestedSetRight  = "nestedSetRight"
    IntrinsicRootName        = "rootName"
    IntrinsicRootServiceName = "rootServiceName"
    IntrinsicTraceDuration   = "traceDuration"
)
```

### 2.3 原有变量保留为别名（向后兼容）

为避免大范围改接口，保留了旧变量名作为新常量的别名：
- `tempoIntrinsicTags = TempoIntrinsicTags`
- `tempoSpanKindValues = TempoSpanKindValues`
- `tempoStatusCodeValues = TempoStatusCodeValues`

## 3. 变更清单

| 文件 | 变更说明 |
|------|---------|
| `extension/adminext/constants.go` | **新增** — 集中定义所有字符串常量 |
| `extension/adminext/prometheus_handler.go` | 替换所有硬编码字符串为常量 |
| `extension/adminext/prometheus_handler_test.go` | 测试中使用常量 |
| `extension/adminext/tempo_handler.go` | 替换所有 hardcoded tag/scope/value |
| `extension/adminext/influxdb_handler.go` | `"__name__"` → `PromLabelName` |
| `extension/adminext/traceql/ast.go` | 新增 Intrinsic 常量 + IsIntrinsic() 使用常量 |
| `extension/adminext/traceql/evaluator.go` | hardcoded key → 常量 |
| `extension/adminext/traceql/planner.go` | hardcoded key → 常量 |

## 4. 验证结果

```
go test ./extension/adminext/...        → ok (0.949s)
go test ./extension/observabilitystorageext/... → all ok
```

无编译错误，所有现有测试通过，无回归。

## 5. 未完成项 / 遗留问题

- `traceql/evaluator.go` 和 `traceql/planner.go` 中仍然有一些 `"service.name"`、`"rootService"` 等硬编码字符串（存在于 map key 和简单字符串传递中），不影响功能但可后续进一步统一
- `label_translator.go` 中的 OTel 属性映射表暂时保留为 map literal，因为它们是 ES 存储层面的 key，与 `fields.go` 的 `Field*` 常量语义层级不同
