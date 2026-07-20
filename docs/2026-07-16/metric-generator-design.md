# MetricGenerator Connector 设计方案

> **创建日期**: 2026-07-16 | **最后更新**: 2026-07-20
> **状态**: ✅ 方案确认，待实施

## 1. 背景与验证结论

### 1.1 为什么需要自研

当前 `spanmetricsconnector` 的不足（ES 实测验证）：

| 问题 | 证据 |
|------|------|
| **无法产出 ServiceGraph 指标** | ES 中只存在 `traces.spanmetrics.calls` + `traces.spanmetrics.duration`，没有 `traces_service_graph_*` |
| **duration 不是真 Histogram** | `type=histogram` 但仅存单值，缺 `bucket_counts`，Grafana 的 `le` 分桶查询返回空 |
| **缺少 `connection_type` 维度** | peer_service processor 已识别 DB/messaging，但 spanmetrics 不产出此标签 |

### 1.2 组件职责重新划分

经过 ES 实测验证后的最终定位：

```
traces pipeline:
  [tokenauth] → [peer_service processor] → [MetricGeneratorConnector] → [storage]
                    ↓ 双向写 peer.service         ↓ 纯读 + 聚合
                    ↓                              ↓
              保留，代码无需改动             新增，替换 spanmetricsconnector
```

- **peer_service processor** — 保留。负责 span 富化（双向写 `peer.service`），已在 `completePair` 中实现
- **MetricGeneratorConnector** — 新增。纯读 span attribute，内存 map 聚合，定期 flush 指标

---

## 2. 架构设计

### 2.1 Connector 内部架构

```
MetricGeneratorConnector
├── ConsumeTraces(ctx, ptrace.Traces)
│     └── for each span:
│           ├── REDGenerator.ProcessSpan()        → 纯 map 聚合
│           └── ServiceGraphGenerator.ProcessSpan() → 纯 map 聚合
│
├── MetricFlusher (定时 15s)
│     ├── REDGenerator.Collect()                  → pmetric.Metrics
│     ├── ServiceGraphGenerator.Collect()          → pmetric.Metrics
│     └── consumer.ConsumeMetrics(ctx, metrics)
│
└── Self-Metrics (运营指标)
```

**关键简化**：不需要 EdgeStore、不需要 ServiceGroupRouter。`peer.service` 属性已在 span 上，connector 只做 `read → aggregate`。

### 2.2 与现有组件的关系

| 组件 | 职责 | 改动 |
|------|------|------|
| **peer_service processor** | span 配对 + 双向写 `peer.service` | 无需改动 |
| **spanmetricsconnector** | — | ✅ S4 已移除 |
| **MetricGeneratorConnector** | RED + ServiceGraph 指标聚合 | Sprint 1-3 实现 |

### 2.3 Pipeline 配置（最终态）

```yaml
connectors:
  metricgen:
    metrics_flush_interval: 15s
    aggregation_temporality: "cumulative"
    red:
      dimensions: [http.method, http.status_code, http.route, rpc.method, rpc.service, peer.service]
      histogram:
        buckets: [5, 10, 25, 50, 100, 250, 500, 1000, 2500, 5000, 10000]
    service_graph:
      dimensions: [http.method]

service:
  pipelines:
    traces:
      receivers: [agent_gateway]
      processors: [tokenauth, peer_service, memory_limiter, batch]
      exporters: [metricgen, observability_storage]

    metrics/generated:
      receivers: [metricgen]
      processors: [batch]
      exporters: [observability_storage]
```

---

## 3. 指标定义（对齐 Tempo MetricGenerator）

### 3.1 RED 指标

| 指标名 | 类型 | Labels |
|--------|------|--------|
| `traces_spanmetrics_calls_total` | Counter | service.name, span.name, span.kind, status.code, peer.service, ...dims |
| `traces_spanmetrics_latency` | Histogram | 同上 |
| `traces_spanmetrics_size_total` | Counter（可选） | 同上 |

### 3.2 ServiceGraph 指标

| 指标名 | 类型 | Labels |
|--------|------|--------|
| `traces_service_graph_request_total` | Counter | client, server, connection_type, ...dims |
| `traces_service_graph_request_failed_total` | Counter | 同上 |
| `traces_service_graph_request_server_seconds` | Histogram | 同上 |
| `traces_service_graph_request_client_seconds` | Histogram | 同上 |
| `traces_service_graph_request_messaging_system_seconds` | Histogram | 同上 |
| `traces_service_graph_request_message_size_bytes` | Histogram | 同上 |

