# PromQL topk/bottomk 支持设计

> 状态：已实施（Sprint 1-3 完成）  
> 设计日期：2025-07-21  
> 最后更新：2025-07-21（Sprint 3 完成）  
> 关联文件：`extension/adminext/prometheus_handler.go`, `extension/adminext/prometheus_handler_test.go`

---

## 1. 问题分析

### 1.1 现象

Grafana Tempo Metrics 页面发送如下 PromQL 查询，返回空结果：

```
topk(5, sum(rate(traces_spanmetrics_calls_total{span_kind="SPAN_KIND_SERVER"}[1800s])) by (span_name))
```

### 1.2 根因：解析器不支持 `topk`

追踪 `parsePromQL` 解析链：

```
输入: topk(5, sum(rate(traces_spanmetrics_calls_total{span_kind="SPAN_KIND_SERVER"}[1800s])) by (span_name))
                                                                              ↑ 第一个 { 的位置
```

| 步骤 | 函数 | 输入 | 匹配？ |
|------|------|------|-------|
| 1 | `parseHistogramQuantileWrapper` | 完整表达式 | ❌ 不以 `histogram_quantile(` 开头 |
| 2 | `parseAggWrapper` | 完整表达式 | ❌ `topk` 不在 `[sum, avg, max, min, count]` |
| 3 | `parseFuncWrapper` | 完整表达式 | ❌ 不以 `rate/increase/irate(` 开头 |
| 4 | `parseSelector` | 完整表达式 | ⚠️ 错误解析 |

第4步 `parseSelector` 按第一个 `{` 分割：

```
name = s[:68]  = "topk(5, sum(rate(traces_spanmetrics_calls_total"
labels         = {span_kind: "SPAN_KIND_SERVER"}
```

最终 `promqlExpr`：

```go
MetricName:    "topk(5, sum(rate(traces_spanmetrics_calls_total"  // 错了！应该是 "traces_spanmetrics_calls_total"
Function:      ""       // 错了！应该是 "rate"
RangeDuration: 0        // 错了！应该是 1800s
Aggregation:   ""       // 错了！应该是 "sum"
GroupBy:       nil      // 错了！应该是 ["span_name"]
```

因为 `Function == "" && RangeDuration == 0`，走 `MetricQuery` 单点查询路径，用错误的 metric name 查 ES → 零结果。

### 1.3 附随发现的 Bug：`execRateInstant` 缺少聚合处理

对比 `execRateRange` 和 `execRateInstant`：

| 聚合处理 | `execRateRange` | `execRateInstant` |
|----------|:----:|:----:|
| `applyAggregation` | ✅ | ❌ |
| `stripMetricToGroupBy` | ✅ | ❌ |

这意味着 `sum(rate(x[5m])) by (label)` 作为 instant query 时，`sum` 和 `by` 不会被处理——数据能查到但缺少聚合。（此 bug 在 QueryFlat 改动前已存在。）

---

## 2. 设计原则

### 2.1 topk 的语义定位

在 Prometheus 中，`topk`/`bottomk` 是 **后处理操作符**：
1. 先用内层表达式算出完整的结果向量
2. 再按值排序取 top/bottom K

**设计原则**：topk 不参与数据查询，只参与结果后处理。这决定了它应该在查询引擎层实现，而不是在存储层。

### 2.2 职责分层

```
┌──────────────────────────────────────┐
│  parsePromQL                         │  ← 解析 topk wrapper，递归解析内层
│  └─ parseTopKWrapper                 │  ← 新增
│  └─ parseAggWrapper (已有)            │
│  └─ parseFuncWrapper (已有)           │
│  └─ parseSelector (已有)              │
├──────────────────────────────────────┤
│  dispatchInstantQuery                │  ← 编排：取数据 → 聚合 → topk
│  ├─ execRateInstant                  │  ← 修复：补上缺失的聚合步骤
│  └─ applyTopK                        │  ← 新增：topk/bottomk 后处理
├──────────────────────────────────────┤
│  storage layer (QueryFlat/Query)     │  ← 不变，不感知 topk
└──────────────────────────────────────┘
```

