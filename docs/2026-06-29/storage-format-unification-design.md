# 存储格式统一化方案 — Writer/Reader 同协议 + OTLP 对齐

> **状态**：Sprint 1 ✅ | 1.5 ✅ | 2 ✅ | 3 ✅ | 4 ✅ | Sprint 5 🔲  
> **创建日期**：2026-06-29  
> **Sprint 1 完成**：2026-06-30  
> **Sprint 1.5 完成**：2026-06-30  
> **Sprint 2 完成**：2026-06-30  
> **目标**：统一写入和读取的数据结构，字段名对齐 OTLP 标准，消除多层类型转换

---

## 一、现状问题

### 1.1 写入和读取使用不同类型

```
写入: OTLP ptrace.Span → spanToDoc() → map[string]any  ──► ES
读取: ES → spanDocument struct → convertSpan() → public Span
```

| 层 | 写入侧 | 读取侧 | 字段名不一致示例 |
|----|--------|--------|----------------|
| 写入 | `map[string]any{"operation_name": ...}` | — | `operation_name` |
| 读取内部 | — | `spanDocument{OperationName: ...}` | `OperationName` |
| 读取公共 | — | `public Span{Name: ...}` | `name` |

**改了写入忘了改读取就是 bug**，没有编译时检查。

### 1.2 多层转换逻辑散落

```
convertSpan()          ← reader_adapter.go (41 行)
convertTrace()         ← reader_adapter.go (含 root span 检测)
MapToKeyValues()       ← types.go
anyToAnyValue()        ← types.go
NormalizeSpanKind()    ← types.go (处理 5 种输入格式)
NormalizeStatusCode()  ← types.go (处理 3 种输入格式)
computeDurationNano()  ← reader_adapter.go
TimeToUnixNano()       ← types.go
```

7 个转换函数、2 个文件，违反 SRP。

### 1.3 新 Provider 成本高

添加新存储后端（MongoDB、ClickHouse）需要：
1. 定义新的内部 Span 类型（类比 `elasticsearch.Span`）
2. 实现新的 writer（类比 `spanToDoc`）
3. 实现新的 reader（类比 `spanDocument`）
4. 实现新的 adapter（类比 `convertSpan`）

其中步骤 3、4 的转换逻辑几乎是 Provider 无关的，但因为类型绑定在 elasticsearch 包下无法复用。

---

## 二、设计目标

1. **Writer 和 Reader 使用同一个 struct** — 单一事实源，改一处两边生效
2. **字段名对齐 OTLP JSON 标准** — `name` 不是 `operation_name`，`kind` 不是 `span_kind`
3. **属性保持紧凑** — flat map 不展开为 `[]KeyValue`，避免 2.4x 膨胀
4. **API 边界仅转换一次** — 只在 `reader_adapter` 出口做 `map[string]any → []KeyValue`
5. **消除 NormalizeXxx 函数** — 存储时就写入标准格式，读取直接使用
6. **新 Provider 只需实现 Write/Read 两个方法** — 转换逻辑复用

---

## 三、分层架构

```
Layer 5: 公共 API        public Span / Trace / LogRecord  (types.go)
Layer 4: 适配器层          reader_adapter.go  (StoredSpan → public Span, 仅属性格式转换)
Layer 3: Provider 接口     SpanReader / SpanWriter 接口  (provider.go)
Layer 2: 转换层           OTLP Span ↔ StoredSpan  (公共函数, 所有 Provider 复用)
Layer 1: Canonical Model  StoredSpan / StoredLogRecord  (stored_span.go)
Layer 0: 存储后端          ES / PG / MongoDB / ClickHouse
```

**关键约束**：
- Layer 1 是唯一的数据格式，从存储读出的就是 StoredSpan，写入存储的也是 StoredSpan
- Layer 2 的转换函数是 package-level 函数，不绑定任何 Provider，所有后端复用
- Layer 3 的接口只接收/返回 Layer 1 的类型，与具体存储解耦
- Layer 4 只做数据结构适配（map→KeyValue），不懂业务语义

