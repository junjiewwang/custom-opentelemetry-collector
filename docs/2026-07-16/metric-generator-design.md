# MetricGenerator Connector 设计方案

> **创建日期**: 2026-07-16
> **状态**: 方案讨论中
> **目标**: 设计自研 MetricGenerator Connector，替换/增强 contrib 的 spanmetricsconnector，支持 Tempo ServiceGraph 能力

---

## 1. 背景与动机

### 1.1 当前现状

项目当前使用 `opentelemetry-collector-contrib` 的 `spanmetricsconnector` 组件，配置如下：

```yaml
connectors:
  spanmetrics:
    histogram:
      explicit:
        buckets: [5ms, 10ms, 25ms, 50ms, 100ms, 250ms, 500ms, 1s, 2.5s, 5s, 10s]
    dimensions:
      - name: http.method
      - name: http.status_code
      - name: http.route
      - name: rpc.method
      - name: rpc.service
    metrics_flush_interval: 15s
    namespace: traces.spanmetrics
    resource_metrics_key_attributes:
      - service.name
      - service.namespace
      - deployment.environment
```

Pipeline 拓扑：
```
traces pipeline → [spanmetrics connector] → metrics/spanmetrics pipeline → storage
```

### 1.2 痛点分析

| 痛点 | 说明 |
|------|------|
| **分布式聚合不完整** | 多实例部署时，同一 Trace 的 Span 可能分散到不同 Collector 实例，导致跨 Span 指标（如 Service Graph、Trace-level 聚合）不准确 |
| **扩展性受限** | contrib 的 spanmetrics 只生成 RED（Rate/Error/Duration）指标，无法灵活扩展自定义聚合逻辑（如业务维度、采样率校正、Trace-level 指标） |
| **多租户隔离** | 当前 spanmetrics 无租户感知，无法按租户独立管理基数限制、维度配置和指标命名空间 |
| **采样校正缺失** | 对接 Head/Tail Sampling 后，指标计数需要根据采样率做校正，contrib 的实现不支持自定义校正逻辑 |
| **指标查询耦合** | `tempo_handler.go` 中的 `translateTraceQLMetric` 硬编码了 spanmetrics 的指标名映射，扩展新指标类型需要改查询层 |

### 1.3 目标

1. **自研 MetricGenerator Connector**，基于 OTel Collector 的 `connector` 组件机制，实现 Traces → Metrics 的转换
2. **分布式友好**：支持多实例部署下的正确聚合（通过 TraceID 路由 + 一致性哈希）
3. **多租户感知**：继承项目的多租户架构，按租户隔离指标聚合
4. **可扩展**：插件化指标生成器，支持 RED、Service Graph、自定义业务指标等多种聚合类型
5. **高性能**：低延迟、高吞吐、可控的内存占用
6. **可测试**：模块化设计，核心逻辑可独立单元测试

---

## 2. 开源实现分析

### 2.1 参考的开源 Connector

| Connector | 核心能力 | 分布式策略 | 借鉴点 |
|-----------|----------|-----------|--------|
| **spanmetricsconnector** | RED 指标（calls/duration/size） | 不支持，需前置 loadbalancer | LRU Cache + CardinalityLimit + Overflow 桶；Delta/Cumulative 双模式；SeriesExpiration 过期清理 |
| **servicegraphconnector** | 服务拓扑图（Client/Server Span 配对） | 不支持，需按 TraceID 路由 | FIFO Store + TTL 过期；虚拟节点推断；metricSeries 15min 自动清理 |
| **countconnector** | 简单计数 | 天然支持（Delta 无状态） | 最简 Connector 骨架；OTTL 条件匹配；MapHash 分组 |
| **loadbalancingexporter** | 按 TraceID 一致性哈希路由 | CRC32 哈希环 + 200 虚拟节点 | DNS/K8s/Static resolver；动态环重建；O(log n) 查找 |

### 2.2 Tempo Metrics-Generator 架构分析

Tempo 的 metrics-generator 是一个独立模块，其架构设计对我们的方案有重要参考价值：

#### 2.2.1 整体架构

```
                PushSpans (gRPC/Kafka)
                     │
                     ▼
          ┌─────────────────────┐
          │     Generator       │  (多租户管理器)
          │  instances map      │  map[tenantID]*instance
          └──────────┬──────────┘
                     │ getOrCreateInstance(tenantID)
                     ▼
          ┌─────────────────────┐
          │     instance        │  (per-tenant 实例)
          │  - registry         │  ManagedRegistry (指标注册中心)
          │  - processors map   │  map[processorName]Processor
          │  - wal (storage)    │  WAL + Remote Write
          └──────────┬──────────┘
                     │ pushSpans → preprocessSpans → for each processor
                     ▼
     ┌───────────────┼───────────────┐
     │               │               │
     ▼               ▼               ▼
┌───────────┐  ┌────────────┐  ┌───────────┐
│span-metrics│  │service-graphs│  │local-blocks│
└───────────┘  └────────────┘  └───────────┘
     │               │               │
     └───────────────┼───────────────┘
                     ▼
          ┌─────────────────────┐
          │  ManagedRegistry    │  (指标注册中心)
          │  - Counter/Histogram│
          │  - Native Histogram │
          └──────────┬──────────┘
                     │ CollectMetrics (定时)
                     ▼
          ┌─────────────────────┐
          │  Storage (WAL)      │
          │  → Remote Write     │
          └─────────────────────┘
```

#### 2.2.2 ServiceGraph 核心实现

Tempo 的 ServiceGraph 处理流程：

```go
// consume 处理入口 - 遍历所有 ResourceSpans
func (p *Processor) consume(resourceSpans []*v1_trace.ResourceSpans) {
    for _, rs := range resourceSpans {
        // 从 Resource 提取 service.name
        svcName, _ := extractServiceName(rs.Resource)
        
        for _, ils := range rs.ScopeSpans {
            for _, span := range ils.Spans {
                p.consumeSpan(svcName, rs.Resource, span)
            }
        }
    }
}

// consumeSpan 对每个 span 做 Edge 配对
func (p *Processor) consumeSpan(svcName string, resource *v1_resource.Resource, span *v1_trace.Span) {
    switch span.Kind {
    case v1_trace.Span_SPAN_KIND_CLIENT:
        key := buildKey(span.TraceId, span.SpanId)     // 注意: Client 用 SpanID
        p.store.UpsertEdge(key, func(e *store.Edge) {
            e.TraceID = span.TraceId
            e.ClientService = svcName
            e.ClientLatencySec = spanDurationSec(span)
            e.Failed = e.Failed || spanFailed(span)
            // 提取额外维度
            p.extractDimensionsClient(e, resource, span)
        })
        
    case v1_trace.Span_SPAN_KIND_SERVER:
        key := buildKey(span.TraceId, span.ParentSpanId)  // Server 用 ParentSpanID
        p.store.UpsertEdge(key, func(e *store.Edge) {
            e.TraceID = span.TraceId
            e.ServerService = svcName
            e.ServerLatencySec = spanDurationSec(span)
            e.Failed = e.Failed || spanFailed(span)
            p.extractDimensionsServer(e, resource, span)
        })
        
    case v1_trace.Span_SPAN_KIND_PRODUCER:
        key := buildKey(span.TraceId, span.SpanId)
        p.store.UpsertEdge(key, func(e *store.Edge) {
            e.ClientService = svcName
            e.ConnectionType = store.MessagingSystem
        })
        
    case v1_trace.Span_SPAN_KIND_CONSUMER:
        key := buildKey(span.TraceId, span.ParentSpanId)
        p.store.UpsertEdge(key, func(e *store.Edge) {
            e.ServerService = svcName
            e.ConnectionType = store.MessagingSystem
        })
    }
}
```

**关键设计点**：

1. **Edge 配对机制**：`key = hash(traceID + spanID/parentSpanID)`，Client Span 用自己的 SpanID，Server Span 用 ParentSpanID，两者匹配时 Edge 完成
2. **虚拟节点推断**：当 Edge 超时（只收到一端）时，通过 peer attributes 推断缺失一端：
   ```go
   // 如果只有 Client 没有 Server，尝试从 peer attributes 推断
   if e.ClientService != "" && e.ServerService == "" {
       if peer := e.PeerNode; peer != "" {
           e.ServerService = peer
           e.VirtualNodeLabel = "server"
       }
   }
   ```
3. **Peer Attributes 优先级**：`server.address` > `peer.service` > `db.name` > `net.sock.peer.addr`

#### 2.2.3 Edge Store 实现

```go
// Store 使用 map + FIFO 链表实现有界存储
type Store struct {
    edges   map[uint64]*Edge    // key → Edge
    queue   *list.List          // FIFO 过期队列
    maxSize int                 // 最大 Edge 数
    ttl     time.Duration       // Edge 生存时间
    
    onComplete func(e *Edge)    // Edge 完成回调（生成指标）
    onExpire   func(e *Edge)    // Edge 超时回调（虚拟节点推断）
}

// UpsertEdge 原子更新 Edge
func (s *Store) UpsertEdge(key uint64, update func(*Edge)) (isNew bool, err error) {
    s.mu.Lock()
    defer s.mu.Unlock()
    
    if e, ok := s.edges[key]; ok {
        update(e)
        if e.isCompleted() {
            s.onComplete(e)     // 两端都齐了 → 生成指标
            s.remove(key)
        }
        return false, nil
    }
    
    // 新 Edge - 检查容量
    if len(s.edges) >= s.maxSize {
        return false, ErrStoreFull
    }
    
    e := &Edge{Key: key, ExpireAt: time.Now().Add(s.ttl)}
    update(e)
    s.edges[key] = e
    s.queue.PushBack(e)     // FIFO 队列用于过期扫描
    return true, nil
}

// Expire 定期调用，清理过期的 Edge
func (s *Store) Expire() {
    s.mu.Lock()
    defer s.mu.Unlock()
    
    now := time.Now()
    for s.queue.Len() > 0 {
        front := s.queue.Front()
        e := front.Value.(*Edge)
        if now.Before(e.ExpireAt) {
            break  // FIFO 保证后续的都没过期
        }
        s.onExpire(e)  // 超时 → 虚拟节点推断
        s.queue.Remove(front)
        delete(s.edges, e.Key)
    }
}
```

#### 2.2.4 ServiceGraph 指标输出

Tempo 生成的 ServiceGraph 指标：

| 指标名 | 类型 | 说明 |
|--------|------|------|
| `traces_service_graph_request_total` | Counter | 边的请求总数 |
| `traces_service_graph_request_failed_total` | Counter | 边的失败请求数 |
| `traces_service_graph_request_server_seconds` | Histogram | Server 端耗时 |
| `traces_service_graph_request_client_seconds` | Histogram | Client 端耗时 |
| `traces_service_graph_request_messaging_system_seconds` | Histogram | 消息系统耗时 |
| `traces_service_graph_unpaired_spans_total` | Counter | 未配对的 Span 数（运营指标） |
| `traces_service_graph_dropped_spans_total` | Counter | 被丢弃的 Span 数（运营指标） |

