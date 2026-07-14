# 统一 TraceQL 解析器

## 需求背景

### 问题

查询 `{resource.service.name="customcol" && duration>1.2s && name="GET /api/v2/tempo/api/metrics/query_range"}` 返回的所有 trace duration 都远小于 1.2s（67ms-452ms），`duration>1.2s` 过滤条件没有生效。

### 根因

原有架构存在两条解析路径：

| 路径 | 触发条件 | 解析器 | 能力 |
|------|----------|--------|------|
| 简单路径 | `IsAdvancedQuery()` 返回 false | `parseTraceQLOrFilter` (字符串分割) | 只处理 `key="value"` 等值匹配 |
| 高级路径 | `IsAdvancedQuery()` 返回 true | `Parse()` (Lexer → AST → Plan) | 完整支持范围比较、结构操作符、OR、管道等 |

`duration>1.2s` 由于：
1. `IsAdvancedQuery()` 对该查询返回 `false`（无结构操作符、无多花括号、无 `||`）
2. 走简单路径 `parseTraceQL` 只提取 `operator == "="` 的条件
3. `duration>1.2s`（操作符为 `>`）被完全丢弃

## 解决方案

**统一使用高级解析器（AST Parser + Planner）处理所有 TraceQL 查询**，删除 `IsAdvancedQuery()` 路由分支。

### 设计决策

1. 所有 TraceQL 查询统一走 `traceql.Parse()` → `traceql.Plan()` 路径
2. 保留 graceful degradation：AST 解析失败时回退到旧解析器
3. `parseTraceQLOrFilter` 保留给 metrics 和 tag-values 路径使用
4. `IsAdvancedQuery()` 标记为 deprecated

## 改动清单

| 文件 | 改动 |
|------|------|
| `extension/adminext/tempo_handler.go` | `parseTempoSearchParams` 删除 `if IsAdvancedQuery` 分支，统一走 `Parse → Plan` |
| `extension/adminext/tempo_handler.go` | 更新 `parseTraceQLOrFilter` 注释 |
| `extension/adminext/traceql/planner.go` | `IsAdvancedQuery` 标记 deprecated |
| `extension/adminext/tempo_handler_test.go` | 新增 `TestParseTempoSearchParams_DurationFilter` 测试 |

## 测试验证

- `TestParseTempoSearchParams_DurationFilter` — 覆盖 `duration>1.2s`、`duration<500ms`、双向范围、`duration>=2s`
- `TestParseTempoSearchParams_IntrinsicFields` — 已有测试通过（验证不影响已有逻辑）
- `TestParseTempoSearchParams_StructuralQueryRelaxesConditions` — 结构查询仍正确
- `traceql` 包全部 40+ 测试通过

## 架构对比

### 改动前

```
traceQL → IsAdvancedQuery()?
    ├── YES → Parse → Plan → TraceQuery
    └── NO  → parseTraceQLOrFilter → TraceQuery (丢失范围操作符)
```

### 改动后

```
traceQL → Parse → Plan → TraceQuery
    └── (Parse 失败) → parseTraceQLOrFilter → TraceQuery (graceful degradation)
```

## 状态

- [x] 统一解析入口
- [x] 清理注释和标记 deprecated
- [x] 新增 duration 测试
- [x] 全量测试通过