### 3.1 Layer 1 — Canonical Model（统一存储类型）

```go
// 文件: extension/observabilitystorageext/storedmodel/stored_span.go
// 独立子包，observabilitystorageext 和 provider/elasticsearch 都 import 它，避免循环依赖
// 字段名对齐 OTLP JSON 标准，属性用紧凑 flat map

type StoredSpan struct {
    // ═══ OTLP 核心字段 ═══
    TraceID       string         `json:"traceId"`
    SpanID        string         `json:"spanId"`
    ParentSpanID  string         `json:"parentSpanId,omitempty"`
    Name          string         `json:"name"`                    // OTLP 标准名称
    Kind          string         `json:"kind"`                    // OTLP enum 字符串
    StartUnixNano int64          `json:"startTimeUnixNano"`       // ← long 纳秒, ES range 优化
    EndUnixNano   int64          `json:"endTimeUnixNano"`         // ← long 纳秒
    Status        StoredStatus   `json:"status"`                  // 嵌套 {code, message}
    TraceState    string         `json:"traceState,omitempty"`    // ← 恢复写入 ⬆️

    // ═══ 紧凑属性（flat map）═���
    Attributes    map[string]any `json:"attributes"`
    Resource      map[string]any `json:"resource"`

    // ═══ Scope 信息（原先丢弃）═══
    Scope         StoredScope    `json:"scope,omitempty"`         // ← 新增 ⬆️

    // ═══ Events & Links ═══
    Events        []StoredEvent  `json:"events,omitempty"`
    Links         []StoredLink   `json:"links,omitempty"`         // ← attributes 已补全 ⬆️

    // ═══ 派生字段（写入时计算，查询时直接使用）════
    DurationNano  int64          `json:"durationNano"`             // ← 存为 long, 支持 ES 排序/过滤
    ServiceName   string         `json:"serviceName"`
    AppID         string         `json:"appId,omitempty"`
}

// StoredScope preserves InstrumentationScope info that was previously discarded.
// Aligned with opentelemetry.proto.common.v1.InstrumentationScope.
type StoredScope struct {
    Name       string         `json:"name"`                      // e.g. "io.opentelemetry.contrib"
    Version    string         `json:"version,omitempty"`         // e.g. "1.0.0"
    Attributes map[string]any `json:"attributes,omitempty"`
}

type StoredStatus struct {
    Code    string `json:"code"`      // "STATUS_CODE_OK" / "STATUS_CODE_ERROR"
    Message string `json:"message,omitempty"`
}

type StoredEvent struct {
    TimeUnixNano int64          `json:"timeUnixNano"`            // ← long 纳秒
    Name         string         `json:"name"`
    Attributes   map[string]any `json:"attributes,omitempty"`
}

type StoredLink struct {
    TraceID    string         `json:"traceId"`
    SpanID     string         `json:"spanId"`
    TraceState string         `json:"traceState,omitempty"`     // ← 恢复 ⬆️
    Attributes map[string]any `json:"attributes,omitempty"`     // ← 恢复 ⬆️
}
```

#### 类型选型：时间与 Duration 为什么用 `int64` 而不是 `uint64`

| 决策 | 原因 |
|------|------|
| **不用 `uint64`** | JSON 没有无符号整数类型。`json.Marshal(uint64(math.MaxUint64))` 在某些实现下行为不一致。ES `long` 和 PG `BIGINT` 都是有符号的 |
| **用 `int64`** | 全程对齐：Go int64 → JSON number → ES long / PG BIGINT。int64 纳秒可表示 ±292 年，duration 场景远超实际需求 |
| **不用 `string`** | 字符串无法做 ES range 聚合、排序、过滤，需要在查询时把所有值 parse 成数字，性能和功能都打折 |

