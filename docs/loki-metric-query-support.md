# Loki Metric Query Support Implementation

## 概述

支持 Grafana Logs Volume 面板所需的 LogQL metric 查询语法。

## 问题

| # | 问题 | 现象 |
|---|------|------|
| 1 | `|= ""` 空过滤器返回 0 结果 | ES `match body ""` 匹配不到任何文档 |
| 2 | `sum by (level) (count_over_time(...))` 解析失败 | LogQL parser 不支持 metric 查询 |
| 3 | `GetLogStats` 的 `date_histogram` 在 long 字段爆桶 | 纳秒值被视为毫秒导致桶数溢出 |

## 设计

### 架构决策

- **组合而非继承**：`MetricExpr` 包含 `LogQLQuery`，复用流选择器和 filter 解析
- **新解析器入口**：`ParseMetric()` 作为独立函数，不修改现有 `Parse()`
- **独立存储路径**：`SearchLogMetric()` 新接口方法，不污染 `SearchLogs`
- **分离关注点**：Handler 只做路由，Evaluator 做转换，Provider 做查询

### 新增/修改文件

| 文件 | 变更类型 | 说明 |
|------|---------|------|
| `logql/ast.go` | 修改 | + `MetricExpr` |
| `logql/parser.go` | 修改 | + `ParseMetric()`, `IsMetricQuery()`, `parseMetric()`, `parseIdentList()`, `parseRangeVector()` |
| `logql/evaluator.go` | 修改 | 空 pattern 跳过 filter |
| `logql/parser_test.go` | 修改 | + 8 个新测试 |
| `adminext/loki_metric.go` | 新增 | metric handler + matrix 响应构建器 |
| `adminext/loki_handler.go` | 修改 | metric 查询路由 |
| `adminext/router.go` | 修改 | POST 支持 |
| `observabilitystorageext/types.go` | 修改 | + `LogMetricQuery`, `LogMetricResult`, `LogMetricSeries`, `LogMetricValue` |
| `observabilitystorageext/provider.go` | 修改 | + `SearchLogMetric()` 接口方法 |
| `observabilitystorageext/reader_adapter.go` | 修改 | + ES adapter impl |
| `observabilitystorageext/pg_reader_adapter.go` | 修改 | + PG stub |
| `elasticsearch/types_reader.go` | 修改 | + ES 层类型 |
| `elasticsearch/log_reader.go` | 修改 | + `SearchLogMetric()` 实现 + `date_histogram`→`histogram` 修复 + `calculateNanoInterval()` |

### 数据流

```
Metric Query:
  isMetricQuery(q?) ──Yes→ ParseMetric(q) → MetricExpr
    → Evaluator.Evaluate(expr.Inner) → LogQuery (filters)
    → computeMetricInterval() → interval nanos
    → LogMetricQuery{LogQuery, GroupBy, Interval}
    → reader.SearchLogMetric() → ES nested agg
    → parseMetricAggResult() → LogMetricResult
    → writeLokiMatrixResponse() → resultType: "matrix"

Log Query (unchanged):
  Parse(q) → LogQLQuery
    → Evaluator.Evaluate() → LogQuery
    → reader.SearchLogs() → LogSearchResult
    → writeLokiStreamsResponse() → resultType: "streams"
```

## 测试结果

- 8 个新增测试全部通过
- 22 个测试包零回归
- 全项目编译通过

## 未完成任务

- [ ] 集成验证：部署后 Grafana health check + Logs Volume 面板
- [ ] PG backend 的 `SearchLogMetric` 实现（当前 stub 返回错误）
- [ ] Grafana Explore 中按 level 分组查看日志
- [ ] 更多 metric 函数支持（`bytes_rate`, `increase` 等）
