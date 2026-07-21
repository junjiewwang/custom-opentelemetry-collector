# Histogram Quantile — QueryFlat 方案设计

> 状态：✅ 已实施完成  
> 设计日期：2025-07-20  
> 实施日期：2025-07-21  
> 关联改动：`extension/adminext/prometheus_handler.go`, `extension/observabilitystorageext/`

---

## 1. 问题背景

当前 `histogram_quantile` 的 ES 查询使用 `QueryRaw`（composite aggregation + top_hits），存在三个根本缺陷：

### 1.1 top_hits size 受 ES 配置限制

- ES `max_inner_result_window` 默认 100
- Histogram range query（1h, step=15s）需要 ~240 个数据点/series
- 即使动态计算 limit 也依赖运维手动调 ES 配置
- **运行时隐患**：升级 ES / 新建索引都可能重置配置

### 1.2 composite 聚合的 labels 分组硬编码 painless script

```painless
doc['labels.client'].value + '|' + doc['labels.server'].value + '|' + doc['labels.connection_type'].value
```

- label key 变化时代码要改
- 违反开放封闭原则（OCP）

### 1.3 QueryRaw 语义不匹配

- `QueryRaw` 设计初衷：为 `rate()/increase()` 服务（少量 series，每个 series 取最新 N 点）
- `histogram_quantile` 需要：所有文档的 bucket_counts 字段，Go 侧做分组和聚合
- 两种场景的数据访问模式完全不同

---

## 2. 方案对比

| 维度 | 方案A：修 QueryRaw limit | 方案B：QueryFlat 平铺查询 | 方案C：ES scroll |
|------|-------------------------|--------------------------|------------------|
| ES 配置依赖 | ❌ 需改 max_inner_result_window | ✅ 无 | ✅ 无 |
| labels 硬编码 | ❌ painless script | ✅ Go 侧分组 | ✅ Go 侧分组 |
| 复杂度 | 低 | 中 | 高（scroll 状态管理） |
| 性能 | 中（聚合+top_hits 两层开销） | ✅ 最优（一次平铺 query） | ✅ 优（但有 round-trip 开销） |
| 数据量适配 | ❌ 受 top_hits 上限约束 | ✅ size 可任意设（ES 默认 10000） | ✅ 无限 |
| 接口复用性 | 低 | ✅ 通用 | 中 |
| 实际数据量 | — | 1h/15s = ~240 doc × N series，远小于 10000 | 不需要 scroll |

**结论：方案 B（QueryFlat）为最优解。**

---

## 3. 详细设计

### 3.1 新增类型定义

```go
// types.go

// MetricFlatQuery defines parameters for a flat document query.
// Returns all matching documents without ES-side grouping.
// Used by histogram_quantile which needs all bucket_counts documents
// and performs grouping + aggregation in Go.
type MetricFlatQuery struct {
    AppID       string            `json:"appId,omitempty"`
    MetricName  string            `json:"metric"`
    Labels      map[string]string `json:"labels,omitempty"`
    LabelMatch  map[string]string `json:"labelMatch,omitempty"`
    ServiceName string            `json:"service,omitempty"`
    TimeRange   TimeRange         `json:"timeRange"`
    // Fields controls which _source fields to return.
    // If empty, returns all fields.
    Fields      []string          `json:"fields,omitempty"`
    // MaxDocs is the hard cap on documents returned (default 10000).
    // Prevents unbounded memory usage.
    MaxDocs     int               `json:"maxDocs,omitempty"`
}

// MetricFlatResult holds flat query results — raw documents without grouping.
type MetricFlatResult struct {
    Samples []MetricSample
    Total   int64 // total matching docs in ES (for truncation detection)
}
```

### 3.2 接口扩展（向后兼容）

```go
// provider.go — MetricReader interface 新增方法

type MetricReader interface {
    // ... existing methods ...

    // QueryFlat returns all matching metric documents without ES-side grouping.
    // Unlike QueryRaw which groups by label set via composite aggregation,
    // QueryFlat returns a flat list for client-side grouping.
    // Designed for histogram_quantile which needs complete bucket data.
    QueryFlat(ctx context.Context, query MetricFlatQuery) (*MetricFlatResult, error)
}
```

### 3.3 ES 实现（metric_reader.go）