标签（Labels）：
- `client`: 调用方服务名
- `server`: 被调方服务名
- `connection_type`: `""` 或 `"messaging_system"`
- `virtual_node`: `""` / `"client"` / `"server"`（标识哪一端是推断的）
- 自定义 dimensions（如 `http.method`）

#### 2.2.5 Tempo Overrides 机制（Per-Tenant 配置覆盖）

Tempo 的 overrides 是分层的配置覆盖机制：

```yaml
# 全局默认
overrides:
  defaults:
    metrics_generator:
      processors: [service-graphs, span-metrics]
      max_active_series: 100000
      collection_interval: 15s
      processor:
        service_graphs:
          dimensions: [http.method]
          enable_virtual_node_label: true
          peer_attributes: [server.address, peer.service]
        span_metrics:
          dimensions: [http.method, http.status_code]

# Per-tenant 覆盖
per_tenant_override_config: /etc/tempo/overrides.yaml
per_tenant_override_period: 10s  # 热加载周期
```

```yaml
# overrides.yaml - per-tenant 覆盖
overrides:
  "tenant-large":
    metrics_generator:
      max_active_series: 500000
      processor:
        service_graphs:
          dimensions: [http.method, http.route]
  
  "tenant-small":
    metrics_generator:
      max_active_series: 10000
      disable_collection: true  # 紧急降级
```

查找优先级：`specific tenant` → `wildcard "*"` → `defaults`

### 2.3 关键结论

1. **官方 Connector 均不内置分布式聚合**——它们假设前置已有正确路由
2. **分布式正确性依赖 loadbalancingexporter**——通过 TraceID 哈希确保同一 Trace 到同一实例
3. **基数控制是核心挑战**——spanmetrics 用 LRU + overflow 桶，servicegraph 用 maxItems 硬上限
4. **Delta Temporality 天然适配分布式**——无需跨实例协调，由下游聚合
5. **Tempo 的 ServiceGraph 实现成熟**——Edge Store + FIFO 过期 + 虚拟节点推断，可直接借鉴
6. **Tempo 的 Overrides 机制灵活**——分层覆盖 + 热加载 + 通配符，适合我们的 APP 级别配置需求

---

## 3. 架构设计

### 3.1 整体架构

```
┌─────────────────────────────────────────────────────────────────────────────────┐
│                           Deployment Topology                                    │
│                                                                                  │
│  ┌─────────────────────────┐                                                     │
│  │    Agent / SDK / Probe   │                                                    │
│  └───────────┬─────────────┘                                                    │
│              │ OTLP traces                                                       │
│              ▼                                                                   │
│  ┌─────────────────────────────────────────────────┐                            │
│  │         Gateway Layer (L1)                       │                            │
│  │  ┌───────────────────────────────────────────┐   │                           │
│  │  │  loadbalancingexporter (TraceID 路由)       │   │                           │
│  │  │  CRC32 一致性哈希环 + DNS/K8s resolver      │   │                           │
│  │  └──────────────┬────────────────────────────┘   │                           │
│  └─────────────────┼───────────────────────────────┘                            │
│                    │ 按 TraceID 路由                                              │
│        ┌───────────┼───────────┐                                                │
│        ▼           ▼           ▼                                                │
│  ┌──────────┐┌──────────┐┌──────────┐                                          │
│  │ Worker-1 ││ Worker-2 ││ Worker-N │  ← MetricGenerator Instances              │
│  │          ││          ││          │                                            │
│  │ ┌──────┐ ││ ┌──────┐ ││ ┌──────┐ │                                          │
│  │ │metric││ │ │metric││ │ │metric│ │                                           │
│  │ │gen   ││ │ │gen   ││ │ │gen   │ │                                           │
│  │ └──┬───┘ ││ └──┬───┘ ││ └──┬───┘ │                                          │
│  └────┼─────┘└────┼─────┘└────┼─────┘                                          │
│       │           │           │                                                  │
│       └───────────┼───────────┘                                                  │
│                   ▼                                                               │
│       ┌──────────────────────┐                                                   │
│       │    Metrics Storage    │                                                   │
│       └──────────────────────┘                                                   │
└─────────────────────────────────────────────────────────────────────────────────┘
```

### 3.2 Connector 内部架构

```
┌──────────────────────────────────────────────────────────────────────────┐
│                    MetricGenerator Connector                               │
│                                                                            │
│  ConsumeTraces(ctx, ptrace.Traces)                                        │
│        │                                                                   │
│        ▼                                                                   │
│  ┌──────────────────────────────────────────────────┐                     │
│  │      ContextResolver                              │                    │
│  │  ctx → TenantID                                   │                    │
│  │  resource → AppID (service.name)                  │                    │
│  │  → OverrideManager.GetAppConfig(tenant, app)      │                    │
│  └────────────────────────┬─────────────────────────┘                     │
│                            │                                               │
│                            ▼                                               │
│  ┌──────────────────────────────────────────────────┐                     │
│  │      AppDiscovery (APP 级隔离管理)                 │                    │
│  │  GetOrCreate(tenantID, appID) → AppMetricSpace    │                    │
│  └────────────────────────┬─────────────────────────┘                     │
│                            │                                               │
│                            ▼                                               │
│  ┌──────────────────────────────────────────────────┐                     │
│  │      SpanRouter (按聚合类型分发)                    │                    │
│  └──┬──────────────────────────────┬────────────────┘                     │
│     │                              │                                       │
│     ▼                              ▼                                       │
│  ┌───────────────────┐   ┌──────────────────────────────┐                 │
│  │ RED Generator      │   │ ServiceGraph Generator        │                │
│  │ (per-APP 隔离)     │   │ (per-ServiceGroup)            │                │
│  │                    │   │                               │                │
│  │ APP-A: {calls,dur} │   │ ┌──────────────────────────┐ │                │
│  │ APP-B: {calls,dur} │   │ │  ServiceGraphRouter      │ │                │
│  │ APP-C: {calls,dur} │   │ │  → EdgeStore (group-1)   │ │                │
│  │ ...                │   │ │  → EdgeStore (group-2)   │ │                │
│  └────────┬───────────┘   │ │  → EdgeStore (auto_tenant)│ │               │
│           │               │ └──────────────────────────┘ │                │
│           │               └──────────────┬───────────────┘                │
│           │                              │                                 │
│           └──────────────────────────────┘                                 │
│                            │                                               │
│                            ▼                                               │
│  ┌──────────────────────────────────────────────────┐                     │
│  │      MetricFlusher (定时 flush)                    │                    │
│  │  - Collect 各 Generator 指标                       │                    │
│  │  - Sampling Rate Correction                       │                    │
│  │  - Delta/Cumulative Temporality                   │                    │
│  └────────────────────────┬─────────────────────────┘                     │
│                            │                                               │
│                            ▼                                               │
│  ┌──────────────────────────────────────────────────┐                     │
│  │      consumer.Metrics (下游 pipeline)              │                    │
│  └──────────────────────────────────────────────────┘                     │
└──────────────────────────────────────────────────────────────────────────┘
```

### 3.3 组件职责划分

| 组件 | 职责 | 设计原则 |
|------|------|----------|
| **ContextResolver** | 从 Context 提取 TenantID，从 Resource 提取 AppID（service.name），加载配置 | 单一职责；与认证中间件解耦 |
| **AppDiscovery** | 管理 APP 级聚合空间的生命周期：自动创建、限制、回收 | 高内聚；隔离管理集中 |
| **SpanRouter** | 将 Span 分发到对应的 Generator | 开闭原则；新增 Generator 无需修改 Router |
| **RED Generator** | Per-APP 隔离的 Rate/Error/Duration 指标 | 接口隔离；每个 APP 独立基数空间 |
| **ServiceGraph Generator** | Per-ServiceGroup 的 Edge 配对和拓扑指标 | 关注点分离；支持跨 APP 串联 |
| **ServiceGraphRouter** | 根据 ServiceGroup 配置将 Span 路由到正确的 EdgeStore | 策略模式；支持自动发现和显式声明 |
| **OverrideManager** | 分层配置覆盖：defaults → tenant → app | 配置与逻辑分离；热加载 |
| **MetricFlusher** | 定时构建 pmetric.Metrics 并发送给下游 | 关注点分离；解耦聚合与输出 |

---

## 4. 详细设计

### 4.1 Connector 接口实现

遵循 OTel Collector 的 `connector` 组件机制，实现 `TracesToMetrics` 类型：

```go
package metricgenconnector

import (
    "context"
    "go.opentelemetry.io/collector/component"
    "go.opentelemetry.io/collector/consumer"
    "go.opentelemetry.io/collector/pdata/pcommon"
    "go.opentelemetry.io/collector/pdata/ptrace"
    "go.uber.org/zap"
)

// metricGenConnector 实现 connector.Traces 接口
type metricGenConnector struct {
    config          *Config
    metricsConsumer consumer.Metrics        // 下游 metrics consumer
    appDiscovery    *AppDiscovery           // APP 级隔离管理
    overrideMgr     *OverrideManager        // 分层配置覆盖
    generators      []Generator             // 可插拔的指标生成器
    sgRouter        *ServiceGraphRouter     // ServiceGraph 路由器
    flusher         *MetricFlusher          // 定时 flush
    clock           Clock                   // 可注入的时钟

    logger          *zap.Logger
    done            chan struct{}
}

func (c *metricGenConnector) Capabilities() consumer.Capabilities {
    return consumer.Capabilities{MutatesData: false}
}

func (c *metricGenConnector) Start(ctx context.Context, host component.Host) error {
    // 启动 APP 发现（含定时清理）
    c.appDiscovery.Start(ctx)
    
    // 启动各 Generator
    for _, gen := range c.generators {
        if err := gen.Start(ctx); err != nil {
            return err
        }
    }
    
    // 启动配置热加载
    c.overrideMgr.StartWatching(ctx)
    
    // 启动定时 flush
    c.flusher.Start(ctx)
    return nil
}

func (c *metricGenConnector) ConsumeTraces(ctx context.Context, td ptrace.Traces) error {
    tenantID := TenantIDFromContext(ctx)

    rss := td.ResourceSpans()
    for i := 0; i < rss.Len(); i++ {
        rs := rss.At(i)
        resource := rs.Resource()
        
        // 从 Resource 提取 APP ID（service.name）
        appID := extractServiceName(resource)
        
        // 获取该 APP 的配置（分层覆盖后的最终配置）
        appConfig := c.overrideMgr.GetAppConfig(tenantID, appID)
        
        // 检查该 APP 是否已禁用
        if appConfig != nil && appConfig.Enabled != nil && !*appConfig.Enabled {
            continue  // 跳过已禁用的 APP
        }
        
        // 获取/创建该 APP 的聚合空间
        appSpace := c.appDiscovery.GetOrCreate(tenantID, appID, c.overrideMgr)
        if appSpace == nil {
            continue  // 超过 maxAppsPerTenant 限制
        }
        
        sss := rs.ScopeSpans()
        for j := 0; j < sss.Len(); j++ {
            ss := sss.At(j)
            spans := ss.Spans()

            for k := 0; k < spans.Len(); k++ {
                span := spans.At(k)
                
                // 1. RED Generator: per-APP 隔离处理
                for _, gen := range c.generators {
                    if gen.Name() == "service_graph" {
                        continue  // ServiceGraph 走独立路由
                    }
                    gen.ProcessSpan(ctx, tenantID, appID, appSpace, resource, span)
                }
                
                // 2. ServiceGraph: 通过 ServiceGroupRouter 路由到正确的 EdgeStore
                if c.sgRouter != nil {
                    c.sgRouter.RouteSpan(tenantID, appID, span, resource)
                }
            }
        }
    }
    return nil
}

func (c *metricGenConnector) Shutdown(ctx context.Context) error {
    close(c.done)
    c.flusher.Stop(ctx)
    c.appDiscovery.Stop()
    c.overrideMgr.Stop()
    // 最终 flush 一次，确保不丢数据
    return c.flusher.FlushOnce(ctx)
}

// extractServiceName 从 Resource 中提取 service.name 作为 APP ID
func extractServiceName(resource pcommon.Resource) string {
    if val, ok := resource.Attributes().Get("service.name"); ok {
        return val.Str()
    }
    return "unknown_service"
}
```

