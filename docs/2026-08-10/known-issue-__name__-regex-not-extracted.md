# 已知缺陷：`{__name__=~"x"}` regex 未提取为 metric name 过滤

## 状态
**待修复**（优先级：中。用户真实场景不受影响，边界 case。修复涉及解析+filter 两层，单独评估。）

## 现象
`{__name__=~"jvm.class.loaded"}` 这类用 `__name__` + regex 选择指标的 PromQL，返回空结果（0 series 或 series label 为空 `{}`）。

对照：裸指标名 `{"jvm.class.loaded"}` 正常工作（series + label 正确）。

## 根因
`__name__` 是 Prometheus 虚拟 label，对应 metric name。ES 存储里 metric name 在**顶层 `name` 字段**（keyword），不在 `labels` 对象。

`extension/adminext/promql_parse.go:125-130` 的提取逻辑只处理 exact 形式：
```go
if name == "" {
    if v, ok := labels[PromLabelName]; ok && v != "" {  // __name__="x" (exact)
        name = v
    }
}
delete(labels, PromLabelName)
```
- `__name__="x"`（exact）→ 提取到 `expr.MetricName` → `buildMetricFilter` 用 `term name=x` ✅
- `__name__=~"x"`（regex）→ **不提取**，留在 `expr.LabelMatch` → `MetricLabelResolver.Resolve("__name__")` 返回 `labels.__name__.keyword` → 该字段在 ES 不存在 → regex 匹配 0 → 空 ❌

`resolver.Resolve("__name__")` 的实测输出（确认字段错误）：
```
"__name__" -> ESField="labels.__name__.keyword" IsPromoted=false
```

## 影响范围
- **用户真实查询不受影响**：Grafana Explore Metrics 用裸指标名 `{"metric"}` + `__ignore_usage__` 内部 label，不用 `__name__=~`。
- `__name__=~` 出现场景：用户手写 PromQL regex 选多指标（如 `{__name__=~"jvm_.*"}`）、某些 Grafana 插件 metric 多选。

## 修复方向
两层改动（比 service_name resolver 重构大，单独评估）：

1. **解析层** `promql_parse.go:125-130`：扩展提取逻辑，`labelMatch["__name__"]`（regex）也提取，存成新字段 `expr.MetricNameRegex`（注意 `__name__` 可能同时有 exact 和 regex，但 PromQL 里 `__name__` 只会出现一次）。

2. **filter 层** `metric_reader.go` `buildMetricFilter`：目前 `metricName` 只支持 exact（`qb.Term(FieldName, metricName)`）。新增 metricName regex 路径：用 ES `regexp` query on `name` 字段（复用 `TranslatePromQLRegex` + `BuildESClauseFromRegex`，field 是 `FieldName` 而非 `labels.xxx.keyword`）。

3. **resolver**：`__name__` 是否要进 `promotedFields`（映射到 `FieldName`）需评估——但 `__name__` 是 metric name 不是普通 label，语义不同，可能更适合在 filter 层单独处理而非走 resolver。

## 复现
```bash
# Grafana Prometheus proxy (datasource id=6, collector)
P=http://grafana.istio-system.svc.cluster.local:3000/api/datasources/proxy/6/api/v1
NOW=$(date +%s); S=$((NOW-10800)); E=$NOW
# A. 裸名 — 正常 (6 series, label={service_name:...})
curl -s -G "$P/query_range" --data-urlencode 'query=sum by(service_name)(rate({"jvm.class.loaded"}[5m]))' \
  --data-urlencode "start=$S" --data-urlencode "end=$E" --data-urlencode "step=60"
# B. __name__=~ — 空 (0 series)
curl -s -G "$P/query_range" --data-urlencode 'query=sum by(service_name)(rate({__name__=~"jvm.class.loaded"}[5m]))' \
  --data-urlencode "start=$S" --data-urlencode "end=$E" --data-urlencode "step=60"
```

## 相关
- `extension/adminext/promql_parse.go:120-130`（exact 提取逻辑 + 注释说明 ES 无 `__name__` label）
- `extension/observabilitystorageext/provider/elasticsearch/metric_reader.go` `buildMetricFilter`（metricName 只支持 term）
- `extension/observabilitystorageext/provider/elasticsearch/metric_label_resolver.go`（`Resolve("__name__")` 返回错误的 `labels.__name__.keyword`）
- 对照参考 `planSeriesMatch`（`prometheus_handler.go`，`/series` 的 match[] 已正确路由 `__name__`）
