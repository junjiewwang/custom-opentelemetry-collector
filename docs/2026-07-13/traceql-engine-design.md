# TraceQL 子集引擎 — 设计方案

## 1. 需求背景

### 1.1 问题描述

Grafana Explore 面板在使用 Tempo 数据源时，会发送高级 TraceQL 查询（结构化查询、Pipeline、Select），当前系统仅支持简单的 `{ .key = "value" }` 单花括号条件解析，无法处理这些高级语法，导致返回 400 错误。

### 1.2 典型报错

```
{"error":"invalid TraceQL query: TraceQL query must be wrapped in { }"}
```

### 1.3 触发场景

Grafana 发出的查询（URL decode 后）：
```
({nestedSetParent<0 && true } &>> { kind = server }) || ({nestedSetParent<0 && true }) 
| select(status, resource.service.name, name, nestedSetParent, nestedSetLeft, nestedSetRight)
```

该查询包含：
- **结构化查询操作符 `&>>`**：祖先操作符，`{A} &>> {B}` 表示 span A 是 span B 的祖先
- **OR 组合 `||`**：两个 span selector 之间的 OR 逻辑
- **Pipeline 操作符 `|`**：管道语法
- **`select()` 函数**：选择要返回的字段
- **`nestedSetParent<0`**：Tempo 内部表示，等价于"根 span"

### 1.4 当前限制

`parseTraceQL()` 函数仅做简单前后缀检查：
```go
if !strings.HasPrefix(raw, "{") || !strings.HasSuffix(raw, "}") {
    return nil, fmt.Errorf("TraceQL query must be wrapped in { }")
}
```

---

## 2. 方案设计：分层 TraceQL 引擎

### 2.1 架构总览

```
┌─────────────────────────────────────────────────┐
│                   Grafana                         │
└───────────────────────┬─────────────────────────┘
                        │ GET /api/search?q=...
                        ▼
┌─────────────────────────────────────────────────┐
│  Layer 1: TraceQL Parser (AST)                   │
│  输入: raw TraceQL string                        │
│  输出: AST (SpanSetFilter | Pipeline | Select)   │
│  职责: 纯解析，不执行                             │
└───────────────────────┬─────────────────────────┘
                        │
                        ▼
┌─────────────────────────────────────────────────┐
│  Layer 2: Query Planner                          │
│  输入: AST                                       │
│  输出: ExecutionPlan (可推给ES的部分 + 需内存处理的部分)│
│  职责: 确定哪些条件可推给ES，哪些需要后处理        │
└───────────────────────┬─────────────────────────┘
                        │
                        ▼
┌─────────────────────────────────────────────────┐
│  Layer 3: ES Query Builder                       │
│  输入: 可下推的条件                               │
│  输出: ES bool query                             │
│  职责: 已有 buildTraceSearchQuery (复用扩展)      │
└───────────────────────┬─────────────────────────┘
                        │
                        ▼
┌─────────────────────────────────────────────────┐
│  Layer 4: Post-Processor (内存引擎)              │
│  输入: ES 返回的 spans + 不可下推的 AST 节点      │
│  输出: 过滤/结构匹配后的 SpanSet                  │
│  职责: 结构操作符、select 投影、pipeline          │
└─────────────────────────────────────────────────┘
```

### 2.2 核心思想

**"能推就推，不能推就内存后处理"**：

1. **Parser** 把任意 TraceQL 解析为 AST
2. **Planner** 决定哪些条件下推给 ES（减少候选集）
3. **Post-processor** 对候选 trace 做结构匹配和字段投影

这样既不需要重写 Tempo 的 Parquet 列存引擎，又能在 ES 存储上实现 TraceQL 的核心语义。

---

## 3. 关键设计决策

### 3.1 AST 节点类型