### 3.3 指标数据来源

```
看到 CLIENT span:
  service.name="tapm-api", peer.service="tapm_db"
  RED:
    → traces_spanmetrics_calls_total{service.name="tapm-api", peer.service="tapm_db"} += 1
    → traces_spanmetrics_latency{...} 记录 span.duration
  ServiceGraph:
    server = peer.service       → "tapm_db"
    client = service.name       → "tapm-api"
    → traces_service_graph_request_total{client="tapm-api", server="tapm_db"} += 1
    → traces_service_graph_request_client_seconds{...} 记录 span.duration

看到 SERVER span (如果有 peer.service):
  service.name="tapm_db", peer.service="tapm-api"
  ServiceGraph:
    server = service.name       → "tapm_db"
    client = peer.service       → "tapm-api"
    → traces_service_graph_request_server_seconds{...} 记录 span.duration

看到 Consumer/Producer span (有 messaging.xxx):
  service.name="tapm-api", peer.service="kafka/order-topic"
  ServiceGraph:
    server = peer.service       → "kafka/order-topic"
    client = service.name       → "tapm-api"
    → traces_service_graph_request_messaging_system_seconds{...} 记录 span.duration
    → traces_service_graph_request_message_size_bytes{...} 记录 messaging.message.body.size
```

**注意**：
- 外部服务不发 trace 时，server-side latency 会缺失（与 Tempo 一致）
- `message_size_bytes` 仅 Consumer span 有 `messaging.message.body.size` 属性时记录（ES 实测：Consumer 1801 条，Producer 0 条）

---

## 4. 核心组件设计

### 4.1 Connector 骨架

```go
package metricgenconnector

type metricGenConnector struct {
    config          *Config
    metricsConsumer consumer.Metrics
    redGen          *REDGenerator
    sgGen           *ServiceGraphGenerator
    flusher         *MetricFlusher
    logger          *zap.Logger
    done            chan struct{}
}

func (c *metricGenConnector) ConsumeTraces(ctx context.Context, td ptrace.Traces) error {
    appID := AppIDFromContext(ctx)
    rss := td.ResourceSpans()
    for i := 0; i < rss.Len(); i++ {
        rs := rss.At(i)
        resource := rs.Resource()
        svcName := extractServiceName(resource)
        for j := 0; j < rs.ScopeSpans().Len(); j++ {
            for k := 0; k < rs.ScopeSpans().At(j).Spans().Len(); k++ {
                span := rs.ScopeSpans().At(j).Spans().At(k)
                c.redGen.ProcessSpan(appID, svcName, resource, span)
                c.sgGen.ProcessSpan(appID, svcName, resource, span)
            }
        }
    }
    return nil
}
```

### 4.2 REDGenerator

纯 map 聚合，直接读 span attribute：

```go
type REDGenerator struct {
    config  *REDConfig
    mu      sync.RWMutex
    series  map[uint64]*redMetricSeries  // hash(dims) → series
    overflow atomic.Int64
}

func (g *REDGenerator) ProcessSpan(appID, svcName string, resource pcommon.Resource, span ptrace.Span) {
    dims := g.extractDimensions(resource, span)
    key := hashDimensions(dims)
    // 基数控制
    finalKey, overflow := g.checkCardinality(key)
    s := g.getOrCreateSeries(finalKey, dims, overflow)
    // 聚合
    s.calls.Add(1)
    s.latency.Record(spanDuration(span))
    if span.Status().Code() == ptrace.StatusCodeError {
        s.errors.Add(1)
    }
}
```

### 4.3 ServiceGraphGenerator

同样纯 map 聚合，按 `{client, server, connection_type}` 分组：

```go
type ServiceGraphGenerator struct {
    config *ServiceGraphConfig
    mu     sync.RWMutex
    edges  map[uint64]*sgEdgeSeries  // hash(client,server,connType) → series
}

func (g *ServiceGraphGenerator) ProcessSpan(appID, svcName string, resource pcommon.Resource, span ptrace.Span) {
    peerSvc := extractPeerService(span)
    if peerSvc == "" { return }

    connType := extractConnectionType(span)  // "" | "messaging_system"

    var client, server string
    switch span.Kind() {
    case ptrace.SpanKindClient, ptrace.SpanKindProducer:
        client, server = svcName, peerSvc
    case ptrace.SpanKindServer, ptrace.SpanKindConsumer:
        if peerSvc == "" { return }
        client, server = peerSvc, svcName
    default:
        return
    }

    edgeKey := hashEdge(client, server, connType)
    e := g.getOrCreateEdge(edgeKey, client, server, connType)
    e.requestTotal.Add(1)
    if isSpanFailed(span) { e.failedTotal.Add(1) }

    duration := spanDuration(span)
    switch span.Kind() {
    case ptrace.SpanKindClient, ptrace.SpanKindProducer:
        e.clientSeconds.Record(duration)
    case ptrace.SpanKindServer, ptrace.SpanKindConsumer:
        e.serverSeconds.Record(duration)
    }
}
```

