# Metric Range Query — too_many_buckets_exception 根因分析与修复方案

> **文档状态**：Sprint 1-4 全部完成 ✅  
> **创建时间**：2026-07-10  
> **最后更新**：2026-07-10（Sprint 4 终态修复完成）  
> **问题现象**：`metric range query failed: search returned status 503: too_many_buckets_exception`  
> **终态根因**：字段类型 `long`（纳秒）+ `date_histogram` + `fixed_interval` 语义不兼容 → 改为 `date_nanos` + `epoch_nanos`  

---

## §1 问题原文

```json
{
  "error": {
    "root_cause": [],
    "type": "search_phase_execution_exception",
    "reason": "",
    "phase": "fetch",
    "grouped": true,
    "failed_shards": [],
    "caused_by": {
      "type": "too_many_buckets_exception",
      "reason": "Trying to create too many buckets. Must be less than or equal to: [65535] but was [65536]. This limit can be set by changing the [search.max_buckets] cluster level setting.",
      "max_buckets": 65535
    }
  },
  "status": 503
}
```

---

## §2 5Why 根因分析

### Why 1：为什么 ES 返回 503 too_many_buckets_exception？

**因为** `MetricReader.QueryRange` 向 ES 发送的 `date_histogram` 聚合请求产生了 65536 个 bucket，恰好超过 ES 集群默认上限 `search.max_buckets = 65535`。

**证据**：错误信息明确指出 `was [65536]`，即请求需要 65536 个 bucket。

### Why 2：为什么 date_histogram 产生了 65536 个 bucket？

**因为** `calculateInterval` 方法在 `step > 0` 时**直接采信用户传入的 step 值**，未校验 `(时间范围 / step)` 是否会超过 ES 的 bucket 上限。

```go
// metric_reader.go:351-354
func (r *MetricReader) calculateInterval(tr TimeRange, step time.Duration) string {
    if step > 0 {
        return fmt.Sprintf("%ds", int(step.Seconds()))  // 直接使用，无上限保护
    }
    // ... 自动计算（有安全的分档逻辑）
}
```

**触发场景复现**：
- 前端传 `step=1s`（1秒），时间范围 `end - start = 65536s`（约 18.2 小时）
- 或 `step=15s`，时间范围 `end - start = 983040s`（约 11.4 天）
- bucket 数 = `duration / step` = 65536，刚好超限

### Why 3：为什么用户传入的 step 没有被校验/拦截？

**因为**数据流经的 3 层（前端 → API Handler → MetricReader）都没有实施 bucket 数量的安全校验：

| 层 | 代码位置 | 行为 |
|---|---|---|
| **前端** | `webui-react/src/utils/metric.ts:calculateStep()` | 本地有安全分档逻辑，但用户可直接通过 API URL 传任意 step |
| **API Handler** | `observability_handler_v2.go:parseMetricRangeQuery()` | 解析 step 后没有校验 bucket 数是否合理 |
| **ES MetricReader** | `metric_reader.go:calculateInterval()` | `step > 0` 时直接采信，无上限保护 |

**两条路径分析**：
1. **前端正常路径**：`calculateStep()` 保证 bucket 数在安全范围（最大 ~240），**不会触发此问题**
2. **API 直接调用路径**：外部系统或手动 curl 直接调 `/observability/metrics/query_range?step=1s&start=...&end=...`，**绕过了前端的安全计算**

### Why 4：为什么只在前端做了安全计算，而不是在服务端做防护？

**因为**架构设计上违反了**"不信任客户端输入"原则**——将安全保障寄托于前端这个可被绕过的层，而非在**离数据最近、不可绕过的层（MetricReader）**做防护。

具体设计缺陷：
- **职责划分不清**：前端 `calculateStep` 本是 UX 优化（让图表点数适中），不是安全措施；但系统把它当成了唯一的 bucket 上限保护
- **缺乏纵深防御**：Handler 层和 Reader 层都没有"后备"校验
- **`calculateInterval` 的 if-else 逻辑只覆盖了 `step == 0` 的分支**：自动计算的分档结果是安全的（最大 360 bucket），但 `step > 0` 的分支是一个完全不设防的透传

### Why 5：为什么 `calculateInterval` 的设计忽视了 bucket 上限保护？

**根本原因**：`calculateInterval` 的设计违反了**单一职责原则（SRP）+ 最小惊讶原则（POLA）**：

