# MetricGenerator 设计方案

## 1. 需求背景

### 1.1 当前架构

```
┌────────────────────────────────────────────────────────────────────────────┐
│                    OpenTelemetry Collector Pipeline                          │
│                                                                            │
│  Traces ── [Receiver] ── [SpanMetrics Connector] ── [Exporter] ──► ES (metrics index)
│       │                                                                    │
│       └───────────────── [Exporter] ─────────────────────────────► ES (traces index)
│                                                                            │
└────────────────────────────────────────────────────────────────────────────┘
```

- **Traces 索引** (`otel-traces-*`)：原始 span 数据（startTimeUnixNano, durationNano, parentSpanId, status.code 等）
- **Metrics 索引** (`otel-metrics-*`)：SpanMetrics Connector 预聚合数据（calls_total, duration_milliseconds）

### 1.2 问题分析

当前 TraceQL metrics 查询（如 `{nestedSetParent<0} | histogram_over_time(duration)`）有两条路径：

| 路径 | 数据源 | 性能 | 适用场景 |
|------|--------|------|----------|
| TraceReader 实时聚合 | ES traces 索引（原始 span） | 慢（需扫描大量原始数据） | 复杂查询、自定义属性过滤 |
| MetricReader 预聚合 | ES metrics 索引（SpanMetrics Connector 写入） | 快（毫秒级） | 标准 RED 查询 |

**核心矛盾**：

1. **SpanMetrics Connector 的局限**：它是 OTel Collector 的通用组件，只按配置的 dimension 聚合。不支持 TraceQL 的 `nestedSetParent<0`（root span）、自定义 attribute 组合过滤等
2. **实时聚合性能差**：从原始 span 聚合 30 天的 `rate()` 需要扫描 7000+ 万 span，可能超时
3. **标准仪表盘查询不需要走 trace 索引**：80%+ 的 Grafana 面板查询是标准 RED metrics

### 1.3 Tempo 的解法（参考）

Tempo 使用**混合架构**：

| 数据时间窗口 | 查询路径 | 数据源 | 性能 |
|---|---|---|---|
| 近期数据（数分钟内） | Metrics Generator / LiveStore | 实时流处理的 span 数据（内存中已聚合） | 极快 |
| 历史数据 | Backend block scan | Parquet 列式存储的原始 span | 中等（靠并行分片补偿） |

**Tempo Metrics Generator 核心设计**：
- 流式处理收到的每一个 span
- 内存中维护时间窗口内的聚合状态（histogram buckets, counter 等）
- 定期通过 Remote Write 推送到 Prometheus/Mimir
- 支持 span-metrics（RED metrics）和 service-graph（服务拓扑）两种 generator

## 2. 方案设计

### 2.1 候选方案对比

| 方案 | 描述 | 优点 | 缺点 |
|------|------|------|------|
| **A. 内置 MetricGenerator** | 在 Collector 内部实现流式聚合组件 | 低延迟、可定制维度、支持 root span 标记 | 开发量较大、内存管理复杂 |
| B. 增强 SpanMetrics Connector 配置 | 通过配置更多 dimension 增加 Connector 覆盖度 | 无需开发 | 维度爆炸、无法支持 root span 概念 |
| C. 定时预计算任务 | 定时从 traces 索引聚合写入 metrics 索引 | 实现简单 | 数据延迟高、不适合实时仪表盘 |
| **D. 混合方案（推荐）** | MetricGenerator + 查询路由优化 | 兼顾性能与灵活性、渐进式实施 | 需要两步走 |

### 2.2 推荐方案：D. 混合方案

```
┌─────────────────────────────────────────────────────────────────────────────────┐
│                      查询路由（handleTempoMetricsQueryRange）                      │
│                                                                                 │
│  1. Parse TraceQL AST                                                           │
│                                                                                 │
│  2. canMapToPreAggregated(plan)?                                                │
│     ┌───────────────────────────────────────────────────────────────────────┐   │
│     │ YES: 查询只用了预聚合支持的维度                                          │   │
│     │   → MetricReader（查预聚合数据，毫秒级响应）                              │   │
│     └───────────────────────────────────────────────────────────────────────┘   │
│     ┌───────────────────────────────────────────────────────────────────────┐   │
│     │ NO: 查询包含复杂过滤（root span, 自定义 attr, duration 范围 等）          │   │
│     │   → TraceReader 实时聚合（较慢但灵活，限制时间范围避免超时）               │   │
│     └───────────────────────────────────────────────────────────────────────┘   │
│                                                                                 │
└─────────────────────────────────────────────────────────────────────────────────┘
```