### 4.4 内部聚合数据结构

```go
// Per-edge 时间序列
type sgEdgeSeries struct {
    client         string
    server         string
    connectionType string
    dimensions     map[string]string
    requestTotal   *counter
    failedTotal    *counter
    clientSeconds  *histogram
    serverSeconds  *histogram
}

// counter / histogram 实现
type counter struct {
    value atomic.Int64
}

type histogram struct {
    buckets []atomic.Int64
    bounds  []float64
    sum     atomic.Int64
    count   atomic.Int64
}
```

### 4.5 Flush 机制

```go
type MetricFlusher struct {
    interval time.Duration
    consumer consumer.Metrics
    redGen   *REDGenerator
    sgGen    *ServiceGraphGenerator
    done     chan struct{}
}

func (f *MetricFlusher) flush() error {
    md := pmetric.NewMetrics()

    // RED 指标 → pmetric
    f.buildREDMetrics(md, f.redGen.Collect())

    // ServiceGraph 指标 → pmetric
    f.buildSGMetrics(md, f.sgGen.Collect())

    if md.DataPointCount() == 0 { return nil }
    return f.consumer.ConsumeMetrics(context.Background(), md)
}
```

---

## 5. 配置设计

```go
type Config struct {
    MetricsFlushInterval   time.Duration        `mapstructure:"metrics_flush_interval"`
    AggregationTemporality string               `mapstructure:"aggregation_temporality"`
    RED                    *REDConfig           `mapstructure:"red"`
    ServiceGraph           *ServiceGraphConfig  `mapstructure:"service_graph"`
    CardinalityLimit       int                  `mapstructure:"cardinality_limit"`
}

type REDConfig struct {
    Enabled      bool            `mapstructure:"enabled"`
    Dimensions   []string        `mapstructure:"dimensions"`
    Histogram    HistogramConfig `mapstructure:"histogram"`
}

type ServiceGraphConfig struct {
    Enabled    bool     `mapstructure:"enabled"`
    Dimensions []string `mapstructure:"dimensions"`
}

type HistogramConfig struct {
    Buckets []float64 `mapstructure:"buckets"`
}
```

默认配置：

```go
func createDefaultConfig() component.Config {
    return &Config{
        MetricsFlushInterval: 15 * time.Second,
        AggregationTemporality: "cumulative",
        CardinalityLimit:       2000,
        RED: &REDConfig{
            Enabled: true,
            Dimensions: []string{"http.method", "http.status_code", "http.route",
                "rpc.method", "rpc.service", "peer.service"},
            Histogram: HistogramConfig{
                Buckets: []float64{5, 10, 25, 50, 100, 250, 500, 1000, 2500, 5000, 10000},
            },
        },
        ServiceGraph: &ServiceGraphConfig{
            Enabled:    true,
            Dimensions: []string{"http.method"},
        },
    }
}
```

---

## 6. 目录结构

```
connector/metricgenconnector/
├── config.go          # Config + Factory
├── connector.go       # metricGenConnector 核心
├── red.go             # REDGenerator
├── servicegraph.go    # ServiceGraphGenerator
├── flusher.go         # MetricFlusher
├── aggregator.go      # counter / histogram 内部聚合类型
├── context.go         # AppIDFromContext + extractServiceName
└── internal/
    └── testutil/      # 测试工具
```

---

## 7. 需同步改动的现有代码

| 文件 | 改动 |
|------|------|
| `cmd/customcol/components.go` | 注册 `metricgenconnector.NewFactory()` |
| `config/build/config.yaml` | 添加 `metricgen` connector + pipeline 配置 |
| `extension/adminext/tempo_handler.go:1983-1990` | `translateTraceQLMetric` 返回值改为 `traces_spanmetrics_*` |
| `extension/adminext/prometheus_handler.go` | 硬编码指标名 `traces.spanmetrics.*` → `traces_spanmetrics_*` |
| `peer_service processor` | 无需改动（已双向写） |
| `spanmetricsconnector` | ✅ S4 已移除 |