### 4.2 Generator 接口（策略模式）

```go
// Generator 定义指标生成策略接口
// 遵循接口隔离原则：每种指标类型独立实现
type Generator interface {
    // Name 返回生成器名称（用于日志和指标标识）
    Name() string

    // Start 初始化生成器
    Start(ctx context.Context) error

    // ProcessSpan 处理单个 Span，提取并累积指标数据
    // 设计为无锁调用（由外层 Aggregator 持锁）
    ProcessSpan(ctx context.Context, tenantID string, resource pcommon.Resource, span ptrace.Span)

    // Collect 收集当前聚合窗口内的指标
    // 返回需要 flush 的 metric series
    Collect(tenantID string) []MetricSeries

    // Shutdown 优雅关闭
    Shutdown(ctx context.Context) error
}

// MetricSeries 表示一条指标时间序列
type MetricSeries struct {
    Name       string
    Type       MetricType  // Counter, Histogram, Gauge
    Attributes pcommon.Map
    Resource   pcommon.Resource
    DataPoints []DataPoint
}

type MetricType int

const (
    MetricTypeCounter MetricType = iota
    MetricTypeHistogram
    MetricTypeGauge
)
```

### 4.3 RED Generator（首个实现）

```go
// REDGenerator 生成 Rate/Error/Duration 指标
// 等价于 spanmetricsconnector 的核心功能，但增加多租户和采样校正
type REDGenerator struct {
    config     *REDConfig
    metrics    map[string]*tenantMetrics  // tenantID → metrics
    keyBuf     *bytes.Buffer              // 复用 buffer，减少 GC
    mu         sync.RWMutex
}

type REDConfig struct {
    Namespace                string
    Dimensions               []Dimension
    Histogram                HistogramConfig
    CardinalityLimit         int
    SeriesExpiration         time.Duration
    SamplingCorrectionEnabled bool
}

func (g *REDGenerator) ProcessSpan(ctx context.Context, tenantID string, resource pcommon.Resource, span ptrace.Span) {
    tm := g.getOrCreateTenantMetrics(tenantID)

    // 1. 提取维度属性
    attrs := g.extractDimensions(resource, span)

    // 2. 计算 metric key（复用 buffer）
    key := g.buildMetricKey(attrs)

    // 3. 采样率校正系数（如果启用）
    weight := g.getSamplingWeight(span)

    // 4. 更新计数器（calls）
    tm.calls.Add(key, attrs, weight)

    // 5. 更新直方图（duration）
    duration := span.EndTimestamp().AsTime().Sub(span.StartTimestamp().AsTime())
    tm.duration.Record(key, attrs, duration, weight)

    // 6. 更新错误计数（如果 status = ERROR）
    if span.Status().Code() == ptrace.StatusCodeError {
        tm.errors.Add(key, attrs, weight)
    }

    // 7. 更新 size（如果配置启用）
    if g.config.EnableSizeMetric {
        tm.size.Record(key, attrs, int64(estimateSpanSize(span)), weight)
    }
}
```

### 4.4 ServiceGraph Generator（Trace-level 聚合，参考 Tempo 实现）

```go
// ServiceGraphGenerator 从 Span 中构建服务调用拓扑
// 借鉴 Tempo 的实现：Edge Store + FIFO 过期 + 虚拟节点推断
// 与 Tempo 的区别：通过 ServiceGroup 机制管理 Edge 归属
type ServiceGraphGenerator struct {
    router   *ServiceGraphRouter   // 按 ServiceGroup 路由
    config   *ServiceGraphConfig
    clock    Clock
    logger   *zap.Logger
}

type ServiceGraphConfig struct {
    // 通用配置
    Dimensions        []Dimension  // 额外维度（如 http.method）
    EnableVirtualNode bool         // 是否启用虚拟节点推断
    PeerAttributes    []string     // 推断对端服务名的属性优先级
    
    // Store 配置（per-ServiceGroup）
    Store StoreConfig
    
    // 指标前缀
    EnableClientServerPrefix bool  // 是否在指标中区分 client/server 前缀
    
    // 连接类型
    EnableMessagingSystem bool     // 是否追踪 Producer/Consumer 类型的 Edge
}

type StoreConfig struct {
    MaxItems int           // 最大等待配对的 Edge 数
    TTL      time.Duration // Edge 过期时间（超时后触发虚拟节点推断）
}

// Edge 数据结构（参考 Tempo store.Edge）
type Edge struct {
    Key             uint64
    TraceID         pcommon.TraceID
    
    // Client 端信息
    ClientService   string
    ClientLatency   time.Duration
    ClientResource  pcommon.Resource
    
    // Server 端信息
    ServerService   string
    ServerLatency   time.Duration
    ServerResource  pcommon.Resource
    
    // 状态
    ConnectionType  ConnectionType  // Direct / MessagingSystem
    Failed          bool
    VirtualNodeLabel string          // "" / "client" / "server"
    
    // 额外维度
    Dimensions      map[string]string
    
    // 过期管理
    ExpireAt        time.Time
}

type ConnectionType string

const (
    ConnectionTypeDirect    ConnectionType = ""
    ConnectionTypeMessaging ConnectionType = "messaging_system"
)

// isCompleted 当 Client 和 Server 两端都填充后，Edge 完成
func (e *Edge) isCompleted() bool {
    return e.ClientService != "" && e.ServerService != ""
}

func (g *ServiceGraphGenerator) ProcessSpan(ctx context.Context, tenantID string, resource pcommon.Resource, span ptrace.Span) {
    appID := extractServiceName(resource)
    
    switch span.Kind() {
    case ptrace.SpanKindClient:
        // Client 用 SpanID 作为 key（与 Server 的 ParentSpanID 匹配）
        key := buildEdgeKey(span.TraceID(), span.SpanID())
        g.router.UpsertEdge(tenantID, appID, key, func(e *Edge) {
            e.TraceID = span.TraceID()
            e.ClientService = appID
            e.ClientLatency = spanDuration(span)
            e.ClientResource = resource
            e.Failed = e.Failed || isSpanFailed(span)
            g.extractClientDimensions(e, resource, span)
        })

    case ptrace.SpanKindServer:
        // Server 用 ParentSpanID 作为 key（与 Client 的 SpanID 匹配）
        key := buildEdgeKey(span.TraceID(), span.ParentSpanID())
        g.router.UpsertEdge(tenantID, appID, key, func(e *Edge) {
            e.TraceID = span.TraceID()
            e.ServerService = appID
            e.ServerLatency = spanDuration(span)
            e.ServerResource = resource
            e.Failed = e.Failed || isSpanFailed(span)
            g.extractServerDimensions(e, resource, span)
        })

    case ptrace.SpanKindProducer:
        if !g.config.EnableMessagingSystem {
            return
        }
        key := buildEdgeKey(span.TraceID(), span.SpanID())
        g.router.UpsertEdge(tenantID, appID, key, func(e *Edge) {
            e.ClientService = appID
            e.ConnectionType = ConnectionTypeMessaging
            e.ClientLatency = spanDuration(span)
        })

    case ptrace.SpanKindConsumer:
        if !g.config.EnableMessagingSystem {
            return
        }
        key := buildEdgeKey(span.TraceID(), span.ParentSpanID())
        g.router.UpsertEdge(tenantID, appID, key, func(e *Edge) {
            e.ServerService = appID
            e.ConnectionType = ConnectionTypeMessaging
            e.ServerLatency = spanDuration(span)
        })
    }
}

// onEdgeComplete Edge 完成时生成指标
func (g *ServiceGraphGenerator) onEdgeComplete(edge *Edge) {
    // 生成指标：
    // - traces_service_graph_request_total{client, server, connection_type, ...dimensions}
    // - traces_service_graph_request_failed_total{...}
    // - traces_service_graph_request_server_seconds{...}
    // - traces_service_graph_request_client_seconds{...}
}

// onEdgeExpire Edge 超时时尝试虚拟节点推断
func (g *ServiceGraphGenerator) onEdgeExpire(edge *Edge) {
    if !g.config.EnableVirtualNode {
        return  // 不启用虚拟节点则直接丢弃
    }
    
    // 尝试从 peer attributes 推断缺失的一端
    if edge.ClientService != "" && edge.ServerService == "" {
        // 只有 Client，缺 Server → 推断 Server
        if peer := g.inferPeerService(edge, "server"); peer != "" {
            edge.ServerService = peer
            edge.VirtualNodeLabel = "server"
            g.onEdgeComplete(edge)
            return
        }
    }
    if edge.ServerService != "" && edge.ClientService == "" {
        // 只有 Server，缺 Client → 推断 Client
        if peer := g.inferPeerService(edge, "client"); peer != "" {
            edge.ClientService = peer
            edge.VirtualNodeLabel = "client"
            g.onEdgeComplete(edge)
            return
        }
    }
    
    // 无法推断 → 记录 unpaired_spans 指标
    g.recordUnpairedSpan(edge)
}

// inferPeerService 从 peer attributes 推断对端服务名
// 优先级参考 Tempo：server.address > peer.service > db.name
func (g *ServiceGraphGenerator) inferPeerService(edge *Edge, missingRole string) string {
    for _, attr := range g.config.PeerAttributes {
        if val, ok := edge.Dimensions[attr]; ok && val != "" {
            return val
        }
    }
    return ""
}

// buildEdgeKey 使用 traceID + spanID 构建唯一 key
// 使用 hash 而非直接拼接，节省内存
func buildEdgeKey(traceID pcommon.TraceID, spanID pcommon.SpanID) uint64 {
    h := fnv.New64a()
    h.Write(traceID[:])
    h.Write(spanID[:])
    return h.Sum64()
}
```

#### ServiceGraph 生成的指标