```go
// traceql/ast.go

type NodeType int
const (
    NodeSpanFilter    NodeType = iota  // { conditions }
    NodeStructural                      // &>>, >>, >, ~
    NodePipeline                        // |
    NodeSelect                          // select(fields...)
    NodeOr                              // ||
    NodeAnd                             // &&
    NodeComparison                      // key op value
)

type Expr interface {
    Type() NodeType
}

// SpanFilter: { cond1 && cond2 }
type SpanFilter struct {
    Conditions []Condition
}

// Condition: key op value
type Condition struct {
    Scope    string  // "resource", "span", "" (intrinsic)
    Key      string  // "service.name", "kind", "status", "nestedSetParent"
    Operator string  // "=", "!=", "<", ">", ">=", "<=", "=~"
    Value    any     // string / int / float / bool / nil
}

// StructuralExpr: {A} &>> {B}  (A is ancestor of B)
type StructuralExpr struct {
    Left     Expr
    Right    Expr
    Operator string  // "&>>", ">>", ">", "~", "!>", "!>>"
}

// PipelineExpr: expr | stage1 | stage2
type PipelineExpr struct {
    Input  Expr
    Stages []PipelineStage
}

// SelectStage: select(field1, field2, ...)
type SelectStage struct {
    Fields []string
}

// OrExpr: exprA || exprB
type OrExpr struct {
    Left  Expr
    Right Expr
}
```

### 3.2 条件下推规则

| 条件类型 | 可下推到 ES？ | 说明 |
|----------|:---:|------|
| `resource.service.name = "X"` | ✅ | term query on `resource.service.name` |
| `kind = server` | ✅ | term query on `kind` |
| `status = error` | ✅ | term query on `status.code` |
| `name = "GET /api"` | ✅ | term query on `name` |
| `duration > 100ms` | ✅ | range query on `durationNano` |
| `.http.method = "GET"` | ✅ | term on `attributes.http.method` |
| `nestedSetParent < 0` | ⚠️ | 等价于 `parentSpanId = ""` (根 span) |
| `&>>` (structural) | ❌ | 需要拉出 trace 后内存判断 |
| `| select(...)` | ❌ | 后处理阶段做字段投影 |
| `||` (OR between span sets) | ⚠️ | 部分可推为 ES `bool.should` |

### 3.3 结构操作符实现策略

ES 里存了 `parentSpanId`，可以重建 span 树并在内存中做结构匹配：

```go
// 执行 {A} &>> {B} 的流程:
// 1. ES 层: 用 A 和 B 的条件合并为 should query，获取候选 trace IDs
// 2. 对每个 trace 拉取全量 span (GetTrace)
// 3. 内存中建树 (parentSpanId → children)
// 4. 对每个匹配 A 的 span，检查其后代中是否有匹配 B 的 span
```

性能优化：结构查询时 ES 查询用 `bool.should` 合并两端条件，只返回"可能"包含匹配结构的 trace，再内存精确过滤。

### 3.4 `nestedSetParent < 0` 的等价翻译

这是 Tempo 的内部表示（nested set model 中 parent=-1 表示根 span），在 ES 存储中等价于：

```go
// nestedSetParent < 0  →  parentSpanId 为空 (即根 span)
// 翻译为 ES: {"bool": {"must_not": [{"exists": {"field": "parentSpanId"}}]}}
// 或: {"term": {"parentSpanId": ""}}
```

---

## 4. 与现有代码的关系

| 现有代码 | 变化 |
|----------|------|
| `parseTraceQL()` | 保留作为简单路径 fallback（单花括号 AND 条件） |
| `parseTraceQLOrFilter()` | 被新 AST parser 的 `OrExpr` 替代 |
| `parseTempoSearchParams()` | 新增分支：检测高级语法 → 走新 parser |
| `TraceQuery` struct | 扩展：新增 `SpanKind`, `Status`, `IsRoot` 等 intrinsic 字段 |
| `buildTraceSearchQuery()` | 扩展：支持新增的 intrinsic 字段下推 |
| `SearchTraceSummaries()` | 保持不变（ES 查询层） |
| `handleTempoSearch()` | 结构查询时切换到 two-phase: ES 宽搜 + 内存精确过滤 |

