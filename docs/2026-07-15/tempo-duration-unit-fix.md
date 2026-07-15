# Tempo Metrics API Duration 单位统一修复

## 需求背景

Grafana traces-drilldown 面板显示 `quantile_over_time(duration)` 时，duration 值显示为 "year" 级别的异常数值。部署验证后确认问题未修复。

## 根因分析

### 数据流链路

```
ES durationNano (纳秒) → extractMetricValue → parseSingleSeries → convertTraceMetricsToTempoResponse → Grafana
```

### 关键事实

1. **ES 存储**: `durationNano` 字段，单位为**纳秒 (ns)**
2. **Tempo 协议约定**: `quantile_over_time(duration)` 返回值单位为**秒 (s)**
3. **Grafana traces-drilldown**: 使用 `setUnit('s')` 渲染 duration，期望值为秒
4. **问题**: 自定义 Collector 直接透传 ES 的纳秒值，被 Grafana 当作秒来显示

### 影响范围

| 路径 | 数据源 | 原始单位 | 期望单位 | 是否有问题 |
|------|--------|----------|----------|-----------|
| 主路径 TraceReader: `quantile_over_time(duration)` | ES `durationNano` | 纳秒 | 秒 | ⚠️ 有问题 |
| Fallback MetricReader: `quantile_over_time` / `histogram_over_time` | `duration_milliseconds` 指标 | 毫秒 | 秒 | ⚠️ 有问题 |
| 主路径: `histogram_over_time` | `value_count` | 计数 | 计数 | ✅ 无问题 |
| 主路径/Fallback: `rate()` | 计数/stepSeconds | span/s | span/s | ✅ 无问题 |

## 修复方案（v2 — 统一 unitconv 包）

### 设计原则

- **DRY（Don't Repeat Yourself）**：所有单位转换规则集中在一个包中
- **单一职责**：`unitconv` 包只负责单位判断和转换，不关心 ES 或 HTTP
- **开闭原则**：新增单位只需增加常量 + switch case，不修改调用方
- **可测试性**：纯函数、零依赖、零 I/O，100% 单元测试覆盖
- **避免循环依赖**：独立子包 `unitconv`，无外部 import

### 架构

```
┌─────────────────────────────────────────────────────────────────┐
│  observabilitystorageext/unitconv/unitconv.go                    │
│                                                                 │
│  DurationSourceUnit (enum): None, Nanoseconds, Milliseconds, s  │
│  ToSeconds(value, sourceUnit) → float64                         │
│  NormalizeSlice(values, sourceUnit)                             │
│  IsDurationFunction(function, field) → bool                     │
│  SourceUnitForTraceReader(function, field) → DurationSourceUnit │
│  SourceUnitForMetricReader(function, field) → DurationSourceUnit│
│                                                                 │
│  纯函数，零依赖，可单元测试                                       │
└─────────────────────────────────────────────────────────────────┘
           ↑                                    ↑
           │                                    │
┌──────────┴────────────────┐    ┌─────────────┴──────────────────┐
│ 主路径: ES TraceReader     │    │ Fallback: tempo_handler.go      │
│ trace_metrics.go           │    │ handleTempoMetricsQueryRange    │
│                            │    │                                 │
│ sourceUnit :=              │    │ sourceUnit :=                   │
│   unitconv.SourceUnitFor   │    │   unitconv.SourceUnitFor        │
│   TraceReader(fn, field)   │    │   MetricReader(fn, "duration")  │
│ val = unitconv.ToSeconds(  │    │ val = unitconv.ToSeconds(       │
│   val, sourceUnit)         │    │   val, sourceUnit)              │
└────────────────────────────┘    └─────────────────────────────────┘
```

### 修改清单

| # | 文件 | 修改内容 |
|---|------|---------|
| 1 | `extension/observabilitystorageext/unitconv/unitconv.go` | **新建**：统一单位转换包，纯函数实现 |
| 2 | `extension/observabilitystorageext/unitconv/unitconv_test.go` | **新建**：全面单元测试（含 benchmark） |
| 3 | `extension/observabilitystorageext/provider/elasticsearch/trace_metrics.go` | 引入 `unitconv`，用 `unitconv.ToSeconds()` 替换内联 `if` 判断 |
| 4 | `extension/adminext/tempo_handler.go` | 引入 `unitconv`，用 `unitconv.SourceUnitForMetricReader()` + `ToSeconds()` 替换内联逻辑 |
| 5 | `extension/observabilitystorageext/provider/elasticsearch/trace_metrics.go` | `fieldForIntrinsic` default 分支增加 warning 日志 |

### 调用示例

**主路径（TraceReader）**:
```go
// 在循环外确定一次 sourceUnit（避免每个点重复判断）
sourceUnit := unitconv.SourceUnitForTraceReader(query.Function, query.Field)

// 在循环内直接调用
val = unitconv.ToSeconds(val, sourceUnit)
// 对于 rate() 等非 duration 函数，sourceUnit = DurationUnitNone → 直接返回原值
```

**Fallback 路径（MetricReader）**:
```go
sourceUnit := unitconv.SourceUnitForMetricReader(parsed.Function, "duration")
if sourceUnit != unitconv.DurationUnitNone {
    for i := range result.Data {
        for j := range result.Data[i].Values {
            result.Data[i].Values[j].Value = unitconv.ToSeconds(result.Data[i].Values[j].Value, sourceUnit)
        }
    }
}
```

### 扩展性

未来如果新增其他 duration 相关字段（如 `selfDuration`、`networkDuration` 等），只需：
1. 在 `unitconv.IsDurationFunction()` 中增加字段判断
2. 无需修改任何调用方代码

## 验证方式

1. `go test ./extension/observabilitystorageext/unitconv/ -v` — 所有单元测试通过
2. `go build ./extension/...` — 编译通过无报错
3. 部署后在 Grafana traces-drilldown 面板查看 `quantile_over_time(duration, 0.5, 0.95, 0.99)` 结果
4. P50 值应在毫秒到秒级别（如 0.05s ~ 2s），而非年级别
5. 检查 Fallback 路径（如果 TraceReader 不可用时）的 duration 展示是否正常

## 状态

- [x] 创建 unitconv 子包（纯函数 + 单元测试）
- [x] 主路径重构使用 unitconv
- [x] Fallback 路径重构使用 unitconv
- [x] fieldForIntrinsic 防御性日志
- [x] 编译 + 测试通过
- [ ] 部署验证
