# Loki Metric Query Support Implementation

## 概述

支持 Grafana Logs Volume 面板所需的 LogQL metric 查询语法。完成于 commit `656a43d` (main)。

## 问题（Sprint 1-2 全部解决）

| # | 问题 | 现象 | Sprint | 状态 |
|---|------|------|--------|------|
| 1 | Health check 返回 503 | `vector(1)+vector(1)` 解析失败 | 1 | ✅ `27a2c9b` |
| 2 | Loki 路由 double prefix | `/api/v2/api/v2/...` 404 | 1 | ✅ `7379005` |
| 3 | Health check JSON 解析失败 | 纳秒时间戳超过 JS Number 精度 | 1 | ✅ `fb35218` |
| 4 | `|= ""` 空过滤器返回 0 结果 | ES `match body ""` 匹配不到文档 | 2 | ✅ `656a43d` |
| 5 | `sum by (level) (count_over_time(...))` 解析失败 | LogQL parser 不支持 metric 查询 | 2 | ✅ `656a43d` |
| 6 | `GetLogStats` `date_histogram` 爆桶 | long 字段的纳秒值被当作毫秒 | 2 | ✅ `656a43d` |

## 架构设计

### 核心决策

| 决策 | 设计原则 | 说明 |
|------|---------|------|
| `MetricExpr` 组合 `LogQLQuery` | 组合优于继承 | 复用流选择器 + filter 解析，不复制代码 |
| `ParseMetric()` 独立入口 | OCP | 不修改现有 `Parse()`，零影响 log query 流 |
| `SearchLogMetric()` 独立方法 | ISP + SRP | 不污染 `SearchLogs`，不强制 PG 实现 |
| Handler 只做路由 | DIP | 依赖 `LogReader` 抽象，不依赖 ES 实现 |
| 嵌套 terms + histogram ES 聚合 | KISS | ES 原生能力，无需自定义计算层 |

### 数据流（两条独立路径）

```
HTTP GET/POST /loki/loki/api/v1/query_range?query=...

IsMetricQuery(q)?
  │
  ├─ YES → Metric Path ──────────────────────────
  │   ParseMetric(q) → MetricExpr
  │     → Evaluator.Evaluate(expr.Inner) → LogQuery (filters)
  │     → computeMetricInterval() → nanos
  │     → LogMetricQuery{LogQuery, GroupBy, IntervalNanos, TopN}
  │     → reader.SearchLogMetric() → ES nested terms+histogram
  │     → parseMetricAggResult() → LogMetricResult{Series}
  │     → writeLokiMatrixResponse() → resultType: "matrix"
  │
  └─ NO → Log Path (unchanged) ─────────────────
      Parse(q) → LogQLQuery
        → Evaluator.Evaluate() → LogQuery
        → reader.SearchLogs() → LogSearchResult
        → writeLokiStreamsResponse() → resultType: "streams"
```

### 新增/修改文件

| 文件 | 类型 | 说明 |
|------|------|------|
| `logql/ast.go` | 修改 | + `MetricExpr`（组合 `LogQLQuery`） |
| `logql/parser.go` | 修改 | + `ParseMetric()`, `IsMetricQuery()`, `parseMetric()`, `parseIdentList()`, `parseRangeVector()` |
| `logql/evaluator.go` | 修改 | 空 pattern 跳过 filter（`|= ""` Bugfix） |
| `logql/parser_test.go` | 修改 | + 8 个新测试 |
| `adminext/loki_metric.go` | **新增** | `handleLokiMetricQuery()` + `writeLokiMatrixResponse()` + `computeMetricInterval()` + `isMetricQuery()` |
| `adminext/loki_handler.go` | 修改 | `handleLokiQueryRange` / `handleLokiInstantQuery` 增加 metric 路由 |
| `adminext/router.go` | 修改 | `query` / `query_range` 增加 POST 方法 |
| `observabilitystorageext/types.go` | 修改 | + `LogMetricQuery`, `LogMetricResult`, `LogMetricSeries`, `LogMetricValue` |
| `observabilitystorageext/provider.go` | 修改 | `LogReader` 接口 + `SearchLogMetric()` |
| `observabilitystorageext/reader_adapter.go` | 修改 | ES adapter `SearchLogMetric()` 实现 |
| `observabilitystorageext/pg_reader_adapter.go` | 修改 | PG stub（返回 "not implemented"） |
| `elasticsearch/types_reader.go` | 修改 | + ES 层 `LogMetricQuery` 等类型 |
| `elasticsearch/log_reader.go` | 修改 | + `SearchLogMetric()` + `parseMetricAggResult()` + `parseNestedAgg()` + `parseHistogramLayer()` + `copyMap()` + `calculateNanoInterval()` + `date_histogram`→`histogram` 修复 |