```go
func (r *MetricReader) QueryFlat(ctx context.Context, query MetricFlatQuery) (*MetricFlatResult, error) {
    esQuery := r.buildFlatQueryFilter(query) // 复用已有 filter 构建逻辑

    maxDocs := query.MaxDocs
    if maxDocs <= 0 {
        maxDocs = 10000 // ES default max_result_window
    }

    sourceFields := query.Fields
    if len(sourceFields) == 0 {
        sourceFields = []string{
            FieldMetricTimeUnixMilli, FieldMetricValue,
            FieldMetricLabels, FieldMetricBucketCounts, FieldMetricExplicitBounds,
        }
    }

    searchReq := &SearchRequest{
        Query:  esQuery,
        Size:   maxDocs,
        Sort:   []map[string]any{{FieldMetricTimeUnixMilli: map[string]any{"order": "asc"}}},
        Source: sourceFields,
    }

    resp, err := r.client.Search(ctx, r.indexPattern(query.AppID), searchReq)
    if err != nil {
        return nil, fmt.Errorf("metric flat query failed: %w", err)
    }

    samples := make([]MetricSample, 0, len(resp.Hits.Hits))
    for _, hit := range resp.Hits.Hits {
        samples = append(samples, r.hitToSample(hit))
    }

    return &MetricFlatResult{
        Samples: samples,
        Total:   resp.Hits.Total.Value,
    }, nil
}
```

### 3.4 Handler 层改造

```go
// prometheus_handler.go

func (e *Extension) execHistogramQuantileRange(...) *promQueryData {
    flatQuery := observabilitystorageext.MetricFlatQuery{
        MetricName: expr.MetricName,
        Labels:     filterInternalLabels(labels),
        TimeRange:  observabilitystorageext.TimeRange{Start: lookbackStart, End: end},
    }

    result, err := e.storageMetricReader.QueryFlat(r.Context(), flatQuery)
    if err != nil { ... }

    // 截断检测
    if result.Total > int64(len(result.Samples)) {
        e.logger.Warn("histogram_quantile data truncated",
            zap.Int64("total", result.Total),
            zap.Int("returned", len(result.Samples)))
    }

    // Go 侧按 labels 分组（替代 ES painless script）
    groups := groupSamplesByLabels(result.Samples)

    // 对每个 group，做滑动窗口 quantile 计算
    // 复用现有 AggregateHistogramSamples + ComputeHistogramQuantile
}

// groupSamplesByLabels 按 labels map 分组，纯 Go 实现
func groupSamplesByLabels(samples []observabilitystorageext.MetricSample) map[string][]HistogramSample {
    groups := make(map[string][]HistogramSample)
    for _, s := range samples {
        key := sortedLabelKey(s.Labels)
        groups[key] = append(groups[key], HistogramSample{
            TimestampMs:  s.TimestampMs,
            Value:        s.Value,
            BucketCounts: s.BucketCounts,
            Bounds:       s.Bounds,
        })
    }
    return groups
}
```

---

## 4. 设计原则验证

| 原则 | 满足情况 |
|------|---------|
| **SRP** | QueryFlat 只负责"取文档"，分组/聚合/计算全在 handler + histogram_calc |
| **OCP** | 新增方法，不改已有 QueryRaw 逻辑 |
| **DIP** | handler 依赖 MetricReader 接口，不依赖 ES 实现细节 |
| **DRY** | filter 构建复用 `buildMetricQuery`；分组逻辑复用 `sortedLabelKey` |
| **高内聚低耦合** | histogram_calc.go 零外部依赖；ES 层只做 IO；handler 做编排 |
| **可扩展** | 未来其他需要全量文档的 PromQL 函数可直接复用 QueryFlat |
| **健壮性** | MaxDocs 防 OOM + Total 截断检测 + nil 兜底 |

---

## 5. 改动范围

| 文件 | 改动内容 | 预估行数 |
|------|---------|---------|
| `observabilitystorageext/provider.go` | 接口 +1 方法 | ~5 行 |
| `observabilitystorageext/types.go` | +MetricFlatQuery / MetricFlatResult 类型 | ~20 行 |
| `observabilitystorageext/provider/elasticsearch/metric_reader.go` | +QueryFlat 实现 | ~35 行 |
| `observabilitystorageext/provider/elasticsearch/types_reader.go` | +MetricFlatQuery ES 类型 | ~15 行 |
| `observabilitystorageext/reader_adapter.go` | +QueryFlat 桥接 | ~15 行 |
| `observabilitystorageext/pg_reader_adapter.go` | +stub | ~3 行 |
| `adminext/prometheus_handler.go` | 改 instant/range 调用 QueryFlat + groupSamplesByLabels | ~40 行 |
| `adminext/prometheus_handler_test.go` | 新增/更新测试 | ~30 行 |

**总计：~160 行新增/改动，0 行 ES 配置修改，0 个 hardcoded label。**

---

## 6. 与旧实现的关系

改完后所有 rate/increase/histogram 路径不再调用 `QueryRaw`：

1. `QueryRaw` 中动态 limit 的改动 → **回滚**，恢复原始 `limit=100`
2. `QueryRaw` 中 painless script → 保持不动，各 rate/increase/histogram 路径已不再使用，待确认无外部调用后可整体废弃
3. `prometheus_handler.go` 中所有 rate/increase/histogram 数据处理函数（`execRateInstant`、`execRateRange`、`execHistogramQuantileInstant`、`execHistogramQuantileRange`）→ 全部改用 `QueryFlat`

