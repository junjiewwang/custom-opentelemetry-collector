# 结构化查询条件下推优化

## 问题描述

### 现象

Grafana 的 Tempo 兼容 error 搜索查询（结构化查询）返回空结果，但简化版本（非结构化查询）能正常返回数据。

**无数据的查询**（结构化）：
```
({nestedSetParent<0 && true && status = error} &>> { status = error }) || ({nestedSetParent<0 && true && status = error})
```

**有数据的查询**（简单）：
```
{nestedSetParent<0 && true && status = error}
```

### 根因分析

结构化查询的两阶段执行流程中存在设计缺陷：

1. **ES 候选获取阶段**：`relaxStructuralConditions()` 无差别清除了 `Status` 和 `SpanKind`，导致 ES 查询丢失了 `status.code=Error` 过滤条件
2. **候选集膨胀**：ES 返回了 525 条 trace（所有有 root span 的 trace），而不是仅 3 条（status=error 的 trace）
3. **截断丢失**：`maxStructuralTraces=50` 的限制只取前 50 个候选做内存验证，而 3 个 error trace 排在第 103、108、110 位，被截断丢弃

### 影响范围

所有包含 `&>>` 结构操作符的查询，当匹配数据不在候选集前 50 名时，都可能出现漏报。

## 解决方案

### 设计思路

**核心原则**：ES 阶段应保留所有 OR 分支根 span filter 中**公共的属性条件**，只排除来自不同 span 的结构关系条件。

**具体做法**：
1. `extract(StructuralExpr)` 只从 left 侧（root span filter）提取条件，不从 right 侧提取
2. `relaxStructuralConditions()` 重构为智能交集算法：
   - 收集所有 OR 分支的根 span filter
   - 计算这些 filter 的条件交集
   - 只保留交集中的条件（所有分支都需要的条件）

### 修改文件

| 文件 | 修改内容 |
|------|---------|
| `extension/adminext/traceql/planner.go` | 重构 `relaxStructuralConditions()` + 新增辅助函数 |
| `extension/adminext/traceql/traceql_test.go` | 新增 4 个测试用例 |

### 关键代码变更

#### 1. `extract(StructuralExpr)` — 只从 left 侧提取

```go
case *StructuralExpr:
    p.HasStructural = true
    // Only extract from the LEFT side (root-span filter).
    // Right side targets DIFFERENT spans — should not be mixed into ES query.
    p.extract(e.Left)
```

#### 2. `relaxStructuralConditions()` — 智能交集

- `computeSafeStructuralConditions(ast)` 遍历 AST，收集每个 OR 分支的根 filter
- `extractFilterConditions(sf)` 提取单个 filter 的条件
- `intersectConditions(a, b)` 计算两组条件的交集

### 效果对比

对于查询 `({nestedSetParent<0 && status=error} &>> {status=error}) || ({nestedSetParent<0 && status=error})`：

| | 修改前 | 修改后 |
|---|--------|--------|
| ES 查询条件 | 时间范围 + IsRoot | 时间范围 + IsRoot + status.code=Error |
| ES 候选数 | 525 | ~3 |
| 截断风险 | 高（50/525） | 无（3<50） |
| 内存验证开销 | 50 次 GetTrace | 3 次 GetTrace |

## 验证

- 所有 75 个现有单元测试通过（无回归）
- 新增 4 个专项测试用例：
  - `TestPlanStructuralRelaxPreservesCommonConditions` — 验证公共条件被保留
  - `TestPlanStructuralRelaxClearsNonCommonConditions` — 验证非公共条件被清除
  - `TestPlanStructuralRelaxPreservesServiceName` — 验证 ServiceName 交集
  - `TestPlanStructuralSimpleNoOr` — 验证简单结构化查询（无 OR）

## 状态

- [x] 根因分析
- [x] 方案设计
- [x] 代码实施
- [x] 单元测试
- [ ] 线上验证

## 遗留问题

1. `maxStructuralTraces=50` 的硬编码限制仍然存在，对于极端情况（大量不同 trace 满足公共条件但只有少数满足结构关系）仍可能截断。建议后续评估是否需要动态调整。
2. `TagsOr` 在结构化查询中被清空（保守策略），如果有更精细的交集逻辑可以进一步优化。