### 2.3 SOLID 验证

| 原则 | 满足方式 |
|------|---------|
| SRP | `parseTopKWrapper` 只解析；`applyTopK` 只计算；各司其职 |
| OCP | 新增函数 + 扩展 struct，不改已有函数接口签名 |
| LSP | `promqlExpr.TopK = 0` 时行为完全不变，100% 向后兼容 |
| ISP | topk 处理不侵入存储层，不侵入 histogram 路径 |
| DIP | dispatch 编排层只依赖纯函数（parseTopKWrapper/applyTopK），不依赖外部 |

---

## 3. 详细设计

### 3.1 `promqlExpr` 扩展

```go
type promqlExpr struct {
    // ... 现有字段不变 ...
    TopK     int  // 0 = 不启用; >0 = topk(N, ...); 负数由 IsBottomK 反转
    IsBottomK bool // true = bottomk（取最小的 K 个）
}
```

> 设计决策：用 `TopK int` + `IsBottomK bool` 代替 `FuncName string + Param int`，避免与现有 `Aggregation`/`Function` 字段语义混淆。`TopK` 独立于它们的执行链。

### 3.2 解析器：`parseTopKWrapper`

```go
// parseTopKWrapper extracts the inner expression from topk(N, ...) or bottomk(N, ...).
// Returns (inner expression, K, isBottomK).
// Returns ("", 0, false) if the input is not a topk/bottomk wrapper.
func parseTopKWrapper(s string) (inner string, k int, isBottomK bool)
```

**实现逻辑**：

```
1. s = TrimSpace(s)
2. 匹配前缀: "topk(" → isBottomK=false; "bottomk(" → isBottomK=true
3. 提取 N:
   - s[len(prefix):] 中找到第一个 ','
   - 中间的字符 TrimSpace → parse int
   - N <= 0 → 返回错误
4. 从 ',' 之后括号深度跟踪找到匹配的 ')':
   - depth = 1 (因为 prefix 中的 '(')
   - 每遇 '(' depth++, 每遇 ')' depth--
   - depth == 0 时找到结尾
5. inner = s[',N' 之后 : 结尾')'之前]
6. return TrimSpace(inner), N, isBottomK
```

**边界处理**：
- `topk( 5 , expr )` → trim space 后正常解析
- `topk(0, expr)` → TopK 无效，返回解析错误
- `topk(-1, expr)` → 返回解析错误
- 无参数的 `topk`（语法错误）→ 返回 `("", 0, false)`，回退给外层

### 3.3 `parsePromQL` 集成（递归解析方案）

**核心设计决策**：`parseTopKWrapper` 剥离外层后，对 inner 表达式 **递归调用 `parsePromQL`**，而非继续线性往下走。

这样能正确处理任意嵌套顺序，例如：
- `topk(5, histogram_quantile(0.95, sum(rate(x_bucket[5m])) by (le)))` — topk 在外层
- `histogram_quantile(0.95, topk(5, sum(rate(x_bucket[5m])) by (le)))` — topk 在内层（语义怪异但语法合法）

```go
func parsePromQL(s string) (*promqlExpr, error) {
    s = strings.TrimSpace(s)

    // 0. histogram_quantile(θ, ...) wrapper (已有)
    if inner, theta := parseHistogramQuantileWrapper(s); inner != "" {
        // 递归解析内层
        expr, err := parsePromQL(inner)
        if err != nil {
            return nil, err
        }
        expr.Aggregation = "histogram_quantile"
        expr.Quantile = theta
        return expr, nil
    }

    // 1. topk(N, ...) / bottomk(N, ...) wrapper (新增)
    if inner, k, isBk := parseTopKWrapper(s); inner != "" {
        // 递归解析内层 — 使 topk 可与任何内层表达式组合
        expr, err := parsePromQL(inner)
        if err != nil {
            return nil, err
        }
        expr.TopK = k
        expr.IsBottomK = isBk
        return expr, nil
    }

    // 2. aggregation wrapper (已有)
    // 3. function wrapper (已有)
    // 4. selector (已有)
    // ... 其余线性解析不变
    expr := &promqlExpr{}
    // ...
    return expr, nil
}
```

