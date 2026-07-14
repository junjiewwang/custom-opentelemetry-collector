# Fix: Grafana Service Structure 视图服务名/操作名显示缺失

## 需求背景

部署 DurationMs 修复后，Tempo Search API 返回的 `durationMs` 已正确（442、4715、80 等），但 Grafana Traces Drilldown 的 **Service Structure** 视图只显示时长（如 `(1.4s)`、`(133.72µs)`），无法显示服务名和操作名。

## 根因分析

通过分析 Grafana `traces-drilldown` 插件源码（`src/utils/trace-merge/tree-node.ts`），发现两处字段映射不匹配：

### 问题 1: service.name 的 key 不匹配

**Grafana 前端代码**：
```typescript
attributes.find(a => a.key === 'service.name')
```

**我们返回的格式**：
```json
{"key": "resource.service.name", "value": {"stringValue": "customcol"}}
```

**原因**：`projectSpanWithSelect` 直接用 `field`（即 select 原始字段名 `resource.service.name`）作为 attribute key，但 Grafana 期望的是去掉 scope 前缀的 `service.name`。

### 问题 2: name 应该是 span 的顶层字段

**Grafana 前端代码**：
```typescript
span.name  // 直接访问顶层字段
```

**我们返回的格式**：
```json
{"spanID":"...", "attributes":[{"key":"name","value":{"stringValue":"/v1/traces"}}]}
```

**原因**：`tempoSearchSpan` 结构体没有 `Name` 顶层字段，`name` 被当作普通 attribute 放入数组。

## 修复方案

### 修复 1: tempoSearchSpan 增加 Name 顶层字段

文件：`extension/adminext/tempo_handler.go`

```go
type tempoSearchSpan struct {
    SpanID            string           `json:"spanID"`
    Name              string           `json:"name,omitempty"`      // ← 新增
    StartTimeUnixNano string           `json:"startTimeUnixNano"`
    DurationNanos     string           `json:"durationNanos"`
    Attributes        []tempoKeyValue  `json:"attributes"`
}
```

### 修复 2: 重构 projectSpanWithSelect 返回结构

将返回值从 `[]tempoKeyValue` 改为 `projectSpanWithSelectResult` 结构体，分离顶层 `Name` 和 `Attributes`：

- `name` 字段不再放入 attributes 数组，而是提取到顶层 `Name`
- `resource.service.name` 等带 scope 前缀的字段，输出 key 时去掉前缀（`resource.service.name` → `service.name`，`span.http.method` → `http.method`）

新增辅助函数 `tempoAttributeKey(field)` 负责 key 转换。

### 修复 3: convert 函数填充 Name

两个构建 span 的函数 `convertTraceSummaryToTempoSearchTrace` 和 `convertStructuralResultToTempoSearchTrace` 更新为使用 `projectSpanWithSelectResult`，将 `Name` 设置到 `tempoSearchSpan.Name` 顶层字段。

## 修复后的输出格式

```json
{
  "spanID": "abc123",
  "name": "/v1/traces",
  "startTimeUnixNano": "1720000000000000000",
  "durationNanos": "442000000",
  "attributes": [
    {"key": "service.name", "value": {"stringValue": "customcol"}},
    {"key": "status", "value": {"stringValue": "STATUS_CODE_OK"}},
    {"key": "nestedSetParent", "value": {"intValue": "-1"}},
    {"key": "nestedSetLeft", "value": {"intValue": "1"}},
    {"key": "nestedSetRight", "value": {"intValue": "6"}}
  ]
}
```

## 测试验证

所有相关测试通过：
- `TestProjectSpanWithSelect_AllFields` — 验证 name 提取为顶层、service.name key 正确
- `TestProjectSpanWithSelect_Attributes` — 验证普通 attribute 不受影响
- `TestProjectSpanWithSelect_MixedFields` — 验证 name 提取 + 其他字段正常
- `TestProjectSpanWithSelect_ScopedFields` — 验证 scope 前缀去除
- `TestProjectSpanWithSelect_EmptyFields` — 验证空 select 时 Name 从 span 取
- `TestProjectSpanWithSelect_NestedSetMixedFields` — 验证 nestedSet + name 混合
- `TestConvertStructuralResult_NestedSetReturnsAllSpans` — 端到端结构化查询
- `TestConvertStructuralResult_SpssLimitsOutput` — spss 限制仍生效

## 实施进展

- [x] tempoSearchSpan 增加 Name 顶层字段
- [x] 重构 projectSpanWithSelect 返回 projectSpanWithSelectResult 结构体
- [x] 新增 tempoAttributeKey() 辅助函数处理 scope 前缀去除
- [x] 更新 convertTraceSummaryToTempoSearchTrace 使用新结构
- [x] 更新 convertStructuralResultToTempoSearchTrace 使用新结构
- [x] 更新全部 10 个相关测试用例
- [x] 编译通过 + 全部测试通过

## 遗留问题

无。
