# 前端 Metrics 查询 "Cannot convert undefined or null to object" 错误分析

## 1. 问题描述

前端查询指标时报错：
```
Cannot convert undefined or null to object
```

后端返回的数据结构：
```json
{
    "data": [
        {
            "labels": null,
            "values": [
                {"timeUnixMilli": "1783660830000", "value": 60},
                {"timeUnixMilli": "1783660845000", "value": 60},
                {"timeUnixMilli": "1783660860000", "value": 60.5}
            ]
        }
    ]
}
```

**关键问题**：`"labels": null`

## 2. 根因分析

### 2.1 崩溃调用链

```
MetricsPage
  → useMetricQuery / useRedPanels (hooks)
    → apiClient.metricQueryRange()
      → 后端返回 { data: [{ labels: null, values: [...] }] }
    → seriesToChartSeries(resp)       // utils/metric.ts 第 17 行
      → formatMetricLabels(series.labels)  // 第 21 行
        → Object.entries(null)         // 第 37 行 💥 TypeError!
```

### 2.2 前端崩溃点

**文件**：`extension/adminext/webui-react/src/utils/metric.ts` 第 37 行

```typescript
export function formatMetricLabels(labels: Record<string, string>): string {
  // labels 为 null 时，Object.entries(null) 抛出 TypeError:
  // "Cannot convert undefined or null to object"
  const filtered = Object.entries(labels).filter(([k]) => k !== '__name__' && k !== 'metric');
  if (filtered.length === 0) return 'value';
  return filtered.map(([k, v]) => `${k}="${v}"`).join(', ');
}
```

### 2.3 TypeScript 类型与实际不一致

**文件**：`extension/adminext/webui-react/src/types/metric.ts`

```typescript
export interface MetricSeries {
  metric?: string;
  labels: Record<string, string>;  // 类型标注为非空，但运行时 API 可能返回 null！
  values: MetricTimeValue[];
}
```

TypeScript 编译期认为 `labels` 不可能为 null，但运行时 JSON 解析后可以是 null。

### 2.4 后端根因

**文件**：`extension/observabilitystorageext/provider/elasticsearch/metric_reader.go` 第 199-202 行

```go
series := MetricSeries{
    Labels: query.Labels,  // ← 当查询没有 label 过滤时，query.Labels 为 nil
    Values: make([]MetricDataPoint, 0, len(agg.Buckets)),
}
```

**Go JSON 序列化行为**：
```go
type MetricSeries struct {
    Labels map[string]string `json:"labels"`  // nil map → JSON "null"
}
```

Go 中 `map[string]string` 的零值是 `nil`，`json.Marshal(nil map)` → `"labels": null`。

**数据流**：
```
query.Labels == nil (用户未指定 label 过滤)
  → MetricSeries.Labels = nil
    → JSON: {"labels": null}
      → 前端: Object.entries(null) → 💥 TypeError
```

## 3. 影响范围

| 场景 | 是否触发 |
|------|:---:|
| 查询指标不带 label 过滤（最常见） | ✅ 触发 |
| 查询指标带 label 过滤（如 `service_name=xxx`） | ❌ 不触发 |
| 聚合查询只有一条时序 | ✅ 触发（labels 仍为 null） |
| 前端 RED panels（自动查询） | ✅ 触发 |

## 4. 修复方案

### 4.1 方案设计原则

**纵深防御**：前后端同时修复，确保任一层都不会因 null 崩溃。

### 4.2 后端修复（根本解决）

**文件**：`metric_reader.go` — `QueryRange` 方法

```go
// Before:
series := MetricSeries{
    Labels: query.Labels,  // 可能为 nil
    ...
}

// After:
labels := query.Labels
if labels == nil {
    labels = make(map[string]string)
}
series := MetricSeries{
    Labels: labels,  // 保证非 nil → JSON "{}" 而非 "null"
    ...
}
```

**效果**：`labels` 序列化为 `{}` 而非 `null`，前端 `Object.entries({})` 返回空数组，不崩溃。

### 4.3 前端修复（防御性编程）

**文件**：`utils/metric.ts` — `formatMetricLabels` 函数

```typescript
// Before:
export function formatMetricLabels(labels: Record<string, string>): string {
  const filtered = Object.entries(labels).filter(...)  // null → crash
  ...
}

// After:
export function formatMetricLabels(labels: Record<string, string> | null | undefined): string {
  if (!labels || typeof labels !== 'object') return 'value';
  const filtered = Object.entries(labels).filter(([k]) => k !== '__name__' && k !== 'metric');
  if (filtered.length === 0) return 'value';
  return filtered.map(([k, v]) => `${k}="${v}"`).join(', ');
}
```

**文件**：`utils/metric.ts` — `seriesToChartSeries` 函数

```typescript
// 调用处增加 null guard：
const safeLabels = series.labels || {};
const name = formatMetricLabels(safeLabels);
```

**文件**：`types/metric.ts` — 类型定义修正

```typescript
export interface MetricSeries {
  metric?: string;
  labels: Record<string, string> | null;  // 反映实际 API 可能返回 null
  values: MetricTimeValue[];
}
```

## 5. 优先级与实施顺序

| 步骤 | 层级 | 改动 | 理由 |
|------|------|------|------|
| 1 | 后端 | `metric_reader.go`: nil → `make(map)` | 根本解决，消除 null 源头 |
| 2 | 前端 | `formatMetricLabels` 加 null guard | 防御性编程，即使后端漏了也不崩 |
| 3 | 前端 | `types/metric.ts` 修正类型 | 类型安全 |

## 6. 其他可能产生 `labels: null` 的代码路径

| 路径 | 文件 | 状态 |
|------|------|------|
| `QueryRange` → `series.Labels` | `metric_reader.go:199` | ⚠️ 需修复 |
| `reader_adapter.go` → `convertMetricRangeResult` | `reader_adapter.go:378` | ⚠️ 透传 nil |
| `Query` (instant) → `hitToDataPoint` | `metric_reader.go:384-398` | doc.Labels 可能为 nil |
| `pg_reader_adapter.go` → PG 适配 | `pg_reader_adapter.go` | 需检查 |

## 7. 变更日志

| 日期 | 版本 | 说明 |
|------|------|------|
| 2026-07-10 | v1.0 | 初始分析 |