## 3. 实施方案

### Sprint 1：查询路由优化（低成本高收益）

**目标**：让标准 RED 查询走预聚合 MetricReader，复杂查询 fallback 到 TraceReader。

**核心逻辑** — `canMapToPreAggregated(plan)` 判断条件：

能映射到预聚合的查询特征：
- **函数**：`rate()`, `count_over_time()`, `histogram_over_time(duration)`, `quantile_over_time(duration)`
- **过滤条件**：只包含 SpanMetrics Connector 维度的等值过滤（service.name, name, kind, status）
- **没有** `IsRoot` 过滤（SpanMetrics 不区分 root span）
- **没有** duration 范围过滤（SpanMetrics 没有这个维度）
- **没有** 自定义 span attribute 过滤（除非 Connector 配置了该 dimension）

**调整后的优先级**：
```
MetricReader（预聚合，快速路径） → TraceReader（实时聚合，灵活路径） → 报错
```

### Sprint 2：内置 MetricGenerator（流式预聚合增强）

**目标**：实现自己的 MetricGenerator，补充 SpanMetrics Connector 无法覆盖的维度。

#### 3.1 架构设计

```
┌─────────────────────────────────────────────────────────────────────┐
│                    MetricGenerator (Processor)                        │
│                                                                     │
│  Input: Span stream (from Receiver, before Exporter)                │
│                                                                     │
│  ┌─────────────────────────────────────────────────────────────┐    │
│  │  SpanMetrics Generator                                       │    │
│  │                                                             │    │
│  │  维度（labels）：                                             │    │
│  │    - service.name                                           │    │
│  │    - operation (span name)                                  │    │
│  │    - span_kind                                              │    │
│  │    - status_code                                            │    │
│  │    - is_root (bool) ← SpanMetrics Connector 不支持的          │    │
│  │    - 可配置的自定义 attribute dimensions                       │    │
│  │                                                             │    │
│  │  输出 metrics：                                              │    │
│  │    - traces.spanmetrics.calls (counter)                     │    │
│  │    - traces.spanmetrics.duration_milliseconds (histogram)    │    │
│  │                                                             │    │
│  └─────────────────────────────────────────────────────────────┘    │
│                                                                     │
│  ┌─────────────────────────────────────────────────────────────┐    │
│  │  Aggregation Engine                                          │    │
│  │                                                             │    │
│  │  - Ring buffer: 保持最近 N 分钟的聚合窗口                       │    │
│  │  - Flush interval: 每 60s 将完成的窗口写入 metrics 索引         │    │
│  │  - 每个 label 组合 → 一个 time series                        │    │
│  │  - Counter: 累加计数                                         │    │
│  │  - Histogram: 维护 bucket boundaries + counts               │    │
│  │                                                             │    │
│  └─────────────────────────────────────────────────────────────┘    │
│                                                                     │
│  Output: → ES metrics index (same format as SpanMetrics Connector)  │
│                                                                     │
└─────────────────────────────────────────────────────────────────────┘
```

#### 3.2 关键设计决策

| 决策点 | 选择 | 理由 |
|--------|------|------|
| **组件类型** | OTel Processor（而非 Connector） | Processor 在 pipeline 中处于 Receiver 之后、Exporter 之前，可以同时输出到 trace 和 metric 两个 pipeline |
| **is_root 判断** | `parentSpanId == ""` 或 `parentSpanId == "0000000000000000"` | 与 TraceReader 中的 root span 判断逻辑一致 |
| **输出格式** | 与 SpanMetrics Connector 完全兼容 | MetricReader 无需修改，统一查询路径 |
| **内存管理** | 每个 label 组合限制 MaxSeries=10000 | 防止维度爆炸导致 OOM |
| **Flush 策略** | 对齐到分钟边界 flush | 方便 MetricReader 聚合时对齐 |
| **Histogram buckets** | 预定义 duration 桶边界: [1, 2, 5, 10, 25, 50, 100, 250, 500, 1000, 2500, 5000, 10000] ms | 与 Prometheus 标准 HTTP duration 桶一致 |