**Source 时间戳的来源**：
- OTLP proto 中 `start_time_unix_nano` 和 `end_time_unix_nano` 是 `fixed64` 类型
- 大部分 SDK 实现中，这两个值来自 `time.Now().UnixNano()`，返回 `int64`
- 极少数情况（时钟回拨、SDK bug）可能出现 end < start
- `safeDuration()` 在转换层做防御：先转 `int64` 再减，负数 clamp 为 0

```
Go SDK                     OTLP proto          StoredSpan          ES/PG
time.Now().UnixNano()  →  fixed64         →  int64            →  long
(int64)                    (wire: uint64)      (JSON: number)      (signed 64bit)
```

### 3.2 Layer 2 — 转换层（OTLP ↔ StoredSpan，公共复用）

```go
// 文件: extension/observabilitystorageext/stored_span.go (同文件)
// package-level 函数，不绑定任何 Provider

// ConvertOTLPSpan 将 OTLP proto Span 转为统一存储格式。
// 所有 Provider 的 writer 都调用此函数，保证格式一致。
func ConvertOTLPSpan(span ptrace.Span, resource pcommon.Map) StoredSpan {
    return StoredSpan{
        TraceID:       span.TraceID().String(),
        SpanID:        span.SpanID().String(),
        ParentSpanID:  toParentID(span.ParentSpanID()),
        Name:          span.Name(),
        Kind:          span.Kind().String(),                 // ← 存入时保证标准格式
        StartUnixNano: int64(span.StartTimestamp()),
        EndUnixNano:   int64(span.EndTimestamp()),
        DurationNano:  safeDuration(span.StartTimestamp(), span.EndTimestamp()), // ← 防御式计算
        Status: StoredStatus{
            Code:    span.Status().Code().String(),
            Message: span.Status().Message(),
        },
        TraceState: span.TraceState().AsRaw(),               // ← 恢复, 之前丢失
        Scope: StoredScope{
            Name:       scope.Name(),
            Version:    scope.Version(),
            Attributes: pcommonMapToFlat(scope.Attributes()), // ← 之前丢弃, 保留
        },
        Attributes: pcommonMapToFlat(span.Attributes()),
        Resource:   pcommonMapToFlat(resource),
        Events:     convertEvents(span.Events()),
        Links:      convertLinks(span.Links()),              // ← attrs 补全
        ServiceName: serviceName,
        AppID:       appID,
    }
}

// convertLinks 补全了之前丢失的 TraceState 和 Attributes。
func convertLinks(links ptrace.SpanLinkSlice) []StoredLink {
    if links.Len() == 0 {
        return nil
    }
    result := make([]StoredLink, links.Len())
    for i := 0; i < links.Len(); i++ {
        l := links.At(i)
        result[i] = StoredLink{
            TraceID:    l.TraceID().String(),
            SpanID:     l.SpanID().String(),
            TraceState: l.TraceState().AsRaw(),
            Attributes: pcommonMapToFlat(l.Attributes()),    // ← 补全
        }
    }
    return result
}

// safeDuration returns end-start as int64, clamping negative values to 0.
// pcommon.Timestamp is uint64 nanosecond unix time. Direct subtraction
// uint64(end-start) can underflow to a huge positive number if the clock
// has drifted (end < start). Converting to int64 first and saturating at 0
// prevents corrupted duration data from entering the store.
func safeDuration(start, end pcommon.Timestamp) int64 {
    startNs := int64(start)
    endNs := int64(end)
    if endNs > startNs {
        return endNs - startNs
    }
    return 0
}

// StoredSpanToPublic 将存储格式转为公共 API 格式。
// int64 纳秒转为 string（公共 API 用 JSON string 防止精度丢失）。
func StoredSpanToPublic(ss StoredSpan) Span {
    return Span{
        TraceID:           ss.TraceID,
        SpanID:            ss.SpanID,
        ParentSpanID:      ss.ParentSpanID,
        Name:              ss.Name,
        Kind:              SpanKind(ss.Kind),
        StartTimeUnixNano: strconv.FormatInt(ss.StartUnixNano, 10),  // int64 → string
        EndTimeUnixNano:   strconv.FormatInt(ss.EndUnixNano, 10),
        TraceState:        ss.TraceState,
        ServiceName:       ss.ServiceName,
        Status:            SpanStatus{Code: StatusCode(ss.Status.Code), Message: ss.Status.Message},
        Attributes:        MapToKeyValues(ss.Attributes),
        Resource:          MapToKeyValues(ss.Resource),
        Events:            storedEventsToPublic(ss.Events),
        Links:             storedLinksToPublic(ss.Links),
        DurationNano:      strconv.FormatInt(ss.DurationNano, 10),  // 直接用存储值
    }
}
```

