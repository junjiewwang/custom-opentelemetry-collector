# TraceQL 括号分组 OR 支持 — 技术方案

## 需求背景

### 问题描述

Grafana Tempo UI 生成的查询 `{(kind="internal" || kind="server") && resource.service.name="tapm-api"}` 返回空结果。

URL：
```
/api/search?q={(kind="internal" || kind="server") && resource.service.name="tapm-api"}&limit=20&start=1783390550&end=1783995350
```

### 根因分析

该查询在 **SpanFilter 内部** 使用了 `(condA || condB) && condC` 的括号分组 OR 语法。当前代码的两条解析路径都无法正确处理：

| 路径 | 结果 | 原因 |
|------|------|------|
| Advanced (AST Parser) | `IsAdvancedQuery` 返回 `false`，不进入 | 启发式判断只检查多花括号和结构操作符，不识别内部 `||` |
| Advanced (强制调用) | `Parse()` 报错：`expected identifier at position 1, got "("` | Parser 在 `parseSpanFilter()` 内不支持括号 |
| Legacy (parseTraceQLOrFilter) | 生成错误 tag: key=`(kind` | `splitTopLevelOr` 找不到顶层 `||`（在 depth=1 内），最终 `parseTraceQLToken` 把 `(kind="internal" || kind="server")` 当成单个 token 解析 |

### 期望行为

查询应正确解析为：
- AND 条件：`service.name = "tapm-api"`
- OR 子组：`[{kind: "internal"}, {kind: "server"}]`

对应 ES 查询：
```json
{
  "bool": {
    "filter": [
      {"term": {"resource.service.name": "tapm-api"}},
      {"bool": {"should": [
        {"term": {"kind": "Internal"}},
        {"term": {"kind": "Server"}}
      ], "minimum_should_match": 1}}
    ]
  }
}
```

---

## 现有架构分析

### 文件结构

```
extension/adminext/traceql/
├── ast.go        # AST 节点类型定义
├── lexer.go      # 词法分析器 (Tokenizer)
├── parser.go     # 递归下降解析器 (Tokens → AST)
├── planner.go    # 查询计划器 (AST → ES 可下推条件)
└── *_test.go     # 测试
```

### Lexer 现状

Lexer **已完整支持括号**：
- `(` → `TokenLParen`
- `)` → `TokenRParen`

无需修改 Lexer。

### Parser 现状

```
TopLevel   = Structural { "||" Structural }
Structural = Primary { structOp Primary }
Primary    = SpanFilter | "(" TopLevel ")"     ← 括号仅在顶层表达式分组
SpanFilter = "{" { Condition "&&"? } "}"       ← 内部只支持 AND
Condition  = Ident Operator Value
```

关键限制：`parseSpanFilter()` 中遍历 `{...}` 内容时，遇到 `TokenLParen` 会在 `parseCondition()` 报错（期望 `TokenIdent`）。

### AST 节点现状

```go
type SpanFilter struct {
    Conditions []Condition  // 纯 AND 列表
}
```

`SpanFilter.Conditions` 是扁平的 `[]Condition`，没有能力表达内部的 OR 关系。

### Planner 现状

```go
type ExecutionPlan struct {
    Tags    map[string]string      // AND 条件
    TagsOr  []map[string]string    // OR 组
    // ... intrinsic fields
}
```

Planner 已有 `TagsOr` 字段和 `extractOrConditions()` 方法来处理 OR 条件。当前只在**顶层 `OrExpr`** 时触发。

### IsAdvancedQuery 现状

```go
func IsAdvancedQuery(raw string) bool {
    // 1. 检查结构操作符 (&>>, >>, | select, ...)
    // 2. 检查花括号数量 > 1
    // 不检查 || 是否存在于花括号内部
}
```

---

## 技术方案

### 设计目标

1. AST Parser 能正确解析 `{(condA || condB) && condC}` 这类括号分组 OR 语法
2. Planner 能从中正确提取 AND 条件和 OR 子组
3. `IsAdvancedQuery` 能识别此类查询并路由到 Advanced path
4. 保持向后兼容，不影响现有简单查询的解析

### 设计原则