| 指标名 | 类型 | Labels | 说明 |
|--------|------|--------|------|
| `traces_service_graph_request_total` | Counter | client, server, connection_type, virtual_node, ...dims | 边的请求总数 |
| `traces_service_graph_request_failed_total` | Counter | 同上 | 边的失败请求数 |
| `traces_service_graph_request_server_seconds` | Histogram | 同上 | Server 端耗时分布 |
| `traces_service_graph_request_client_seconds` | Histogram | 同上 | Client 端耗时分布 |
| `traces_service_graph_request_messaging_system_seconds` | Histogram | 同上 | 消息系统耗时（Producer→Consumer） |
| `traces_service_graph_unpaired_spans_total` | Counter | service, side | 未配对的 Span 数（运营指标） |
| `traces_service_graph_dropped_spans_total` | Counter | service | Store 满时被丢弃的 Span（运营指标） |

### 4.5 MetricAggregator（Per-APP 聚合管理器）

```go
// AppAggregator 每个 APP 独立的聚合器
// 管理该 APP 的所有指标 series、基数控制、过期清理
type AppAggregator struct {
    tenantID         string
    appID            string
    seriesCache      *lru.Cache[uint64, *MetricSeries]  // hash(labels) → Series
    cardinalityLimit int
    currentCardinality atomic.Int64
    lastActive       time.Time
    mu               sync.RWMutex
}

// 基数控制：达到上限时走 overflow 桶（OTel 标准溢出机制）
func (aa *AppAggregator) CheckCardinality(key uint64) (uint64, bool) {
    current := aa.currentCardinality.Load()
    if current >= int64(aa.cardinalityLimit) {
        return overflowKey, true // 溢出到 "otel.metric.overflow" 标签
    }
    aa.currentCardinality.Add(1)
    return key, false
}

// RecordSpan 记录一个 Span 的指标数据
func (aa *AppAggregator) RecordSpan(key uint64, attrs pcommon.Map, duration time.Duration, weight float64, failed bool) {
    aa.mu.RLock()
    series, ok := aa.seriesCache.Get(key)
    aa.mu.RUnlock()
    
    if !ok {
        finalKey, overflow := aa.CheckCardinality(key)
        if overflow {
            key = finalKey  // 溢出到统一桶
        }
        
        aa.mu.Lock()
        series = newMetricSeries(key, attrs)
        aa.seriesCache.Add(key, series)
        aa.mu.Unlock()
    }
    
    // 更新 series 数据（原子操作）
    series.Calls.Add(weight)
    series.Duration.Record(duration, weight)
    if failed {
        series.Errors.Add(weight)
    }
    
    aa.lastActive = aa.clock.Now()
}

// Collect 收集并重置该 APP 的所有 series（用于 flush）
func (aa *AppAggregator) Collect() []*MetricSeries {
    aa.mu.Lock()
    defer aa.mu.Unlock()
    
    result := make([]*MetricSeries, 0, aa.seriesCache.Len())
    for _, key := range aa.seriesCache.Keys() {
        if series, ok := aa.seriesCache.Get(key); ok {
            result = append(result, series.Snapshot())
        }
    }
    return result
}
```

### 4.6 MetricFlusher（定时 flush）

```go
// MetricFlusher 负责定时收集各 Generator 的指标并发送给下游
type MetricFlusher struct {
    interval        time.Duration
    consumer        consumer.Metrics
    generators      []Generator
    aggregator      *MetricAggregator
    temporality     pmetric.AggregationTemporality
    samplingCorrector *SamplingCorrector

    done            chan struct{}
    logger          *zap.Logger
}

func (f *MetricFlusher) Start(ctx context.Context) {
    go f.flushLoop(ctx)
}

func (f *MetricFlusher) flushLoop(ctx context.Context) {
    ticker := time.NewTicker(f.interval)
    defer ticker.Stop()

    for {
        select {
        case <-ticker.C:
            if err := f.FlushOnce(ctx); err != nil {
                f.logger.Error("flush metrics failed", zap.Error(err))
            }
        case <-f.done:
            return
        case <-ctx.Done():
            return
        }
    }
}

func (f *MetricFlusher) FlushOnce(ctx context.Context) error {
    md := pmetric.NewMetrics()

    // 从各 Generator 收集指标
    for _, gen := range f.generators {
        series := gen.Collect("")  // 空 tenantID 表示收集所有
        f.buildMetrics(md, series)
    }

    if md.DataPointCount() == 0 {
        return nil
    }

    // 发送给下游
    return f.consumer.ConsumeMetrics(ctx, md)
}
```

### 4.7 配置结构

```go
// Config MetricGenerator Connector 的完整配置
type Config struct {
    // 全局配置
    MetricsFlushInterval   time.Duration `mapstructure:"metrics_flush_interval"`    // 默认 15s
    AggregationTemporality string        `mapstructure:"aggregation_temporality"`   // "delta" | "cumulative"
    Namespace              string        `mapstructure:"namespace"`                 // 指标命名空间

    // 生成器配置
    RED          *REDConfig          `mapstructure:"red"`           // RED 指标生成器
    ServiceGraph *ServiceGraphConfig `mapstructure:"service_graph"` // 服务拓扑生成器
    Custom       []CustomGenConfig   `mapstructure:"custom"`        // 自定义生成器

    // APP 隔离配置
    AppIsolation AppIsolationConfig `mapstructure:"app_isolation"`

    // ServiceGroup 配置
    ServiceGroups []ServiceGroupConfig `mapstructure:"service_groups"`

    // 采样校正
    SamplingCorrection SamplingCorrectionConfig `mapstructure:"sampling_correction"`
    
    // 配置覆盖
    Overrides OverridesConfig `mapstructure:"overrides"`
}

// AppIsolationConfig APP 级别隔离配置
type AppIsolationConfig struct {
    Enabled              bool          `mapstructure:"enabled"`                 // 是否启用 APP 级隔离
    MaxAppsPerTenant     int           `mapstructure:"max_apps_per_tenant"`     // 每 Tenant 最大 APP 数（默认 500）
    DefaultCardinalityLimit int        `mapstructure:"default_cardinality_limit"` // 每 APP 默认基数限制
    IdleTimeout          time.Duration `mapstructure:"idle_timeout"`            // APP 空闲回收时间（默认 1h）
    CleanupInterval      time.Duration `mapstructure:"cleanup_interval"`        // 清理检查周期
}

// ServiceGroupConfig 定义一组可互相建立 ServiceGraph Edge 的 APP
type ServiceGroupConfig struct {
    Name      string              `mapstructure:"name"`       // 服务组名称
    TenantID  string              `mapstructure:"tenant_id"`  // 所属 Tenant（空表示跨 Tenant）
    Members   []ServiceMemberConfig `mapstructure:"members"`  // 组内成员
    Config    *StoreConfig        `mapstructure:"config"`     // 该组的 EdgeStore 配置
}

type ServiceMemberConfig struct {
    AppID    string `mapstructure:"app_id"`     // service.name
    TenantID string `mapstructure:"tenant_id"`  // 所属 Tenant（跨 Tenant 时填写）
}

// ServiceGraphConfig ServiceGraph 生成器配置
type ServiceGraphConfig struct {
    Enabled              bool        `mapstructure:"enabled"`
    Store                StoreConfig `mapstructure:"store"`                    // 默认 EdgeStore 配置
    Dimensions           []Dimension `mapstructure:"dimensions"`
    EnableVirtualNode    bool        `mapstructure:"enable_virtual_node"`      // 默认 true
    PeerAttributes       []string    `mapstructure:"peer_attributes"`          // 推断对端的属性优先级
    EnableMessagingSystem bool       `mapstructure:"enable_messaging_system"`  // 追踪 Producer/Consumer
    AutoDiscover         bool        `mapstructure:"auto_discover"`            // Tenant 内自动发现
    AutoDiscoverScope    string      `mapstructure:"auto_discover_scope"`      // "tenant" | "explicit_only"
    ExcludeApps          []string    `mapstructure:"exclude_apps"`             // 自动发现时排除的 APP
}

// OverridesConfig 配置覆盖机制（借鉴 Tempo）
type OverridesConfig struct {
    // Per-Tenant 覆盖
    TenantOverrides map[string]*MetricGenOverride `mapstructure:"tenant_overrides"`
    
    // Per-APP 覆盖（最高优先级）
    AppOverrides []AppOverrideConfig `mapstructure:"app_overrides"`
    
    // 动态加载（热更新）
    DynamicConfigPath   string        `mapstructure:"dynamic_config_path"`   // 外部覆盖文件路径
    DynamicReloadPeriod time.Duration `mapstructure:"dynamic_reload_period"` // 热加载周期（默认 10s）
}

type AppOverrideConfig struct {
    TenantID string              `mapstructure:"tenant_id"`
    AppID    string              `mapstructure:"app_id"`
    Config   *MetricGenOverride  `mapstructure:"config"`
}

type MetricGenOverride struct {
    CardinalityLimit int           `mapstructure:"cardinality_limit"`
    RateLimit        int           `mapstructure:"rate_limit"`          // spans/s
    Dimensions       []Dimension   `mapstructure:"dimensions"`
    Histogram        *HistogramConfig `mapstructure:"histogram"`
    Enabled          *bool         `mapstructure:"enabled"`             // nil 表示不覆盖
    EnableRED        *bool         `mapstructure:"enable_red"`
    EnableServiceGraph *bool       `mapstructure:"enable_service_graph"`
}

type SamplingCorrectionConfig struct {
    Enabled   bool   `mapstructure:"enabled"`
    Attribute string `mapstructure:"attribute"` // 采样率属性名（默认 "sampling.rate"）
}
```

### 4.8 Factory 注册

```go
package metricgenconnector

import (
    "go.opentelemetry.io/collector/connector"
    "go.opentelemetry.io/collector/connector/xconnector"
)

const typeStr = "metricgen"

func NewFactory() connector.Factory {
    return xconnector.NewFactory(
        component.MustNewType(typeStr),
        createDefaultConfig,
        xconnector.WithTracesToMetrics(createTracesToMetricsConnector, component.StabilityLevelAlpha),
    )
}

func createDefaultConfig() component.Config {
    return &Config{
        MetricsFlushInterval:   15 * time.Second,
        AggregationTemporality: "cumulative",
        Namespace:              "traces.spanmetrics",
        RED: &REDConfig{
            Enabled: true,
            Histogram: HistogramConfig{
                Unit:    UnitMilliseconds,
                Buckets: defaultBuckets,
            },
            CardinalityLimit: 2000,
            SeriesExpiration: 15 * time.Minute,
        },
        Aggregation: AggregatorConfig{
            DefaultCardinalityLimit: 2000,
            ResourceCacheSize:       1000,
            MetricsExpiration:       30 * time.Minute,
            CleanupInterval:         1 * time.Minute,
        },
    }
}
```

---

## 5. 分布式设计

### 5.1 分布式部署拓扑