#### 实施调整：pdata 短名存储

Go pdata 的 `Kind().String()` / `Code().String()` 返回短名（`"Server"` / `"Ok"`），非 OTLP JSON 全名（`"SPAN_KIND_SERVER"` / `"STATUS_CODE_OK"`）。存储层直接使用 pdata 原始短名，`NormalizeSpanKind`/`NormalizeStatusCode` 保留在公共 API 出口归一化。

### 3.3 Layer 3 — Provider 接口（抽象，与存储无关）

```go
// 文件: extension/observabilitystorageext/provider.go

// SpanWriter 接收公共 StoredSpan，Provider 实现负责持久化
type SpanWriter interface {
    WriteSpans(ctx context.Context, spans []StoredSpan) error
    Flush(ctx context.Context) error
}

// SpanReader 返回公共 StoredSpan，Provider 实现负责查询
type SpanReader interface {
    SearchSpans(ctx context.Context, query SpanQuery) ([]StoredSpan, error)
    GetTrace(ctx context.Context, traceID string) ([]StoredSpan, error)
    GetServices(ctx context.Context, timeRange TimeRange) ([]Service, error)
    GetOperations(ctx context.Context, service string, timeRange TimeRange) ([]Operation, error)
    GetDependencies(ctx context.Context, timeRange TimeRange) ([]Dependency, error)
}
```

**Provider 实现者只需关心**：如何把 `[]StoredSpan` 序列化到具体存储、如何从具体存储反序列化回 `[]StoredSpan`。不需要理解 OTLP proto、不需要知道 public Span 的格式。

### 3.4 Layer 4 — 适配器层（减薄到仅委托 + 属性格式转换）

```go
// 文件: extension/observabilitystorageext/reader_adapter.go

// 适配器只做两件事：
// 1. 委托具体 Provider 的 SpanReader/SpanWriter
// 2. 调用 StoredSpanToPublic() 做最后一次格式转换

func (a *traceReaderAdapter) GetTrace(ctx context.Context, traceID string) (*Trace, error) {
    spans, err := a.inner.GetTrace(ctx, traceID)   // ← 返回 []StoredSpan
    if err != nil {
        return nil, err
    }
    return buildTraceFromStoredSpans(spans), nil    // ← 公共转换逻辑
}

func buildTraceFromStoredSpans(spans []StoredSpan) *Trace {
    publicSpans := make([]Span, len(spans))
    for i, ss := range spans {
        publicSpans[i] = StoredSpanToPublic(ss)     // ← Layer 2 的公共函数
    }
    // ... root span 检测、service 计数等业务逻辑 ...
}
```

### 3.5 Layer 5 — 公共 API（不变）

`types.go` 中的 `Span`、`Trace`、`LogRecord` 等公共类型保持不变。Layer 2 的 `StoredSpanToPublic()` 是唯一的桥接点。

---

## 四、架构对比

### 4.1 当前 — 无抽象层，类型散落

```
Layer 5: public Span (types.go)            ← 独立类型
Layer 4: reader_adapter (ES 耦合)           ← 直接 import elasticsearch
Layer 3: elasticsearch.TraceReader         ← 返回 elasticsearch.Span
Layer 2: ❌ 无公共转换层                     ← normalize 逻辑在 reader_adapter
Layer 1: ❌ 无统一标准类型                   ← spanDocument (ES) vs traceRow (PG)
Layer 0: ES Bulk / PG COPY
```

