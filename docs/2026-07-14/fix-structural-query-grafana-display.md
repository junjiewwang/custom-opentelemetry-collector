# Fix: Grafana Service Structure 面板显示异常

## 需求背景

Grafana Traces Drilldown 面板的 **Service Structure** 视图显示异常，只展示一个无名称的聚合条，无法按 Service & Operation 展开树形结构。

### Grafana 发送的实际请求

```
GET /api/search?q=({nestedSetParent<0 && true } &>> { kind = server }) || ({nestedSetParent<0 && true }) | select(status, resource.service.name, name, nestedSetParent, nestedSetLeft, nestedSetRight)&limit=200&spss=20&start=...&end=...
```

### 查询语义

- 第一分支：找 root span 且有 server 类型后代的 trace（结构匹配）
- 第二分支：找所有有 root span 的 trace（兜底匹配所有）
- `select(nestedSetParent, nestedSetLeft, nestedSetRight)`: Grafana 需要这些字段来重建层级树

## 根因分析

### 问题 1: ES 候选搜索过严

**现象**：大量 trace 在 Phase 1（ES 宽松搜索）阶段就被过滤掉。

**根因**：Planner 将 `OrExpr` 中 `StructuralExpr` 两侧的条件全部 AND 合并为 ES 查询条件：
- 左侧 `{nestedSetParent<0}` → `IsRoot=true`
- 右侧 `{kind=server}` → `SpanKind="server"`
- 最终 ES 查询要求：一个 span 同时是 root span 且 kind=server

但这两个条件描述的是**不同的 span**（root span 和 server span），不应 AND 在一起。如果 root span 的 kind 不是 server（如 internal），就找不到候选。

### 问题 2: spanSet 只返回匹配端点

**现象**：即使找到匹配的 trace，响应中只包含结构匹配对的 2 个端点 span。

**根因**：`convertStructuralResultToTempoSearchTrace` 只返回 `matchedSpanIDs` 中的 span，但 Grafana Service Structure 需要整个 trace 的**全部 span**（加上 nestedSetParent/Left/Right 信息）来渲染层级树。

## 修复方案

### 修复 1: Planner 放宽结构化查询的 ES 条件

文件：`extension/adminext/traceql/planner.go`

新增 `relaxStructuralConditions()` 方法：当检测到 `HasStructural=true` 时，清除来自不同 SpanFilter 的 intrinsic 条件（`SpanKind`、`Status`），避免 ES 搜索过严。

**保留的条件**：
- `IsRoot` — 帮助窄化候选（找到有 root span 的 trace）
- `ServiceName` / `OperationName` — 通常与 IsRoot 来自同一个 filter
- `Tags` / `TagsOr` — 可能仍有用

**清除的条件**：
- `SpanKind` — 通常来自结构表达式的非 root 侧
- `Status` — 同理

### 修复 2: nestedSet 场景返回 trace 全部 span

文件：`extension/adminext/tempo_handler.go`

修改 `convertStructuralResultToTempoSearchTrace`：当 `selectFields` 包含 nestedSet 字段时，返回 trace 中的**所有** span（受 spss 限制），而不仅仅是 `matchedSpanIDs` 中的 span。这样 Grafana 前端可以通过 `nestedSetParent/Left/Right` 重建完整的 Service Structure 层级树。

## 测试验证

### 新增测试用例

| 测试 | 文件 | 验证内容 |
|------|------|---------|
| `TestPlanGrafanaQueryWithNestedSetSelect` | `traceql/traceql_test.go` | Grafana 完整查询的 plan 正确：HasStructural=true, SpanKind 被清除 |
| `TestPlanStructuralRelaxDoesNotAffectNonStructural` | `traceql/traceql_test.go` | 非结构化查询不受影响 |
| `TestConvertStructuralResult_NestedSetReturnsAllSpans` | `tempo_handler_test.go` | nestedSet select 返回全部 span |
| `TestConvertStructuralResult_SpssLimitsOutput` | `tempo_handler_test.go` | spss 限制仍然生效 |
| `TestParseTempoSearchParams_StructuralQueryRelaxesConditions` | `tempo_handler_test.go` | 端到端验证 ES query 不含 SpanKind |

### 修改的测试用例

| 测试 | 变更 |
|------|------|
| `TestPlanStructural` | SpanKind 断言从 `"server"` 改为 `Empty` |
| `TestPlanGrafanaQuery` | 同上 |

## 实施进展

- [x] 修复 Planner: `relaxStructuralConditions()` 放宽 ES 搜索条件
- [x] 修复 spanSet 组装: nestedSet 场景返回全部 span
- [x] 更新现有测试
- [x] 添加新测试用例
- [x] 编译通过 + 全部测试通过

## 遗留问题

无。