```
┌─────────────────────────────────────────────────────────────────┐
│                     分布式部署架构                                 │
│                                                                   │
│  ┌─────────────┐    ┌─────────────┐    ┌─────────────┐          │
│  │  Agent-1    │    │  Agent-2    │    │  Agent-N    │          │
│  │ (app-host)  │    │ (app-host)  │    │ (app-host)  │          │
│  └──────┬──────┘    └──────┬──────┘    └──────┬──────┘          │
│         │                   │                   │                 │
│         └───────────────────┼───────────────────┘                 │
│                             │ OTLP                                │
│                             ▼                                     │
│  ┌─────────────────────────────────────────────────────────────┐ │
│  │            Gateway Cluster (loadbalancingexporter)           │ │
│  │                                                              │ │
│  │   routing_key: traceID                                       │ │
│  │   resolver: k8s (EndpointSlice watch)                        │ │
│  │   hash_ring: CRC32 + 200 虚拟节点/endpoint                   │ │
│  │                                                              │ │
│  │   ┌──────────────────────────────────────────────────────┐   │ │
│  │   │  一致性哈希环: hash(traceID) → Worker Instance        │   │ │
│  │   └──────────────────────────────────────────────────────┘   │ │
│  └────────────────────────┬────────────────────────────────────┘ │
│                           │                                       │
│      ┌────────────────────┼───────────────────────┐              │
│      │                    │                       │               │
│      ▼                    ▼                       ▼               │
│  ┌──────────┐      ┌──────────┐           ┌──────────┐          │
│  │Worker-1  │      │Worker-2  │           │Worker-N  │          │
│  │          │      │          │           │          │          │
│  │┌────────┐│      │┌────────┐│           │┌────────┐│          │
│  ││MetricGen││     ││MetricGen││          ││MetricGen││         │
│  ││Connector││     ││Connector││          ││Connector││         │
│  │└────────┘│      │└────────┘│           │└────────┘│          │
│  │          │      │          │           │          │          │
│  │┌────────┐│      │┌────────┐│           │┌────────┐│          │
│  ││ Trace   ││     ││ Trace   ││          ││ Trace   ││         │
│  ││ Storage ││     ││ Storage ││          ││ Storage ││         │
│  │└────────┘│      │└────────┘│           │└────────┘│          │
│  └──────────┘      └──────────┘           └──────────┘          │
│                                                                   │
└─────────────────────────────────────────────────────────────────┘
```

### 5.2 TraceID 路由保证

**核心前提**：MetricGenerator 中需要 Trace-level 聚合的 Generator（如 ServiceGraph）要求同一 Trace 的所有 Span 在同一实例上处理。

**实现方案**：复用项目已有的 `loadbalancingexporter`（contrib 提供），配置如下：

```yaml
exporters:
  loadbalancing:
    routing_key: "traceID"
    protocol:
      otlp:
        tls:
          insecure: true
    resolver:
      k8s:
        service: "collector-worker.observability.svc.cluster.local"
        ports: [4317]
```

### 5.3 分布式场景下的聚合策略

| 聚合类型 | 是否需要 Trace 完整性 | 分布式策略 |
|----------|---------------------|-----------|
| **RED（calls/duration/errors）** | ❌ 不需要 | 各实例独立聚合，使用 Delta Temporality，下游存储自动合并 |
| **ServiceGraph** | ✅ 需要 | 依赖前置 loadbalancingexporter 按 TraceID 路由 |
| **Trace-level 指标（如 Trace Duration）** | ✅ 需要 | 同上 |
| **Custom（按 Span 级别）** | ❌ 不需要 | 各实例独立聚合 |

### 5.4 扩缩容时的一致性保证

**问题**：当 Worker 实例数变化时（扩/缩容），一致性哈希环会重新分配 TraceID 的归属，导致：
1. 正在聚合的 Trace 可能被路由到新实例
2. 旧实例上的 Edge 配对可能永远等不到另一半

**解决方案**：

1. **短时间窗口容忍**：ServiceGraph 的 Edge TTL 设置为较短值（如 10s），超时后触发虚拟节点推断，避免永久挂起
2. **Delta Temporality 自愈**：RED 指标使用 Delta 模式，重分配后新实例重新开始累计，下游自动合并
3. **K8s 滚动更新**：Resolver 监听 EndpointSlice 变化，哈希环平滑过渡
4. **Metrics Expiration**：旧实例上的"孤儿" series 会在过期后自动清理

### 5.5 故障恢复

| 故障场景 | 影响 | 恢复策略 |
|----------|------|---------|
| Worker 实例宕机 | 该实例上的聚合状态丢失 | K8s 自动重启；哈希环自动摘除；Delta 模式下丢失一个 flush 周期的数据 |
| Gateway 实例宕机 | 部分 Agent 连接中断 | Agent 重连到其他 Gateway；Gateway 无状态，影响可控 |
| Redis 不可用 | 多租户配置不可读取 | 使用本地缓存的上一次配置继续运行；降级为默认配置 |

---

## 6. 隔离设计：APP 级别隔离 + 跨 APP 服务串联

### 6.1 隔离粒度分析

#### 为什么不做 Tenant 级别隔离

| 维度 | Tenant 级隔离 | APP 级隔离 |
|------|-------------|-----------|
| **隔离粒度** | 太粗 —— 一个 Tenant 下所有 APP 共享基数配额 | ✅ 合适 —— 每个 APP 独立管理 |
| **基数控制** | 大 APP 吃掉整个 Tenant 配额 | ✅ 每个 APP 独立限制 |
| **配置灵活性** | 一个 Tenant 内所有 APP 同一配置 | ✅ 不同 APP 可配置不同维度/桶 |
| **ServiceGraph** | 需要跨 APP 工作 | ✅ 可通过 ServiceGroup 机制配置跨 APP 关系 |
| **运维** | 降级/限流只能到 Tenant | ✅ 可精确到单个 APP |

**结论**：隔离粒度到 APP（即 `service.name` 级别），Tenant 只作为逻辑分组和鉴权单位。

#### APP 的定义

```
APP ≈ resource.attributes["service.name"]
```

一个 APP 对应一个独立的指标聚合空间。同一 Tenant 下的不同 APP 有各自独立的：
- 基数限制（CardinalityLimit）
- 维度配置（Dimensions）
- 直方图桶配置（Histogram Buckets）
- 启用/禁用状态

### 6.2 APP 级隔离模型

```go
// AppMetricSpace 每个 APP 独立的聚合空间
type AppMetricSpace struct {
    TenantID         string           // 所属 Tenant（用于鉴权和分组）
    AppID            string           // APP 标识 = service.name
    Config           *AppMetricConfig // 该 APP 的指标配置
    REDAggregator    *REDAggregator   // RED 指标聚合器
    CardinalityUsed  int64            // 当前基数使用量
    CardinalityLimit int64            // 基数上限
    LastActive       time.Time        // 最后活跃时间（用于自动回收）
}

type AppMetricConfig struct {
    // RED 配置
    Dimensions       []Dimension     // 该 APP 启用的维度
    Histogram        HistogramConfig // 直方图桶配置
    Namespace        string          // 指标命名空间前缀
    
    // 限制配置
    CardinalityLimit int             // 基数限制
    RateLimit        int             // Span 处理速率上限（spans/s）
    
    // 控制开关
    Enabled          bool            // 是否启用指标生成
    EnableRED        bool            // 是否启用 RED 指标
    EnableServiceGraph bool          // 是否参与 ServiceGraph
    
    // ServiceGraph 关联配置
    ServiceGroups    []string        // 该 APP 所属的 ServiceGroup（用于跨 APP 串联）
}
```

### 6.3 跨 APP 服务串联：ServiceGroup 机制

#### 6.3.1 问题分析

ServiceGraph 的本质是跨服务的 —— 它需要配对不同 APP 之间的 Client/Server Span。如果严格按 APP 隔离，ServiceGraph 就无法工作。

**需要解决的问题**：
1. APP-A 调用 APP-B，Edge 配对需要同时访问两个 APP 的 Span 数据
2. 同一 Tenant 下的 APP 之间服务串联是常见场景
3. 跨 Tenant 的服务串联也可能存在（如平台级中间件）
4. 需要配置机制来声明哪些 APP 之间允许建立拓扑关系

#### 6.3.2 ServiceGroup 设计

**核心概念**：`ServiceGroup` 是一组允许互相建立 ServiceGraph Edge 的 APP 集合。

```go
// ServiceGroup 定义一组可互相建立拓扑关系的 APP
type ServiceGroup struct {
    Name        string            // 服务组名称（如 "order-flow", "payment-chain"）
    TenantID    string            // 所属 Tenant（跨 Tenant 时为空或特殊标识）
    Members     []ServiceMember   // 组内成员
    Config      *ServiceGraphConfig // 该组的 ServiceGraph 配置
}

type ServiceMember struct {
    AppID     string   // service.name
    TenantID  string   // 所属 Tenant（支持跨 Tenant 场景）
    Role      string   // 可选：标记角色（如 "gateway", "backend", "database"）
}
```

#### 6.3.3 ServiceGraph 的隔离策略

```
┌─────────────────────────────────────────────────────────────────────────┐
│                        隔离 vs 串联的关系                                  │
│                                                                          │
│  ┌─── Tenant-A ──────────────────────────────────────────────────────┐  │
│  │                                                                    │  │
│  │  ┌─────────┐     ┌─────────┐     ┌─────────┐     ┌─────────┐    │  │
│  │  │ APP-1   │     │ APP-2   │     │ APP-3   │     │ APP-4   │    │  │
│  │  │(gateway)│     │(order)  │     │(payment)│     │(notify) │    │  │
│  │  └────┬────┘     └────┬────┘     └────┬────┘     └────┬────┘    │  │
│  │       │               │               │               │          │  │
│  │       │  RED: 独立隔离  │  RED: 独立隔离  │  RED: 独立隔离 │          │  │
│  │       │               │               │               │          │  │
│  │       └───────┬───────┘               └───────┬───────┘          │  │
│  │               │                               │                   │  │
│  │      ServiceGroup: "order-flow"     ServiceGroup: "pay-flow"     │  │
│  │      (APP-1 ↔ APP-2 的 Edge)        (APP-2 ↔ APP-3 的 Edge)     │  │
│  │                                                                    │  │
│  └────────────────────────────────────────────────────────────────────┘  │
│                                                                          │
│  ┌─── Tenant-B ─────────┐                                              │
│  │  ┌─────────┐         │                                              │
│  │  │ APP-X   │         │                                              │
│  │  └─────────┘         │                                              │
│  └───────────────────────┘                                              │
│                                                                          │
│  注意：APP-4 没有配置到任何 ServiceGroup，因此不参与 ServiceGraph          │
│  但它的 RED 指标正常生成                                                   │
└─────────────────────────────────────────────────────────────────────────┘
```

#### 6.3.4 两种配置模式

**模式 1：显式 ServiceGroup 声明**（推荐用于精确控制）

```yaml
metric_generator:
  service_groups:
    - name: "order-flow"
      tenant_id: "tenant-a"
      members:
        - app_id: "gateway-service"
        - app_id: "order-service"
        - app_id: "inventory-service"
      config:
        max_items: 10000
        ttl: 10s
        dimensions: [http.method, http.route]
    
    - name: "payment-chain"
      tenant_id: "tenant-a"
      members:
        - app_id: "order-service"
        - app_id: "payment-service"
        - app_id: "bank-gateway"
      config:
        max_items: 5000
        ttl: 15s
```

