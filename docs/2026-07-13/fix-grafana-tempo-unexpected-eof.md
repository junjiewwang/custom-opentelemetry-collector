# Fix: Grafana Tempo "unexpected EOF" Protobuf 解码错误

## 问题描述

Grafana 12 通过 Tempo 数据源查询 Trace 时报错：

```
"Failed to convert tempo response to Otlp" error="unexpected EOF"
```

HTTP 500，Trace by ID 查询 (`/api/v2/traces/{traceID}`) 返回了 protobuf 响应，但 Grafana 解码失败。

## 根因分析

### 环境信息

- Minikube + Istio（Collector Pod 有 Istio sidecar，2/2 Running）
- Grafana 12.0.1 部署在 `istio-system` namespace
- 请求链路：Grafana → Istio Envoy Sidecar → Collector Handler
- Collector 自监控 trace 确认：`protobuf_bytes_expected == protobuf_bytes_written`（传输层无截断）

### 真正的根因：Protobuf Schema 不匹配

Grafana 12 的 Tempo 插件 V2 端点 (`/api/v2/traces/{traceID}`) 期望的 protobuf 类型是 **`tempopb.TraceByIDResponse`**：

```protobuf
// Grafana 期望的格式（源自 github.com/grafana/tempo/pkg/tempopb/tempo.proto）
message TraceByIDResponse {
    Trace trace = 1;               // field 1: 嵌套 message
    TraceByIDMetrics metrics = 2;
    PartialStatus status = 3;
    string message = 4;
}

message Trace {
    repeated ResourceSpans resourceSpans = 1;  // field 1
}
```

而我们返回的是 **OTLP `TracesData`**：

```protobuf
// 我们实际返回的格式（opentelemetry.proto.trace.v1）
message TracesData {
    repeated ResourceSpans resource_spans = 1;  // field 1
}
```

Grafana 的反序列化代码 (`pkg/tsdb/tempo/trace.go`)：

```go
// V2 路径
var tr tempopb.TraceByIDResponse
err = proto.Unmarshal(traceBody, &tr)       // ← 按 TraceByIDResponse 解析
frame, err = TraceToFrame(tr.Trace.ResourceSpans)

// V1 路径（fallback when V2 returns 404）
var otTrace tempopb.Trace
err = proto.Unmarshal(traceBody, &otTrace)  // ← 按 Trace 解析
```

**问题**：当 Grafana 将我们的 `TracesData` bytes 按 `TraceByIDResponse` 解析时：
- field 1 在 `TraceByIDResponse` 中是 embedded message `Trace`
- 但我们的 field 1 直接是 `repeated ResourceSpans`
- 虽然 wire type 相同（LEN），但嵌套层级不同，导致 protobuf 解码器在解析嵌套结构时读到超出边界的数据，触发 `unexpected EOF`

### Wire Format 兼容性确认

- `tempopb.Trace` 的 field 1 = `repeated ResourceSpans` (opentelemetry trace v1)
- OTLP `TracesData` 的 field 1 = `repeated ResourceSpans` (相同的 field number 和 type)
- **结论**：`TracesData` bytes == `Trace` bytes（wire format 完全兼容）
- **需要做的**：在 `TracesData` bytes 外面再包一层 `TraceByIDResponse` 的 field 1 envelope

### 之前的错误假设

之前猜测是 "chunked transfer encoding 导致截断" 并添加了 `Content-Length` header。实际上 `Content-Length` 是正确做法（Good Practice），但不是 `unexpected EOF` 的根因。自监控 trace 已确认 bytes 完整写出，问题在于 Grafana 收到的 bytes 不是它期望的 proto schema。

## 修复方案

**文件**: `extension/adminext/tempo_handler.go`

新增 `wrapAsTraceByIDResponse()` 函数，在 `TracesData` bytes 前添加一个 protobuf field 1 (LEN) 的 tag + length 前缀，构成 `TraceByIDResponse` 的 wire format：

```go
// Wire encoding: [tag: field=1, type=LEN] [varint: length] [TracesData bytes]
func wrapAsTraceByIDResponse(tracesDataBytes []byte) []byte {
    const fieldNumber = 1
    buf := make([]byte, 0, ...)
    buf = protowire.AppendTag(buf, fieldNumber, protowire.BytesType)
    buf = protowire.AppendVarint(buf, uint64(len(tracesDataBytes)))
    buf = append(buf, tracesDataBytes...)
    return buf
}
```

**为什么不引入 `github.com/grafana/tempo` 依赖**：
- Tempo 是重量级模块（Parquet、AWS SDK、大量传递依赖），会显著增加编译时间和 binary 大小
- `TraceByIDResponse.trace = field 1` 是 Tempo 的公开 proto API 契约，field number 不会变化（protobuf backward compatibility 保证）
- 使用标准库 `google.golang.org/protobuf/encoding/protowire`（已在依赖中），代码简洁且类型安全

**为什么这个方案是正确的**：
- Protobuf wire format 兼容性是 protobuf 的核心设计保证
- field number 是稳定的 ABI 契约（变化 = breaking change，所有客户端都会破坏）
- 通过单元测试验证了 round-trip 正确性（构造 → 包装 → 手动解析 → 还原 → 数据一致）

### 修改的 Handler

| Handler | 修改内容 |
|---------|---------|
| `handleTempoV2GetTrace` | `convertTraceToProtobuf` 后调用 `wrapAsTraceByIDResponse` 包装 |

## 验证方法

1. 单元测试通过：`go test ./extension/adminext/ -run TestWrapAsTraceByIDResponse -v`
2. 重新部署 Collector
3. 在 Grafana Explore 中通过 Tempo 数据源查询已知 TraceID
4. 确认不再出现 `"unexpected EOF"` 错误
5. 确认 Trace 详情页正常渲染 span 瀑布图

## 状态

- [x] 根因分析完成（proto schema 不匹配，非传输层问题）
- [x] 代码修复实施（wrapAsTraceByIDResponse）
- [x] 单元测试通过
- [ ] 部署验证

## 参考资料

- Grafana Tempo V2 API Proto: https://github.com/grafana/tempo/blob/main/pkg/tempopb/tempo.proto
- Grafana 12 Tempo Plugin: `pkg/tsdb/tempo/trace.go` (getTrace 函数)
- OTLP Trace Proto: https://github.com/open-telemetry/opentelemetry-proto/blob/main/opentelemetry/proto/trace/v1/trace.proto