### 4.1 新增包结构

```
extension/adminext/traceql/
├── ast.go          // AST 节点定义
├── lexer.go        // Tokenizer
├── parser.go       // 递归下降解析器
├── planner.go      // Query Planner (条件下推决策)
├── evaluator.go    // 内存后处理引擎 (结构匹配、select)
└── traceql_test.go // 单元测试
```

---

## 5. 实施路线图

### Sprint 4: TraceQL Parser (AST) + 降级兼容
**目标**: 解析完整 TraceQL 语法，不支持的部分降级为宽松搜索  
**预估**: 3-4 天

- [x] 新建 `extension/adminext/traceql/` 包
- [x] 实现 Lexer（tokenizer）: 识别 `{`, `}`, `(`, `)`, `|`, `||`, `&&`, `&>>`, `>>`, `>`, `~`, 比较运算符, 标识符, 字符串, 数字
- [x] 实现 Parser: 递归下降解析器，输出 AST
- [x] 实现 `ExtractPushdownFilters(ast) → TraceQuery.Tags + intrinsic filters`（即 `Plan()` 函数）
- [x] 修改 `parseTempoSearchParams`: 检测高级语法 → 调用新 parser → 提取可用条件
- [x] 不支持的部分优雅忽略（返回结果，不报错）

### Sprint 5: 结构操作符 + 内存引擎 ✅ 已完成
**目标**: 支持 `&>>`, `>>`, `>` 结构查询的精确求值  
**预估**: 3-4 天

- [x] 实现 `evaluator.go`：span tree builder（`BuildSpanTree`）、结构匹配器（`EvaluateStructural`）、条件匹配（`MatchSpanFilter`/`matchCondition`）
- [x] 支持 6 种结构操作符：`&>>`（祖先）、`>>`（后代）、`>`（子节点）、`~`（兄弟）、`!>`（非子节点）、`!>>`（非后代）
- [x] 修改 `handleTempoSearch` 流程：当 `plan.HasStructural=true` 时进入两阶段评估
- [x] Phase 1: ES 宽搜（复用现有 `SearchTraceSummaries`，计划器已将两侧条件合并为 should）
- [x] Phase 2: 逐 trace 获取全量 span（`GetTrace`）→ 构建 SpanTree → 结构匹配 → 过滤
- [x] 性能保护：`maxStructuralTraces = 50`（默认上限，超出时截断并记录日志）
- [x] 修改 `parseTempoSearchParams` 返回值：额外返回 `*traceql.ExecutionPlan`（携带 HasStructural/FullAST）
- [x] 辅助函数：`convertSpansToSpanData`、`spanKindToString`、`spanStatusToString`、`keyValueString`
- [x] 单元测试：15 个新测试（SpanTree 构建/关系、条件匹配、结构评估 4 种算子、Grafana 完整查询、helper 函数）
- [x] 全量测试：traceql 40+ PASS、adminext ALL PASS、observabilitystorageext ALL PASS

### 新增文件

| 文件 | 职责 |
|------|------|
| `extension/adminext/traceql/evaluator.go` | 内存后处理引擎：SpanData/SpanTree 构建、结构操作符匹配、条件评估 |

### 关键架构决策

**两阶段评估流程**：
```
handleTempoSearch
  ├─ parseTempoSearchParams → plan + query
  ├─ plan.HasStructural? 
  │    ├─ NO  → SearchTraceSummaries → 直接返回（原有路径）
  │    └─ YES → Phase 1: SearchTraceSummaries（ES 宽搜，计划器已放宽条件）
  │              └─ Phase 2: 逐 trace GetTrace → BuildSpanTree → EvaluateStructural
  │                   └─ 过滤 → 返回验证通过的 traces（上限 maxStructuralTraces=50）
  └─ 构建 Tempo 响应
```

**SpanTree 实现**：基于 `parentSpanID` 的单向链表向上查找，避免递归构建完整的 n 叉树

