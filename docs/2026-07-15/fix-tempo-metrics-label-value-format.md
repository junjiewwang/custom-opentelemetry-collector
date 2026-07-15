# 修复 Tempo Metrics Label Value 序列化格式

## 问题描述

Grafana 在查询 TraceQL metrics（如 `quantile_over_time(duration, 0.9) by(resource.service.name)`）时报错：

```
json: cannot unmarshal string into Go value of type map[string]json.RawMessage
```

错误发生在 Grafana Tempo 数据源插件（`pkg/tsdb/tempo/tempo.go:35`）解析后端返回的 metrics 响应时。

## 根因分析

Grafana Tempo 插件期望 metrics series 中的 `labels[].value` 是一个 **typed value object**：

```json
{"key": "resource.service.name", "value": {"stringValue": "frontend"}}
```

但我们的实现返回的是一个 **plain string**：

```json
{"key": "resource.service.name", "value": "frontend"}
```

这导致 Grafana 在反序列化 `value` 字段时，尝试将一个 JSON string unmarshal 为 `map[string]json.RawMessage`，从而报错。

## 修复方案

### 修改文件

`extension/adminext/tempo_handler.go`

### 变更内容

1. **修改 `tempoMetricLabel` 结构体**：将 `Value` 字段类型从 `string` 改为 `tempoAnyValue`（复用已有的 typed value 结构）

2. **新增 `stringToTempoAnyValue` 辅助函数**：将 string 包装为 Tempo 兼容的 typed value 格式

3. **修改 `convertTraceMetricsToTempoResponse` 函数**：使用 `stringToTempoAnyValue` 构建 label value

4. **修改 `convertMetricRangeToTempoMetrics` 函数**：同上

### 修复前后对比

修复前输出：
```json
{
  "series": [{
    "labels": [{"key": "resource.service.name", "value": "my-service"}],
    "samples": [...]
  }]
}
```

修复后输出：
```json
{
  "series": [{
    "labels": [{"key": "resource.service.name", "value": {"stringValue": "my-service"}}],
    "samples": [...]
  }]
}
```

## 影响范围

所有带 `by()` 分组的 TraceQL metrics 查询，包括：
- `quantile_over_time(duration, P) by(key)`
- `rate() by(key)`
- 其他 metrics 聚合函数

## 验证方式

在 Grafana Explore Traces 中执行带 `by()` 分组的 metrics 查询，确认返回数据正常渲染。

## 状态

- [x] 实施完成
- [ ] 部署验证