## 真实 ES 验证（2026-07-23，集群 9.134.106.132:9200）

| # | LogQL 查询 | ES 结果 | 状态 |
|---|-----------|---------|------|
| 1 | `count_over_time({}[30m])` | 7 桶 × 66,587 docs | ✅ |
| 2 | `sum by (level) (count_over_time({}[30m]))` | INFO=47,866 / WARN=15,795 / ERROR=2,926 | ✅ |
| 3 | `sum by (level, service_name) (...)` | 5 svc × 3 level = 10 series | ✅ |
| 4 | `\|= "error"` 内容过滤 | 2,221 docs, WARN+ERROR 正确分组 | ✅ |
| 5 | `{service_name="test-java-order-service"}` | 14,776 docs, 正确按 level 分组 | ✅ |
| 6 | `\|= ""` 空过滤器 | **0 docs**（Bug，已在代码中修复） | 🔧 已修复 |
| 7 | `date_histogram` on long field | **too_many_buckets**（Bug，已在代码中修复） | 🔧 已修复 |

### 当前索引规模

| 索引 | 文档数 | 大小 |
|------|--------|------|
| `otel-logs-*-2026.07.23` | 279,691 | 179 MB |
| `otel-logs-*-2026.07.22` | 3,155,060 | 1.9 GB |
| `otel-logs-*-2026.07.21` | 2,152,412 | 1.3 GB |

### 数据 schema

```json
{
  "timeUnixNano": "long",          // ← histogram 聚合用此字段
  "observedTimeUnixNano": "long",
  "severityText": "keyword",       // ← 分组字段
  "severityNumber": "integer",
  "serviceName": "keyword",        // ← 分组字段
  "body": "text",                  // ← 内容搜索
  "traceId": "keyword",
  "spanId": "keyword",
  "appId": "keyword",              // ← 租户隔离
  "attributes": "flattened",
  "resource": "flattened"
}
```

## 已完成的提交

| Commit | Sprint | 内容 |
|--------|--------|------|
| `7379005` | 1 | Loki 路由 double prefix 修复 |
| `27a2c9b` | 1 | Health check `vector(1)+vector(1)` 处理 |
| `fb35218` | 1 | 时间戳格式修复（纳秒→秒.纳秒） |
| `656a43d` | 2 | Metric 查询支持 + 空 filter 修复 + `date_histogram` 修复 |

## 支持的 LogQL 语法（Sprint 1+2 合计）

| 语法 | 示例 | 说明 |
|------|------|------|
| 流选择器 | `{app="foo", env=~"prod\|stag"}` | `=`, `!=`, `=~`, `!~` |
| 行过滤器 | `\|= "error"`, `!= "debug"`, `\|~ "pattern"` | 含/不含/正则 |
| 管道解析 | `\| json`, `\| logfmt`, `\| unpack` | 结构化解析 |
| 管道过滤 | `\| json \| level = "error"` | 解析后按标签过滤 |
| 行格式化 | `\| line_format "{{.level}}"` | 模板输出 |
| 空过滤器 | `\|= ""`, `!= ""` | 匹配所有行 / 不匹配任意行 |
| 范围聚合 | `count_over_time({}[5m])` | 按时间桶计数 |
| 聚合分组 | `sum by (level) (...)` | 按标签分组聚合 |
| 嵌套聚合 | `sum by (level, service_name) (...)` | 多级分组 |
| 速率函数 | `rate({}[1m])`, `increase({}[1m])` | 速率/增量 |

## 不支持（待 Sprint 3+）

| 语法 | 说明 |
|------|------|
| `avg`, `max`, `min`, `topk`, `bottomk` | 聚合函数（parser 已支持，evaluator 未实现） |
| `bytes_rate`, `bytes_over_time` | 字节相关函数 |
| 二元操作符 | `+`, `-`, `*`, `/` between queries |
| `offset` modifier | `count_over_time({}[5m] offset 10m)` |
| `unwrap` + metric | `quantile_over_time(... \| unwrap latency)` |
| `label_replace`, `label_join` | 标签变换函数 |

## 遗留事项

- [ ] **集成验证**：部署到集群后验证 Grafana health check + Logs Volume 面板
- [ ] **PG backend**：`SearchLogMetric` 当前 stub 返回 "not implemented"
- [ ] **appId 多租户**：`LogMetricQuery` 继承 `LogQuery.AppID`，但 metric handler 未显式传递 `appId` 参数
- [ ] **错误信息优化**：metric 查询失败时返回更有意义的 Loki 格式错误
- [ ] **性能测试**：大时间范围 + 多级分组的 ES 聚合性能评估
- [ ] **测试覆盖**：`loki_metric.go` + ES `SearchLogMetric` 的单元测试