---

## 8. Roadmap

| Sprint | 内容 | 验收标准 | 状态 |
|--------|------|---------|------|
| **S1** (2w) | Connector 骨架 + REDGenerator | 与 spanmetrics 并行运行，指标数值一致 | ✅ 2026-07-20 已实施骨架，待部署验证 |
| **S2** (1w) | ServiceGraphGenerator | `traces_service_graph_request_total` 正确计数 | ✅ 2026-07-20 已实施，待部署验证 |
| **S3** (1w) | 查询层适配 + 测试完善 | Grafana 可查询新指标 | ✅ 2026-07-20：_sum/_bucket 查询 + _labels + translateTraceQLMetric + 19 新测试 |
| **S4** (1w) | 性能测试 + 下线 spanmetricsconnector | 100K spans/s | ✅ 2026-07-20：bench 达标 + spanmetricsconnector 已移除 |
| **S5** (1w) | 生产验证 | 线上灰度 10→50→100% | ⬜ 待部署验证 |

---

## 9. 关键决策

| # | 决策 | 理由 |
|---|------|------|
| 1 | peer_service processor 保留 | 已双向写 `peer.service`，代码正确无需改 |
| 2 | Connector 不做 EdgeStore | peer.service 已在 span 属性中，直接读即可 |
| 3 | 不需要 ServiceGroupRouter | 跨服务关联由 peer_service processor 完成 |
| 4 | 指标名对齐 Tempo | `traces_spanmetrics_calls_total` / `traces_service_graph_request_total` 等 |
| 5 | 无 namespace 前缀 | Tempo 命名自包含，无需额外 namespace |
| 6 | 纯 map 聚合 | 无 FIFO 队列、无 TTL、无配对等待，实现极简 |
| 7 | 渐进替换 | 新旧并行，Sprint 4 灰度切换 |

---

## 10. 不做的

| 项目 | 原因 |
|------|------|
| EdgeStore / SpanID 配对 | peer_service processor 已处理 |
| ServiceGroup / 跨 APP 串联 | 当前无多租户需求，后续按需添加 |
| Redis 配置热加载 | 首版配置文件即可，Sprint 3+ 按需 |
| 采样校正 | 当前未启用采样，后续按需 |
| 虚拟节点推断 | 外部服务不发 trace，无法推断，与 Tempo 行为一致 |

---

## 11. 实施记录

### S1 (2026-07-20) — Connector 骨架 + REDGenerator

| 文件 | 说明 |
|------|------|
| `connector/metricgenconnector/config.go` | Config + REDConfig + ServiceGraphConfig + 默认值 |
| `connector/metricgenconnector/aggregator.go` | counter / histogram 线程安全原子类型 |
| `connector/metricgenconnector/context.go` | extractServiceName / extractAppID / spanDuration |
| `connector/metricgenconnector/red.go` | REDGenerator (ProcessSpan + Collect + 基数控制) |
| `connector/metricgenconnector/flusher.go` | metricFlusher (15s 定时 → pmetric → consumer) |
| `connector/metricgenconnector/connector.go` | metricGenConnector (ConsumeTraces + Start/Shutdown) |
| `connector/metricgenconnector/factory.go` | NewFactory (TracesToMetrics) |
| `cmd/customcol/components.go` | 注册 metricgenconnector |
| `config/build/config.yaml` | metricgen connector 配置 + metrics/metricgen pipeline |

### S2 (2026-07-20) — ServiceGraphGenerator

| 文件 | 说明 |
|------|------|
| `connector/metricgenconnector/servicegraph.go` | ServiceGraphGenerator (6 类指标，client→server edge) |
| `connector/metricgenconnector/flusher.go` | buildSGMetrics + emitCounter/emitHistogramIfNonEmpty |
| `connector/metricgenconnector/config.go` | SG 独立 HistogramConfig + DefaultServiceGraphLatencyBuckets |
| `connector/metricgenconnector/factory.go` | ServiceGraphGenerator 创建注入 |
| 修复: Producer 边计数 | Producer→Kafka 和 Kafka→Consumer 为独立边，两端均计数 |
| 修复: 单位 | SG seconds 指标除以 1000 (ms→s)，RED latency 保持 ms |
| 修复: app_id | 所有指标 Resource 带 app_id，解决 ES 拒绝写入 |