**为什么采用递归而非线性剥离？**

| 方案 | 优点 | 缺点 |
|------|------|------|
| 线性剥离（原设计） | 简单，改动少 | 对嵌套顺序有隐式约束，`topk(N, histogram_quantile(...))` 无法正确解析 |
| 递归解析（修正方案） | 与 Prometheus 语义一致，任意嵌套顺序均可正确处理 | 需要改造 `parseHistogramQuantileWrapper` 也走递归（一并修正） |

**为什么 topk 放在 aggregation 之前？**

因为 `topk(5, sum(rate(...)) by (...))` 中，`sum(...) by (...)` 是内层表达式。`parseAggWrapper` 负责解析 `sum(xxx) by (labels)`。topk 包装在 sum 外面，必须先剥离 topk 才能让 sum 正确匹配。

### 3.4 引擎层：`applyTopK`

```go
// applyTopK sorts vectors by value and returns the top K (or bottom K).
// When isBottomK is true, returns the K smallest values.
// If k >= len(vectors), returns all vectors (no-op).
// Sorting is stable to ensure deterministic results for equal values.
func applyTopK(k int, isBottomK bool, vectors []promVectorSample) []promVectorSample
```

**实现逻辑**：

```go
if k >= len(vectors) {
    return vectors
}

// Sort by value descending (topk) or ascending (bottomk)
// Use sort.SliceStable for deterministic ordering on ties
sort.SliceStable(vectors, func(i, j int) bool {
    vi := parseVectorValue(vectors[i].Value)
    vj := parseVectorValue(vectors[j].Value)
    if isBottomK {
        return vi < vj
    }
    return vi > vj
})

return vectors[:k]
```

**为什么独立为函数？**
- 纯计算逻辑，无外部依赖 → 可单独单元测试
- `dispatchInstantQuery` 和未来 `dispatchRangeQuery` 都可复用

### 3.5 引擎层：`dispatchInstantQuery` 重构

#### 3.5.1 当前结构问题

```
当前结构（有问题）:
  dispatchInstantQuery
  ├─ [rate 路径] return execRateInstant(...)        ← 直接 return，缺少聚合 + 缺少 topk
  └─ [普通路径] 聚合 → return                        ← 返回前缺少 topk
```

关键缺陷：`execRateInstant` 返回 `*promQueryData`（包装类型），dispatch 中直接 `return`，无法在返回后追加后处理。

#### 3.5.2 重构方案：统一编排层

```
重构后:
  dispatchInstantQuery
  ├─ [rate 路径] execRateInstant → 返回 []promVectorSample
  ├─ [普通路径] Query → 构建 []promVectorSample
  ├─ [统一后处理] 聚合 → topk → 包装为 promQueryData → 返回
  └─ [histogram 路径] 独立处理（已有）
```

#### 3.5.3 `execRateInstant` 返回类型修正

**修正前**：`func (e *Extension) execRateInstant(...) *promQueryData`  
**修正后**：`func (e *Extension) execRateInstant(...) []promVectorSample`

修正理由：
- handler 只负责数据获取 + 计算（QueryFlat → 分组 → 计算 rate）
- 后处理（聚合、topk、包装）是编排层的职责
- 返回原始 vectors 让编排层拥有完整控制权，避免类型断言风险

#### 3.5.4 伪代码

```go
func (e *Extension) dispatchInstantQuery(r *http.Request, expr *promqlExpr, ...) *promQueryData {
    // ... label exploration 不变 ...

    var vectors []promVectorSample

    if expr.Function != "" && expr.RangeDuration > 0 {
        vectors = e.execRateInstant(r, expr, evalTime, labels)
        if vectors == nil {
            vectors = []promVectorSample{}
        }
    } else {
        // 普通 instant 查询 (已有逻辑)
        // ... 构建 vectors ...
    }

    // ═══════ 统一后处理 pipeline ═══════

    // Step 1: 聚合
    if expr.Aggregation != "" && expr.Aggregation != "histogram_quantile" {
        vectors = applyAggregation(expr.Aggregation, expr.GroupBy, vectors)
        stripMetricToGroupBy(vectors, expr.GroupBy)
    }

    // Step 2: topk/bottomk
    if expr.TopK > 0 {
        vectors = applyTopK(expr.TopK, expr.IsBottomK, vectors)
    }

    return &promQueryData{ResultType: "vector", Result: vectors}
}
```