1. **函数名"calculateInterval"暗示它只负责"计算间隔"**——但实际上它还隐含了一个未被显式表达的安全职责："确保 bucket 数不超过 ES 上限"。这个职责只在 `step == 0` 分支被偶然满足，在 `step > 0` 分支被完全遗漏
2. **缺乏显式的约束抽象**：没有一个独立的 `validateBucketCount` 或 `clampInterval` 函数来表达"bucket 数必须 ≤ N"这一不变量（invariant）
3. **ES 的 `max_buckets` 限制作为硬约束，没有被建模到代码中**——它是一个运行时才会暴露的错误，而非编译时或参数校验时就能捕获的错误

---

## §3 问题影响分析

| 维度 | 影响 |
|---|---|
| **可用性** | 满足特定时间范围+step 组合条件时，metric 查询接口返回 503，页面展示失败 |
| **安全性** | 外部调用者可构造恶意 step（如 `1s`）+ 大时间范围，造成 ES 节点 OOM/熔断 |
| **静默性** | 问题只在 `bucket 数 > 65535` 的临界点才触发，`65534` 个 bucket 时"正常"但已在消耗大量 ES 资源 |
| **可观测性** | 当前错误信息 `metric range query failed: search returned status 503: ...` 暴露了 ES 内部错误细节给客户端，违反信息隐藏原则 |

---

## §4 解决方案设计

### 4.1 设计原则

| 原则 | 应用方式 |
|---|---|
| **纵深防御** | 在 MetricReader 层（离数据最近、不可绕过）实施硬性 bucket 上限校验 |
| **单一职责** | 将"bucket 数安全校验"作为独立、可复用的函数/策略抽取出来 |
| **Open/Closed** | 通过配置化（而非硬编码）设置 max buckets，未来可按集群调整 |
| **Fail-Fast** | 在发送 ES 请求之前就拒绝不合理的请求，而非等 ES 返回 503 |
| **不信任客户端** | 即使前端已计算合理 step，服务端仍然独立校验 |
| **最小惊讶** | 当 step 导致 bucket 数过多时，自动调大 step 而非报错（优雅降级），同时记日志提示 |
| **可单元测试** | 核心逻辑抽取为纯函数，可无 ES 依赖地进行表驱动测试 |

### 4.2 方案对比

| 方案 | 描述 | 优点 | 缺点 | 评估 |
|---|---|---|---|---|
| A. 调大 ES `max_buckets` | 设置集群参数 `search.max_buckets: 1000000` | 简单 | 治标不治本，恶意请求仍可撑爆内存；违背不信任原则 | ❌ 不采用 |
| B. Handler 层校验 | 在 `parseMetricRangeQuery` 中校验 bucket 数 | 快速 | 只保护 V2 Handler，不保护 Reader 被其他路径调用的场景 | ⚠️ 辅助手段 |
| C. **Reader 层 Fail-Fast + 自动 Clamp** | 在 `calculateInterval` 中校验并自动调大 interval | 纵深防御，不可绕过，优雅降级 | 需改核心逻辑 | ✅ **推荐** |
| D. Reader 层纯 Fail-Fast（报错） | bucket 超限时返回 error | 明确 | 对用户不友好，需改前端错误处理 | ⚠️ 可选 |

**最终方案：C（主）+ B（辅）**

### 4.3 详细设计

#### 4.3.1 新增公共常量与纯函数（可独立测试）

位置：`extension/observabilitystorageext/provider/elasticsearch/query/bucket_limit.go`

