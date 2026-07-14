# Fix: 结构查询（Structural Query）kind 匹配失败导致返回空结果

## 问题描述

Grafana Traces Drilldown 发出的 TraceQL 查询返回空结果：

```
({nestedSetParent<0 && true} &>> {kind = server}) || ({nestedSetParent<0 && true})
| select(status, resource.service.name, name, nestedSetParent, nestedSetLeft, nestedSetRight)
```

返回：`{"traces":[],"metrics":{"inspectedTraces":703,"inspectedBytes":"0"}}`

**关键线索**：`inspectedTraces: 703` 证明 Phase 1（ES 宽泛搜索）确实找到了 703 个候选 trace，但 Phase 2（结构验证）全部失败。

## 根因分析

### 问题 1：`spanKindToString` 无法处理 ES 中实际存储的 kind 格式

**数据流**：
1. 写入 ES 时：`span.Kind().String()` → `"Server"` (ptrace 的 String() 返回首字母大写)
2. 从 ES 读回时：`StoredSpanToPublic` → `Kind: SpanKind("Server")`
3. 结构验证时：`spanKindToString(SpanKind("Server"))` 尝试匹配 `SpanKindServer = "SPAN_KIND_SERVER"` → 不匹配 → 返回 `"unspecified"`
4. Evaluator 比较：`"unspecified" == "server"` → false → 所有 trace 验证失败

### 问题 2：OR 组合查询中 SpanFilter 分支被忽略

查询结构为 `(StructuralExpr) || (SpanFilter)`，但 `EvaluateTraceStructural` 只调用 `findStructuralExpr` 提取第一个 `StructuralExpr` 做全局验证。OR 右侧的简单 `SpanFilter`（`{nestedSetParent<0 && true}`）被完全忽略，即使它应该对所有包含根 span 的 trace 返回匹配。

## 修复方案

### 修复 1：`spanKindToString` — 多格式兼容（DRY 原则）

统一走 `NormalizeSpanKind()` 标准化后做一次映射，消除重复 switch：
- `NormalizeSpanKind` 内部处理所有格式（`"SPAN_KIND_SERVER"`, `"Server"`, `"server"`, `"2"`）
- `spanKindToString` 只需对标准化结果做一次 switch 映射到短格式

同理 `spanStatusToString` 统一走 `NormalizeStatusCode()`。

### 修复 2：`EvaluateTraceStructural` — 支持 OR 分支独立评估

重构为递归评估 OR 表达式的每个分支：
- `StructuralExpr`：执行结构匹配 → `MatchTypeStructural`
- `SpanFilter`：执行 span 级过滤 → `MatchTypeFilter`（LeftSpanID == RightSpanID）
- `OrExpr`：递归评估两个分支，union 结果

任一分支有匹配结果即视为 trace 通过验证。

### 修复 3：`StructuralMatch` 类型安全增强

引入 `MatchType` 枚举（`MatchTypeStructural` / `MatchTypeFilter`），使结构匹配和过滤匹配的语义显式化于类型层面，避免下游消费者需要通过 `LeftSpanID == RightSpanID` 隐式判断匹配来源。

## 修改清单

| 文件 | 修改内容 |
|------|----------|
| `extension/adminext/tempo_handler.go` | `spanKindToString`: 统一走 `NormalizeSpanKind`，消除重复 switch |
| `extension/adminext/tempo_handler.go` | `spanStatusToString`: 统一走 `NormalizeStatusCode`，消除重复 switch |
| `extension/adminext/traceql/evaluator.go` | 重构 `EvaluateTraceStructural` 支持 OR 分支独立评估 |
| `extension/adminext/traceql/evaluator.go` | 新增 `evaluateExprStructural` 递归函数 |
| `extension/adminext/traceql/evaluator.go` | 新增 `unwrapPipeline` 辅助函数 |
| `extension/adminext/traceql/evaluator.go` | 新增 `MatchType` 枚举 + `StructuralMatch.Type` 字段 |
| `extension/adminext/tempo_handler_test.go` | 新增 `TestSpanKindToString` 测试（15 cases） |
| `extension/adminext/tempo_handler_test.go` | 新增 `TestSpanStatusToString` 测试（8 cases） |
| `extension/adminext/traceql/traceql_test.go` | 新增 `TestEvaluateTraceStructural_OrWithStructuralAndFilter` |
| `extension/adminext/traceql/traceql_test.go` | 新增 `TestEvaluateTraceStructural_OrWithPipeline` |

## 设计原则

- **DRY**：`spanKindToString`/`spanStatusToString` 统一委托 Normalize 函数，只有一处映射逻辑
- **健壮性**：不假设 ES 中存储的枚举值格式，使用 normalize 函数兼容多种格式
- **高内聚低耦合**：新增 `evaluateExprStructural` 递归函数，各 AST 节点类型独立处理
- **开闭原则**：新增 SpanFilter 分支处理不影响已有 StructuralExpr 逻辑
- **类型安全**：`MatchType` 枚举使匹配来源在类型层面显式化，避免隐式约定
- **复用**：利用已有的 `NormalizeSpanKind` / `NormalizeStatusCode`，避免重复实现

## 测试验证

```bash
go test ./extension/adminext/... -count=1 -timeout 60s
# PASS (all tests pass including new ones)
```

## 状态

- [x] 根因分析
- [x] 修复 spanKindToString 多格式兼容
- [x] 修复 spanStatusToString 多格式兼容
- [x] 重构 EvaluateTraceStructural 支持 OR 分支
- [x] 单元测试通过
- [ ] 集成测试（需要 Grafana + ES 环境验证完整查询链路）
- [ ] 部署验证