---

## 7. 实施后变更摘要

### 新增文件
无

### 修改文件

| 文件 | 改动 |
|------|------|
| `observabilitystorageext/types.go` | +`MetricFlatQuery`, `MetricFlatResult` 类型; `MetricSample` +`Labels` 字段 |
| `observabilitystorageext/provider.go` | `MetricReader` 接口 +`QueryFlat` 方法 |
| `observabilitystorageext/provider/elasticsearch/types_reader.go` | ES 侧 +`MetricFlatQuery`, `MetricFlatResult` 类型; `MetricSample` +`Labels` 字段 |
| `observabilitystorageext/provider/elasticsearch/metric_reader.go` | +`QueryFlat` 实现, +`hitToSample`, 抽取 `buildMetricFilter` 公共方法; `parseRawResult` samples 填充 `Labels` |
| `observabilitystorageext/reader_adapter.go` | +`QueryFlat` 桥接; `QueryRaw` 桥接填充 `Labels` |
| `observabilitystorageext/pg_reader_adapter.go` | +`QueryFlat` stub |
| `adminext/histogram_calc.go` | `HistogramSample` +`Labels` 字段 |
| `adminext/prometheus_handler.go` | `execHistogramQuantileInstant`/`execHistogramQuantileRange` 改用 `QueryFlat`; `execRateRange`/`execRateInstant` 改用 `QueryFlat`; +`groupSamplesByLabels`, `groupMetricSamplesByLabels`, `checkFlatTruncation` |
| `docs/histogram-quantile-queryflat-design.md` | 状态更新为已实施 |

### 已回滚
- `QueryRaw` 动态 limit 计算代码 → 恢复原始 `limit=100`
- `prometheus_handler.go` 中移除 histogram limit 估算逻辑

### 补充修复（2025-07-21）

#### execRateRange 改用 QueryFlat

1. **`execRateRange` 改用 `QueryFlat`**：rate/increase/irate range 查询原来走 `QueryRaw`（composite+top_hits），受 ES `max_inner_result_window` 限制。改为走 `QueryFlat`（平铺查询，`size=N`）彻底消除该限制。

2. **新增 `groupMetricSamplesByLabels`**：Go 侧按 labels 分组 `MetricSample`（保持原类型，不转换为 `HistogramSample`），供 `computeRate` 直接使用。

3. **`checkFlatTruncation` 日志去硬编码**：从 "histogram_quantile" 改为通用 "QueryFlat"。

#### execRateInstant 改用 QueryFlat（数据路径统一）

4. **`execRateInstant` 改用 `QueryFlat`**：修复 Grafana Tempo service graph "No service graph data found" 问题。

   **根因**：`execRateInstant`（第703行）仍用 `QueryRaw`，默认 `limit=100`（ES 侧硬编码），Grafana Tempo service graph 发出 8 个 instant 查询（1h 窗口约需 ~240 samples），100 的 limit 导致数据被截断。

   **修复**：切换到 `QueryFlat` + `groupMetricSamplesByLabels` + `computeRateAtTime`，与 `execRateRange` 完全统一：
   - 消除了 `limit=100` 的 ES 硬截断（QueryFlat 默认 10000）
   - 消除了 painless script 中硬编码的 label 字段（`client|server|connection_type`）
   - 消除了 `execRateInstant` 和 `execRateRange` 之间的 DRY 违反

5. **统一数据路径**：`execRateInstant`、`execRateRange`、`execHistogramQuantileInstant`、`execHistogramQuantileRange` 全部走 `QueryFlat` + Go 侧分组。QueryRaw（composite+top_hits+painless script）仅在 histogram 外的所有路径中不再使用。

### 测试
- 所有已有测试通过（20+ histogram_calc 测试, handler 测试, storage 层测试）
- 无新增 lint 问题

### 关键设计决策变更
- 设计文档中原计划 `MetricFlatResult` 带并行 `Labels []map[string]string` 切片，实施时改为 `MetricSample` 自带 `Labels` 字段——更简洁，复用性更好
- `HistogramSample` 同步增加 `Labels` 字段，使 Go 侧分组链完全贯通

---

## 8. 遗留问题

- [ ] `MetricFlatQuery.MaxDocs` 默认 10000，极端场景（>10000 doc, 如 6h+ 窗口）可能截断数据，届时可考虑 `search_after` 分页
- [ ] PostgreSQL provider 的 QueryFlat 实现（当前 stub，待后续需求驱动）
- [ ] `buildMetricFilter` 公共方法可进一步被 `buildRawQueryFilter` 和 `buildQueryFilter` 复用（当前 `buildQueryFilter` 仍有独立实现）
- [ ] **考虑废弃 `QueryRaw`**：所有 rate/increase/histogram 路径已全部切到 `QueryFlat`，`QueryRaw`（composite+top_hits+painless script）后续可标记为 deprecated 并在确认无外部调用后移除
