# Fix: TraceQL Intrinsic Fields Not Mapped to Correct ES Fields

## 需求背景

在 Grafana Tempo 数据源中使用 TraceQL 查询 `{resource.service.name="tapm-api" && name="/api/v1/DescribeApmInfoByAppId"}` 时，返回空结果。

## 根因分析

`parseTempoSearchParams` 函数在解析 TraceQL 查询时：

1. `name="/api/v1/..."` 被解析为 `Tags["name"] = "/api/v1/..."`
2. 代码**没有**将 `Tags["name"]` 提取到 `query.OperationName`（只处理了 `service.name` → `ServiceName`）
3. 在 ES 查询构建时（`buildTraceSearchQuery`），Tags 中的 `name` 被错误地翻译为 `attributes.name` 或 `resource.name` 的 term 查询
4. 而 span name 实际存储在 ES 顶层字段 `name`（对应 `FieldName` 常量），不在 attributes/resource 下
5. 因此查询结果为空

同样的问题也存在于 `kind`、`status` 等 intrinsic 字段。

## 修复方案

### 1. `parseTempoSearchParams` 统一提取 intrinsic 字段

**文件**: `extension/adminext/tempo_handler.go`

在函数末尾（所有解析路径合流后），统一将 intrinsic 字段从 `Tags` map 提取到对应的结构化字段，并从 Tags 中删除：

- `Tags["name"]` → `query.OperationName`，然后 `delete(Tags, "name")`
- `Tags["service.name"]` → `query.ServiceName`，然后 `delete(Tags, "service.name")`
- `Tags["kind"]` → `query.SpanKind`，然后 `delete(Tags, "kind")`
- `Tags["status"]` → `query.Status`，然后 `delete(Tags, "status")`

### 2. `buildTraceSearchQuery` 防御性 intrinsic 映射

**文件**: `extension/observabilitystorageext/provider/elasticsearch/trace_reader.go`

新增 `intrinsicTermClause` 辅助函数，在 AND/OR Tags 遍历中识别 intrinsic 字段名，将其映射到正确的 ES 顶层字段（而非 `attributes.*` / `resource.*`）。这是一个防御性措施，确保即使上游未提取 intrinsic 字段，ES 查询层也能正确处理。

## 实施进展

- [x] 修复 `parseTempoSearchParams` 中 intrinsic 字段提取逻辑
- [x] 添加 `intrinsicTermClause` 防御性映射
- [x] 编写单元测试 `TestParseTempoSearchParams_IntrinsicFields`
- [x] 编译通过
- [x] 全量测试通过

## 变更文件

| 文件 | 变更说明 |
|------|----------|
| `extension/adminext/tempo_handler.go` | 统一提取 intrinsic 字段并从 Tags 中删除 |
| `extension/observabilitystorageext/provider/elasticsearch/trace_reader.go` | 新增 `intrinsicTermClause` 函数，AND/OR Tags 查询中正确映射 intrinsic 字段 |
| `extension/adminext/tempo_handler_test.go` | 新增 `TestParseTempoSearchParams_IntrinsicFields` 测试 |

## 遗留问题

无。