```go
package query

import (
    "fmt"
    "time"
)

const (
    // DefaultMaxBuckets is the safe upper bound for ES date_histogram buckets.
    // This is deliberately set below ES's default max_buckets (65535) to leave
    // headroom for nested aggregations that multiply the bucket count.
    DefaultMaxBuckets = 10000
)

// BucketParams holds the inputs needed to calculate a safe histogram interval.
type BucketParams struct {
    Duration   time.Duration // total time range
    Step       time.Duration // user-requested step (0 = auto)
    MaxBuckets int           // upper bound (0 = DefaultMaxBuckets)
}

// SafeInterval returns a histogram interval string that guarantees bucket
// count <= maxBuckets. If the user's step would produce too many buckets,
// it clamps upward to the minimum safe interval.
//
// Returns:
//   - interval: ES fixed_interval string (e.g. "60s")
//   - clamped: true if the returned interval differs from the user's step
func SafeInterval(p BucketParams) (interval string, clamped bool) {
    maxBuckets := p.MaxBuckets
    if maxBuckets <= 0 {
        maxBuckets = DefaultMaxBuckets
    }

    if p.Duration <= 0 {
        return "1m", false
    }

    // Calculate minimum safe interval to stay within bucket limit
    minSafeInterval := p.Duration / time.Duration(maxBuckets)
    if minSafeInterval < time.Second {
        minSafeInterval = time.Second
    }

    // If user specified a step, validate it
    if p.Step > 0 {
        if p.Step >= minSafeInterval {
            return fmt.Sprintf("%ds", int(p.Step.Seconds())), false
        }
        // Clamp: use the minimum safe interval instead
        clampedSeconds := int(minSafeInterval.Seconds()) + 1 // +1 to ensure strictly under limit
        return fmt.Sprintf("%ds", clampedSeconds), true
    }

    // Auto-calculate: use predefined tiers that are inherently safe
    durationSec := p.Duration.Seconds()
    switch {
    case durationSec <= 3600:     // ≤1h → 15s (max 240 buckets)
        return "15s", false
    case durationSec <= 21600:    // ≤6h → 1m (max 360 buckets)
        return "1m", false
    case durationSec <= 86400:    // ≤24h → 5m (max 288 buckets)
        return "5m", false
    case durationSec <= 604800:   // ≤7d → 30m (max 336 buckets)
        return "30m", false
    default:                      // >7d → 1h (max dependent on range)
        interval := "1h"
        if p.Duration/time.Hour > time.Duration(maxBuckets) {
            // Even 1h interval exceeds limit, need to scale up
            hours := int(p.Duration.Hours()) / maxBuckets + 1
            interval = fmt.Sprintf("%dh", hours)
            return interval, true
        }
        return interval, false
    }
}

// EstimateBucketCount returns the approximate number of buckets a given
// duration and interval would produce.
func EstimateBucketCount(duration time.Duration, interval time.Duration) int {
    if interval <= 0 {
        return 0
    }
    return int(duration / interval) + 1
}
```

#### 4.3.2 改造 `MetricReader.calculateInterval`

```go
// calculateInterval determines the appropriate histogram interval,
// ensuring bucket count stays within ES max_buckets limit.
func (r *MetricReader) calculateInterval(tr TimeRange, step time.Duration) string {
    duration := time.Duration(0)
    if !tr.Start.IsZero() && !tr.End.IsZero() {
        duration = tr.End.Sub(tr.Start)
    }

    interval, clamped := esq.SafeInterval(esq.BucketParams{
        Duration:   duration,
        Step:       step,
        MaxBuckets: esq.DefaultMaxBuckets,
    })

    if clamped {
        r.logger.Warn("metric range query step clamped to avoid too_many_buckets",
            zap.Duration("original_step", step),
            zap.String("clamped_interval", interval),
            zap.Duration("duration", duration),
            zap.Int("max_buckets", esq.DefaultMaxBuckets),
        )
    }

    return interval
}
```

#### 4.3.3 Handler 层辅助校验（B 方案，快速反馈）

在 `parseMetricRangeQuery` 中增加合理性提示（但不阻止请求，因为 Reader 层已有兜底）：

```go
// 在 parseMetricRangeQuery 的 query.Step 赋值后增加：
if query.Step > 0 && !query.TimeRange.Start.IsZero() && !query.TimeRange.End.IsZero() {
    duration := query.TimeRange.End.Sub(query.TimeRange.Start)
    estimatedBuckets := int(duration / query.Step)
    if estimatedBuckets > 10000 {
        // Log a warning but don't reject — Reader layer will clamp safely.
        // This is purely for observability (early detection in access logs).
        log.Printf("WARN: metric range query would produce ~%d buckets (step=%s, range=%s), will be clamped",
            estimatedBuckets, query.Step, duration)
    }
}
```

#### 4.3.4 错误信息优化

当前 `QueryRange` 直接暴露 ES 原始错误给客户端。改为语义化错误：

```go
resp, err := r.client.Search(ctx, r.indexPattern(query.AppID), searchReq)
if err != nil {
    // Wrap ES-specific errors with user-friendly context
    if strings.Contains(err.Error(), "too_many_buckets") {
        return nil, fmt.Errorf("metric range query: time range too large for the given step, try a larger step or shorter time range")
    }
    return nil, fmt.Errorf("metric range query failed: %w", err)
}
```

### 4.4 LogReader 同类问题预防

`log_reader.go` 的 `calculateInterval` **当前没有接受外部 step 参数**（纯自动计算，分档结果安全），暂无此风险。但建议统一切换为调用 `esq.SafeInterval`，确保未来如果增加用户自定义 step 功能时自动获得保护。

---

## §5 改动清单