#### 3.3 配置示例

```yaml
processors:
  metric_generator:
    # 聚合窗口大小（保持多少个窗口在内存中）
    aggregation_window: 60s
    # 最大 series 数量（防止维度爆炸）
    max_series: 10000
    # 额外维度（除标准 service/operation/kind/status 外）
    dimensions:
      - is_root        # 是否 root span
      - http.method    # 自定义 attribute
      - http.status_code
    # Histogram bucket 边界（毫秒）
    histogram_buckets: [1, 2, 5, 10, 25, 50, 100, 250, 500, 1000, 2500, 5000, 10000]
    # 输出 metric 名称前缀
    metric_prefix: "traces.spanmetrics"
```

#### 3.4 数据流对比

**现有（SpanMetrics Connector）**：
```
Traces Receiver → SpanMetrics Connector → Prometheus Exporter → ES metrics index
                                          (无 is_root 维度)
```

**新增（MetricGenerator）**：
```
Traces Receiver → MetricGenerator Processor → ES Exporter → ES metrics index
                  (包含 is_root 维度)          (直接写 metrics 格式)
```

**兼容策略**：两者可以并存，MetricGenerator 写入的数据与 SpanMetrics Connector 格式一致，MetricReader 统一查询。

### Sprint 3：查询路由增强

当 MetricGenerator 上线后，`canMapToPreAggregated` 的覆盖范围将扩展：

```diff
 可映射到预聚合的查询特征：
   函数：rate(), count_over_time(), histogram_over_time(duration), quantile_over_time(duration)
   过滤条件：service.name, name, kind, status 等值过滤
-  不支持：IsRoot 过滤
+  支持：IsRoot 过滤（MetricGenerator 的 is_root 维度）
+  支持：配置的自定义 attribute 过滤
```

## 4. 性能对比预估

| 查询场景 | 现有（TraceReader 实时聚合） | 目标（MetricReader 预聚合） | 提升倍数 |
|----------|------|------|------|
| 1h rate() | ~2s | ~50ms | 40x |
| 24h rate() | ~10s | ~100ms | 100x |
| 7d histogram_over_time() | ~30s | ~200ms | 150x |
| 30d rate() | 可能超时 | ~500ms | ∞ |

## 5. 实施计划

| 阶段 | 内容 | 验收标准 | 优先级 |
|------|------|----------|--------|
| Sprint 1 | 查询路由优化：MetricReader 为主路径，TraceReader 为 fallback | 标准 RED 查询走预聚合，响应 <200ms | P0 |
| Sprint 2 | MetricGenerator 核心实现：流式聚合 + is_root 维度 | 支持 `{nestedSetParent<0} \| rate()` 走预聚合 | P1 |
| Sprint 3 | 维度扩展 + 查询路由增强 | 覆盖 90%+ 常用 TraceQL metrics 查询 | P2 |

## 6. 风险与缓解

| 风险 | 影响 | 缓解措施 |
|------|------|----------|
| MetricGenerator 内存占用过大 | OOM crash | MaxSeries 限制 + 定时淘汰冷 series |
| 维度爆炸（高基数 attribute） | 写入放大、存储膨胀 | 只允许配置低基数维度，运行时监控 series 数量 |
| 预聚合与实时聚合结果不一致 | 用户困惑 | 确保聚合逻辑完全一致，加入 hint 标注数据来源 |
| SpanMetrics Connector 冲突 | 重复数据 | MetricGenerator 使用不同 metric name 前缀或标记，逐步替换 |

## 7. 遗留问题

- [ ] Sprint 1：实现 `canMapToPreAggregated` 函数并调整查询路由优先级
- [ ] Sprint 2：MetricGenerator 组件开发（Processor 框架 + 聚合引擎）
- [ ] Sprint 3：与 SpanMetrics Connector 的共存/迁移策略
- [ ] 长期：是否需要 service-graph generator（类似 Tempo 的服务拓扑图）

## 8. 变更记录

| 日期 | 变更内容 |
|------|----------|
| 2026-07-14 | 创建文档；修复 trace_metrics.go 中 date_histogram → histogram 聚合 bug（ES 对 long 字段不支持 date_histogram 的 fixed_interval 数字值） |
