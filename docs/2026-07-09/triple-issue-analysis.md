# 运维问题三重分析

> 2026-07-09 | 状态：分析阶段，修订中

---

## 问题 1：指标存储 AppID 大小写不一致

### 证据

```
Trace 生产路径（canonical）:
  WriteTraces → convertOTLPTraces → ConvertOTLPSpan(stored_span.go:93)
  → getAppIDAttr(attrs) → returns RAW value (no sanitizeAppID)
  → StoredSpan.AppID = raw
  → WriteSpans → getIndexName(rawAppID)
  → 索引名: otel-traces-xUXCbjcSnSy5LZUJ-2026.07.09

Deprecated 路径:
  WriteTraces → trace_writer.WriteTraces → getAppID(res)
  → sanitizeAppID(id)
  → 索引名: otel-traces-xuxcbjcsnsy5lzuj-2026.07.09

Metric 生产路径:
  WriteMetrics → metric_writer.WriteMetrics → getAppID(res)
  → sanitizeAppID(id)
  → 索引名: otel-metrics-xuxcbjcsnsy5lzuj-2026.07.09
```

### 5Why

#### Why 1：为什么不一致？
Trace canonical path 走 `getAppIDAttr`（raw），Metric path 走 `getAppID`（sanitized）。两个工具函数不同。

#### Why 2：为什么有两个不同的函数？
`getAppIDAttr` 在 `storedmodel/stored_span.go:197`，`getAppID` 在 `elasticsearch/model.go:53`。一个在共享模型层（被所有 provider 共用），一个在 ES provider 层（只有 ES 用）。

#### Why 3：为什么没有统一？
`getAppIDAttr` 是 `ConvertOTLPSpan` 的内部 helper，在被引用时继承了该包的"不需要 sanitize"假设。`getAppID` 则是 ES provider 的 helper，考虑了 ES index 命名约束。

#### Why 4：为什么现在才发现？
ES 自动 lowercase 索引名。即使写入时传大写，ES 存下来也是小写。所以查询层面没出问题——只是"看起来不一致"。

#### Why 5：需要修吗？
不需要立即修。ES 的 case-insensitive 行为保证了数据完整性。但如果未来切到 PG（PostgreSQL），索引名大小写是区分的——那时会出问题。

### 修复建议（可选）
统一两个写入路径都走 `sanitizeAppID`，或都走 raw + 信任 ES。

---

## 问题 2：Prometheus Remote Write "out of order sample"

### 证据

```
prometheusremotewrite exporter: HTTP 400 out of order sample, dropped 4 items
频率: ~30s 间隔，持续数分钟
```

### 5Why

#### Why 1：为什么 Prometheus 拒绝？
同一 label set 的样本时间戳必须递增。collector 发了更早的时间戳 → 乱序。

#### Why 2：为什么 collector 会发乱序？
OTel exporter 不做时间戳排序。如果上游给的数据点就是乱序的（多 goroutine 并发采集 → 微秒级时间戳差异），exporter 原样转发。

#### Why 3：为什么只有 4 条异常？
4 条样本被 Prometheus 拒绝 → 进入重试队列 → 每 ~30s 重试 → 持续被拒 → 连续报错。不是持续产生新乱序数据，是固定 4 条卡在队列里。

#### Why 4：为什么 collector 不丢弃这些数据？
`prometheusremotewrite` exporter 的 retryqueue 对 "permanent error"（HTTP 400）仍会重试（根据配置 `sending_queue.retry_on_failure`），不会自动丢弃。

#### Why 5：怎么修？
| 方案 | 可行性 |
|------|--------|
| OTel collector 配置 `sending_queue.enabled: false` | 丢弃队列，旧数据消失 |
| Agent 侧采集时排序 | Agent 改代码 |
| Prometheus `out_of_order_time_window` 配置 | Server 侧 |

### 修复建议
重启 collector 清空 retry queue。这是 transient 数据，不是持续性 bug。

---

## 问题 3：ES Metric Range Query "too_many_buckets"

### 证据

```
QueryRange: date_histogram aggregation, 自定义 step 参数
Error: too_many_buckets_exception: 65536 > 65535
```

### 5Why

#### Why 1：为什么触发了 65535 上限？
`QueryRange` 的 `date_histogram` 使用 `fixed_interval`（来自用户的 `step` 参数或 `calculateInterval` 自动计算）。如果用户传了极小的 step（如 `1s`）或时间跨度过大，bucket 数爆炸。

验证：
```
calculateInterval:
  ≤1h: 15s → 最大 240 buckets
  ≤6h: 1m → 最大 360 buckets
  ≤24h: 5m → 最大 288 buckets

自动计算是安全的。
但用户可传自定义 step → 步长 1ms × 60s = 60000 buckets ← 超过 65535
```

#### Why 2：为什么不对用户 step 做上限校验？
`QueryRange` 直接使用 `query.Step`，没做 `min(step, floor_limit)` 约束。

#### Why 3：为什么 ES 报 `fetch phase` 而不是 `aggregation phase`？
date_histogram 在 ES 内部是先 query → 聚合 → 提取 bucket。bucket 数超过上限时，ES 在创建 bucket 数组阶段失败——归类为 fetch phase。

#### Why 4：为什么 date_histogram 不用 composite（分页聚合）？
`calculateInterval` 自动计算的场景下，bucket 数在安全范围内。设计时未考虑用户自定义极端 step。

#### Why 5：怎么修？
| 方案 | 可行性 |
|------|--------|
| P0: ES `search.max_buckets` → 100000 | 运维 5 分钟 |
| P1: 代码加 step floor —— min(step, timeRange/65535) | 代码 3 行 |
| P2: `date_histogram` → `composite` | 重构 |

### 修复建议
P0 即刻修复；P1 代码保护加在 `calculateInterval` 中。

---

## 总结

| # | 问题 | 根因 | 严重度 | 修复 |
|---|------|------|--------|------|
| 1 | AppID 不一致 | `getAppIDAttr` vs `getAppID` 语义不同 | 低 | 暂无需修 |
| 2 | out of order sample | retry queue 卡 4 条过期样本 | 低 | 重启清队列 |
| 3 | too_many_buckets | 用户自定义 step 过小 | 高 | ES 配置 + step cap |