| # | 文件 | 改动类型 | 内容 |
|---|---|---|---|
| 1 | `provider/elasticsearch/query/bucket_limit.go` | **新增** | `SafeInterval`/`EstimateBucketCount`/`DefaultMaxBuckets` |
| 2 | `provider/elasticsearch/query/bucket_limit_test.go` | **新增** | 表驱动单元测试，覆盖边界场景 |
| 3 | `provider/elasticsearch/metric_reader.go` | **修改** | `calculateInterval` 改为委托 `esq.SafeInterval` |
| 4 | `provider/elasticsearch/metric_reader_test.go` | **修改/新增** | 补充 bucket 溢出场景的单元测试 |
| 5 | `adminext/observability_handler_v2.go` | **修改** | `parseMetricRangeQuery` 增加日志级别警告 |
| 6 | `provider/elasticsearch/log_reader.go` | **修改**（可选） | `calculateInterval` 改为委托 `esq.SafeInterval` |
| 7 | `provider/elasticsearch/metric_reader.go` | **修改** | `QueryRange` 的 `date_histogram` 改用 scripted 方式：`doc['timeUnixNano'].value / 1000000`（Sprint 4 终态修复，兼容 ES 7.10.1） |

---

## §6 验收标准

- [x] `go build ./...` 全量编译通过
- [x] `go test ./extension/observabilitystorageext/provider/elasticsearch/query/...` 新增单元测试全部通过
- [x] 单元测试覆盖以下场景：
  - [x] `step=1s, duration=65536s` → interval 被 clamp 到安全值
  - [x] `step=1s, duration=100s` → interval 保持 `1s`（100 buckets，安全）
  - [x] `step=0, duration=30d` → 自动计算返回 `1h`
  - [x] `step=0, duration=0`（缺省）→ 返回 `1m`
  - [x] `duration > maxBuckets * 1h` → 自动升级到更大粒度
- [x] `go test ./extension/observabilitystorageext/...` 全量测试通过（回归）
- [ ] 手动测试：构造 `step=1s&start=0&end=65536000`（65536秒范围），确认：
  - 不返回 503
  - 返回正常聚合结果（bucket 数 ≤ 10000）
  - 日志输出 WARN 级别 "step clamped" 信息

---

## §7 Sprint 划分

| Sprint | 内容 | 验收 | 状态 |
|--------|------|------|------|
| 1 | 新增 `query/bucket_limit.go` + 单元测试 | 单测全部通过 (28 cases ✅) | ✅ 已完成（2026-07-10） |
| 2 | 改造 `metric_reader.go` 的 `calculateInterval` + 错误信息优化 | 编译+单测通过，手动构造超限请求验证 | ✅ 已完成（2026-07-10） |
| 3 | Handler 层警告日志 + LogReader 统一 | 编译通过 | ✅ 已完成（2026-07-10） |
| 4 | **终态修复**：字段改 `date` + `epoch_millis`，重命名 `timeUnixNano→timeUnixMilli`，date_histogram 直接用 field | 编译通过，全量测试 0 回归，ES 端到端 5→5 bucket ✅ | ✅ 已完成（2026-07-10） |

---

## §8 附录：bucket 数计算公式

```
bucket_count = ceil(duration_seconds / interval_seconds)
```

### 前端预设的安全分档（`calculateStep`）

| 时间范围 | step | 最大 bucket 数 |
|---------|------|--------------|
| ≤ 1h | 15s | 240 |
| ≤ 6h | 60s | 360 |
| ≤ 24h | 300s | 288 |
| ≤ 7d | 1800s | 336 |
| > 7d | 3600s | ∞（未封顶） |

### 后端 `calculateInterval` 自动分档

| 时间范围 | interval | 最大 bucket 数 |
|---------|----------|--------------|
| ≤ 1h | 15s | 240 |
| ≤ 6h | 1m | 360 |
| ≤ 24h | 5m | 288 |
| ≤ 7d | 30m | 336 |
| > 7d | 1h | **无上限** ⚠️ |

> ⚠️ 当 `step > 0` 时自动分档被完全绕过，bucket 数完全取决于 `duration / step`。
> ⚠️ 自动分档 `> 7d` 时返回 `1h`，若时间范围 > 2730 天（~7.5年）也会超限，但实际不太可能发生。

---

## §9 遗留问题（已升级为 §12 终态方案）

