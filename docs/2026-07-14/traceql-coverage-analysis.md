# TraceQL 支持度与覆盖范围分析

> 对照 [Grafana Tempo TraceQL 官方文档](https://grafana.com/docs/tempo/latest/traceql/construct-traceql-queries/) + [查询示例](https://grafana.com/docs/grafana/latest/datasources/tempo/query-editor/traceql-query-examples/)，分析当前实现的支持程度和缺失功能。

## 总体评价

当前实现了 TraceQL 的**核心子集**，覆盖了 Grafana 日常查询场景（Service Structure 视图、Search、Structural Query）。但缺失企业级功能（聚合/分组/指标查询）和部分标准语法（trace 作用域、event/link 作用域、nil 比较等），整体完成度约 **60-65%**。

---

## 1. Span 过滤器（Span Filters）— 支持度：高

### ✅ 已支持

| 特性 | 示例 | 支持 |
|------|------|------|
| 等式 `=` | `{ span.http.method = "GET" }` | ✅ |
| 不等 `!=` | `{ span.http.status_code != 200 }` | ✅ |
| 大于 `>` | `{ duration > 100ms }` | ✅ |
| 大于等于 `>=` | `{ span.http.status_code >= 200 }` | ✅ |
| 小于 `<` | `{ span.http.status_code < 500 }` | ✅ |
| 小于等于 `<=` | `{ span.duration <= 100ms }` | ✅ |
| 正则匹配 `=~` | `{ span.db.system =~ "postgresql\|mysql" }` | ✅ |
| AND 组合（span 内） | `{ status = error && duration > 1s }` | ✅ |
| OR 组合（括号） | `{ (kind=server \|\| kind=client) }` | ✅ |
| 作用域前缀 `resource.` | `{ resource.service.name = "api" }` | ✅ |
| 作用域前缀 `span.` | `{ span.http.method = "GET" }` | ✅ |
| 无前缀（默认 span） | `{ name = "HTTP GET" }` | ✅ |
| 内在字段: `name` | `{ name = "POST /api" }` | ✅ |
| 内在字段: `status` | `{ status = error }` | ✅ |
| 内在字段: `kind` | `{ kind = server }` | ✅ |
| 内在字段: `duration` | `{ duration > 100ms }` | ✅ |

### ❌ 未支持

| 特性 | 示例 | 缺失说明 |
|------|------|---------|
| 正则不匹配 `!~` | `{ span.http.method !~ "DELETE" }` | 词法分析器无 `TokenNotRegex`，parser 无对应处理 |
| nil 比较 | `{ span.optional_field = nil }` | 无 `nil` 关键词解析 |
| span 内在字段前缀 `span:` | `{ span:name = "..." }` | 只支持 `name`（无 scope）或 `span.name`（点号），不支持标准冒号前缀 |
| 属性名中带空格/特殊字符 | `{ span."attribute name" = "val" }` | 不支持引号包裹属性名 |

---

## 2. 作用域（Scopes）— 支持度：中

### ✅ 已支持

| 作用域 | 示例 | 支持 |
|--------|------|------|
| `span.*` | `{ span.http.status_code = 200 }` | ✅ |
| `resource.*` | `{ resource.service.name = "api" }` | ✅ |
| 无 scope（span 默认） | `{ name = "foo" }`, `{ http.method = "GET" }` | ✅ |

### ❌ 未支持

| 作用域 | 示例 | 缺失说明 |
|--------|------|---------|
| `trace:*` | `{ trace:duration > 100ms }` | 无 `trace:` 前缀解析，不支持 trace 级内在字段 |
| `event:*` | `{ event:name = "exception" }` | 完全不支持 event/link/instrumentation 作用域 |
| `link:*` | `{ link:traceID = "abc..." }` | 同上 |
| `instrumentation:*` | `{ instrumentation:name = "grpc" }` | 同上 |

### 内在字段对照

| 标准字段 | 当前实现 | 差距 |
|---------|---------|------|
| `name` | ✅ `name` | 作为无 scope 内在字段，基本可用 |
| `duration` | ✅ `duration` | 基本可用 |
| `status` | ✅ `status` | 基本可用 |
| `kind` | ✅ `kind` | 基本可用 |
| `span:id` | ❌ | 完全未实现 |
| `span:parentID` | ❌ | 完全未实现 |
| `span:childCount` | ❌ | 完全未实现 |
| `span:statusMessage` | ❌ | 完全未实现 |
| `trace:duration` | ❌ (有 `traceDuration` 但不通过 parser) | parser 不支持 `trace:` 前缀 |
| `trace:rootName` | ❌ (有 `rootName` 但只用于 ES pushdown) | parser 不支持 `trace:` 前缀 |
| `trace:rootService` | ❌ (有 `rootServiceName` 但只用于 ES pushdown) | 同上 |
| `trace:id` | ❌ | 完全未实现 |

---

## 3. 逻辑操作符（跨 Spanset）— 支持度：中

### ✅ 已支持

| 操作符 | 示例 | 支持 |
|--------|------|------|
| `&&`（不同 spanset） | `{ resource.service.name = "A" } && { status = error }` | ✅ |
| `\|\|`（不同 spanset） | `{ resource.service.name = "A" } \|\| { resource.service.name = "B" }` | ✅ |

> 注意：Grafana 用 `{...} && {...}` 表示在两个不同 span 上满足条件，当前实现将 `&&` 两边分别评估。但实际行为是否正确（是否允许在不同 span 上分别匹配）取决于 evaluator 实现。

---

## 4. 结构操作符（Structural Operators）— 支持度：高

### ✅ 已支持

| 操作符 | 含义 | 支持 |
|--------|------|------|
| `>>` | 后代 | ✅ |
| `>` | 直接子节点 | ✅ |
| `~` | 兄弟节点 | ✅ |
| `&>>` | 后代（返回双方） | ✅ |
| `!>` | 非直接子 | ✅ |
| `!>>` | 非后代 | ✅ |

### ❌ 未支持

| 操作符 | 含义 | 缺失说明 |
|--------|------|---------|
| `<<` | 祖先 | 未在 parser structural 操作符中注册 |
| `<` | 直接父节点 | 与 `>` 冲突，需上下文区分 |
| `&<<` | 祖先（返回双方） | 同上 |
| `&>` | 直接子（返回双方） | 未注册 |
| `&<` | 直接父（返回双方） | 同上 |
| `&~` | 兄弟（返回双方） | 未注册 |
| `!<<` | 非祖先 | 实验性，未注册 |
| `!<` | 非直接父 | 实验性，未注册 |
| `!~` | 非兄弟 | 实验性，未注册 |

> **说明**：`<<`、`<`（作为父/祖先）与关系比较符 `<` 冲突，标准 TraceQL 根据上下文区分（在结构表达式中作为父操作符）。当前实现只支持自左向右的层级关系。

---

## 5. 管道与投射（Pipeline & Selection）— 支持度：中

### ✅ 已支持

| 特性 | 示例 | 支持 |
|------|------|------|
| `select()` 投射 | `{ status = error } \| select(name, service.name)` | ✅ |
| 管道连接 | `expr \| select(...)` | ✅ |
| 多管道阶段 | `expr \| select(f1) \| select(f2)` | ✅ |

### ❌ 未支持

| 特性 | 示例 | 缺失说明 |
|------|------|---------|
| `count()` | `{ status = error } \| count() > 3` | 无 `NodeAggregate` AST 节点 |
| `avg()` | `avg(duration) > 20ms` | 同上 |
| `max()` | `max(span.bytes)` | 同上 |
| `min()` | `min(duration)` | 同上 |
| `sum()` | `sum(span.bytesProcessed)` | 同上 |
| `by()` 分组 | `by(resource.service.name)` | 无 group-by 支持 |
| `rate()` 指标查询 | `{} \| rate()` | 完全未实现 Metrics 模式 |
| `count_over_time()` | `{...} \| count_over_time()` | 同上 |
| 标量过滤器 | `expr \| count() > 3` | 无 ScalarFilter AST 节点 |

> **关键缺口**：聚合/分组/指标查询是 TraceQL 区别于简单标签搜索的核心差异化能力。Grafana Metrics 模式完全依赖这些功能，当前实现不支持任何指标查询。

---

## 6. 数值类型与字面量 — 支持度：高

### ✅ 已支持

| 类型 | 示例 | 支持 |
|------|------|------|
| 整数 | `{ span.http.status_code = 200 }` | ✅ |
| 浮点数 | `{ span.value > 1.5 }` | ✅ |
| Duration 字符串 | `{ duration > 100ms }` | ✅ (解析为 duration 字符串) |
| 字符串 | `{ span.http.method = "GET" }` | ✅ |
| `true` / `false` | `{ nestedSetParent<0 && true }` | ✅ (TrueExpr) |

### ❌ 未支持

| 特性 | 示例 | 缺失说明 |
|------|------|---------|
| `nil` | `{ span.field = nil }` | 无 nil 字面量 |
| `minInt` / `maxInt` | `{ span.value != minInt }` | 无特殊常量 |
| 负 Duration | `{ event:timeSinceStart > -5s }` | 无负值 duration 解析 |
| 算术表达式 | `{ span.length > 10 * 1024 * 1024 }` | 完全不支持算术运算 |

---

## 7. 查询提示（Hints）— 不支持

| 特性 | 示例 | 支持 |
|------|------|------|
| `with (most_recent=true)` | `{} with (most_recent=true)` | ❌ 完全不支持 |

---

## 8. 数组支持 — 不支持

TraceQL 标准对数组属性的比较行为（`=` 匹配任意元素、`!=` 要求全部不匹配），当前无特殊处理。

---

## 9. ES Pushdown（Planner 优化）— 支持度：中

已下推到 ES 索引的条件：

| 条件类型 | 支持 |
|---------|------|
| `resource.service.name = "X"` | ✅ ES term query |
| `name = "X"` | ✅ ES term query |
| `status = error/ok/unset` | ✅ ES term query |
| `kind = server/client/...` | ✅ ES term query |
| `duration > X` | ✅ ES range query |
| 自定义 tag `span.X = Y` | ✅ ES term/nested query |
| `traceDuration > X` | ✅ ES range query (trace 级) |
| `rootName = "X"` | ✅ ES term query |
| `rootServiceName = "X"` | ✅ ES term query |
| `isRoot = true` (via `nestedSetParent<0`) | ✅ ES term query |
| OR 分组推送到 ES | ✅ 部分支持 (TagsOr) |
| 结构算子条件 | ⚠️ 放宽策略（清除 SpanKind/Status），不做精确 pushdown |

---

## 10. 综合评估矩阵

| 功能模块 | 完成度 | 评价 |
|---------|--------|------|
| 基础 Span 过滤 | **90%** | 缺 `!~` 和 `nil`，其余完整 |
| 内在字段 | **55%** | 缺 `span:id/parentID/childCount/statusMessage`、`trace:*` |
| 作用域 | **40%** | 只支持 span/resource，缺 trace/event/link/instrumentation |
| 结构操作符 | **50%** | 支持 6/12 种，缺逆向操作符和 union 变体 |
| 逻辑组合 | **75%** | && 和 \|\| 均支持，但跨 spanset 行为待验证 |
| 管道/投射 | **30%** | 有 `select()` 但缺聚合/分组/标量过滤 |
| 聚合/指标 | **0%** | 完全未实现，这是最大缺口 |
| 数值/字面量 | **70%** | 缺 nil/minInt/maxInt/算术 |
| ES Pushdown | **70%** | 常见条件已覆盖，缺 event/link 过滤 |
| 查询提示 | **0%** | 不支持 `with (most_recent=true)` |

**总体完成度: ~60%**

---

## 11. 优先级建议

### 🔴 P0 — Grafana 功能受阻

| 项目 | 影响 |
|------|------|
| `event:*` 作用域基础支持 | 无法查询异常（`event.exception.message =~ "..."`） |
| 聚合 `count()/avg()/sum()` | 无法使用 Metrics 模式 |
| `!~` 正则不匹配 | 常见反向过滤不可用 |

### 🟡 P1 — 功能完整性

| 项目 | 影响 |
|------|------|
| `trace:*` 内在字段 | `trace:duration/rootName/rootService` 是高性能查询路径 |
| `span:id/parentID/childCount` | 查询特定 span / 高扇出 trace |
| `nil` 比较 | 检查属性缺失 |
| `<<` / `&~` 结构操作符 | Grafana Service Structure 的标准查询语法 |
| `!~` 非兄弟结构操作符 | 叶子 span / 级联错误分析 |

### 🟢 P2 — 锦上添花

| 项目 | 影响 |
|------|------|
| `by()` 分组 | 进阶聚合分析 |
| 算术表达式 | 语义化阈值 |
| `most_recent` 提示 | 最新数据优先 |
| 带引号属性名 | 兼容特殊命名 |
| `minInt/maxInt` | 边界值过滤 |