**问题**：Layer 1 不存在，每个 Provider 定义自己的内部类型；Layer 2 不存在，转换逻辑耦合在 reader_adapter 中。

### 4.2 目标 — 明确分层，每层职责单一

```
Layer 5: public Span / Trace (types.go)          ← 面向 API，不变
         ▲
Layer 4: reader_adapter.go                       ← 薄适配，仅委托 + StoredSpanToPublic
         ▲
Layer 3: SpanReader / SpanWriter 接口 (provider)  ← 抽象接口，与存储无关
         ▲
Layer 2: ConvertOTLPSpan / StoredSpanToPublic    ← 公共转换，所有 Provider 复用
         ▲
Layer 1: StoredSpan                              ← 唯一标准，Writer + Reader 共用
         ▲
Layer 0: ES / PG / MongoDB / ClickHouse          ← 只关心 [StoredSpan] ↔ 存储格式
```

**每层只做一件事**：
- Layer 1：定义"存储格式是什么" — 一个 struct，与任何存储无关
- Layer 2：定义"怎么在 OTLP 和存储格式之间转换" — 纯函数，与任何 Provider 无关
- Layer 3：定义"Provider 需要具备什么能力" — 接口，只依赖 Layer 1 的类型
- Layer 4：组装 Layer 2 + Layer 3，做最终适配 — 不做任何业务逻辑或类型推断
- Layer 5：对 API 消费者暴露的格式 — 独立演进

### 4.3 新架构的核心约束

```
Provider 实现者看到的类型：   StoredSpan (Layer 1)
Provider 实现者调用的函数：   ConvertOTLPSpan() (Layer 2)
Provider 实现者实现的接口：   SpanWriter / SpanReader (Layer 3)
Provider 实现者不需要知道：   OTLP proto / public Span / MapToKeyValues
Adapter 做的事情：           委托 Provider 接口 + 调用 StoredSpanToPublic()
Adapter 不做的事情：          Normalize / 类型推断 / 业务逻辑
```

---

## 五、改动范围

### 5.1 按抽象层组织的变更

| 层 | 动作 | 文件 |
|----|------|------|
| **Layer 1** Canonical Model | 新建 | `storedmodel/stored_span.go`（独立子包，避免循环依赖） |
| **Layer 2** 转换层 | 新建 | `storedmodel/stored_span.go` 内 package-level 函数 |
| **Layer 3** Provider 接口 | 修改 | `provider.go` — SpanReader/Writer 签名为 StoredSpan |
| **Layer 4** 适配器层 | 删减 | `reader_adapter.go` — 替换为委托 + StoredSpanToPublic |
| **Layer 5** 公共 API | 删减 | `types.go` — 删 NormalizeSpanKind/NormalizeStatusCode |
| **Layer 0** ES 具体实现 | 修改 | `trace_writer.go`/`trace_reader.go`/`admin.go` — 字段名同步 |
| **Layer 0** PG 具体实现 | 修改 | PG writer/reader — 同上 |

### 5.2 各层变更细节

#### Layer 1 + 2 — stored_span.go（新建）

```go
// 一行文件包含：
//   - Layer 1: StoredSpan 类型定义
//   - Layer 2: ConvertOTLPSpan()    — OTLP → StoredSpan
//   - Layer 2: StoredSpanToPublic() — StoredSpan → public Span
// 所有 Provider 共享这两个函数，不重复实现
```

#### Layer 3 — provider.go（接口签名调整）

```diff
 type SpanWriter interface {
-    WriteTraces(ctx context.Context, td ptrace.Traces) error
+    WriteSpans(ctx context.Context, spans []StoredSpan) error
     Flush(ctx context.Context) error
 }

 type SpanReader interface {
-    SearchTraces(ctx context.Context, query TraceQuery) (*TraceSearchResult, error)
+    SearchSpans(ctx context.Context, query SpanQuery) ([]StoredSpan, error)
-    GetTrace(ctx context.Context, traceID string) (*Trace, error)
+    GetTrace(ctx context.Context, traceID string) ([]StoredSpan, error)
     ...
 }
```

