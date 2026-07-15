# TraceQL AttributeResolver 统一属性映射层设计

> 文档状态：Sprint 1 ✅ / Sprint 2 ✅ / Sprint 3 ✅  
> 创建时间：2026-07-15  
> 最后更新：2026-07-15  
> 关联代码：`extension/observabilitystorageext/provider/elasticsearch/`, `extension/adminext/traceql/`

---

## 1. 问题背景

### 1.1 核心问题

TraceQL 查询中，`by()` 分组标签与 `filter` 条件走了不同的字段映射路径，导致带 scope 前缀的字段（如 `resource.service.name`、`span.name`）在 `by()` 中映射到错误的 ES 字段。

### 1.2 数据流分析

```
TraceQL 查询文本
    → Parser (parseCondition → parseScopeAndKey 分离 scope/key)
    → Planner (extractCondition: 用 key 做 switch 匹配)
    → Handler (把 plan 的已拆分字段传给 TraceMetricsQuery)
    → ES Reader (fieldForAttribute: 把 attribute name 映射到 ES 字段)
```

**关键不一致**：

| 路径 | 输入到 `fieldForAttribute` | 来源 | 是否经过 `parseScopeAndKey` |
|------|---|---|---|
| **filter 条件** (Tags) | `"http.method"`, `"service.name"` 等纯 key | planner `extractCondition` | ✅ 是（scope 已剥离） |
| **by() 标签** (ByLabels) | `"resource.service.name"` 等完整 raw literal | parser `parseMetricsStage` | ❌ **否**（直接用 tokenLiteral） |

### 1.3 受影响字段

| TraceQL 写法 | `by()` 中传入 | `fieldForAttribute` 结果 | 正确 ES 字段 | 是否有 bug |
|---|---|---|---|---|
| `resource.service.name` | `"resource.service.name"` | `"attributes.resource.service.name"` ❌ | `"serviceName"` | **是** |
| `span.name` | `"span.name"` | `"attributes.span.name"` ❌ | `"name"` | **是** |
| `span.kind` | `"span.kind"` | `"attributes.span.kind"` ❌ | `"kind"` | **是** |
| `span.status` | `"span.status"` | `"attributes.span.status"` ❌ | `"status.code"` | **是** |
| `span.duration` | `"span.duration"` | `"attributes.span.duration"` ❌ | `"durationNano"` | **是** |
| `resource.http.method` | `"resource.http.method"` | `"attributes.resource.http.method"` ❌ | `"resource.http.method"` | **是** |
| `service.name` (无前缀) | `"service.name"` | `"serviceName"` ✅ | `"serviceName"` | 无 |
| `http.method` (自定义属性) | `"http.method"` | `"attributes.http.method"` ✅ | `"attributes.http.method"` | 无 |

### 1.4 其他问题

~~Planner 中还存在运算符支持不完整的问题~~（Sprint 2 已修复）：

| 操作 | Sprint 1 状态 | Sprint 2 状态 |
|---|---|---|
| `key = value` | ✅ term query | ✅ 不变 |
| `key != nil` | ❌ 被忽略 | ✅ `exists` query |
| `key != value` | ❌ 被忽略 | ✅ `must_not` + `term` |
| `key =~ regex` | ❌ 被忽略 | ✅ `regexp` query |

---

## 2. 方案对比

| 方案 | 描述 | 优点 | 缺点 |
|---|---|---|---|
| A. `by()` 中调用 `parseScopeAndKey` | 在 parser 的 `by()` 解析处对 label 做 scope 剥离 | 改动最小 | 只修了 `by()` 的 bug，`fieldForAttribute` 仍不感知 scope |
| B. `fieldForAttribute` 增强 | 让 `fieldForAttribute` 自身处理带 scope 前缀的写法 | 改动集中在一处 | 两个映射规则（有scope/无scope）可能不一致 |
| **C. 统一 Attribute Resolver（推荐）** | 新建一个独立的 `AttributeResolver`，所有地方统一调用 | 单一职责、可测试、可扩展 | 需要重构调用点 |