- **最小侵入**：尽量复用现有基础设施（Lexer 已支持括号，Planner 已支持 TagsOr）
- **正交扩展**：新增 AST 节点类型表达 SpanFilter 内的条件逻辑
- **渐进式**：优先支持最常见的模式（平坦 OR 组），保留扩展空间

---

### 改动 1：AST 节点扩展 (`ast.go`)

新增 `ConditionExpr` 接口和 `OrConditionGroup` 节点，用于表达 SpanFilter 内部的 OR 关系。

#### 方案：引入 `FilterExpr` 抽象

当前 `SpanFilter.Conditions []Condition` 只能表达 AND 列表。改为：

```go
// FilterExpr represents an expression inside a SpanFilter.
// It can be a single Condition (leaf) or an OrConditionGroup (branch).
type FilterExpr interface {
    filterExpr() // marker method
}

// SpanFilter represents a span selector with mixed AND/OR conditions.
type SpanFilter struct {
    Filters []FilterExpr  // AND 关系的多个 FilterExpr
}

// SingleCondition wraps a Condition as a FilterExpr leaf node.
type SingleCondition struct {
    Condition
}
func (SingleCondition) filterExpr() {}

// OrConditionGroup represents (condA || condB || condC) inside a SpanFilter.
// Each element is itself a list of AND conditions (for nested cases like (a && b || c && d)).
type OrConditionGroup struct {
    Groups [][]Condition  // 每个 group 内部是 AND 关系，groups 之间是 OR
}
func (OrConditionGroup) filterExpr() {}
```

**向后兼容**：纯 AND 条件的 SpanFilter 其 `Filters` 全部是 `SingleCondition`。

#### 替代方案（更简洁，推荐）

保持 `Conditions []Condition` 不变用于表达顶层 AND 条件，新增一个 OR 组字段：

```go
type SpanFilter struct {
    Conditions []Condition           // 顶层 AND 条件 (如 resource.service.name="tapm-api")
    OrGroups   [][]Condition         // 括号 OR 组 (如 [(kind="internal"), (kind="server")])
}
```

每个 `OrGroups` 元素表示一个 `(... || ...)` 括号组，组内每个 `[]Condition` 是一个 OR 分支（分支内条件为 AND）。

**选择此方案**，理由：
- 改动最小，不引入新接口
- 与 Planner 的 `TagsOr []map[string]string` 天然对齐
- 简单查询完全无感知（`OrGroups` 为 nil）

---

### 改动 2：Parser 增强 (`parser.go`)

修改 `parseSpanFilter()` 方法，在花括号内部遇到 `TokenLParen` 时，进入括号 OR 组解析：

```go
func (p *Parser) parseSpanFilter() (Expr, error) {
    p.advance() // consume {

    var conditions []Condition
    var orGroups [][]Condition

    for p.peek().Type != TokenRBrace && p.peek().Type != TokenEOF {
        // Handle "true" literal
        if p.peek().Type == TokenTrue {
            p.advance()
            if p.peek().Type == TokenAnd {
                p.advance()
            }
            continue
        }

        // NEW: Handle parenthesized OR group
        if p.peek().Type == TokenLParen {
            group, err := p.parseOrGroup()
            if err != nil {
                return nil, err
            }
            orGroups = append(orGroups, group...)
            // Consume optional && after group
            if p.peek().Type == TokenAnd {
                p.advance()
            }
            continue
        }

        cond, err := p.parseCondition()
        if err != nil {
            return nil, err
        }
        conditions = append(conditions, cond)

        if p.peek().Type == TokenAnd {
            p.advance()
        }
    }

    if p.peek().Type != TokenRBrace {
        return nil, fmt.Errorf("expected '}' at position %d", p.peek().Pos)
    }
    p.advance() // consume }

    return &SpanFilter{Conditions: conditions, OrGroups: orGroups}, nil
}

// parseOrGroup parses: ( cond1 || cond2 || cond3 )
// Returns a slice of condition slices (each OR branch may have multiple AND conditions).
func (p *Parser) parseOrGroup() ([][]Condition, error) {
    p.advance() // consume (

    var groups [][]Condition
    var currentGroup []Condition

    for p.peek().Type != TokenRParen && p.peek().Type != TokenEOF {
        if p.peek().Type == TokenOr {
            // End of current OR branch, start new one
            p.advance()
            if len(currentGroup) > 0 {
                groups = append(groups, currentGroup)
                currentGroup = nil
            }
            continue
        }

        if p.peek().Type == TokenAnd {
            p.advance() // consume && within a branch
            continue
        }

        cond, err := p.parseCondition()
        if err != nil {
            return nil, err
        }
        currentGroup = append(currentGroup, cond)
    }

    // Don't forget the last group
    if len(currentGroup) > 0 {
        groups = append(groups, currentGroup)
    }

    if p.peek().Type != TokenRParen {
        return nil, fmt.Errorf("expected ')' at position %d", p.peek().Pos)
    }
    p.advance() // consume )

    return groups, nil
}
```