Provider 不再依赖 `ptrace.Traces` 包，只依赖 `StoredSpan`（Layer 1）。

#### Layer 4 — reader_adapter.go（从 600 行缩减到 ~200 行）

```diff
- type traceReaderAdapter struct { inner *elasticsearch.TraceReader }
+ type traceReaderAdapter struct { inner SpanReader }  // 依赖接口，不依赖 ES

- convertSpan()      // 41 行 — 删除
- convertTrace()     // 含 Normalize + 业务逻辑 — 用 StoredSpanToPublic + buildTraceFromSpans 替代
- NormalizeSpanKind  // 删除
- NormalizeStatusCode// 删除

+ StoredSpanToPublic() // Layer 2 公共函数
+ buildTraceFromSpans() // 纯业务逻辑：root span 检测，不涉及类型转换
```

#### Layer 5 — types.go（仅删除）

```diff
- func NormalizeSpanKind(kind string) SpanKind   // 不再需要
- func NormalizeStatusCode(code string) StatusCode// 不再需要
  // Span / Trace / KeyValue 定义保持不变
```

#### Layer 0 — ES / PG 具体实现

```diff
// trace_writer.go
- func spanToDoc(span ptrace.Span, serviceName string, ...) map[string]any
+ func (w *TraceWriter) WriteSpans(ctx context.Context, spans []StoredSpan) error
  // 直接 json.Marshal(StoredSpan) → ES Bulk API

// trace_reader.go
- type spanDocument struct { OperationName string `json:"operation_name"` ... }
+ // 直接 json.Unmarshal → StoredSpan
```

##### ES Mapping 完整定义

```json
{
  "template": "otel-traces-*",
  "mappings": {
    "properties": {
      "traceId":           { "type": "keyword" },
      "spanId":            { "type": "keyword" },
      "parentSpanId":      { "type": "keyword" },
      "name":              { "type": "keyword" },
      "kind":              { "type": "keyword" },
      "startTimeUnixNano": { "type": "long" },
      "endTimeUnixNano":   { "type": "long" },
      "durationNano":      { "type": "long" },
      "status": {
        "properties": {
          "code":          { "type": "keyword" },
          "message":       { "type": "text" }
        }
      },
      "traceState":        { "type": "keyword" },
      "scope": {
        "properties": {
          "name":          { "type": "keyword" },
          "version":       { "type": "keyword" },
          "attributes":    { "type": "flattened" }
        }
      },
      "attributes":        { "type": "flattened" },
      "resource":          { "dynamic": true },
      "events": {
        "type": "nested",
        "properties": {
          "timeUnixNano":  { "type": "long" },
          "name":          { "type": "keyword" },
          "attributes":    { "type": "flattened" }
        }
      },
      "links": {
        "type": "nested",
        "properties": {
          "traceId":       { "type": "keyword" },
          "spanId":        { "type": "keyword" },
          "traceState":    { "type": "keyword" },
          "attributes":    { "type": "flattened" }
        }
      },
      "serviceName":       { "type": "keyword" },
      "appId":             { "type": "keyword" }
    }
  }
}
```

**字段变更对照**：

| 当前 | 新 | 类型变更 | 说明 |
|------|----|---------|------|
| `operation_name` | `name` | — | 对齐 OTLP |
| `span_kind` | `kind` | — | 对齐 OTLP |
| `status_code` | `status.code` | — | 嵌套 |
| `status_message` | `status.message` | — | 嵌套 |
| `start_time` | `startTimeUnixNano` | date_nanos→**long** | 纳秒整数,高效 range |
| `end_time` | `endTimeUnixNano` | date_nanos→**long** | 同上 |
| `duration_us` | `durationNano` | long→long, **微秒→纳秒** | 写入时计算,ES 直接 sort/filter |
| — | `traceState` | **新增** keyword | 之前丢弃 |
| — | `scope` | **新增** nested | InstrumentationScope |
| — | `links.traceState` | **新增** keyword | link 扩展 |
| — | `links.attributes` | **新增** flattened | link 扩展 |