**模式 2：Tenant 内自动发现**（简单场景的默认行为）

```yaml
metric_generator:
  service_graph:
    # 默认模式：同 Tenant 下的所有 APP 自动组成一个 ServiceGroup
    auto_discover: true
    auto_discover_scope: "tenant"  # "tenant" | "explicit_only"
    
    # 也可以排除某些 APP（如只做边缘代理的 sidecar）
    exclude_apps:
      - "envoy-sidecar"
      - "otel-collector-agent"
```

#### 6.3.5 ServiceGroup 的实现逻辑

```go
// ServiceGraphRouter 负责将 Span 路由到正确的 ServiceGroup 进行 Edge 配对
type ServiceGraphRouter struct {
    // appToGroups: 每个 APP 可能属于多个 ServiceGroup
    appToGroups map[AppKey][]string   // AppKey{TenantID, AppID} → []groupName
    
    // groupStores: 每个 ServiceGroup 独立的 EdgeStore
    groupStores map[string]*EdgeStore // groupName → EdgeStore
    
    // autoDiscover: 是否启用 Tenant 内自动发现
    autoDiscover    bool
    autoDiscoverScope string
    
    mu sync.RWMutex
}

type AppKey struct {
    TenantID string
    AppID    string  // = service.name
}

// RouteSpan 将 Span 路由到所有它参与的 ServiceGroup 的 EdgeStore
func (r *ServiceGraphRouter) RouteSpan(tenantID, appID string, span ptrace.Span, resource pcommon.Resource) {
    key := AppKey{TenantID: tenantID, AppID: appID}
    
    r.mu.RLock()
    groups := r.appToGroups[key]
    r.mu.RUnlock()
    
    // 如果没有显式配置，且启用了自动发现，使用 Tenant 级的默认 Group
    if len(groups) == 0 && r.autoDiscover {
        groups = []string{r.defaultGroupName(tenantID)}
    }
    
    for _, groupName := range groups {
        store := r.groupStores[groupName]
        if store == nil {
            continue
        }
        store.ProcessSpan(tenantID, appID, span, resource)
    }
}

// defaultGroupName 自动发现模式下使用 Tenant 作为默认 Group
func (r *ServiceGraphRouter) defaultGroupName(tenantID string) string {
    return fmt.Sprintf("auto_%s", tenantID)
}
```

#### 6.3.6 跨 Tenant 服务串联场景

某些场景下，平台级中间件（如统一网关、消息队列）会被多个 Tenant 共同依赖：

```yaml
metric_generator:
  service_groups:
    # 跨 Tenant 的平台级服务组
    - name: "platform-gateway"
      # 不指定 tenant_id → 跨 Tenant
      members:
        - app_id: "api-gateway"
          tenant_id: "platform"     # 平台自有的 Tenant
        - app_id: "order-service"
          tenant_id: "tenant-a"     # 业务 Tenant A
        - app_id: "user-service"
          tenant_id: "tenant-b"     # 业务 Tenant B
      config:
        # 跨 Tenant 的 ServiceGroup 通常只关注拓扑，不暴露业务维度
        dimensions: []
        max_items: 20000
```

### 6.4 隔离模型总结

```
┌────────────────────────────────────────────────────────────────┐
│                    隔离模型分层                                   │
│                                                                  │
│  ┌─────────────────────────────────────────────────────────┐    │
│  │  Layer 1: Tenant（鉴权 + 逻辑分组）                       │    │
│  │  - 认证鉴权：确定数据归属                                   │    │
│  │  - 不做指标隔离，只做安全边界                                │    │
│  └─────────────────────────────────────────────────────────┘    │
│                                                                  │
│  ┌─────────────────────────────────────────────────────────┐    │
│  │  Layer 2: APP（指标隔离单位）                              │    │
│  │  - 独立基数限制：APP-A 爆表不影响 APP-B                     │    │
│  │  - 独立配置：每个 APP 有自己的维度、桶、命名空间             │    │
│  │  - 独立开关：可精确降级/禁用某个 APP                         │    │
│  │  - RED 指标：完全独立                                       │    │
│  └─────────────────────────────────────────────────────────┘    │
│                                                                  │
│  ┌─────────────────────────────────────────────────────────┐    │
│  │  Layer 3: ServiceGroup（跨 APP 串联配置）                  │    │
│  │  - 声明哪些 APP 之间可以建立 ServiceGraph Edge              │    │
│  │  - 每个 ServiceGroup 有独立的 EdgeStore                     │    │
│  │  - 支持同 Tenant 自动发现 or 显式声明                       │    │
│  │  - 支持跨 Tenant 的平台级服务串联                           │    │
│  └─────────────────────────────────────────────────────────┘    │
│                                                                  │
└────────────────────────────────────────────────────────────────┘
```

### 6.5 配置覆盖机制（借鉴 Tempo Overrides）

采用 Tempo 风格的分层覆盖机制，但粒度到 APP：

```go
// OverrideManager 管理配置覆盖
type OverrideManager struct {
    defaults        *MetricGenConfig          // 全局默认
    tenantOverrides map[string]*MetricGenConfig  // per-tenant 覆盖
    appOverrides    map[AppKey]*AppMetricConfig  // per-app 覆盖（最高优先级）
    
    reloadInterval  time.Duration  // 热加载周期
    mu              sync.RWMutex
}

// GetAppConfig 获取指定 APP 的最终配置（按优先级合并）
func (m *OverrideManager) GetAppConfig(tenantID, appID string) *AppMetricConfig {
    m.mu.RLock()
    defer m.mu.RUnlock()
    
    // 优先级：app override > tenant override > defaults
    key := AppKey{TenantID: tenantID, AppID: appID}
    
    if cfg, ok := m.appOverrides[key]; ok {
        return cfg
    }
    if cfg, ok := m.tenantOverrides[tenantID]; ok {
        return cfg.ToAppConfig(appID)
    }
    return m.defaults.ToAppConfig(appID)
}
```

配置文件示例：

```yaml
metric_generator:
  # 全局默认
  defaults:
    cardinality_limit: 2000
    dimensions: [http.method, http.status_code]
    histogram:
      buckets: [5, 10, 25, 50, 100, 250, 500, 1000, 2500, 5000, 10000]
    service_graph:
      auto_discover: true
      max_items: 10000
      ttl: 10s

  # Per-Tenant 覆盖（适用于该 Tenant 下所有 APP 的默认值）
  tenant_overrides:
    "large-customer":
      cardinality_limit: 10000
      dimensions: [http.method, http.status_code, http.route]
  
  # Per-APP 覆盖（最高优先级）
  app_overrides:
    - tenant_id: "large-customer"
      app_id: "high-traffic-gateway"
      config:
        cardinality_limit: 50000
        rate_limit: 200000  # 该 APP 流量大，需要更高限制
        dimensions: [http.method]  # 只保留少数维度，控制基数
    
    - tenant_id: "large-customer"
      app_id: "low-priority-batch"
      config:
        enabled: false  # 批处理服务不需要指标
```

### 6.6 自动 APP 发现与回收

```go
// AppDiscovery 自动发现和管理 APP 的生命周期
type AppDiscovery struct {
    activeApps    map[AppKey]*AppMetricSpace
    maxAppsPerTenant int           // 每 Tenant 最大 APP 数（防止泄露）
    idleTimeout   time.Duration    // APP 空闲多久后回收
    mu            sync.RWMutex
}

// GetOrCreate 首次看到某 APP 时自动创建其聚合空间
func (d *AppDiscovery) GetOrCreate(tenantID, appID string, configMgr *OverrideManager) *AppMetricSpace {
    key := AppKey{TenantID: tenantID, AppID: appID}
    
    d.mu.RLock()
    if space, ok := d.activeApps[key]; ok {
        space.LastActive = time.Now()
        d.mu.RUnlock()
        return space
    }
    d.mu.RUnlock()
    
    // 双重检查锁
    d.mu.Lock()
    defer d.mu.Unlock()
    
    if space, ok := d.activeApps[key]; ok {
        space.LastActive = time.Now()
        return space
    }
    
    // 检查 Tenant 下的 APP 数量限制
    if d.countAppsForTenant(tenantID) >= d.maxAppsPerTenant {
        return d.overflowSpace(tenantID)  // 超限时合并到 overflow APP
    }
    
    config := configMgr.GetAppConfig(tenantID, appID)
    space := newAppMetricSpace(tenantID, appID, config)
    d.activeApps[key] = space
    return space
}

// CleanupIdle 定期清理长时间无数据的 APP 聚合空间
func (d *AppDiscovery) CleanupIdle() {
    d.mu.Lock()
    defer d.mu.Unlock()
    
    now := time.Now()
    for key, space := range d.activeApps {
        if now.Sub(space.LastActive) > d.idleTimeout {
            space.Close()  // 最后一次 flush + 释放资源
            delete(d.activeApps, key)
        }
    }
}
```

---

## 7. 性能设计

### 7.1 内存优化

| 优化手段 | 说明 |
|----------|------|
| **Buffer 复用** | 使用 `sync.Pool` + `bytes.Buffer` 构建 metric key，减少 GC 压力 |
| **LRU 缓存** | Resource 和 Series 使用有界 LRU，自动淘汰低频访问项 |
| **Overflow 桶** | 基数超限后不再创建新 series，合并到统一溢出桶 |
| **按需分配** | Generator 只在被启用时才分配内存 |

### 7.2 CPU 优化

| 优化手段 | 说明 |
|----------|------|
| **批量处理** | `ConsumeTraces` 接收批量 Span，一次锁操作处理整批 |
| **锁粒度** | Generator 内部使用分片锁（per-tenant）避免全局争抢 |
| **Zero-copy 维度提取** | 使用 pdata 原生 API，不做额外序列化 |
| **MapHash** | 维度组合使用 128-bit hash 作为 map key，避免字符串拼接 |

### 7.3 吞吐量基准目标

| 指标 | 目标值 |
|------|--------|
| 单实例 Span 处理吞吐 | ≥ 100K spans/s |
| ConsumeTraces P99 延迟 | < 5ms（per batch） |
| 内存占用（10K series） | < 200MB |
| Flush 耗时 | < 50ms（10K series） |

### 7.4 背压处理

```go
// ConsumeTraces 不应阻塞太久，如果聚合器压力大则快速返回
func (c *metricGenConnector) ConsumeTraces(ctx context.Context, td ptrace.Traces) error {
    // 非阻塞：如果 aggregator 正在 flush，不等待锁
    // 而是将 span 放入 ring buffer，由后台 goroutine 异步处理
    if c.config.AsyncMode {
        return c.asyncProcess(ctx, td)
    }
    // 同步模式：直接处理
    return c.syncProcess(ctx, td)
}
```

---

## 8. 可测试性设计

### 8.1 接口抽象（便于 Mock）

