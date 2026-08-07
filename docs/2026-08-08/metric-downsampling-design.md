# 指标降采样（Downsampling）设计文档

## 1. 需求背景

本项目用 Elasticsearch 存储 OTel 指标，通过 Prometheus 兼容 API 服务 Grafana。
当前每个 OTLP 数据点 = 一个 ES 文档（毫秒粒度），**无任何降采样/预聚合**。
查询用 ES `date_histogram` 聚合，但有 `DefaultMaxBuckets=10000` 硬限制：
当 `用户 step × 时间范围 > 10000` 时，step 被向上 clamp，返回点数远少于
预期，用户感知为"长时间范围查询少数据"。

**目标**：分层存储 + 查询时按时间范围路由选层，让任意时间范围（几小时到
1 年）的查询都在 ES `max_buckets` 安全区内返回足量数据点，counter 类仍可
正确计算 rate。

## 2. 根因分析（代码证据）

| 限制 | 位置 | 机制 | 影响 |
|------|------|------|------|
| **date_histogram bucket 上限** | `provider/elasticsearch/query/bucket_limit.go:16` `DefaultMaxBuckets=10000` | `SafeInterval` 当 `step×range>10000` 时 clamp step | 7d@10s=60480 buckets → 只返回 ~9914 点 |
| series 数上限 | `metric_reader.go:512` composite `size=seriesLimit`(默认 100) | groupBy 查询 series > 100 时截断 | 部分序列丢失 |
| QueryFlat doc 上限 | `metric_reader.go:1001` `maxDocs=10000` | rate/increase/histogram_quantile 拉原始 doc 被截断 | 静默数据丢失 |

**根因**：存储是原始粒度 + 查询时 bucket 上限。参考 Mimir（TSDB block 多级
降采样）与 VictoriaMetrics（持续降采样 + `reduce_resolution`），但本项目是
ES 不是 TSDB——Mimir 的 block compaction / query-frontend split / 流式降采样
在 ES 上是 anti-pattern。

## 3. 方案对比

| 方案 | 描述 | 优点 | 缺点 | 适用性 |
|------|------|------|------|--------|
| **A. ES 适配：rollup index + 路由选层** | 后台聚合写新 rollup index，查询按 span 路由 | 复用 ES `date_histogram` 天然能力、与现有 index 分片模式一致、不动写入主链路 | 需后台 job + 分布式协调 | ✅ 本项目 |
| B. 纯查询时放大 step | 只调大 maxBuckets / 自动放大 step | 零存储改动、1-2 天 | 治标不治本：长范围仍受限、丢失细节 | 仅 Phase 1 缓解 |
| C. Mimir block 模式 | TSDB block compaction 原地降采样 | 高效原地合并 | ES 无 block 概念，不能原地合并已有 doc | ❌ ES 不适用 |
| D. query-frontend split | 按时间边界拆分查询分派多 block | 精确选分辨率 | ES `date_histogram` 跨 index 天然工作，无需 split | ❌ ES 无必要 |
| E. 流式持续降采样 | VM `-downsampling.period` 流式 | 实时性好 | ES 是批量引擎，流式增加写入放大 | ❌ 不贴合 ES 批量特性 |

**最终选择：方案 A**。ES 方案 = 复用 `date_histogram` + 写新 rollup index +
查询时按 span 路由选层。弃 Mimir 的 block compaction / split / 流式（ES
anti-pattern）。

## 4. 三层存储设计

| Tier | 分辨率 | Index 前缀 | 来源 | 保留 |
|------|--------|-----------|------|------|
| Tier 0 原始 | 采集粒度 | `otel-metrics` | 实时写入（现有） | 7d（现有 ILM） |
| Tier 1 rollup | 5min | `otel-metrics-rollup-5m` | 后台 job 聚合 Tier 0 | 30d |
| Tier 2 rollup | 1h | `otel-metrics-rollup-1h` | 后台 job 聚合 Tier 1 | 1y |

每层仍按 `{prefix}-{appID}-{date}` 分索引（复用 `metric_writer.go:111`
`getIndexName`，只换 prefix），保留 app 隔离与按天 rollover。

### 查询路由（按 span = end−start，不按 step）