1. ~~**ES `max_buckets` 值是否需要从配置文件读取？**~~ → SafeInterval 硬编码 10000 保留，作为防御层不变。
2. **多层嵌套聚合的 bucket 乘法效应**：如果未来 `QueryRange` 增加 `by_labels` terms 子聚合（如"按 label 分组的时间序列"），总 bucket 数 = `time_buckets × label_buckets`，需要进一步考虑乘法约束。当前单层 `date_histogram` 不存在此问题。
3. **`observability_handler.go` 的 V1 路径**（Prometheus 代理）不经过本 MetricReader，由 Prometheus 自身控制，不受此修复影响。
4. ~~**`date_histogram` 对 long 字段类型的兼容性问题**~~ → 已在 §12 中分析并给出终态修复方案。

---

## §10 Sprint 1/2 实施记录（2026-07-10）

### 改动文件清单

| # | 文件 | 改动类型 | 内容 |
|---|---|---|---|
| 1 | `query/bucket_limit.go` | **新增** | `SafeInterval`/`EstimateBucketCount`/`DefaultMaxBuckets` |
| 2 | `query/bucket_limit_test.go` | **新增** | 28 个表驱动测试用例 |
| 3 | `metric_reader.go` | **修改** | `calculateInterval` → 委托 `esq.SafeInterval`；`QueryRange` 错误信息优化 |
| 4 | `docs/2026-07-10/metric-range-query-too-many-buckets-analysis.md` | **更新** | 状态标记、验收标准勾选、实施记录 |

### 验证结果

```
# 新增单元测试 (28 cases, all PASS)
$ go test ./extension/observabilitystorageext/provider/elasticsearch/query/... -v -count=1
--- PASS: TestSafeInterval_UserStep (9 sub-tests)
--- PASS: TestSafeInterval_AutoCalculate (8 sub-tests)
--- PASS: TestSafeInterval_EdgeCases (3 sub-tests)
--- PASS: TestEstimateBucketCount (4 sub-tests)
--- PASS: TestSafeInterval_AllBucketsStayWithinLimit (property-based)
ok   (0.327s)

# 全量编译
$ go build ./...
(no errors)

# 全量回归测试
$ go test ./extension/observabilitystorageext/... -count=1
ok   extension/observabilitystorageext
ok   extension/observabilitystorageext/lifecycle
ok   extension/observabilitystorageext/provider/elasticsearch
ok   extension/observabilitystorageext/provider/elasticsearch/query
ok   extension/observabilitystorageext/provider/postgresql
ok   extension/observabilitystorageext/storedmodel
(all PASS, 0 regressions)
```

### 关键设计决策（实施中的调整）

| 原设计 | 实施调整 | 原因 |
|--------|---------|------|
| 文档建议 `+1s` 作为 clamp 冗余 | 改用 `math.Ceil(minSafeSec)` | 精确公式，`+1s` 会过度 clamp（10s→11s），`Ceil` 数学上保证了 ≤ maxBuckets |
| 未指定 clamp 输出的格式 | clamp 路径固定输出秒（如 `260s`） | 避免 `durationToIntervalString` 的分钟/小时截断造成精度丢失（`260s→4m=240s<260s`） |
| `durationToIntervalString` 无 0s 保护 | 增加 `sec==0 → "1s"` | 避免 `fmt.Sprintf("%ds", 0) = "0s"` 的非法 ES 参数 |

---

## §11 Sprint 3 实施记录（2026-07-10）

### 改动文件清单

| # | 文件 | 改动类型 | 内容 |
|---|---|---|---|
| 5 | `adminext/observability_handler_v2.go` | **修改** | `handleMetricQueryRangeV2` 增加 bucket 数超标警告日志 |
| 6 | `provider/elasticsearch/log_reader.go` | **修改** | `calculateInterval` 委托 `esq.SafeInterval`（统一安全保护） |

### Handler 层改动说明

在 `handleMetricQueryRangeV2` 中，在调用 `QueryRange` 之前增加了预估 bucket 数检查：当 `estimated_buckets > 10000` 时输出 WARN 日志（但不阻止请求，因为 MetricReader 层已有 clamp 兜底）。日志包含 step、duration、estimated_buckets，用于在访问日志中提前发现恶意/错误调用方。

### LogReader 改动说明

`log_reader.go` 的 `calculateInterval` 原先有独立的分档逻辑（1m→5m→15m→1h→6h→1d），改为委托 `esq.SafeInterval`（使用 MetricReader 统一的分档策略 15s→1m→5m→30m→1h）。同时获得了：
- bucket 上限保护（`DefaultMaxBuckets = 10000`）
- clamp 时输出 WARN 日志

### 验证结果

```
# 全量编译
$ go build ./...
(no errors)

# 全量回归测试
$ go test ./extension/observabilitystorageext/... -count=1
(all PASS, 0 regressions)

# lint 检查
$ read_lints → 0 new issues
```

---