---

### 改动 3：IsAdvancedQuery 增强 (`planner.go`)

在现有检查之后，增加对花括号内部 `||` 的检测：

```go
func IsAdvancedQuery(raw string) bool {
    // ... existing checks ...

    // NEW: Check for || inside braces (parenthesized OR within span filter).
    // e.g., {(kind="internal" || kind="server") && resource.service.name="tapm-api"}
    if braceCount == 1 && containsUnquoted(raw, "||") {
        return true
    }

    return false
}
```

逻辑：单花括号查询中如果包含 `||`（且不在引号内），则走 Advanced path。

---

### 改动 4：Planner 增强 (`planner.go`)

修改 `extractFromSpanFilter()` 以处理 `OrGroups`：

```go
func (p *ExecutionPlan) extractFromSpanFilter(sf *SpanFilter) {
    // 提取 AND 条件（与现有逻辑一致）
    for _, cond := range sf.Conditions {
        p.extractCondition(cond)
    }

    // NEW: 提取 OR 组
    for _, orGroup := range sf.OrGroups {
        // 每个 orGroup 是 [][]Condition，每个分支的等值条件提取为 TagsOr
        var tagsOrGroups []map[string]string
        for _, branch := range orGroup {
            group := make(map[string]string)
            for _, cond := range branch {
                if cond.Operator == "=" {
                    valStr := condValueToString(cond.Value)
                    if valStr != "" {
                        // 处理 scope
                        key := cond.Key
                        if cond.Scope == "resource" || cond.Scope == "span" {
                            // 保持原始 key（不加 scope 前缀），因为下游
                            // intrinsicTermClause 和 Tags 遍历都用 bare key
                        }
                        group[key] = valStr
                    }
                }
            }
            if len(group) > 0 {
                tagsOrGroups = append(tagsOrGroups, group)
            }
        }
        if len(tagsOrGroups) > 0 {
            p.TagsOr = append(p.TagsOr, tagsOrGroups...)
        }
    }
}
```

---

### 改动 5：SpanFilter.String() 更新 (`ast.go`)

更新 `String()` 方法以正确序列化包含 OR 组的 SpanFilter：

```go
func (s *SpanFilter) String() string {
    var parts []string
    for _, c := range s.Conditions {
        parts = append(parts, c.String())
    }
    for _, orGroup := range s.OrGroups {
        var orParts []string
        for _, branch := range orGroup {
            var branchParts []string
            for _, c := range branch {
                branchParts = append(branchParts, c.String())
            }
            orParts = append(orParts, strings.Join(branchParts, " && "))
        }
        parts = append(parts, "("+strings.Join(orParts, " || ")+")")
    }
    return "{ " + strings.Join(parts, " && ") + " }"
}
```

---

### 改动 6：不需要改动 Lexer

Lexer 已经完整支持 `(` `)`、`||`、`&&` 的词法化，无需任何修改。

---

## 验证计划

### 单元测试用例

| 查询 | 期望 ServiceName | 期望 TagsOr | 期望 Tags |
|------|-----------------|-------------|-----------|
| `{(kind="internal" \|\| kind="server") && resource.service.name="tapm-api"}` | `tapm-api` | `[{kind: internal}, {kind: server}]` | `{}` |
| `{(name="/api/a" \|\| name="/api/b") && resource.service.name="my-svc" && span.http.method="GET"}` | `my-svc` | `[{name: /api/a}, {name: /api/b}]` | `{http.method: GET}` |
| `{(status="error" \|\| status="unset")}` | `` | `[{status: error}, {status: unset}]` | `{}` |
| `{resource.service.name="svc" && (kind="server" \|\| kind="client") && (status="error" \|\| status="ok")}` | `svc` | `[{kind: server}, {kind: client}, {status: error}, {status: ok}]` | `{}` |