### S3 (2026-07-20) — 查询层适配

| 文件 | 说明 |
|------|------|
| `prometheus_handler.go` | `_sum`/`_bucket` 后缀检测 + resolveHistogramBucket |
| `prometheus_handler.go` | `_labels`: dispatchLabelExplore (ES terms agg → label 组合) |
| `tempo_handler.go` | translateTraceQLMetric → 下划线命名 |
| `storedmodel/stored_metric.go` | StoredMetricDataPoint + BucketCounts/ExplicitBounds |
| `storedmodel/stored_metric.go` | convertHistogramPoints 存储 bucket 数据 |
| `types.go` | MetricDataPoint + BucketCounts/ExplicitBounds + LabelCombinationsQuery/Result |
| `types_reader.go` | ES MetricDataPoint + BucketCounts/ExplicitBounds |
| `provider.go` | MetricReader 接口 + ListLabelCombinations |
| `fields.go` | FieldMetricBucketCounts / FieldMetricExplicitBounds 常量 |
| `metric_reader.go` | _source + hitToDataPoint 支持 bucket fields |
| `metric_reader.go` | ListLabelCombinations: nested ES terms agg + 递归扁平化 |
| `reader_adapter.go` | ES → public type 桥接 ListLabelCombinations |
| `pg_reader_adapter.go` | PG stub |
| 测试 | stored_metric_test.go + prometheus_handler_test.go (19 新用例) |

### S4 (2026-07-20) — 性能测试 + 下线 spanmetrics

| 文件 | 说明 |
|------|------|
| `benchmark_test.go` | 6 个 benchmark：RED single/unique/collect, SG client, SG messaging, 100-span batch |
| `config/build/config.yaml` | 移除 spanmetrics connector 配置 + metrics/spanmetrics pipeline |
| `cmd/customcol/components.go` | 移除 spanmetricsconnector import + Factory 注册 |
| Bench 结果 | 345ns/span (RED), 453ns/span (SG), 40µs/100-span batch (~2.9M spans/s) |

配置精简后：

```yaml
connectors:
  metricgen:                           # 唯一 connector
    metrics_flush_interval: 15s
    red:
      enabled: true
    service_graph:
      enabled: true

service:
  pipelines:
    traces:      [metricgen, observability_storage]
    metrics/metricgen: [metricgen → batch → observability_storage]
```

### S3+ (2026-07-20) — Histogram Quantile 查询支持

| 文件 | 说明 |
|------|------|
| `extension/adminext/histogram_calc.go` | 纯函数：`AggregateHistogramBuckets` + `ComputeHistogramQuantile` + helper |
| `extension/adminext/histogram_calc_test.go` | 12 个测试：基础 p50/p90/p99/p95/边界/聚合/多分位数 |
| `extension/adminext/prometheus_handler.go` | `parseHistogramQuantileWrapper`: 剥离 `histogram_quantile(θ, ...)` → 提取 θ + inner expr |
| | `execHistogramQuantileInstant`: QueryRaw → 聚合 bucket_counts → ComputeHistogramQuantile → vector |
| | `promqlExpr.Quantile`: θ 字段 |
| `types.go` | `MetricSample` + `BucketCounts` + `Bounds` |
| `types_reader.go` | ES `MetricSample` + `BucketCounts` + `Bounds` |
| `metric_reader.go` | `parseRawResult` 填充 bucket fileds |
| `reader_adapter.go` | QueryRaw adapter 传递 BucketCounts/Bounds |

**设计原则**：
- `histogram_calc.go`：零外部依赖纯函数包，可独立单测
- `parseHistogramQuantileWrapper`：解析器单向依赖（只从 PromQL 提取参数）
- `execHistogramQuantileInstant`：handler 层只负责路由+格式转换，委托 Calculator 计算
- `MetricSample.Bounds/BucketCounts`：类型层正交扩展，不影响非 histogram 查询

### peer_service processor 修复 (2026-07-20)

| 文件 | 说明 |
|------|------|
| `processor/peerserviceprocessor/processor.go` | completePair nil 守卫 (同侧碰撞不 panic) |
| | Producer/Consumer 改 fast-path (不走 store 配对) |
| | buildMessagingPeerService: broker 地址 + destination + system 复合 peer.service |
| | expire/evict: alreadyPaired 跳过已配对副本，避免双重 release |