### 5.3 删除项

| 删除 | 原文件 | 原因 |
|------|--------|------|
| `NormalizeSpanKind()` | `types.go` | 写入时保证标准格式，不再需要读取侧校正 |
| `NormalizeStatusCode()` | `types.go` | 同上 |
| `convertSpan()` | `reader_adapter.go` | 替换为 `StoredSpanToPublic()` |
| `spanDocument` struct | `trace_reader.go` | 替换为 `StoredSpan` |
| `spanToDoc()` | `trace_writer.go` | 替换为 `ConvertOTLPSpan() + json.Marshal` |
| `traceRow` struct | `pg trace_writer.go` | 替换为 `StoredSpan` |
| 各 Provider 的 `toSpan()` / `convertTrace()` | 各自包内 | 替换为 Layer 2 公共函数 |

---

## 六、实施路线

### Sprint 1 — Trace 信号（✅ 已完成 2026-06-30）

- [x] 新建 `storedmodel/stored_span.go`（独立子包，解决循环依赖）
- [x] 新建 `stored_to_public.go`（父包，StoredSpan→public Span，避循环依赖）
- [x] 改 ES `trace_writer.go` → `spanToDoc`(80行)→`convertSpan`(3行) 委托 ConvertOTLPSpan
- [x] 改 ES `trace_reader.go` → `spanDocument`+`toSpan()`→`StoredSpan`+compat(兼容旧字段)
- [x] 改 ES `admin.go` → mapping 字段名全量同步（name, kind, status, scope, traceState, links extended）
- [ ] 删 `reader_adapter.go` 中 `convertSpan()` — **暂缓**，ES reader 仍返回本地 Trace 类型，待 Provider 接口改造后统一
- [x] `NormalizeSpanKind`/`NormalizeStatusCode` — **保留**，pdata 返回短名("Server")而公共 API 需全名("SPAN_KIND_SERVER")
- [x] 编译 + 全项目测试（我们修改的包全部通过，instrumentationmanager 预存 FAIL 无关）
- [ ] 新 index template 生效 — 需重新部署后 ES ILM rollover

#### Sprint 1 实施备注

1. **pdata 短名存储**：Go pdata `Kind().String()` 返回 `"Server"`（非 `"SPAN_KIND_SERVER"`）。存储层直接使用 pdata 原始短名，`NormalizeSpanKind` 在公共 API 出口归一化为 OTLP 全名格式。

2. **ES 查询字段名暂不更新**：旧索引仍用 `operation_name`/`span_kind`/`start_time` 等旧字段名。新 mapping 已生成，等待 ILM rollover 后新数据自动使用新字段名。读取侧 `compatStoredSpan()` 同时兼容新旧格式。

3. **适配器层未大改**：ES reader 仍返回内部 `Trace`/`Span` 类型（通过 `storedSpanToLocalSpan` 转换）。Provider 接口改造（Layer 3）留到后续 Sprint。

4. **文件清单**：
   | 新增 | 修改 |
   |------|------|
   | `storedmodel/stored_span.go` (~220行) | `trace_writer.go` (-77行) |
   | `stored_to_public.go` (~75行) | `trace_reader.go` (+110行) |
   | | `admin.go` (+30行 mapping) |
   | | `trace_writer_test.go` (重写) |

### Sprint 2 — Log 信号

### Sprint 1.5 — Provider 接口统一（✅ 已完成 2026-06-30）

打通 `extension → adapter → provider` 的完整 StoredSpan 链路。核心变更：

- `provider.go`: `TraceWriter`→`SpanWriter`, `TraceReader`→`SpanReader`（返回 `[]StoredSpan`）
- `extension.go`: `WriteTraces` 内部调用 `ConvertOTLPSpan` → 委托新接口
- ES provider: 去掉 `storedSpanToLocalSpan`，直接返回 `[]StoredSpan`
- `reader_adapter.go`: 从 600 行缩减到 ~100 行，用 `StoredSpanToPublic`
- `factory.go`: 接口对齐
- 编译 + 测试