#### 3.5.5 职责划分（最终版）

| 函数 | 负责 | 不负责 |
|------|------|--------|
| `execRateInstant` | QueryFlat → 分组 → 计算 rate → 返回 `[]promVectorSample` | 聚合、topk、结果包装 |
| `execRateRange` | QueryFlat → 分组 → 计算 rate → 聚合 → 返回 matrix | topk（暂不加，后续按需驱动） |
| `dispatchInstantQuery` | 编排：调 handler → 聚合 → topk → 包装返回 | 存储细节、rate 计算 |

> 设计决策：`execRateRange` 保留内部聚合，因为 range 路径的聚合逻辑不同（走 `aggregateMatrix`），
> 且 range query 的 topk 语义更复杂（选 top K 条完整时间序列），不在此次范围内。

### 3.6 Bug 修复：`execRateInstant` 聚合缺失

**问题本质**：`execRateInstant` 直接返回 `*promQueryData`，绕过了聚合步骤，导致 `sum(rate(x[5m])) by (label)` 在 instant query 下不做聚合。

**修复方案**：已在 §3.5 的重构中一并解决——`execRateInstant` 返回类型改为 `[]promVectorSample`，聚合统一由 `dispatchInstantQuery` 的后处理 pipeline 完成。

**回归保障**：
- 非 rate 路径的聚合逻辑从散落位置收拢到统一后处理 pipeline，行为不变
- rate 路径新增聚合步骤是 bug fix，属于行为修正
- 所有已有测试必须 100% 通过

---

## 4. 改动范围

| 文件 | 改动 | 行数 |
|------|------|------|
| `prometheus_handler.go` — `promqlExpr` | 新增 `TopK int`, `IsBottomK bool` | 2 |
| `prometheus_handler.go` — `parsePromQL` | 插入 `parseTopKWrapper` 调用 | 5 |
| `prometheus_handler.go` — `parseTopKWrapper` | 新函数 | ~35 |
| `prometheus_handler.go` — `applyTopK` | 新函数 | ~20 |
| `prometheus_handler.go` — `dispatchInstantQuery` | 重构编排层：提取聚合 + 追加 topk | ~15 |
| `prometheus_handler_test.go` — 解析器测试 | `TestParseTopKWrapper`, `TestParsePromQL_TopK` | ~60 |
| `prometheus_handler_test.go` — 引擎测试 | `TestApplyTopK` | ~50 |

**总计 ~185 行，0 行存储层改动，0 行 ES 层改动。**

---

## 5. 不在此次范围内的需求

| 需求 | 原因 |
|------|------|
| Range query 的 topk | `execRateRange` 产生 `[]promMatrixSample`（矩阵），topk 语义不同（选 top K 条完整时间序列），需更多设计。当前 Tempo Metrics 只用 instant 查询，按需驱动 |
| `topk` 作为中间表达式（如 `sum(topk(5, x))`） | Prometheus 本身不支持嵌套 topk，实际无此场景 |
| `topk` 的 `without` 子句 | 尚未见到实际使用，后续按需 |

---

## 6. 测试策略

### 6.1 解析器单元测试（纯函数，无 mock）

```
TestParseTopKWrapper:
  - "topk(5, metric_name{label=\"val\"})" → inner="metric_name{label=\"val\"}", k=5, isBottomK=false
  - "bottomk(10, sum(rate(x[5m])))"       → inner="sum(rate(x[5m]))",     k=10, isBottomK=true
  - "topk( 3 ,  metric_name  )"           → inner="metric_name",          k=3  (whitespace)
  - "sum(rate(x[5m]))"                    → ("", 0, false)               (not topk/bottomk)
  - "topk(0, expr)"                       → error                         (invalid K)
  - "topk(-5, expr)"                      → error                         (invalid K)
  - "topk(abc, expr)"                     → error                         (non-integer K)

TestParsePromQL_TopK:
  - "topk(5, sum(rate(traces_spanmetrics_calls_total{span_kind=\"SPAN_KIND_SERVER\"}[1800s])) by (span_name))"
    → expr.MetricName="traces_spanmetrics_calls_total", Function="rate", Aggregation="sum",
      GroupBy=["span_name"], TopK=5
```