---

## 3. 推荐方案：统一 Attribute Resolver

### 3.1 设计原则

1. **Single Source of Truth**：所有 TraceQL → ES 的映射集中在一处
2. **统一入口**：无论是 filter 条件、`by()` 标签、还是 `select()` 字段，都通过 `Resolve(raw)` 进入
3. **Scope 感知**：正确处理 `resource.service.name` vs `service.name`
4. **可扩展**：未来新增 intrinsic 字段只改一处
5. **可测试**：独立函数，表驱动测试即可覆盖所有 case
6. **消除隐式约定**：不再依赖"parser 必须先剥离 scope"的隐性契约

### 3.2 核心结构设计

```go
// attributeresolver.go — 统一属性映射层
// 位置：extension/observabilitystorageext/provider/elasticsearch/attribute_resolver.go

// ResolvedField represents a TraceQL attribute resolved to its ES field.
type ResolvedField struct {
    ESField  string // ES document field path
    IsExists bool   // true if the query is "field exists" semantics
}

// AttributeResolver maps TraceQL attribute references to ES fields.
// 处理：
// 1. Intrinsic fields (service.name, name, kind, status, duration)
// 2. Scope-prefixed intrinsics (resource.service.name, span.name, span.kind)
// 3. Custom attributes (resource.X → resource.X, span.X → attributes.X)
type AttributeResolver struct{}

func (r *AttributeResolver) Resolve(raw string) ResolvedField {
    scope, key := parseScopeAndKey(raw) // 复用已有的 parseScopeAndKey
    return r.resolveWithScope(scope, key)
}

func (r *AttributeResolver) resolveWithScope(scope, key string) ResolvedField {
    // 1. Intrinsic fields — 与 scope 无关，这些是 top-level ES fields
    switch key {
    case "service.name":
        return ResolvedField{ESField: FieldServiceName}
    case "name":
        if scope == "" || scope == "span" {
            return ResolvedField{ESField: FieldName}
        }
    case "kind":
        if scope == "" || scope == "span" {
            return ResolvedField{ESField: FieldKind}
        }
    case "status":
        if scope == "" || scope == "span" {
            return ResolvedField{ESField: FieldStatus + ".code"}
        }
    case "duration":
        if scope == "" || scope == "span" || scope == "trace" {
            return ResolvedField{ESField: FieldDurationNano}
        }
    }

    // 2. Custom attributes — 按 scope 确定 ES 路径
    switch scope {
    case "resource":
        return ResolvedField{ESField: "resource." + key}
    case "span", "":
        return ResolvedField{ESField: "attributes." + key}
    default:
        return ResolvedField{ESField: "attributes." + key}
    }
}
```

### 3.3 调用方修改

**修改前（`trace_metrics.go` 中 `buildMetricsAggTree`）**：
```go
for _, label := range query.ByLabels {
    field := fieldForAttribute(label) // ❌ 不感知 scope
    ...
}
```

**修改后**：
```go
resolver := &AttributeResolver{}
for _, label := range query.ByLabels {
    resolved := resolver.Resolve(label) // ✅ 统一走 Resolver
    field := resolved.ESField
    ...
}
```

**修改前（`buildMetricsFilter` 中 `query.Tags`）**：
```go
for _, tag := range query.Tags {
    field := fieldForAttribute(tag.Key)
    ...
}
```

**修改后**：
```go
resolver := &AttributeResolver{}
for _, tag := range query.Tags {
    resolved := resolver.Resolve(tag.Key) // 已经是纯 key，Resolve 也能处理
    field := resolved.ESField
    ...
}
```

### 3.4 废弃 `fieldForAttribute`

`fieldForAttribute` 作为旧的映射函数将被 `AttributeResolver.Resolve()` 替代。迁移完成后可删除。

---

## 4. 实施计划

### Sprint 1：创建 AttributeResolver + 修复 by() 映射

**目标**：修复所有 `by()` 字段映射错误

