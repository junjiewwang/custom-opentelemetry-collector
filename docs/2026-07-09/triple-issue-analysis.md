# 运维问题三重分析

> 2026-07-09 | 状态：分析阶段，未实施

---

## 问题 1：指标存储 AppID 大小写不一致

### 5Why

#### Why 1：为什么问题被提出？
用户观察到 trace 的索引名中的 appID 是原始大小写（`xUXCbjc...`），担心 metrics 不一样。

#### Why 2：metrics 的 appID 是什么？
`metric_writer.go:49 getAppID(res)` → `model.go:56 sanitizeAppID(id)` → **lowercase**。

```go
// model.go:53-65
func getAppID(res pcommon.Resource) string {
    if val, ok := res.Attributes().Get("app_id"); ok {
        return sanitizeAppID(val.AsString())   // ← lowercase
    }
    if val, ok := res.Attributes().Get("app.id"); ok {
        return sanitizeAppID(val.AsString())   // ← lowercase
    }
    return ""
}
```

#### Why 3：trace 也是一样的路径吗？
**是的。** `trace_writer.go:51 getAppID(res)` → 同一个 `getAppID` 函数 → `sanitizeAppID`。

Trace 和 Metric 写入路径**完全一致**——都经过 `sanitizeAppID` 小写化。

#### Why 4：那为什么用户觉得不一致？
用户可能看的是 ES 索引的实际名称。ES 存储索引时**强制 lowercase**，所以无论写入时传大写还是小写，ES 存下来都是小写。之前的 analysis 确认了这一点：

```
写入: otel-traces-xUXCbjcSnSy5LZUJ-2026.07.09
ES 存: otel-traces-xuxcbjcsnsy5lzuj-2026.07.09  (auto-lowercase)
读取: otel-traces-xUXCbjcSnSy5LZUJ-*              (pattern normalize → match)
```

#### Why 5：结论是什么？
**Trace 和 Metric 的 appID 处理策略完全一致，不存在不一致的问题。**

写入层都经过 `sanitizeAppID`，读取层都用原始 appID 构造 ES pattern。ES 的 case-insensitive 特性保证了匹配正确。

### 修复建议
无需修复。提供文档向用户说明：两组信号共用 `getAppID → sanitizeAppID` 路径。

---

## 问题 2：Prometheus Remote Write "out of order sample"

### 5Why

#### Why 1：为什么 Prometheus 拒绝样本？
```
remote write returned HTTP status 400: out of order sample
```

Prometheus 时序数据库要求同一 label set 的样本**时间戳必须严格递增**。collector 给同一个 series 发了时间戳更早的样本 → 拒绝。

#### Why 2：为什么 collector 会发乱序样本？
两个可能：
- **A：同一个 Agent 多线程并发摘要**。多个 goroutine 同时观测同一个 metric，时间戳有细微差异，batching 时未排序就发给 collector。
- **B：Agent 重启**。重启后重新采集 → 时间戳从头开始 → 早于之前发过的样本。

#### Why 3：为什么不是 collector 的问题？
collector 的 `prometheusremotewrite` exporter 是 OTel 社区组件，只负责转发——不做时间戳排序。乱序检测是 Prometheus server 侧的校验。

#### Why 4：为什么不是偶尔出现而是持续报错？
每 20~30 秒 `dropped_items: 4`，持续了 3 分钟。说明有 4 条固定的过期样本卡在重试队列里，每次重试都被拒绝。

#### Why 5：如何避免？
| 方案 | 可行性 |
|------|--------|
| Agent 侧排序（发前 sort by timestamp） | Agent 改造 |
| collector 侧排序（prometheusremotewrite 前排序） | OTel 社区组件，不能改 |
| Prometheus `out_of_order_time_window` 配置 | Prom server 配置 |
| 接受丢弃 | 当前行为 |

### 修复建议
**这是 agent 侧的时序问题，不是 collector 的 bug。** 排查方向：
- Agent 是否存在多线程同时写同一个 metric counter 的情况
- Agent 重启后是否重发了旧样本

---

## 问题 3：ES Metric Range Query "too_many_buckets"

### 5Why

#### Why 1：为什么 ES 报 too_many_buckets？
```
Trying to create too many buckets. Must be ≤ 65535 but was 65536.
```

ES 的 `terms` aggregation 创建了超过 65535 个 bucket。ES 默认 `search.max_buckets=65535`，被触发上限。

#### Why 2：哪个查询产生了这么多 bucket？
```
metric range query failed → metric_reader.go:178 RangeQuery
```

`RangeQuery` 按 `label set` 做 `terms` aggregation。如果有 65536+ 个不同的 label 组合，就会触发上限。

#### Why 3：为什么有 65536+ 个不同的 label set？
Metric datapoint 的 `labels` 字段包含**所有 resource attributes + datapoint attributes**。如果包含高基数字段（如 `pid`、`host`、`instance_id`、`thread_name`），每个 agent 实例都有独特的 label 组合 → 基数量指数级增长。

```
1 metric name × N agents × M instances × K threads → 65536+ unique label sets
```

#### Why 4：为什么 trace 没这个问题？
Trace 的聚合粒度是 `traceID`。同一 trace 的 span 数量通常 <1000，远低于 65535 上限。

Metric 的聚合粒度是 `label set`，基数远高于 trace。

#### Why 5：怎么修？
| 方案 | 可行性 |
|------|--------|
| 提高 `search.max_buckets` (ES 配置) | 临时，治标 |
| 聚合时做 `size` 分页 | 治本，`"terms":{"field":"...", "size":10000}` |
| 去掉高基数字段 | 治本，但要评估影响 |
| 用 `composite` aggregation 替代 `terms` | 最优，天然分页 |

### 修复建议
P0：提高 `search.max_buckets` 到 100000（运维层面，5 分钟搞定）
P1：metric range query 用 `composite` aggregation 替代 `terms` aggregation（天然支持分页，没有 65535 限制）

---

## 总结

| # | 问题 | 根因 | 严重度 | 修复 |
|---|------|------|--------|------|
| 1 | AppID 大小写 | 实际上一致，误解 | 无 | 文档说明 |
| 2 | out of order sample | Agent 侧乱序时间戳 | 低（丢 4 样本） | Agent 排查 |
| 3 | too_many_buckets | label 基数 >65535 | 高（指标全挂） | ES 配置 + composite agg |