### 6.2 引擎单元测试（纯函数，无 mock）

```
TestApplyTopK:
  - TopK=3, 5 vectors sorted → 前3个
  - TopK=10, 5 vectors → 全部5个 (k >= len 保持原样)
  - IsBottomK=true → 升序取最小的3个
  - 相同值的 vectors → stable sort 不改变原有顺序
  - 空 vectors → 返回空切片
```

### 6.3 集成验证

- `sum(rate(x[5m])) by (label)` 作为 instant query：聚合正确应用
- `topk(5, sum(rate(x[30m])) by (span_name))`：end-to-end 返回正确结果

---

## 7. 架构评审结论

### 7.1 评审矩阵

| 维度 | 评分 | 说明 |
|------|:----:|------|
| **可行性** | ✅ | 方案可行，核心逻辑正确 |
| **最优性** | ✅ | 递归解析 + 统一编排层后为当前架构约束下的最优解 |
| **内聚性** | ✅ | 解析/引擎/存储分层清晰，各层职责单一 |
| **耦合性** | ✅ | topk 不侵入存储层，不侵入 histogram 路径 |
| **可扩展性** | ✅ | 递归解析消除了嵌套顺序约束，新增包装函数只需加一个 if 分支 |
| **健壮性** | ✅ | 错误场景 graceful degradation，类型安全（无裸断言） |
| **可测试性** | ✅ | 纯函数设计，所有关键逻辑可独立单测 |

### 7.2 关键设计决策记录

| 决策点 | 选择 | 理由 |
|--------|------|------|
| 解析方式 | 递归调用 `parsePromQL(inner)` | 消除嵌套顺序隐式约束，与 Prometheus 语义一致 |
| `execRateInstant` 返回类型 | `[]promVectorSample` | 避免 `any` 类型断言风险，让编排层拥有完整控制权 |
| 聚合位置 | 统一上提到 `dispatchInstantQuery` | handler 只做取数+计算，编排层统一后处理 pipeline |
| K 无效时行为 | 返回解析错误 | 明确报错优于静默降级 |
| `TopK` 字段设计 | `int + bool` 而非 `string + int` | 避免与 `Aggregation`/`Function` 语义混淆 |

### 7.3 `promqlExpr` 字段膨胀预警

当前 `promqlExpr` 有 10 个字段，加上 `TopK` + `IsBottomK` 变为 12 个。在当前 mini PromQL parser 范围内可控。

**阈值**：当字段超过 15 个时，应考虑重构为 AST tree node 结构：

```go
// 未来考虑（当前不实施）
type PromQLNode interface {
    Children() []PromQLNode
    Evaluate(vectors []promVectorSample) []promVectorSample
}
```

### 7.4 风险分析

| 风险 | 概率 | 影响 | 缓解措施 |
|------|:----:|:----:|---------|
| `execRateInstant` 返回类型变更影响调用方 | 中 | 中 | 编译期检查；调用方仅 `dispatchInstantQuery` 一处 |
| `parseHistogramQuantileWrapper` 改为递归引入栈溢出 | 极低 | 低 | PromQL 嵌套深度实际不超过 4 层；可加最大递归深度保护 |
| 聚合上提后 rate 路径行为变更 | 中 | 高 | 现有测试覆盖 + 新增 instant 聚合回归测试 |
| Grafana 发送未预期的 topk 变体 | 低 | 低 | 解析失败时 graceful 回退，返回空结果而非 500 |

---

## 8. 实施计划