**任务清单**：
- [ ] 创建 `attribute_resolver.go`，实现 `AttributeResolver` 结构及 `Resolve` 方法
- [ ] 创建 `attribute_resolver_test.go`，表驱动测试覆盖所有 intrinsic/custom/scope 组合
- [ ] 修改 `trace_metrics.go` 中 `buildMetricsAggTree`，使用 `AttributeResolver.Resolve`
- [ ] 修改 `trace_metrics.go` 中 `buildMetricsFilter`，统一使用 `AttributeResolver.Resolve`
- [ ] 清除旧的 `fieldForAttribute` 函数（确认无其他调用者后）
- [ ] 验证：`by(resource.service.name)` 正确映射到 `serviceName`

### Sprint 2：扩展 Planner 运算符支持

**目标**：支持 `!= nil`（exists）、`!= value`（must_not）、`=~ regex`

**任务清单**：
- [ ] Planner `extractCondition` 中增加 `OpNotEqual` 处理，生成 `must_not` + `term`
- [ ] Planner `extractCondition` 中增加 `!= nil` 特殊 case，生成 `exists` query
- [ ] ES Reader `buildMetricsFilter` 中支持 `exists` 和 `must_not` DSL 构建
- [ ] 添加对应的单元测试
- [ ] 验证：`{resource.service.name != nil}` 正确生成 `exists` query

---

## 5. 风险评估

| 风险 | 概率 | 影响 | 缓解措施 |
|---|---|---|---|
| `parseScopeAndKey` 在 Resolver 中复用时行为不一致 | 低 | 中 | 表驱动测试全覆盖 |
| 移除 `fieldForAttribute` 时遗漏调用点 | 低 | 高 | 全局搜索确认 + 编译检查 |
| 新 Resolver 对已有查询产生回归 | 中 | 高 | 保持 `fieldForAttribute` 原有 case 的行为不变，增量新增 scope 感知 |
| planner 扩展运算符后 ES query 性能影响 | 低 | 低 | `exists` 和 `term` 都是高效查询 |

---

## 6. 验收标准

### Sprint 1
- `by(resource.service.name)` → ES 聚合字段为 `serviceName`
- `by(span.name)` → ES 聚合字段为 `name`
- `by(span.kind)` → ES 聚合字段为 `kind`
- `by(resource.http.method)` → ES 聚合字段为 `resource.http.method`
- 已有查询（无 scope 前缀）行为不变（回归测试通过）

### Sprint 2
- `{resource.service.name != nil}` → 生成 `{"exists": {"field": "serviceName"}}`
- `{http.method != "GET"}` → 生成 `{"must_not": [{"term": {"attributes.http.method": "GET"}}]}`
- 已有 `=` 条件行为不变

---

## 7. 遗留问题分析

### 7.1 `select()` 字段是否有类似的映射问题？

**结论：无问题 ✅**

`select()` 字段的处理路径与 `by()`/`filter` 完全不同：

1. **Parser** — `parseSelectStage()` 直接保存 token 原始字面值到 `SelectStage.Fields`
2. **Planner** — 直接转存 `sel.Fields` 到 `p.SelectFields`，不做任何变换
3. **执行** — select 字段**不下推到 ES 查询**，只用于结果投影

关键点在于 `resolveSelectField()`（`tempo_handler.go`）有自己独立的 scope 解析逻辑：

```go
func resolveSelectField(span Span, field string, ...) *tempoAnyValue {
    key := field
    scope := ""
    if strings.HasPrefix(field, "resource.") {
        scope = "resource"
        key = field[len("resource."):]
    } else if strings.HasPrefix(field, "span.") {
        scope = "span"
        key = field[len("span."):]
    }
    // ... 然后用 key 做 intrinsic 匹配
}
```

由于 select 是**在 Span 对象上做内存投影**（不构造 ES query），所以它不经过 `fieldForAttribute`，**不受本次 bug 影响**。