**条件匹配**：处理 "=" 和 "!=" 字符串匹配、"<" ">" 等数值匹配、`nestedSetParent<0` 等价翻译为 `ParentSpanID=""`

### Sprint 6: Select + Pipeline + SpanSet 语义 ✅ 已完成
**目标**: 支持 `| select(...)` 投影，返回 Grafana 期望的 SpanSet 结构  
**预估**: 2-3 天

- [x] 实现 `projectSpanWithSelect()` / `resolveSelectField()`：从 span 中仅返回 select 指定的字段
- [x] 支持系统字段投影：`name`, `kind`, `status`, `status.code`, `status.message`, `duration`, `resource.service.name`, `span.X`/`.X` 属性
- [x] 增强 `structuralPostFilter`：返回 `structuralVerifyResult`（含 matchedSpanIDs + fullSpans）
- [x] 新增 `convertStructuralResultToTempoSearchTrace()`：使用 matched span IDs 构建精确的 spanSet
- [x] 实现 SpanSet 语义：`tempoSpanSet.Matched` 反射实际匹配的 span 数量（不再是 `len(searchSpans)`）
- [x] 修改 `convertTraceSummaryToTempoSearchTrace`：接受 `selectFields` 参数，应用 select 投影
- [x] 修改 `handleTempoSearch` 两个分支：非 structural 传递 selectFields，structural 使用新转换器
- [x] 单元测试：7 个 select projection 测试 + 全量 adminext/traceql/observabilitystorageext PASS
- [x] 全量测试：adminext 全 PASS、traceql 42+ PASS、observabilitystorageext 全 PASS

### Select 投影支持字段

| select 字段 | 解析来源 | 示例 |
|------------|---------|------|
| `name` | `span.Name` | `"GET /api/v1"` |
| `kind` | `span.Kind` | `"SPAN_KIND_SERVER"` |
| `status` | `span.Status.Code` | `"STATUS_CODE_OK"` |
| `status.code` | `span.Status.Code` | `"STATUS_CODE_ERROR"` |
| `status.message` | `span.Status.Message` | `"something went wrong"` |
| `duration` | `span.DurationNano` | `"150000000"` |
| `resource.service.name` | `span.ServiceName` | `"tapm-api"` |
| `resource.X` | `span.Resource[X]` | 资源属性值 |
| `span.X` / `.X` | `span.Attributes[X]` | span 属性值 |
| 裸字段名 `X` | 先查 Attributes，再查 Resource | 属性值 |

### SpanSet 语义变更

**之前**：所有 spans 都在单个 spanSet 中，`Matched = len(searchSpans)`
**之后**：
- 非 structural 查询：保持不变（所有 spanSet 中的 spans 都被视为 matched）
- Structural 查询：`Matched = len(matchedSpanIDs)`（实际结构匹配到的 span 数量）

---

## 6. 风险与约束

### 6.1 性能风险

- **结构查询需拉取全量 trace**：当匹配的 trace 过多时，逐个 GetTrace 开销大
- **缓解措施**：设置精确评估上限（如最多评估 100 个 trace），超出时返回近似结果

### 6.2 语义完整性

- **不支持的高级特性**：`rate()`、`count_over_time()`、`avg_over_time()` 等时序聚合（属于 TraceQL Metrics，已通过单独的 query_range 端点处理）
- **Span Set 语义简化**：暂不支持 span set 的交集/差集操作

### 6.3 ES 存储限制

- ES 不支持 Parquet 的列式裁剪和 nested set 模型优化
- 但 `parentSpanId` 提供了等价的关系推断能力

---

## 7. 总结

| 维度 | 说明 |
|------|------|
| **总工作量** | ~8-11 天，分 3 个 Sprint |
| **核心收益** | Grafana Tempo 数据源高级功能完整可用（结构查询、服务拓扑图等） |
| **架构原则** | 分层解耦（Parser → Planner → ES Builder → Post-processor） |
| **兼容策略** | 简单查询走原有路径 fallback，高级查询走新引擎 |

