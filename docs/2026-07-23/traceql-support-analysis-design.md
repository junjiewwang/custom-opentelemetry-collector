# TraceQL 支持完备性分析与设计方案

> 文档创建日期：2026-07-23
> 状态：分析完成，设计待实施
> 参考：[Grafana Tempo TraceQL Spec (v3.0)](https://grafana.com/docs/tempo/latest/traceql/construct-traceql-queries/)

---

## 1. 当前实现架构

```
HTTP → traceql.Parse(q) → AST → Planner.pushdownConditions
                                    ↓
                              AttributeResolver → ES Query → TraceSummaries
                                    ↓
                              Evaluator.matchConditions → in-memory post-filter
```

三个关键组件：

| 组件 | 职责 | 文件 |
|------|------|------|
| **Planner** | AST → 可推到 ES 的条件列表 | `traceql/planner.go` |
| **AttributeResolver** | TraceQL 属性名 → ES 字段路径 | `elasticsearch/attribute_resolver.go` |
| **Evaluator** | 内存中后置过滤（span 数据匹配） | `traceql/evaluator.go` |

---

## 2. 功能支持矩阵

### 2.1 Intrinsics（内建字段）

| Intrinsic | Parser | Planner | Resolver | Evaluator | 状态 |
|-----------|:---:|:---:|:---:|:---:|:---:|
| `span:status` | ✅ | ✅ | ✅ | ⚠️ scope gap | **Partial** |
| `span:statusMessage` | ✅ | ⚠️ | ✅ | ❌ | **Partial** |
| `span:duration` | ✅ | ⚠️ | ✅ | ❌ | **Partial** |
| `span:name` | ✅ | ⚠️ | ✅ | ❌ | **Partial** |
| `span:kind` | ✅ | ⚠️ | ✅ | ❌ | **Partial** |
| `span:id` | ✅ | ⚠️ | ✅ (刚修复) | ❌ | **Partial** |
| `span:parentID` | ❌ | ❌ | ❌ | ❌ | ❌ |
| `span:childCount` | ❌ | ❌ | ❌ | ❌ | ❌ |
| `trace:duration` | ✅ | ✅ | ✅ | ✅ | ✅ |
| `trace:rootName` | ✅ | ✅ | ✅ | ✅ | ✅ |
| `trace:rootService` | ✅ | ✅ | ✅ | ✅ | ✅ |
| `trace:id` | ❌ | ❌ | ✅ (刚修复) | ❌ | ❌ |
| `link:spanID` | ❌ | ❌ | ❌ | ❌ | ❌ |
| `link:traceID` | ❌ | ❌ | ❌ | ❌ | ❌ |
| `event:name` | ❌ | ❌ | ❌ | ❌ | ❌ |
| `event:timeSinceStart` | ❌ | ❌ | ❌ | ❌ | ❌ |
| `instrumentation:name` | ❌ | ❌ | ❌ | ❌ | ❌ |
| `instrumentation:version` | ❌ | ❌ | ❌ | ❌ | ❌ |

**核心问题**：Planner 中 `extractCondition()` 的 intrinsic 匹配仅覆盖 `cond.Scope == ""`（unscoped），但 `span:xxx` 格式的 scope="span" 会被漏过，依赖 AttributeResolver 兜底。Evaluator 同样缺失 scope 感知的 intrinsic 处理。

### 2.2 Scoped Attributes（自定义属性）

| Scope | Parser | Planner | Resolver | Evaluator | 状态 |
|-------|:---:|:---:|:---:|:---:|:---:|
| `span.xxx` | ✅ | ✅ (Tags) | ✅ | ✅ | ✅ |
| `resource.xxx` | ✅ | ✅ (Tags) | ✅ | ✅ | ✅ |
| `event.xxx` | ✅ | ✅ (Tags) | ⚠️ | ❌ | **Partial** |
| `link.xxx` | ❌ | ❌ | ❌ | ❌ | ❌ |
| `instrumentation.xxx` | ❌ | ❌ | ❌ | ❌ | ❌ |

### 2.3 运算符

| 运算符 | 说明 | Parser | Planner | Evaluator | 状态 |
|--------|------|:---:|:---:|:---:|:---:|
| `=` | 等于 | ✅ | ✅ | ✅ | ✅ |
| `!=` | 不等于 | ✅ | ✅ | ✅ | ✅ |
| `>` | 大于 | ✅ | ✅ | ✅ | ✅ |
| `>=` | 大于等于 | ✅ | ✅ | ✅ | ✅ |
| `<` | 小于 | ✅ | ✅ | ✅ | ✅ |
| `<=` | 小于等于 | ✅ | ✅ | ✅ | ✅ |
| `=~` | 正则匹配 | ✅ | ✅ (regexp query) | ✅ | ✅ |
| `!~` | 正则不匹配 | ✅ | ⚠️ | ✅ | ✅ |
| `&&` | 逻辑与 (同一 spanset) | ✅ | ✅ | ✅ | ✅ |
| `&&` | 逻辑与 (跨 spanset) | ❌ | ❌ | ❌ | ❌ |
| `\|\|` | 逻辑或 | ❌ | ❌ | ❌ | ❌ |
| `>>` | 后代 | ❌ | ❌ | ❌ | ❌ |
| `<<` | 祖先 | ❌ | ❌ | ❌ | ❌ |
| `>` | 直接子 | ❌ | ❌ | ❌ | ❌ |
| `<` | 直接父 | ❌ | ❌ | ❌ | ❌ |
| `~` | 兄弟 | ❌ | ❌ | ❌ | ❌ |
| `&>>` 等 | 联合结构 | ❌ | ❌ | ❌ | ❌ |
| `!>>` 等 | 反结构 | ❌ | ❌ | ❌ | ❌ |
| `= nil` | nil 检查 | ❌ | ❌ | ❌ | ❌ |
| `!= nil` | not-nil 检查 | ❌ | ❌ | ❌ | ❌ |

### 2.4 Pipeline 阶段

| 阶段 | 状态 |
|------|:---:|
| `count()` | ❌ |
| `avg()` | ❌ |
| `max()` | ❌ |
| `min()` | ❌ |
| `sum()` | ❌ |
| `by()` | ❌ |
| `select()` | ❌ |

### 2.5 其他

| 功能 | 状态 |
|------|:---:|
| most_recent=true | ❌ |
| Duration literals (100ms, 5s) | ✅ (Scanner) → ⚠️ (Planner) |
| Float literals (1.5) | ✅ |
| Integer literals | ✅ |
| String literals | ✅ |

---

## 3. 核心设计缺陷分析

### 3.1 缺陷 1：Planner 中 Intrinsic scope 感知缺失

**现状**：`extractCondition()` 对 intrinsic 的判断使用 `cond.Scope == ""`（unscoped 检查），未处理 `cond.Scope == "span"` 等显式 scope。导致 `span:name` 被当做自定义属性 `attributes.name` 而不是 `name` 字段。

**根因**：unscoped intrinsic（如 `{ status = error }`）和 scoped intrinsic（如 `{ span:status = error }`）共享相同的 key 名，但 planner 只处理 unscoped 形式。

**修复方向**：扩展 planner 条件匹配的 scope 检查，对 scope="span"/"trace"/"event" 也执行 intrinsic 映射。

### 3.2 缺陷 2：Evaluator 中 Intrinsic 处理碎片化

**现状**：`evaluator.go` 中 `matchCondition()` 通过 `cond.IsIntrinsic()` 判断是否为 intrinsic，但 `IsIntrinsic()` 仅检查 `cond.Scope == ""` 且 key 在已知 intrinsic 列表中。当 scope="span" 时，返回 false，导致 evaluator 在 attributes map 中找 key，找不到则返回 false。

**修复方向**：统一 Intrinsic 解析为独立函数，不依赖 Scope 判断。

### 3.3 缺陷 3：ES 存储与 TraceQL 属性的映射耦合

**现状**：`AttributeResolver` 充当 TraceQL key → ES field 的映射表，但结构的反向映射（ES field → TraceQL display）缺失。且所有新增的 intrinsic（如 span:id、link:traceID）都需要手动在 switch case 中添加。

**修复方向**：抽取 `AttrRegistry` 统一管理 intrinsic 注册、ES 映射、反向查询。

---

## 4. 目标架构设计

### 4.1 新组件：`AttrRegistry`

```
AttrRegistry (单例)
  ├── RegisterIntrinsic(name, scope, ESField, evaluator)
  ├── Resolve(key, scope) → ResolvedAttr
  ├── ResolveESField(esFieldName) → DisplayName  (反向查询)
  └── AllIntrinsics() → []ResolvedAttr  (用于 ListLabelNames)

Planner / Evaluator / AttributeResolver → 统一通过 AttrRegistry 解析
```

### 4.2 数据流

```
Parser AST → Planner.extractCondition(key, scope, value)
              │
              ├── AttrRegistry.Resolve(key, scope) → intrinsic or custom?
              │     ├── intrinsic → Planner maps to ES intrinsic field pass
              │     └── custom → Planner maps to Tags (Attributes/Resources)
              │
              ├── ES Query (pushdown via AttributeResolver or direct field)
              │
              └── Post-conditions for Evaluator
                    └── Evaluator.match → AttrRegistry.Evaluate(key, scope, value, span)
```

### 4.3 按优先级分组

#### P0 — 已暴露的用户可见缺陷

| 问题 | 影响 |
|------|------|
| `span:id` / `trace:id` evaluator gap | Grafana span link navigation 查不到数据 |
| `span:parentID` 缺失 | 无法按 parent span ID 搜索 |
| `trace:id` parser+planner 缺失 | 无法用 traceID 搜索 |
| scoped intrinsic 全部在 evaluator 中失效 | evaluator post-filter 漏掉匹配，返回 false negative |

#### P1 — 高频使用但缺失

| 功能 | 优先级 |
|------|:---:|
| `\|\|` (OR) 跨 spanset | 中 |
| `event:name`, `link:spanID/traceID` | 中 |
| `nil` 检查 | 中 |
| Pipeline: `count()`, `by()` | 中 |

#### P2 — 低频 / 高级功能

| 功能 |
|------|
| 结构运算符 (`>>`, `<<`, `~` 等) |
| `select()` |
| `most_recent=true` |
| `span:childCount` |
| `avg()`, `max()`, `min()`, `sum()` 管道 |

---

## 5. 分步实施计划

### Sprint 1：修复 Planner + Evaluator 的 scope 缺口（P0）

**目标**：使所有已支持 intrinsic 在 explicit scope（`span:`/`trace:`）下也能正确工作。

**改动**：
- `planner.go`：扩展 scope 检查覆盖 `"span"` 和 `"trace"`
- `evaluator.go`：统一 Intrinsic 匹配，不依赖 `IsIntrinsic()` 的 scope 检查
- `attribute_resolver.go`：补充已缺失的 intrinsic 映射

**验证**：Grafana `{span:id="..."}` 查询能返回数据。

### Sprint 2：补充缺失的高频 intrinsic（P0）

**目标**：添加 `span:parentID`、`trace:id`、`link:spanID`、`link:traceID`、`event:name`。

**改动**：
- `ast.go`：新增 intrinsic 常量
- `lexer.go`/`parser.go`：识别新 keyword
- `planner.go`：plan 到 ES 字段
- `evaluator.go`：evaluate 匹配
- `attribute_resolver.go`：映射 ES 字段

### Sprint 3：`||` OR 跨 spanset（P1）

**目标**：支持 `{A} || {B}` 并集查询。

**改动**：
- `parser.go`：识别 `||` 分叉为多个 spanset
- `planner.go`：plan 为 ES `should` 查询
- `evaluator.go`：trace-level OR 合并

### Sprint 4：nil 检查 + 基础 pipeline（P1）

**目标**：支持 `= nil`/`!= nil`、`count()`、`by()`。

### Sprint 5+：结构运算符、高级 pipeline（P2）

详见文档。

---

## 6. 设计原则体现

| 原则 | 体现 |
|------|------|
| **SRP** | `AttrRegistry` 独立负责 intrinsic 注册和解析，不与 Planner/Evaluator 耦合 |
| **OCP** | 新增 intrinsic 只需在 `AttrRegistry` 注册，无需修改 Planner/Evaluator |
| **DIP** | Planner/Evaluator 依赖 `AttrRegistry` 接口，不直接依赖具体 ES 字段名 |
| **高内聚** | Intrinsic 的解析、映射、评估逻辑集中在新组件内 |
| **低耦合** | Parser→Planner→Resolver→Evaluator 通过 AttrRegistry 解耦 |
| **可测试** | AttrRegistry 可 mock，Planner/Evaluator 独立可测 |

---

## 7. 遗留问题

1. **结构运算符**（`>>`, `<<`, `~` 等）需要 trace tree 索引支持，当前 ES 存储只有单个 span 文档，结构运算符需要跨文档分析，对现有架构改动较大。
2. **Pipeline stages**（`count()`, `by()`）与 Grafana 聚合面板集成，当前 trace search 返回 summaries 而非聚合指标，需设计新的响应格式。
3. **Evaluator Post-filter** 仅对 Planner 下推到 ES 后的候选 trace 做内存过滤。当 Planner 下推不完整（如 `link:spanID` 不能推到 ES）时，evaluator 负担过大。