> **注意**：虽然 select 本身无问题，但 `resolveSelectField()` 与 `AttributeResolver` 存在逻辑重复。未来可考虑统一到 Resolver 层，但不属于当前 Sprint 范围。

---

### 7.2 `=~` 正则匹配的实现路径与转义问题

**结论：需要实现三层支持（planner 下推 + evaluator 执行 + 正则转义）**

#### 当前状态

| 层 | 状态 |
|---|---|
| Lexer | ✅ 已支持 `TokenRegex`（`=~`）和 `TokenNotRegex`（`!~`）|
| Parser | ✅ 已解析为 `Condition{Operator: "=~"}` |
| Planner | ❌ `extractCondition()` 只处理 `Operator == "="`，正则条件被忽略 |
| Evaluator | ❌ `matchStringValue()` 只处理 `=` 和 `!=`，`=~` 走 default 返回 false |
| ES Reader | ❌ trace 查询中没有 regexp DSL |

#### ES regexp query 参考实现

`metric_reader.go` 中已有可复用的模式：

```go
// 已有实现（metric_reader.go 行 349-358）
for k, pattern := range query.LabelMatch {
    fieldPath := fmt.Sprintf(FieldMetricLabels+".%s", k)
    qb.Raw(map[string]any{
        "regexp": map[string]any{
            fieldPath: map[string]any{
                "value": pattern,
            },
        },
    })
}
```

#### 正则转义问题分析

**ES regexp 语法 vs Go regexp 语法差异**：