```go
// 所有外部依赖通过接口注入
type Dependencies struct {
    MetricsConsumer consumer.Metrics       // 下游 consumer（可 mock）
    TenantStore     TenantConfigStore      // 租户配置存储（可 mock）
    Clock           Clock                  // 时钟接口（测试可控制时间）
    Logger          *zap.Logger
}

// Clock 接口用于测试时控制时间
type Clock interface {
    Now() time.Time
    NewTicker(d time.Duration) *time.Ticker
}

// TenantConfigStore 租户配置存储接口
type TenantConfigStore interface {
    GetTenantConfig(ctx context.Context, tenantID string) (*TenantMetricConfig, error)
    WatchConfigChanges(ctx context.Context, callback func(tenantID string, config *TenantMetricConfig))
}
```

### 8.2 单元测试示例

```go
func TestREDGenerator_ProcessSpan(t *testing.T) {
    // Arrange
    gen := NewREDGenerator(&REDConfig{
        Dimensions: []Dimension{{Name: "http.method"}},
        Histogram:  HistogramConfig{Buckets: []float64{10, 50, 100}},
    })

    span := newTestSpan(
        withDuration(50 * time.Millisecond),
        withAttribute("http.method", "GET"),
        withStatus(ptrace.StatusCodeOk),
    )

    // Act
    gen.ProcessSpan(context.Background(), "tenant-1", newTestResource(), span)

    // Assert
    series := gen.Collect("tenant-1")
    assert.Len(t, series, 2) // calls + duration

    callsSeries := findSeries(series, "calls")
    assert.Equal(t, int64(1), callsSeries.Sum())
    assert.Equal(t, "GET", callsSeries.Attributes().Get("http.method"))

    durationSeries := findSeries(series, "duration")
    assert.Equal(t, 50.0, durationSeries.HistogramValue()) // 落入 50ms 桶
}

func TestMetricAggregator_CardinalityLimit(t *testing.T) {
    agg := NewMetricAggregator(&AggregatorConfig{
        DefaultCardinalityLimit: 3,
    })

    // 前 3 个不同的 key 正常通过
    for i := 0; i < 3; i++ {
        key, overflow := agg.CheckCardinality("tenant-1", fmt.Sprintf("key-%d", i))
        assert.False(t, overflow)
        assert.Equal(t, fmt.Sprintf("key-%d", i), key)
    }

    // 第 4 个触发 overflow
    key, overflow := agg.CheckCardinality("tenant-1", "key-3")
    assert.True(t, overflow)
    assert.Equal(t, overflowKey, key)
}

func TestServiceGraphGenerator_EdgePairing(t *testing.T) {
    gen := NewServiceGraphGenerator(&ServiceGraphConfig{
        Store: StoreConfig{MaxItems: 100, TTL: 5 * time.Second},
    })

    traceID := pcommon.NewTraceIDEmpty()
    clientSpanID := pcommon.NewSpanIDEmpty()

    // Client span
    clientSpan := newTestSpan(
        withTraceID(traceID),
        withSpanID(clientSpanID),
        withKind(ptrace.SpanKindClient),
    )
    gen.ProcessSpan(ctx, "t1", newResource("service-a"), clientSpan)

    // Server span (ParentSpanID = Client SpanID)
    serverSpan := newTestSpan(
        withTraceID(traceID),
        withParentSpanID(clientSpanID),
        withKind(ptrace.SpanKindServer),
    )
    gen.ProcessSpan(ctx, "t1", newResource("service-b"), serverSpan)

    // Edge 应该已完成配对
    series := gen.Collect("t1")
    assert.NotEmpty(t, series)
    // 验证 client=service-a, server=service-b
}
```

### 8.3 集成测试

```go
func TestMetricGenConnector_EndToEnd(t *testing.T) {
    // 使用 mock consumer 验证完整流程
    mockConsumer := &mockMetricsConsumer{}
    
    connector := newTestConnector(t, &Config{
        MetricsFlushInterval: 100 * time.Millisecond,
        RED: &REDConfig{Enabled: true},
    }, mockConsumer)

    // 发送测试 traces
    td := generateTestTraces(100)
    err := connector.ConsumeTraces(context.Background(), td)
    require.NoError(t, err)

    // 等待 flush
    time.Sleep(200 * time.Millisecond)

    // 验证输出
    metrics := mockConsumer.AllMetrics()
    assert.NotEmpty(t, metrics)
    // 验证指标名、维度、值的正确性
}
```

---

## 9. 目录结构设计

```
connector/
└── metricgenconnector/
    ├── config.go                    // 配置定义
    ├── config_test.go
    ├── factory.go                   // Factory 注册
    ├── factory_test.go
    ├── connector.go                 // 核心 Connector 实现
    ├── connector_test.go
    ├── tenant.go                    // TenantResolver + 多租户逻辑
    ├── tenant_test.go
    ├── flusher.go                   // MetricFlusher
    ├── flusher_test.go
    ├── generator/                   // Generator 接口 + 实现
    │   ├── generator.go             // Generator 接口定义
    │   ├── red/                     // RED Generator
    │   │   ├── generator.go
    │   │   ├── generator_test.go
    │   │   └── config.go
    │   ├── servicegraph/            // ServiceGraph Generator
    │   │   ├── generator.go
    │   │   ├── generator_test.go
    │   │   ├── config.go
    │   │   └── store.go             // Edge Store
    │   └── custom/                  // Custom Generator (预留)
    │       ├── generator.go
    │       └── config.go
    ├── aggregator/                  // 聚合管理
    │   ├── aggregator.go
    │   ├── aggregator_test.go
    │   ├── cardinality.go           // 基数控制
    │   └── cache.go                 // LRU Cache
    └── internal/
        ├── metrics/                 // 内部指标类型
        │   ├── histogram.go
        │   ├── counter.go
        │   └── unit.go
        └── testutil/                // 测试工具
            └── helpers.go
```

---

## 10. 与现有系统集成

### 10.1 在 components.go 中注册

```go
// cmd/customcol/components.go
import (
    "go.opentelemetry.io/collector/custom/connector/metricgenconnector"
)

factories.Connectors, err = connector.MakeFactoryMap(
    forwardconnector.NewFactory(),
    routingconnector.NewFactory(),
    spanmetricsconnector.NewFactory(),  // 保留，渐进替换
    metricgenconnector.NewFactory(),    // 新增
)
```

### 10.2 配置示例

```yaml
connectors:
  metricgen:
    metrics_flush_interval: 15s
    aggregation_temporality: "cumulative"
    namespace: "traces.spanmetrics"

    # RED 指标生成器
    red:
      enabled: true
      dimensions:
        - name: http.method
          default: GET
        - name: http.status_code
        - name: http.route
        - name: rpc.method
        - name: rpc.service
      histogram:
        unit: ms
        explicit:
          buckets: [5, 10, 25, 50, 100, 250, 500, 1000, 2500, 5000, 10000]
      cardinality_limit: 5000
      series_expiration: 15m

    # ServiceGraph 生成器
    service_graph:
      enabled: true
      store:
        max_items: 10000
        ttl: 10s
      dimensions:
        - name: http.method
      enable_virtual_node: true
      peer_attributes:
        - server.address
        - peer.service
        - db.name
      enable_messaging_system: true
      auto_discover: true            # Tenant 内自动发现
      auto_discover_scope: "tenant"
      exclude_apps:                  # 自动发现时排除的 APP
        - "otel-collector-agent"
        - "envoy-sidecar"

    # APP 级别隔离配置
    app_isolation:
      enabled: true
      max_apps_per_tenant: 500       # 每 Tenant 最大 APP 数
      default_cardinality_limit: 2000 # 每 APP 默认基数限制
      idle_timeout: 1h               # APP 空闲 1h 后回收
      cleanup_interval: 5m

    # ServiceGroup 配置（显式声明跨 APP 串联关系）
    service_groups:
      - name: "order-flow"
        tenant_id: "tenant-a"
        members:
          - app_id: "api-gateway"
          - app_id: "order-service"
          - app_id: "inventory-service"
        config:
          max_items: 10000
          ttl: 10s
      
      - name: "payment-chain"
        tenant_id: "tenant-a"
        members:
          - app_id: "order-service"
          - app_id: "payment-service"
          - app_id: "bank-gateway"
        config:
          max_items: 5000
          ttl: 15s
      
      # 跨 Tenant 的平台级服务组
      - name: "platform-gateway"
        members:
          - app_id: "unified-gateway"
            tenant_id: "platform"
          - app_id: "order-service"
            tenant_id: "tenant-a"
          - app_id: "user-service"
            tenant_id: "tenant-b"
        config:
          max_items: 20000
          ttl: 10s

    # 采样校正
    sampling_correction:
      enabled: true
      attribute: "sampling.rate"

    # 配置覆盖（分层：defaults → tenant → app）
    overrides:
      dynamic_config_path: "/etc/collector/overrides.yaml"
      dynamic_reload_period: 10s
      
      # Per-Tenant 覆盖
      tenant_overrides:
        "large-customer":
          cardinality_limit: 10000
          dimensions:
            - name: http.method
            - name: http.status_code
            - name: http.route
      
      # Per-APP 覆盖（最高优先级）
      app_overrides:
        - tenant_id: "large-customer"
          app_id: "high-traffic-gateway"
          config:
            cardinality_limit: 50000
            rate_limit: 200000
            dimensions:
              - name: http.method
        
        - tenant_id: "large-customer"
          app_id: "batch-processor"
          config:
            enabled: false  # 批处理服务不需要指标

service:
  pipelines:
    traces:
      receivers: [agent_gateway]
      processors: [tokenauth, memory_limiter, batch]
      exporters: [metricgen, observability_storage]

    metrics/generated:
      receivers: [metricgen]
      processors: [batch]
      exporters: [observability_storage]
```

### 10.3 与查询层对接

更新 `extension/adminext/tempo_handler.go` 的 `translateTraceQLMetric` 映射：

```go
// 保持向后兼容：指标名和现有 spanmetrics 一致
func translateTraceQLMetric(fn string) string {
    switch fn {
    case "quantile_over_time":
        return "traces.spanmetrics.duration_milliseconds"
    case "histogram_over_time":
        return "traces.spanmetrics.duration_milliseconds"
    default:
        return "traces.spanmetrics.calls"
    }
}
```

---

## 11. 关键决策

| # | 决策 | 理由 |
|---|------|------|
| 1 | **使用 Connector 组件机制** | 复用 OTel Collector 的 Pipeline 编排；热插拔；标准生命周期管理 |
| 2 | **前置 loadbalancingexporter 做 TraceID 路由** | 与开源方案一致；Connector 本身保持简单；不引入额外分布式协调 |
| 3 | **策略模式实现 Generator** | 开闭原则；新增指标类型不改核心代码；独立测试 |
| 4 | **APP 级隔离（非 Tenant 级）** | 粒度更合理：APP-A 爆表不影响 APP-B；Tenant 只做鉴权分组 |
| 5 | **ServiceGroup 机制实现跨 APP 串联** | ServiceGraph 天然跨服务；通过配置声明哪些 APP 可建立拓扑关系；支持同 Tenant 自动发现 + 跨 Tenant 显式配置 |
| 6 | **默认 Cumulative + 支持 Delta** | Cumulative 适合 Prometheus 生态；Delta 适合分布式场景下的自动合并 |
| 7 | **基数控制：LRU + Overflow 桶（Per-APP）** | OTel 标准溢出机制；不会因高基数 OOM；每个 APP 独立限制 |
| 8 | **分层配置覆盖（defaults → tenant → app）** | 借鉴 Tempo overrides；全局默认 + Tenant 覆盖 + APP 精确覆盖；支持热加载 |
| 9 | **ServiceGraph 借鉴 Tempo 实现** | Edge Store + FIFO 过期 + 虚拟节点推断，经过生产验证的成熟方案 |
| 10 | **渐进式替换而非一步到位** | 保留 spanmetricsconnector，新旧并行验证后再切换 |
| 11 | **Clock 接口 + 依赖注入** | 可测试性；测试中控制时间前进 |
| 12 | **Gateway + Worker 两层架构** | Gateway 无状态做路由；Worker 有状态做聚合；各层独立扩缩容 |