### Sprint 2 — Log 信号（✅ 已完成 2026-06-30）

- [x] 新建 `storedmodel/stored_log.go`（StoredLogRecord 类型 + ConvertOTLPLog）
- [x] `stored_to_public.go` 加 `StoredLogRecordToPublic`
- [x] 改 ES `log_writer.go` — `logRecordToDoc` 委托 ConvertOTLPLog
- [x] 改 ES `log_reader.go` — `logDocument`→`StoredLogRecord` + `compatLogRecord`
- [x] 改 ES `admin.go` — log mapping 字段名同步
- [x] 编译 + 测试通过

### Sprint 3 — Metric 信号（✅ 已完成 2026-06-30）

- [x] 新建 `storedmodel/stored_metric.go`（StoredMetricDataPoint + ConvertOTLPMetric）
- [x] `stored_to_public.go` 加 `StoredMetricDataPointToPublic`
- [x] 改 ES `metric_writer.go` — 130 行 helper → ConvertOTLPMetric + WriteMetricPoints
- [x] 改 ES `admin.go` — metric mapping 字段名同步
- [x] 编译 + 测试通过

### Sprint 4 — PG Provider 对齐（✅ 已完成 2026-06-30）

- [x] PG `trace_writer.go` → `WriteSpans([]StoredSpan)` + `storedSpanToTraceRow`
- [x] PG `log_writer.go` → `WriteLogRecords([]StoredLogRecord)`
- [x] PG `metric_writer.go` → `WriteMetricPoints([]StoredMetricDataPoint)`
- [x] PG `provider.go` → 实现 WriteSpans/WriteLogRecords/WriteMetricPoints

### Sprint 5 — Hybrid Provider 对齐

- [ ] Hybrid writer/reader 路由适配新接口

### Sprint 6 — ES ILM Rollover

- [ ] 新部署 → ES ILM rollover → 新数据使用新字段名

---

## 七、兼容性

### 7.1 向后兼容

ES index template 变更仅影响新创建的索引。已有索引中的字段名（`operation_name`、`span_kind`）不变，读取侧需同时兼容新旧两种格式：

```go
func unmarshalSpan(data []byte) (StoredSpan, error) {
    var ss StoredSpan
    if err := json.Unmarshal(data, &ss); err != nil {
        return ss, err
    }
    // 兼容旧字段
    if ss.Name == "" {
        ss.Name = extractLegacy(data, "operation_name")
    }
    if ss.Kind == "" {
        ss.Kind = extractLegacy(data, "span_kind")
    }
    return ss, nil
}
```

### 7.2 ES ILM Rollover

旧索引由 ILM 自动 rollover 并删除（按配置的 retention），无需手动迁移。

---

## 八、设计决策（已确认）

| 决策 | 结论 | 理由 |
|------|------|------|
| `startTimeUnixNano` 类型 | **`int64` (ES long)** | string keyword 不支持 range 聚合, long 保留高效时间范围查询 |
| `TraceState` | **恢复** | OTLP 标准字段,之前丢弃不合理 |
| `Link.Attributes` | **补全** | OTLP 标准字段,存储成本低(大部分 link 无 attributes) |
| `Link.TraceState` | **补全** | 同上 |
| `scope` (InstrumentationScope) | **保留** | OTLP 二层嵌套(Resource→Scope→Span)的关键信息,用于区分同一 service 不同 instrumentation library |
| `durationNano` | **存储为派生字段** | ES 不支持原生计算字段排序/过滤，每条 span 在查询时跑 script 成本很高。写入时计算并存储为 `long`，8 bytes/span 换即时排序 |
| 时间格式 | **long 纳秒** | 避免 date_nanos 解析开销,ES range query 高效,PG 直接做数值比较 |