注意最后一个用例：两个独立的 OR 组，需要分别作为独立的 should 子句。设计上需要将 `OrGroups` 改为 `[][]Condition`（单个 OR 组）或 `[][][]Condition`（多个 OR 组），具体见下方讨论。

### 多 OR 组的表达

对于 `{(a || b) && (c || d)}`，语义是：
- `(a OR b)` AND `(c OR d)`

这在 ES 中需要两个独立的 `bool.should` 子句，都放在 `bool.filter` 中。

因此 `SpanFilter.OrGroups` 应该是 **`[][][]Condition`**（外层是多个 OR 组，每个 OR 组是 `[][]Condition`）:

```go
type SpanFilter struct {
    Conditions []Condition      // AND 条件
    OrGroups   [][][]Condition  // 多个独立的 OR 组，组间 AND，组内分支间 OR
}
```

这样 `{(a || b) && (c || d)}` 解析为：
```
OrGroups = [
    [[a], [b]],    // 第一个 OR 组
    [[c], [d]],    // 第二个 OR 组
]
```

### ES 查询生成验证

对 `{(kind="internal" || kind="server") && resource.service.name="tapm-api"}` 最终应生成：
```json
{
  "bool": {
    "filter": [
      {"term": {"resource.service.name": "tapm-api"}},
      {"bool": {"should": [
        {"term": {"kind": "Internal"}},
        {"term": {"kind": "Server"}}
      ], "minimum_should_match": 1}}
    ]
  }
}
```

---

## 实施步骤

### Sprint 1：核心 Parser 支持 ✅ 已完成
- [x] 修改 `ast.go`：`SpanFilter` 增加 `OrGroups [][][]Condition` 字段，更新 `String()` 方法
- [x] 修改 `parser.go`：`parseSpanFilter()` 支持括号 OR 组，新增 `parseOrGroup()`
- [x] 修改 `planner.go`：`IsAdvancedQuery` 识别含 `||` 的单花括号查询
- [x] 修改 `planner.go`：`extractFromSpanFilter()` 处理 `OrGroups`，映射到 `TagsOr`
- [x] 单元测试：Parser（5 用例）、Planner（3 用例）、IsAdvancedQuery（3 用例）、错误用例（2 用例）
- [x] 全量测试验证：traceql 25 PASS、adminext 全 PASS、observabilitystorageext 全 PASS、全项目编译通过

### 实际实施细节

**`ast.go` 改动**：
- `SpanFilter.OrGroups [][][]Condition`：外层 OR 组之间 AND 关系，中层每个分支之间 OR 关系，内层分支内条件 AND 关系
- `parseOrGroup()` 返回 `[][]Condition`（单个 OR 组），`SpanFilter.OrGroups` 是其列表

**`parser.go` 改动**：
- `parseSpanFilter()` 在遇到 `TokenLParen` 时调用 `parseOrGroup()`
- `parseOrGroup()` 按 `||` 拆分 OR 分支，每个分支内按 `&&` 连接条件

**`IsAdvancedQuery` 改动**：
- 单花括号查询中包含 `||`（非引号内）→ 返回 `true`，路由到 AST Parser

### Sprint 2：ES 查询层联动 ✅ 已完成
- [x] 修改 `types.go` + `storedmodel/trace_query.go`：`TagsOr` 从 `[]map[string]string` 升级为 `[][]map[string]string`（外层 OR 组之间 AND 关系，内层 map 之间 OR 关系）
- [x] 修改 `reader_adapter.go`：类型跟随调整，无需逻辑变更
- [x] 修改 `planner.go`：`ExecutionPlan.TagsOr` 类型升级；`extractOrConditions` 包裹单组；`extractFromSpanFilter` 每个 OrGroup 独立成组
- [x] 修改 `tempo_handler.go`：`parseTraceQLOrFilter` 返回类型升级为 `[][]map[string]string`，扁平结果包裹为单组；`traceqlMetricsQuery.TagsOr` 类型跟随
- [x] 修改 `trace_reader.go`：嵌套循环遍历 TagsOr 分组，每组独立生成 `bool.should` 子句
- [x] 更新所有测试 + 全量测试通过（traceql 25 PASS、adminext ALL PASS、observabilitystorageext ALL PASS）