| span | 路由目标 | bucket 数 |
|------|----------|-----------|
| ≤ 2h | Tier 0（看细节） | 看原始粒度 |
| 2h ~ 7d | Tier 1（5min） | 7d=2016 ≪ 65535 |
| > 7d | Tier 2（1h） | 1y=8760 < 65535 |

**跨边界**：不做 query split（ES `date_histogram` 跨 index 天然工作）。
靠 job 把每层推进到 `now−lag` 内保证连续。span≤2h 但 Tier 0 已被 ILM 删除
（>7d 的近窗）→ 回退 Tier 1。

## 5. metric 类型聚合规则 + rollup 文档 schema

复用 `StoredMetricDataPoint`（`storedmodel/stored_metric.go:13`），新增可选
`Rollup` 子对象（不破坏现有读写）：

```go
type RollupStats struct {
    First, Last, Min, Max, Sum float64
    Count int64
}
// 加到 StoredMetricDataPoint: Rollup *RollupStats `json:"rollup,omitempty"`
```

| 类型 | bucket 聚合 | rate 还原 |
|------|------------|-----------|
| gauge | `value=avg` + rollup{min,max,sum,count}（ES `avg/max/min/sum/value_count`） | 直接用 value |
| **counter** | `value=last` + rollup{first,last,count}（ES `top_hits` asc/desc size 1） | 查询层每 bucket 展开成 `[first@start, last@end]` 两样本喂 `computeRate`（`prometheus_handler.go:1568`），跨 bucket 用相邻 last→last 连接，reset 用 `last < 上bucket.last` 检测——与现有 `counterIncrease:1665` 一致 |
| histogram | `value=sum` + rollup{count} + **合并 bucket_counts**（数组逐位加） | `AggregateHistogramSamples:1308` 逻辑不变 |
| summary | `value=sum` + rollup{count}（现状只存 sum） | 同 counter |

**histogram bucket_counts 合并**：ES 单聚合做不了数组逐位加。用
`composite by labels` + `date_histogram(5m)` + 每桶 `top_hits(_source:
bucket_counts)` 拉回内存累加；兜底用 `SearchAfter`（`client_search.go:22`）
游标拉全部 doc，复用 `groupMetricSamplesByLabels`（`prometheus_handler.go:1504`）。

## 6. 后台降采样 job + 分布式协调

### 6.1 复用现有 lifecycle 基建（不新写）

collector 以 N 副本运行，rollup 必须不重复、不遗漏、不写坏。**复用
`observabilitystorageext/lifecycle` 包**（已为 index purge 解决此问题）：

- **Leader 选举**：`lifecycle/leader_elector.go` `RedisLeaderElector`
  （`SetNX(leaderKey, nodeID, 30s TTL)` + CAS Lua 释放脚本 + `activeEpoch`
  2h TTL）。`LocalLeaderElector` 单节点。rollup engine 内嵌 elector；只让
  leader 规划，所有 replica 可执行工作项。
- **Leader/Worker 分工**：对标 `lifecycle/interfaces.go:153/167` 的
  `IndexLister`（Leader 列任务）+ `SingleIndexPurger`（Worker 执行）。rollup
  对应有 `RollupTaskLister`（Leader 列待处理的 `(appID, date)`）+
  `RollupTaskExecutor`（Worker 聚合一个切片）。
- **统一任务引擎**：`lifecycle/scheduler.go` `distributedPurgeViaEngine` 用
  `taskengine.Engine`（leader 规划 + worker 一次执行一个）。rollup 接入同一
  engine，不另起 goroutine+ticker。

### 6.2 分布式四个必须点

1. **共享 watermark（Redis，非每实例）**：
   `HSET otel:rollup:watermark:{tier} {appID} {lastBucketMs}`（复用
   `appmanager/repository_redis.go:51` 的 `HSetNX`）。否则重启后不知道上次
   跑到哪，要么重复跑要么漏跑。
2. **任务认领锁（比 leader 锁更细）**：
   `SETNX otel:rollup:claim:{tier}:{appID}:{date} {nodeID} {2×tickTTL}`。
   多 worker 并行不同 appID；只有认领成功者聚合该切片。