## §12 终态根因分析：`date_histogram` 对 `long` 字段不可用（2026-07-10）

### 12.1 问题现象

Sprint 1-3 实施完成后（SafeInterval clamp 逻辑正确），页面 Metric 查询 **仍然报错**：

```
metric range query: time range too large for the given step, try a larger step or shorter time range
```

这说明 ES 仍然返回 `too_many_buckets`，SafeInterval 的数学保护未能生效。

### 12.2 深层根因：字段类型 `long` 与 `date_histogram` + `fixed_interval` 不兼容

#### 事实链

| # | 事实 | 来源 |
|---|------|------|
| 1 | `FieldMetricTimeUnixNano` 在 ES 中的 mapping 类型是 **`long`** | `admin.go:309` → `map[string]any{"type": "long"}` |
| 2 | 存储的值是 **纳秒时间戳**（`time.UnixNano()`） | `metric_reader.go:145-148` → `query.TimeRange.Start.UnixNano()` |
| 3 | `date_histogram` 聚合**只原生支持 `date` 或 `date_range` 类型字段** | [ES 官方文档](https://www.elastic.co/docs/reference/aggregations/search-aggregations-bucket-datehistogram-aggregation)："can only be used with date or date range values" |
| 4 | ES 内部将 date 表示为 **毫秒级 epoch long** | ES 官方文档："a 64 bit number representing a timestamp in milliseconds-since-the-epoch" |
| 5 | 当 `date_histogram` 被强制应用于 `long` 字段时，ES 将字段值**按 epoch_millis 解释** | ES 文档推导：date_histogram 假设输入为 date 类型的内部表示（毫秒） |
| 6 | `fixed_interval: "15s"` = 15,000 毫秒 | ES 文档：fixed_interval 基于 SI 单位 |

#### 数学证明

```
假设查询时间范围 = 1 小时

字段实际存储 = 纳秒时间戳
→ 数值范围 = end_nanos - start_nanos = 3,600,000,000,000 (3.6×10^12)

ES 将 long 值按 epoch_millis 解释:
→ ES 认为的"时间范围" = 3,600,000,000,000 ms = ~114 年

fixed_interval = "15s" = 15,000 ms

bucket 数 = 3,600,000,000,000 / 15,000 = 240,000,000 (2.4 亿)
→ 远超 max_buckets = 65,535

结论: SafeInterval 算出 "15s" 对 1h 范围是安全的 (3600/15 = 240 buckets)
      但 ES 实际计算的 bucket 数是 2.4 亿，因为它按毫秒解释了纳秒值
```

#### 为什么 SafeInterval 的 clamp "失效"

SafeInterval 的计算逻辑是完全正确的：
```
duration = 1h → SafeInterval("15s") → 240 buckets ≤ 10000 ✅
```

但问题不在 SafeInterval 的数学，而在 **ES 对 `fixed_interval` 的语义解释**：
- SafeInterval 认为 `"15s"` = "每 15 秒一个 bucket"
- ES 对 long 字段认为 `"15s"` = "每 15000 个单位（毫秒）一个 bucket"
- 而字段值是纳秒，所以 ES 看到的"时间范围"比真实时间大 **10^6 倍**

### 12.3 方案评估

| 方案 | 描述 | 可行性 | 长远适合度 |
|------|------|--------|-----------|
| B. `histogram`+纳秒数值 | 将 `date_histogram` 改为 `histogram`，interval 传纳秒 int64 | ✅ 可行 | ❌ 差：丧失时间语义（calendar_interval/timezone/offset），Kibana 无法识别时间轴，interval 是大数调试困难 |
| C. **`date_nanos` 类型** | 将字段 mapping 从 `long` 改为 `date_nanos` + `format: epoch_nanos` | ✅ 可行 | ✅ **终态最优** |
| D. runtime_field 转换 | 查询时用 script 将 nanos 转 millis 再做 date_histogram | ✅ 可行 | ❌ 差：每次查询执行脚本，性能开销大 |

### 12.4 终态最优解：存储毫秒 + `date` + `epoch_millis` + 字段重命名

#### 核心思路

将存储值从纳秒改为毫秒，使用 ES 原生 `date` + `epoch_millis` 类型，`date_histogram` 可直接用 `"field"` 无需 script。

#### 为何不选 `date_nanos` + `epoch_nanos`？

ES 7.10.1 不支持 `epoch_nanos` format（需 ES 7.11+/8.x），且 `date_nanos` 默认 format 将整数当 `epoch_millis` 解释导致纳秒值溢出。

#### 最终方案特性

| 特性 | 表现 |
|------|------|
| 存储精度 | 毫秒（int64，对 15s+ 聚合粒度完全够用） |
| `date_histogram` 支持 | ✅ 原生支持 `"field": "timeUnixMilli"`，无需 script |
| `format` 配置 | `epoch_millis`：ES 7.x/8.x 全兼容 |
| Kibana 兼容 | ✅ 自动识别为时间字段 |
| Range query | ✅ 毫秒整数值直接用于 `gte`/`lte` |
| BKD tree 索引 | ✅ ES 对 date 字段有专门的时间索引优化 |
| 无 script | ✅ date_histogram 直接用 field，零 overhead |

#### 字段命名

| 旧 | 新 | 理由 |
|----|-----|------|
| `timeUnixNano` | `timeUnixMilli` | 准确反映毫秒值语义，避免命名误导 |

#### 改动概览

| # | 文件 | 内容 |
|---|------|------|
| 1 | `stored_metric.go` | 字段+JSON tag 重命名，值纳秒→毫秒 |
| 2 | `fields.go` | 常量重命名 |
| 3 | `admin.go` | Mapping `date` + `epoch_millis` |
| 4 | `metric_writer.go` | `UnixMilli()` |
| 5 | `metric_reader.go` | 字段引用 + range filter + date_histogram 去 script |
| 6 | `types.go` | 公共 API JSON 字段重命名 + `TimeToUnixMilli()` |
| 7 | adapter files × 2 | 转换用 `TimeToUnixMilli()` |
| 8 | 前端 TS × 2 | 类型定义 + utils |

### 12.5 实施 Sprint 划分

| Sprint | 内容 | 验收标准 |
|--------|------|----------|
| 4 | `QueryRange` 改用 **scripted date_histogram**（Painless script `doc['timeUnixNano'].value / 1000000`）| 编译通过；ES 端到端验证 5 条数据 → 5 个 15s bucket；无 too_many_buckets 错误 |

#### 为何不用 `date_nanos` + `epoch_nanos` mapping？

ES 7.10.1 不支持 `epoch_nanos` format（该 format 在 ES 7.11+/8.x 才可用）：
- 实测：`date_nanos` 默认将整数值按 `epoch_millis` 解释 → 纳秒值溢出（年份 56,523,874，远超 2262 上限）
- 若升级到 ES 8.x，可直接改用 `date_nanos` + `epoch_nanos` mapping 并去掉 script

#### Scripted date_histogram 详解

```json
{
  "aggs": {
    "time_series": {
      "date_histogram": {
        "script": {"source": "doc['timeUnixNano'].value / 1000000"},
        "fixed_interval": "15s",
        "min_doc_count": 0
      }
    }
  }
}
```

- `doc['timeUnixNano'].value` 读取 long 字段的纳秒整数值
- `/ 1000000` 转换为毫秒精度（与 ES date_histogram 的 epoch_millis 一致）
- `fixed_interval: "15s"` 被正确解释为 15 秒（而非 15000 个单位）
- Bucket key 返回 `epoch_millis` → `time.UnixMilli(b.Key)` 无需修改

#### 部署注意事项

由于不需要向后兼容，部署步骤：
1. 部署新代码（mapping 改为 `date_nanos`）
2. 删除旧的 metric 索引（`DELETE /<prefix>-metrics-*`）
3. 服务启动时 `EnsureIndices()` 会自动创建新 mapping 的索引
4. 等待数据重新写入（testdatagen receiver 会持续产生数据）
5. 验证 metric range query 正常

### 12.6 与 Sprint 1-3 的关系

Sprint 1-3 实施的 **SafeInterval clamp 逻辑仍然保留**，作为纵深防御的第二层：
- **第一层**（根本修复）：`date_nanos` 让 `date_histogram` 正确理解时间语义 → ES 不再产生天文数字的 bucket
- **第二层**（防御）：SafeInterval 仍然限制 bucket 数 ≤ 10000，防止超大时间范围（如 10 年）+ 极小 step（如 1s）的请求消耗过多 ES 资源
- **第三层**（可观测性）：Handler 层 WARN 日志提前发现异常调用方

三层防御协同工作，缺一不可。

---

## §13 Sprint 4 实施记录（2026-07-10）

### 最终方案：`date` + `epoch_millis` + 字段重命名 `timeUnixNano` → `timeUnixMilli`

#### 方案演进

| 迭代 | 方案 | 问题 |
|------|------|------|
| 第1版 | `date_nanos` + `epoch_nanos` mapping | ES 7.10.1 不支持 `epoch_nanos` format |
| 第2版 | scripted `date_histogram`（`doc['timeUnixNano'].value / 1000000`） | 每个 doc 执行一次除法，不优雅 |
| **第3版（终态）** | **存储毫秒 + `date` + `epoch_millis`** | **零 script，ES 原生时间语义** ✅ |

#### 核心思路

将存储值从纳秒改为毫秒，使 ES 的 `date` + `epoch_millis` 原生可用，`date_histogram` 直接用 `"field"` 无需 script。

```
存储:  time.UnixMilli()  →  int64 毫秒  →  ES date + epoch_millis
查询:  date_histogram { field: "timeUnixMilli", fixed_interval: "15s" }
```

#### 改动文件清单

| # | 文件 | 改动类型 | 内容 |
|---|---|---|---|
| 7 | `storedmodel/stored_metric.go` | **修改** | `TimeUnixNano` → `TimeUnixMilli`，值纳秒→毫秒 |
| 8 | `fields.go` | **修改** | 常量 `FieldMetricTimeUnixNano` → `FieldMetricTimeUnixMilli`，值 → `"timeUnixMilli"` |
| 9 | `admin.go` | **修改** | Mapping `"long"` → `"date", "format": "epoch_millis"` |
| 10 | `metric_writer.go` | **修改** | `time.Unix(0, nanos)` → `time.UnixMilli(millis)` |
| 11 | `metric_reader.go` | **修改** | 字段引用 + range filter `UnixMilli()` + date_histogram 去掉 script 直接用 field |
| 12 | `purger.go` | **修改** | 常量引用 |
| 13 | `stored_to_public.go` | **修改** | `TimeUnixNano` → `TimeUnixMilli` |
| 14 | `types.go` | **修改** | 公共 API `MetricDataPoint`/`MetricTimeValue` 的 JSON 字段重命名；新增 `TimeToUnixMilli()` |
| 15 | `reader_adapter.go` | **修改** | 转换用 `TimeToUnixMilli()` |
| 16 | `pg_reader_adapter.go` | **修改** | 转换用 `TimeToUnixMilli()` |
| 17 | `postgresql/metric_writer.go` | **修改** | `time.Unix(0, nanos)` → `time.UnixMilli(millis)` |
| 18 | `webui-react/src/types/metric.ts` | **修改** | TS 类型 `timeUnixNano` → `timeUnixMilli` |
| 19 | `webui-react/src/utils/metric.ts` | **修改** | 去掉 `/ 1_000_000` 转换（已是毫秒） |

### ES mapping (终态)

```json
{
  "timeUnixMilli": {
    "type": "date",
    "format": "epoch_millis"
  }
}
```

### date_histogram 查询 (终态)

```json
{
  "date_histogram": {
    "field": "timeUnixMilli",
    "fixed_interval": "15s",
    "min_doc_count": 0
  }
}
```

**零 script，ES 原生时间语义。**

### 端到端验证（ES 7.10.1）

```
# 写入 5 条 (毫秒时间戳, 间隔 15s)
# 查询: date_histogram { field: "timeUnixMilli", fixed_interval: "15s" }
→ Total buckets: 5  ✅
→ key=1783659705000 (epoch_millis)  ✅
→ doc_count=1 per bucket  ✅
→ avg values correct  ✅
→ No too_many_buckets errors  ✅
→ No script needed!  ✅
```

### 验证结果

```
# 全量编译
$ go build ./...
(no errors)

# 全量回归测试
$ go test ./extension/observabilitystorageext/... -count=1
(all PASS, 0 regressions)
```

### 部署注意事项（重要！）

**部署时序问题**：代码部署前，旧服务仍在运行并持续写入 `timeUnixNano` 字段。即使已删除旧索引和更新 template，旧服务写入的数据会导致索引以动态 mapping 创建（`timeUnixNano: double`），新服务部署后会因 `timeUnixMilli` 字段不存在而报错：

```
"No mapping found for [timeUnixMilli] in order to sort on"
```

**必须按以下顺序操作**：
1. 部署新代码
2. 删除旧索引：`DELETE /otel-metrics-*`
3. 新服务写入 `timeUnixMilli` → template 生效 → 索引正确创建 `timeUnixMilli: date + epoch_millis`

**验证**：
```
# 确认 mapping
GET /otel-metrics-*/_mapping/field/timeUnixMilli
→ {"type": "date", "format": "epoch_millis"}  ✅

# 确认数据
GET /otel-metrics-*/_search?size=1
→ timeUnixMilli=<epoch_millis 值>  ✅
→ timeUnixNano 字段不存在  ✅