### 类型升级核心设计

**之前**：`TagsOr []map[string]string` — 扁平列表，所有 OR 分支混在一起
```
{A} || {B}  → TagsOr: [{A}, {B}]              // 1 个 should block ✓
{(A||B) && (C||D)} → TagsOr: [{A},{B},{C},{D}]  // 错误的4选1语义 ✗
```

**之后**：`TagsOr [][]map[string]string` — 分组结构，组间 AND，组内 OR
```
{A} || {B}  → TagsOr: [[{A}, {B}]]              // 1 个 should block: (A||B) ✓
{(A||B) && (C||D)} → TagsOr: [[{A},{B}], [{C},{D}]]  // 2 个 should block: (A||B) AND (C||D) ✓
```

**ES 查询生成**：每个 TagsOr 组生成一个独立的 `bool.should`（min_should_match=1）：
```json
{
  "bool": {
    "must": [
      {"bool": {"should": [A, B], "minimum_should_match": 1}},
      {"bool": {"should": [C, D], "minimum_should_match": 1}}
    ]
  }
}
```

### Sprint 3：清理与优化 ✅ 已完成
- [x] 评估 legacy parser `parseTraceQLOrFilter` 的 `||` 处理：保留。原因：(1) AST parser 失败时的降级 fallback；(2) metrics 查询路径 `parseTraceQLMetricsQuery` 直接调用
- [x] `SpanFilter.String()` 方法已在 Sprint 1 完成
- [x] 补充多 OR 组边界测试：`TestParseMultipleOrGroups`（解析2组+校验所有组）、`TestPlanMultipleOrGroups`（计划2组各生 TagsOr 组）、`TestParseOrGroupWithAttributes`（属性字段 OR）、`TestStringRoundTrip`（String 可重解析，3 用例）
- [x] 全量测试：traceql 29 PASS、adminext ALL PASS、observabilitystorageext ALL PASS

---

## 影响范围

| 文件 | 改动类型 | 改动量 |
|------|---------|--------|
| `extension/adminext/traceql/ast.go` | `SpanFilter.OrGroups` 字段 + `String()` | 中 |
| `extension/adminext/traceql/parser.go` | `parseOrGroup()` + `parseSpanFilter()` 支持括号 | 中 |
| `extension/adminext/traceql/planner.go` | `IsAdvancedQuery` + `extractFromSpanFilter` + `extractOrConditions` + TagsOr 类型 | 中 |
| `extension/adminext/traceql/traceql_test.go` | 新增 10+ 测试用例 | 中 |
| `extension/adminext/tempo_handler.go` | `parseTraceQLOrFilter` 返回到分组格式 + `traceqlMetricsQuery.TagsOr` 类型 | 小 |
| `extension/.../types.go` | `TraceQuery.TagsOr` 类型：`[]map` → `[][]map` | 小 |
| `extension/.../storedmodel/trace_query.go` | `TraceQuery.TagsOr` 类型：`[]map` → `[][]map` | 小 |
| `extension/.../reader_adapter.go` | 类型跟随（无逻辑变更） | 小 |
| `extension/.../elasticsearch/trace_reader.go` | `buildTraceSearchQuery` 嵌套循环分组 should blocks | 中 |

---

## 风险评估

| 风险 | 影响 | 缓解措施 |
|------|------|---------|
| 现有简单查询行为变更 | 高 | `OrGroups` 为 nil 时完全兼容原有逻辑 |
| `IsAdvancedQuery` 误判 | 中 | 只对含 `||`（非引号内）的单花括号查询触发 |
| 嵌套括号 `((a || b) || c)` | 低 | 第一版不支持嵌套括号，遇到时报 parse error |
| TagsOr 多组扩展影响 ES 查询 | 中 | ✅ 已验证：`buildTraceSearchQuery` 支持分组 `[][]map`，每组独立 `bool.should` |

---

## 遗留事项

- [ ] 嵌套括号支持（`((a || b) || c)`）— 后续迭代
- [ ] 括号内混合 AND+OR 不加括号（如 `{a || b && c}`，优先级歧义）— 暂不支持，要求显式括号
- [ ] 正式对齐 Grafana Tempo 官方 TraceQL 语法规范