3. **幂等是兜底，不是锁**：rollup doc `_id` 确定性
   （`{tier}:{metric}:{labelsHash}:{bucketMs}`）+ `op_type:index`。即使
   leader-lock 失效 / claim-lock 过期时 owner 仍在跑，双写同 doc 同值收敛
   （histogram bucket_counts 的 read-modify-write 同窗口确定性收敛）。
4. **bucket 边界确定性**：`bucketStart = floor(ts / 5m) * 5m`，绝不用
   `time.Now()` 推导 bucket，避免时钟漂移导致各 replica 算出不同边界。

**核心原则**：幂等保证最终一致，锁只是减少重复工作的优化（非正确性依赖）。
leader-lock 短暂失效不会导致数据错误。

### 6.3 触发与窗口

- 5m 层每 5min tick，聚合 `[watermark, now−10min]` 的 Tier 0 → Tier 1
- 1h 层每 1h tick，Tier 1 → Tier 2
- 10min 滞后保证 late data 已落盘（`refresh_interval:10s` + buffer flush）
- 只聚合"已关闭 bucket"（bucket end < now−lag），避免聚合还在增长的 bucket

## 7. rollup index schema

- 命名：`otel-metrics-rollup-5m-{appID}-{date}` / `otel-metrics-rollup-1h-{appID}-{date}`
- `admin.go` 新增 `createRollupTemplate(ctx, tier)`，复用 `metricTemplateMappings:324`
  结构，差异：`index_patterns`、ILM policy（5m=30d / 1h=1y）、`refresh_interval`
  （5m 层 30s、1h 层 5m，写入频率低放宽省资源）、shards=1、新增 `rollup` object 子字段映射
- `timeUnixMilli` 仍是 `date epoch_millis` → `date_histogram` 路径完全复用
- `InitSchema:32` 追加两层；`Admin.Purge/PurgeByApp:108/145` 扩展兼容 rollup
  signal（三层独立 ILM/retention，生命周期解耦）

## 8. 分阶段实施路线

### Phase 1 — 查询时缓解（✅ 已完成，commit `e9ca6af`，2026-08-08）

不引入 rollup，只缓解"少数据"感知：

1. **放宽 bucket 上限**（`query/bucket_limit.go`）：按聚合形状分桶上限——
   非 groupY（单 date_histogram）用 `DefaultMaxBucketsFlat=65535`（ES 每分片
   硬限），7d@10s 不再 clamp；groupY（date_histogram 嵌在 composite 下）用
   `ESHardMaxBuckets/seriesLimit`，保证 `series×time ≤ 65535`，把
   `too_many_buckets` 报错变成 clamp。
2. **`calculateInterval` 加 `maxBuckets` 参数**（`metric_reader.go`）；
   `QueryRange` 按 GroupBy 计算 maxBuckets。
3. **QueryFlat maxDocs 自适应**（`adaptiveFlatMaxDocs`：floor 10000 / ceiling
   50000），rate/increase/histogram_quantile 长范围不再被静默截断。

**Grafana 代理实测验证**（数据源 id=6 → collector）：

| 场景 | 修复前 | 修复后 |
|------|--------|--------|
| 非 groupY `sum(m)` 7d@10s | ~9914 点（clamp） | 36304 点 ✅ |
| groupY `sum by(svc)(m)` 7d@30s | too_many_buckets 报错 | success ✅ |

**暂缓至 Phase 2**：clamp/truncation warning 透传到 `promResponse.Warnings`
（`prometheus_handler.go:65` 字段已存在但未用）。`dispatchRangeQuery:816` 是
4+ 分支复杂函数，透传需改签名 + `writePromSuccess` + 所有调用点，跨层成本高；
Phase 2 rollup 也要改 `QueryRange` 返回结构，warning 透传顺带做，避免重复
跨层改动。

### Phase 2 — 5min rollup + 路由（~1-2 周）

- `StoredMetricDataPoint.Rollup` 字段 + `stored_metric.go`/`fields.go`/`admin.go` rollup template
- `rollup_engine.go`/`rollup_aggregator.go`（复用 `lifecycle` leader+worker+taskengine，见 §6）
- 共享 watermark + 认领锁（Redis）
- `bulkBuffer.AddWithID`（确定性 `_id` + `op_type:index` 幂等）
- `routeIndexPattern`（≤2h→Tier0，2h~7d→Tier1）
- counter rate 还原（`execRateRange:958` rollup 样本展开）
- gauge/counter/histogram 三类 rollup