| Sprint | 内容 | 验证 | 预估 |
|--------|------|------|------|
| Sprint 1 | `promqlExpr` 扩展 + `parseTopKWrapper` + `parsePromQL` 递归改造 | 解析器测试 100% 通过 | 1h |
| Sprint 2 | `execRateInstant` 返回类型修正 + `dispatchInstantQuery` 统一编排 + `applyTopK` | 引擎测试 + 回归测试 100% 通过 | 2h |
| Sprint 3 | 日志/span 打点 + Grafana Tempo 端到端验证 | 已有测试全部通过 + Grafana 页面数据正确 | 0.5h |

---

## 9. 进展记录

| 日期 | 事项 | 状态 |
|------|------|:----:|
| 2025-07-21 | 初版设计文档 | ✅ |
| 2025-07-21 | 架构评审 + 修正（递归解析、返回类型、统一编排） | ✅ |
| 2025-07-21 | Sprint 1 实施 | ✅ |
| 2025-07-21 | Sprint 2 实施 | ✅ |
| 2025-07-21 | Sprint 3 实施 | ✅ |

### Sprint 1 实施记录

**变更文件**：
- `extension/adminext/prometheus_handler.go` — `promqlExpr` 新增 `TopK`/`IsBottomK` 字段；新增 `parseTopKWrapper()`；`parsePromQL` 递归改造（`histogram_quantile` 和 `topk/bottomk` 均改为递归解析）
- `extension/adminext/prometheus_handler_test.go` — `TestParseTopKWrapper`（11 个用例）、`TestParsePromQL_TopK`（3 个用例）、`TestParsePromQL_HistogramQuantile_StillWorks`（回归验证）、`TestParsePromQL_NoTopK_DefaultsZero`（向后兼容）

**测试结果**：全部 PASS，无回归

### Sprint 2 实施记录

**变更文件**：
- `extension/adminext/prometheus_handler.go`:
  - `execHistogramQuantileInstant` / `execRateInstant` 返回类型 `*promQueryData` → `[]promVectorSample`
  - 新增 `applyTopK()`（stable sort + top/bottom K 截断）
  - 新增 `parseVectorValue()` 辅助函数（从 `Value[1]` 提取 float64）
  - 新增 `sort` import
  - `dispatchInstantQuery` 重构：rate 路径和普通路径汇合到统一后处理 pipeline（聚合 → topk → 包装）
- `extension/adminext/prometheus_handler_test.go` — `TestParseVectorValue`（5 个用例）、`TestApplyTopK`（7 个用例，含 topk/bottomk/边界/stable sort）

**测试结果**：全部 PASS，全量回归 0 失败

### Sprint 3 实施记录

**变更文件**：
- `extension/adminext/prometheus_handler.go`:
  - `handlePromQuery` / `handlePromQueryRange`: span 属性新增 `promql.top_k`（int）和 `promql.is_bottom_k`（bool）
  - `dispatchInstantQuery`: topk 步骤后追加 `promql.topk_count` span 属性
  - **Bugfix**（2025-07-21）: `applyAggregation` 修复聚合后 groupBy 标签丢失问题 — 新增 `filterMetricByKeys()` 在聚合后从 group 的第一个 vector 恢复 groupBy 标签
- `extension/adminext/prometheus_handler_test.go`: `TestApplyAggregation_PreservesGroupByLabels`（2 个用例）、`TestFilterMetricByKeys`

**Regression 根因**：`aggregateGroup` 返回 `promMetric{}`（空 metric），`stripMetricToGroupBy` 无法从空 metric 中取出 groupBy 标签。此问题在 Sprint 2 之前被掩盖（rate instant 路径直接 return 跳过聚合），Sprint 2 统一编排层后暴露。

**Span 属性矩阵**（最终态）：

| 属性 | 来源 | 示例 |
|------|------|------|
| `promql.expr` | handler 层 | `topk(5, sum(...))` |
| `promql.top_k` | handler 层 | `5` |
| `promql.is_bottom_k` | handler 层 | `false` |
| `promql.series_count` | dispatch 层（取数后） | `120` |
| `promql.aggregated_count` | dispatch 层（聚合后） | `15` |
| `promql.topk_count` | dispatch 层（topk 后） | `5` |

**测试结果**：全部 PASS，全量回归 0 失败