---

## 8. Sprint 4 实施记录

### 8.1 完成内容

Sprint 4 已实施完成，包括以下工作：

#### 新增 `extension/adminext/traceql/` 包

| 文件 | 职责 |
|------|------|
| `ast.go` | AST 节点定义（SpanFilter, StructuralExpr, OrExpr, PipelineExpr, SelectStage, Condition） |
| `lexer.go` | Tokenizer：支持 `{}`, `()`, `||`, `&&`, `&>>`, `>>`, `>`, `~`, `!>`, `!>>`, `|`, 比较运算符, 标识符, 字符串, 数字/duration |
| `parser.go` | 递归下降解析器：parseTopLevel → parseStructural → parsePrimary → parseSpanFilter → parseCondition → parsePipeline |
| `planner.go` | Query Planner：条件下推提取 + `IsAdvancedQuery()` 快速判断 |
| `traceql_test.go` | 17 个单元测试覆盖 Lexer、Parser、Planner、IsAdvancedQuery |

#### 扩展存储层

| 文件 | 改动 |
|------|------|
| `storedmodel/trace_query.go` | `TraceQuery` 新增 `SpanKind`, `Status`, `IsRoot` 字段 |
| `observabilitystorageext/types.go` | 公共 `TraceQuery` 同步新增字段 |
| `reader_adapter.go` | `SearchTraces` + `SearchTraceSummaries` 传递新字段 |
| `provider/elasticsearch/trace_reader.go` | `buildTraceSearchQuery` 支持 `kind`, `status.code`, `parentSpanId=""` 过滤 |

#### 集成 Handler

| 文件 | 改动 |
|------|------|
| `extension/adminext/tempo_handler.go` | `parseTempoSearchParams` 检测高级语法 → 调用新 parser → 提取可用条件；解析失败时优雅降级到旧解析器 |

### 8.2 兼容策略

- **简单查询**（`{ .key = "value" }`）：走原有 `parseTraceQLOrFilter` 路径，零行为变更
- **高级查询**（含 `&>>`, `>>`, `| select`, `nestedSetParent`, 多花括号）：走新 AST parser + planner
- **解析失败**：自动降级到旧解析器（graceful degradation）

### 8.3 验证结果

- ✅ 全项目编译通过 (`go build ./...`)
- ✅ 17 个新增 traceql 包测试全部通过
- ✅ 所有现有测试无回归

### 8.4 已知限制（后续 Sprint 解决）

- 结构操作符 (`&>>`, `>>`) 仅做条件宽搜推给 ES，精确结构匹配待 Sprint 5 实现
- `| select()` 字段投影解析完成，实际返回字段裁剪待 Sprint 6 实现
- `rate()`, `histogram_over_time()`, `quantile_over_time()` 已支持实时聚合（通过 /api/metrics/query_range 端点 → TraceReader.QueryTraceMetrics）

---

## 变更记录

| 日期 | 变更内容 | 状态 |
|------|----------|------|
| 2026-07-13 | 初始方案设计，完成架构设计和路线图规划 | ✅ 已完成 |
| 2026-07-14 | Sprint 6 实施：Select + Pipeline + SpanSet 语义（projectSpanWithSelect + structuralVerifyResult + Matched 计数） | ✅ 已完成 |
| 2026-07-14 | 修复 TraceQL Metrics 查询空数据 bug：date_histogram 单位从毫秒改为纳秒、isRoot 改用 parentSpanId 判断、status 改用 status.code + capitalizeFirst、SpanKind 加 capitalizeFirst | ✅ 已完成 |
| 2026-07-14 | 修复 /api/metrics/query_range 路由：Grafana 调用此端点进行 metrics 查询，改为优先走 AST parser + TraceReader 实时聚合，修复 extractMetricValue JSON 解析 bug，修复 histogram_over_time 使用错误的 ES aggregation 类型 | ✅ 已完成 |