---

## 12. 风险与缓解

| 风险 | 严重度 | 缓解措施 |
|------|--------|---------|
| 一致性哈希重分配导致短暂指标不准 | 中 | 短 TTL + Delta 模式自愈；扩缩容走滚动更新而非突变 |
| 高基数 APP 内存爆炸 | 高 | Per-APP CardinalityLimit + Overflow 桶 + 降级开关 |
| APP 数量膨胀导致内存增长 | 中 | maxAppsPerTenant 硬上限 + 空闲回收 + Overflow APP 兜底 |
| Flush 期间 ConsumeTraces 阻塞 | 中 | Flush 时快速 swap 状态，不持长锁；或使用 Double Buffer |
| ServiceGraph Edge 永远配对不上 | 低 | TTL 超时后推断虚拟节点并释放；配合 TraceID 路由降低概率 |
| ServiceGroup 配置错误导致拓扑不完整 | 低 | 默认启用 auto_discover（Tenant 内自动组 Group）；显式配置为增强项 |
| 跨 Tenant ServiceGroup 的安全风险 | 中 | 跨 Tenant 配置需要两端 Tenant 都在配置中明确声明；只暴露拓扑关系，不暴露业务维度 |
| 与现有 spanmetrics 指标名不兼容 | 中 | 默认使用相同 namespace 和指标命名；提供兼容模式 |

---

## 13. Roadmap

### Sprint 1：基础骨架 + RED Generator（2 周）

**目标**：实现最小可用版本，功能等价于当前 spanmetricsconnector

| 任务 | 详情 | 验收标准 |
|------|------|---------|
| 1.1 Connector 骨架 | Factory + Config + Connector 接口实现 | 能注册到 Collector 并启动 |
| 1.2 Generator 接口 | 定义接口 + REDGenerator 实现 | 单元测试通过 |
| 1.3 MetricAggregator | LRU Cache + CardinalityLimit + Overflow | 基数控制单元测试通过 |
| 1.4 MetricFlusher | 定时 flush + Cumulative/Delta 双模式 | 集成测试验证输出正确 |
| 1.5 配置兼容 | 配置项兼容现有 spanmetrics 配置格式 | 无缝替换不改上层配置 |
| 1.6 E2E 验证 | 与现有 spanmetrics 并行运行，对比指标输出 | 数值一致性 > 99% |

### Sprint 2：APP 级隔离 + 采样校正（2 周）

**目标**：实现 APP 级别的指标隔离和配置覆盖机制

| 任务 | 详情 | 验收标准 |
|------|------|---------|
| 2.1 AppDiscovery | 自动发现 APP（通过 service.name），管理 APP 聚合空间生命周期 | 新 APP 首次出现时自动创建聚合空间 |
| 2.2 Per-APP 基数控制 | 每个 APP 独立的 CardinalityLimit + Overflow | APP-A 基数超限不影响同 Tenant 下的 APP-B |
| 2.3 OverrideManager | 分层配置覆盖：defaults → tenant → app | 配置优先级正确；单元测试覆盖 |
| 2.4 配置热加载 | 监听外部覆盖文件变更，定时 reload | 修改覆盖文件后 10s 内生效 |
| 2.5 APP 级降级 | 支持禁用某个 APP 的指标生成 | 通过配置覆盖立即停止该 APP 的聚合 |
| 2.6 采样率校正 | 根据 Span 上的采样率属性校正计数 | 1% 采样率下 count 自动乘以 100 |
| 2.7 APP 空闲回收 | 长时间无数据的 APP 自动释放资源 | 空闲 1h 后自动回收，内存稳定 |

### Sprint 3：ServiceGraph Generator + ServiceGroup（2.5 周）

**目标**：实现 Tempo 风格的 ServiceGraph，支持跨 APP 服务串联配置

| 任务 | 详情 | 验收标准 |
|------|------|---------|
| 3.1 EdgeStore（借鉴 Tempo） | FIFO 链表 + TTL 过期 + maxItems + onComplete/onExpire 回调 | 内存稳定、过期清理正确；单元测试覆盖 |
| 3.2 Span 配对逻辑 | Client(SpanID)↔Server(ParentSpanID) + Producer↔Consumer | Edge 完成率 > 95%（有 TraceID 路由时） |
| 3.3 虚拟节点推断 | 超时未配对时通过 peer attributes 推断对端 | 未 instrument 的服务正确出现在拓扑中 |
| 3.4 ServiceGraphRouter | 按 ServiceGroup 配置路由 Span 到正确的 EdgeStore | 同一 ServiceGroup 内的 APP 可建立 Edge |
| 3.5 自动发现模式 | 同 Tenant 下的 APP 默认组成一个 ServiceGroup | 无需手动配置即可看到 Tenant 内的拓扑 |
| 3.6 显式 ServiceGroup | 配置文件声明跨 APP 甚至跨 Tenant 的串联关系 | 平台级中间件可出现在多 Tenant 的拓扑中 |
| 3.7 ServiceGraph 指标 | 生成 Tempo 兼容的 edge 级指标 | 可在 Grafana 中展示服务拓扑（Node Graph Panel） |
| 3.8 分布式验证 | 配合 loadbalancingexporter 验证多实例正确性 | 3 实例部署，Edge 配对率不低于单实例 |

### Sprint 4：分布式部署 + 性能调优（1.5 周）

**目标**：生产可用的分布式部署方案

| 任务 | 详情 | 验收标准 |
|------|------|---------|
| 4.1 Gateway + Worker 拓扑 | 配置模板：Gateway 用 loadbalancingexporter，Worker 用 metricgen | 部署文档完整 |
| 4.2 K8s 部署配置 | Deployment + Service + HPA | 自动扩缩容验证通过 |
| 4.3 性能基准测试 | Benchmark 测试 ConsumeTraces 吞吐和延迟 | ≥ 100K spans/s，P99 < 5ms |
| 4.4 内存压力测试 | 模拟高基数场景验证 OOM 保护 | 10K series 内存 < 200MB |
| 4.5 扩缩容平滑验证 | 滚动更新期间指标正确性 | 无指标断崖/跳变 |
| 4.6 可观测性完善 | 暴露 metricgen 自身的运行指标 | 聚合延迟、基数使用率、flush 耗时等 |

### Sprint 5：高级特性 + 正式切换（2 周）

**目标**：替换旧组件，上线生产

| 任务 | 详情 | 验收标准 |
|------|------|---------|
| 5.1 Custom Generator 框架 | 支持通过配置注册自定义聚合逻辑 | 用户可配置新的维度/指标组合 |
| 5.2 Exemplar 支持 | 在指标中关联 TraceID 作为 Exemplar | Grafana Tempo 中可点击跳转 Trace |
| 5.3 查询层适配 | 更新 tempo_handler 的指标映射逻辑 | TraceQL Metrics 查询无感切换 |
| 5.4 灰度切换 | Feature Flag 控制新旧 connector 切换 | 灰度 10% → 50% → 100% |
| 5.5 下线旧组件 | 移除 spanmetricsconnector 依赖 | 代码清理完成 |
| 5.6 文档完善 | 使用指南、运维手册、性能调优指南 | 文档齐全 |

---

## 14. 决策记录

| # | 决策项 | 选择 | 备选方案 | 理由 |
|---|--------|------|---------|------|
| 1 | 组件类型 | ✅ Connector | Processor / Exporter | Connector 是 OTel 标准的 Traces→Metrics 桥接组件类型 |
| 2 | 分布式策略 | ✅ 前置 loadbalancingexporter | 内建一致性哈希 / Redis Stream | 复用成熟开源方案；Connector 保持简单无外部依赖 |
| 3 | Generator 扩展机制 | ✅ 接口 + 注册模式 | OTTL 表达式 / 脚本引擎 | Go 接口编译时安全；性能最优；测试最方便 |
| 4 | 隔离粒度 | ✅ APP 级（service.name） | Tenant 级 / 无隔离 | Tenant 太粗（大 APP 影响小 APP）；APP 级精确控制基数和配置 |
| 5 | 跨 APP 服务串联 | ✅ ServiceGroup 配置机制 | 全局无限制 / 严格 APP 隔离 | ServiceGraph 天然跨服务；配置声明允许的串联关系；支持自动发现 + 显式声明两种模式 |
| 6 | ServiceGraph 实现 | ✅ 借鉴 Tempo（Edge Store + FIFO + Virtual Node） | 自研全新方案 / 直接用 contrib servicegraphconnector | Tempo 方案经过大规模生产验证；Virtual Node 推断解决了未 instrument 服务问题 |
| 7 | 聚合 Temporality | ✅ 默认 Cumulative，可选 Delta | 仅 Delta / 仅 Cumulative | Cumulative 适配 Prometheus；Delta 适配分布式 |
| 8 | 基数控制 | ✅ LRU + Overflow + Per-APP | 全局限制 / Per-tenant 限制 | Per-APP 最公平；Overflow 保证数学正确 |
| 9 | 配置覆盖 | ✅ 分层覆盖（defaults → tenant → app） | 单层配置 / 只有全局配置 | 借鉴 Tempo overrides；灵活度最高；支持热加载 |
| 10 | 替换策略 | ✅ 渐进式灰度 | 一步切换 | 降低风险；可随时回滚 |

---

## 15. 遗留问题

- [ ] 是否需要支持 LogsToMetrics（从日志生成指标）？
- [ ] ServiceGraph 生成的指标是否需要单独的 Pipeline 输出？
- [ ] 配置覆盖的存储：复用 Redis / 控制面 / 独立 YAML 文件？
- [ ] 高基数场景下是否需要自适应采样（动态调整维度）？
- [ ] 是否需要对接外部指标存储（如 Prometheus TSDB）做远端聚合？
- [ ] ServiceGroup 的动态管理：是否需要通过 API 动态创建/修改 ServiceGroup，还是只支持配置文件？
- [ ] 跨 Tenant ServiceGroup 的鉴权策略：哪些 Tenant 有权将自己的 APP 加入跨 Tenant 的 ServiceGroup？
- [ ] APP 自动发现时新 APP 首次出现的冷启动行为：是否需要延迟配对，等待 APP 配置加载完成？
- [ ] Tempo 的 `enable_client_server_prefix` 是否需要支持（在指标名中区分 client_/server_ 前缀）？