**验证**：写 Tier0 → 5min 后 Tier1 出现 rollup doc；重跑同 bucket → `_id` 不变
值更新（幂等）；Grafana 7d 走 Tier1 返回 ~2016 buckets；counter rate over 7d
与 Tier0 2h 趋势一致；多实例同 appID+date 只一个写（锁竞争日志）。

**风险**：histogram bucket_counts 合并性能（需压测）；first/last top_hits 高
基数慢（限 composite size + 分 appID 并行）；Tier1 近 10min 缺失段回退 Tier0。

### Phase 3 — 1h rollup + 自动路由（~1-2 周）

- 1h 层 engine（1h tick，Tier1→Tier2）；`routeIndexPattern` >7d → Tier2
- summary rollup；`Admin.Purge` 兼容三层；retention 配置
- 运维 watermark 指标暴露到 admin/metrics

**验证**：30d/1y 返回 720/8760 buckets；三层 ILM 各自按 retention 删互不影响；
停 Tier1 job 1h → 重启从 watermark 续跑无重复 doc。

## 9. 关键文件

**Phase 1（已完成）**：
- `extension/observabilitystorageext/provider/elasticsearch/query/bucket_limit.go` — 桶上限按形状分
- `extension/observabilitystorageext/provider/elasticsearch/metric_reader.go` — `calculateInterval(maxBuckets)` + `adaptiveFlatMaxDocs`

**Phase 2/3 待改**：
- `extension/observabilitystorageext/provider/elasticsearch/metric_reader.go` — `routeIndexPattern` 新增 + `QueryRange/Query/QueryFlat/QueryRaw` 路由
- `extension/observabilitystorageext/provider/elasticsearch/admin.go` — `createRollupTemplate` + `InitSchema` 两层 + `Purge` 兼容
- `extension/observabilitystorageext/provider/elasticsearch/bulk_buffer.go` — `AddWithID`
- `extension/observabilitystorageext/storedmodel/stored_metric.go` — `Rollup *RollupStats` + `LabelsHash`
- `extension/observabilitystorageext/provider/elasticsearch/fields.go` — `FieldRollup`
- `extension/adminext/prometheus_handler.go` — `execRateRange:958` rollup 样本展开 + `promResponse.Warnings:65` 透传
- `extension/observabilitystorageext/provider/elasticsearch/provider.go` — `Start`/`Shutdown` 接 rollup engine
- `extension/observabilitystorageext/provider/elasticsearch/config.go` + `config/template/config.yaml` — `RollupConfig`

**Phase 2/3 新增**：
- `extension/observabilitystorageext/provider/elasticsearch/rollup_engine.go`（复用 `lifecycle` leader+taskengine）
- `extension/observabilitystorageext/provider/elasticsearch/rollup_aggregator.go`（ES composite+date_histogram 组装 rollup doc）
- `extension/observabilitystorageext/provider/elasticsearch/rollup_lock.go`（Redis 认领锁 + watermark）

**复用（不改）**：
`lifecycle/leader_elector.go`、`lifecycle/scheduler.go`、`lifecycle/interfaces.go`、
`MetricWriter.WriteMetricPoints:87`、`Client.Search/MultiSearch/BulkIndex/PutIndexTemplate`、
`aggregation.go` first/last AggregationFunc、`prometheus_handler.go` `counterIncrease:1665`/`computeRate:1568`/`groupMetricSamplesByLabels:1504`

## 10. 参考

- **Mimir**：TSDB block 多级降采样（原始→5m→1h）+ query-frontend 按时间边界选 block + query split
- **VictoriaMetrics**：`-downsampling.period` 持续后台降采样 + `reduce_resolution` 查询时去重 + `maxPointsPerTimeseries` 默认 30000

本项目 ES 方案取其"分层 + 路由选层"核心，弃其"block compaction / query split / 流式"（ES anti-pattern）。ES 靠 `date_histogram` 天然能力 + 写新 rollup index + 确定性 `_id` 幂等去重（写时幂等优于查询时去重）。