| 特性 | ES Regexp（Lucene） | Go Regexp（RE2） |
|---|---|---|
| 语法基础 | Lucene/Java Pattern 子集 | RE2 |
| 默认锚定 | 完整匹配（自动 `^...$`） | 部分匹配（需显式 `^$`） |
| 特殊字符 | `. ? + * | { } [ ] ( ) " \` | 同左 |
| 不支持的 | Lookahead/lookbehind、backreference | 同左 |

**设计建议**：

1. **Planner 下推**：将 `=~` 条件直接传递到 ES query 层，生成 `regexp` DSL
2. **不做语法转换**：用户输入的正则直接传给 ES（因为两者语法高度重合）
3. **安全性**：ES 对 regexp 有内置的资源保护（`max_determinized_states` 默认 10000），无需额外限制
4. **Evaluator 兜底**：对于无法下推的情况（如 OR 中混合条件），evaluator 用 Go `regexp.MatchString` 做内存匹配
5. **输入校验**：在 planner 层做一次 `regexp.Compile` 预检查，语法错误时返回 parse error 而非透传到 ES

#### 实施建议

纳入 **Sprint 2** 统一处理，任务拆分为：

- [ ] Planner：`=~` 条件提取到 `ExecutionPlan` 新字段（如 `TagsRegex []TagCondition`）
- [ ] ES Reader：`buildMetricsFilter` / `buildTraceSearchQuery` 增加 `regexp` DSL 构建
- [ ] Evaluator：`matchStringValue` 增加 `=~` 和 `!~` case，调用 `regexp.MatchString`
- [ ] 输入校验：planner 中 `regexp.Compile(value)` 预检，失败返回 error

---

### 7.3 `span.status.message` 等嵌套 intrinsic 字段

**结论：select 投影已支持，但条件过滤未支持，需要扩展 intrinsic 字段集合**

#### ES 存储结构

```go
// StoredStatus — ES 中以嵌套对象存储
type StoredStatus struct {
    Code    string `json:"code"`    // ES 路径: status.code
    Message string `json:"message"` // ES 路径: status.message
}
```

#### 各层支持情况

| 层 | `status`（code） | `status.message` |
|---|---|---|
| ES 存储 | ✅ `status.code` | ✅ `status.message` |
| Select 投影 | ✅ `resolveSelectField` 支持 | ✅ 支持（返回 `span.Status.Message`） |
| Planner 下推 | ✅ `p.Status = valStr` | ✅ 通过 `Tags`/`TagsNot`/`TagsExists`/`TagsRegex`（Sprint 3） |
| Evaluator 过滤 | ✅ 匹配 `span.StatusCode` | ✅ 匹配 `span.StatusMessage`（Sprint 3） |
| ES trace_reader | ✅ `qb.Term("status.code", ...)` | ✅ `qb.Term("status.message", ...)`（Sprint 3） |

#### Bug 表现（Sprint 3 已修复 ✅）

~~用户写 `{ status.message = "timeout" }` 时：~~ 现在已正常工作：
1. Parser 解析出 `key = "status.message"`，`scope = ""`
2. Planner `default` 分支正确提取到 `Tags["status.message"]`（含 `!=`、`!= nil`、`=~` 支持）
3. ES 下推通过 `intrinsicTermClause` 映射到 `status.message` 字段
4. Evaluator 通过 `span.StatusMessage` 做内存匹配
5. 全部路径已验证通过

#### 需要支持的嵌套 intrinsic 字段清单

| TraceQL 写法 | ES 字段路径 | 当前支持 | 建议 |
|---|---|---|---|
| `status` 或 `status.code` | `status.code` | ✅ | — |
| `status.message` | `status.message` | ❌ 条件过滤不支持 | Sprint 3 支持 |
| `resource.service.name` | `serviceName`（顶层字段） | ❌ by() 中错误 | Sprint 1 修复 |
| `resource.service.namespace` | `resource.service.namespace` | — | 按需 |
| `resource.service.instance.id` | `resource.service.instance.id` | — | 按需 |

#### 实施建议

建议增加 **Sprint 3**（可选），专项处理嵌套 intrinsic 字段：

- [ ] `AttributeResolver` 中增加 `status.message` 映射：`"status.message"` → `"status.message"`
- [ ] Planner `extractCondition` 增加 `case key == "status.message"` 提取到新字段（如 `p.StatusMessage`）
- [ ] Evaluator `matchCondition` 增加 `status.message` 匹配 `span.StatusMessage`
- [ ] ES Reader `buildTraceSearchQuery` 增加 `status.message` 的 term query
- [ ] 单元测试覆盖

---

### 7.4 遗留问题优先级总结

| # | 问题 | 结论 | 优先级 | 归属 |
|---|---|---|---|---|
| 1 | `select()` 字段映射 | **无问题**，不需要修复 | — | — |
| 2 | `=~` 正则匹配 | 需三层实现（planner/evaluator/ES） | P2 | Sprint 2 |
| 3 | `status.message` 条件过滤 | 需 planner/evaluator/ES 扩展 | P3 | Sprint 3（可选） |

---

## 8. 修订后的完整实施计划

### Sprint 1：创建 AttributeResolver + 修复 by() 映射 ⭐ 已完成

**目标**：修复所有 `by()` 字段映射错误

**任务清单**：
- [x] 创建 `attribute_resolver.go`，实现 `AttributeResolver` 结构及 `Resolve` 方法
- [x] 创建 `attribute_resolver_test.go`，表驱动测试覆盖所有 intrinsic/custom/scope 组合（32 个测试用例，全部通过）
- [x] 修改 `trace_metrics.go` 中 `buildMetricsAggTree`，使用 `AttributeResolver.Resolve`
- [x] 修改 `trace_metrics.go` 中 `buildMetricsFilter`，统一使用 `AttributeResolver.Resolve`
- [x] 清除旧的 `fieldForAttribute` 函数（已确认无其他调用者，已删除）
- [x] 全部现有测试回归通过（`go test ./extension/...`）

**变更文件**：
- 新增：`extension/observabilitystorageext/provider/elasticsearch/attribute_resolver.go`
- 新增：`extension/observabilitystorageext/provider/elasticsearch/attribute_resolver_test.go`
- 修改：`extension/observabilitystorageext/provider/elasticsearch/trace_metrics.go`

### Sprint 2：扩展运算符支持（`!=`、`!= nil`、`=~`） ✅ 已完成

**目标**：完整支持否定条件和正则匹配

**任务清单**：
- [x] Planner `extractCondition` 增加 `!=` 处理，生成 `must_not` + `term`
- [x] Planner `extractCondition` 增加 `!= nil` 特殊 case，生成 `exists` query
- [x] Planner 增加 `=~` 条件提取（新字段 `TagsRegex`，intrinsic + generic 全覆盖）
- [x] Planner 增加 `regexp.Compile` 预校验
- [x] ES Reader `buildMetricsFilter` 支持 `exists`、`must_not`、`regexp` DSL
- [x] ES Reader `buildTraceSearchQuery` 支持 `exists`、`must_not`、`regexp` DSL
- [x] Evaluator `matchStringValue` 增加 `=~` 和 `!~` case
- [x] 单元测试全覆盖（planner 5 + evaluator 4 + ES reader 3 = 12 个新用例）
- [x] 全部回归测试通过（`go test ./extension/...`）

**变更文件**：
- 修改：`extension/adminext/traceql/planner.go` — ExecutionPlan 扩展 + extractCondition 增强
- 修改：`extension/adminext/traceql/evaluator.go` — matchStringValue 增加 =~ / !~
- 修改：`extension/adminext/traceql/traceql_test.go` — 12 个新测试用例
- 修改：`extension/adminext/tempo_handler.go` — 透传新字段到 TraceQuery/TraceMetricsQuery
- 修改：`extension/observabilitystorageext/types.go` — TraceQuery 增加 TagsNot/TagsExists/TagsRegex
- 修改：`extension/observabilitystorageext/trace_metrics.go` — TraceMetricsQuery 增加新字段
- 修改：`extension/observabilitystorageext/reader_adapter.go` — 3 个 adapter 透传新字段
- 修改：`extension/observabilitystorageext/storedmodel/trace_query.go` — TraceQuery 增加新字段
- 修改：`extension/observabilitystorageext/provider/elasticsearch/types_reader.go` — TraceMetricsQuery 增加新字段
- 修改：`extension/observabilitystorageext/provider/elasticsearch/trace_metrics.go` — buildMetricsFilter 处理新字段
- 修改：`extension/observabilitystorageext/provider/elasticsearch/trace_reader.go` — buildTraceSearchQuery 处理新字段 + intrinsicField helper
- 修改：`extension/observabilitystorageext/provider/elasticsearch/trace_reader_query_test.go` — 3 个新测试

### Sprint 3（可选）：嵌套 intrinsic 字段扩展 ✅ 已完成

**目标**：支持 `status.message` 等嵌套字段的条件过滤

**任务清单**：
- [x] `AttributeResolver` 增加 `status.message` → `"status.message"` 映射
- [x] Planner 无需单独改动 — `status.message` 通过 `default` 分支自然流入 `Tags`
- [x] Evaluator `matchCondition` 增加 `status.message` → `span.StatusMessage` 匹配（含 `!= nil` 语义）
- [x] ES Reader `buildTraceSearchQuery` 增加 `status.message` 映射（`intrinsicTermClause` + `intrinsicField`）
- [x] 单元测试覆盖（planner 1 + evaluator 3 + ES reader 2 + resolver 2 = 8 个新用例）
- [x] 全部回归测试通过

**变更文件**：
- 修改：`extension/observabilitystorageext/provider/elasticsearch/attribute_resolver.go` — status.message 映射
- 修改：`extension/observabilitystorageext/provider/elasticsearch/attribute_resolver_test.go` — 2 个测试用例
- 修改：`extension/observabilitystorageext/provider/elasticsearch/trace_reader.go` — intrinsicTermClause + intrinsicField
- 修改：`extension/observabilitystorageext/provider/elasticsearch/trace_reader_query_test.go` — 2 个 ES query 测试
- 修改：`extension/adminext/traceql/evaluator.go` — matchCondition status.message 处理
- 修改：`extension/adminext/traceql/traceql_test.go` — 4 个测试用例（planner + evaluator）
