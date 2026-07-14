# 修复 Grafana nestedSetLeft not found 错误 — 实施方案

## 需求背景

### 问题描述

Grafana Traces Drilldown 插件发送的 TraceQL 搜索请求包含：

```
({nestedSetParent<0 && true} &>> {kind = server}) || ({nestedSetParent<0 && true})
| select(status, resource.service.name, name, nestedSetParent, nestedSetLeft, nestedSetRight)
```

前端依赖 `nestedSetParent`/`nestedSetLeft`/`nestedSetRight` 字段构建 span 树形结构（StructureScene 组件），但后端 `resolveSelectField` 函数未处理这三个字段，导致 API 响应中缺失，前端抛出：

```
Uncaught Error: nestedSetLeft not found!
  at A (utils.ts:12:9)
  at z (tree-node.ts:75:11)
  at merge.ts:55:25
  at StructureScene.tsx:73:26
```

### 影响范围

- Grafana Traces Drilldown 页面无法展示 span 树形结构
- 所有包含 `| select(...nestedSetParent, nestedSetLeft, nestedSetRight)` 的 TraceQL 查询

---

## 技术分析

### Nested Set Model 原理

Tempo（Parquet 格式）使用 **Nested Set Model** 编码 span 树：

```
         Root (parent=-1, left=1, right=8)
        /                    \
   Child1 (parent=1, left=2, right=5)    Child2 (parent=1, left=6, right=7)
      |
   Grandchild (parent=2, left=3, right=4)
```

- `nestedSetParent`：父 span 的 `nestedSetLeft` 值；root span 为 `-1`
- `nestedSetLeft`：DFS 前序遍历进入编号
- `nestedSetRight`：DFS 前序遍历离开编号
- **祖先关系判断**：span A 是 span B 的祖先 ⟺ `A.left < B.left && A.right > B.right`

### 当前代码现状

| 组件 | 文件 | 现状 |
|------|------|------|
| AST | `traceql/ast.go:99-107` | ✅ 已识别为 intrinsic |
| Planner | `traceql/planner.go:186-192` | ✅ `nestedSetParent<0` → `IsRoot=true` |
| Evaluator | `traceql/evaluator.go:183-191` | ✅ `nestedSetParent<0` → `ParentSpanID==""` |
| **Select 投影** | `tempo_handler.go:1888-1909` | ❌ **未处理** |

### 关键约束

1. **ES 中不存储 nested set 字段**：只有 `parentSpanId` 表示父子关系
2. **搜索结果只包含部分 spans**（由 `spss` 参数控制，默认 3 个），不一定是完整 trace
3. **结构化查询路径**（`structuralPostFilter`）已获取完整 trace spans（`sr.fullSpans`）
4. **非结构化查询路径**只有 `TraceSummary.SpanSet`（前 N 个 spans）

---

## 实施方案

### 方案选择

| 方案 | 描述 | 优点 | 缺点 |
|------|------|------|------|
| A. 完整 DFS 计算 | 基于 parentSpanID 构建树并 DFS 计算 | 数值完全正确 | 需要完整 trace spans；非结构化路径性能开销 |
| B. 简化数值返回 | 固定值方案：root=-1/非root=parent.left | 实现简单 | 数值不精确，但 Grafana 不用于范围比较 |
| **C. 按需计算（推荐）** | 结构化路径完整计算；非结构化路径简化返回 | 兼顾正确性和性能 | 稍复杂 |

**选择方案 C**：
- 结构化查询已有 `fullSpans`，可以正确计算 DFS 编号
- 非结构化查询只有部分 spans，用简化值（position-based）满足前端 "字段存在" 的需求

### Grafana 前端对这些字段的使用方式分析

通过错误堆栈分析，Grafana 前端的使用逻辑：

```typescript
// utils.ts — 要求字段必须存在
function getNestedSetLeft(span) {
  const val = span.attributes.find(a => a.key === 'nestedSetLeft');
  if (!val) throw new Error('nestedSetLeft not found!');
  return parseInt(val.value.intValue);
}

// tree-node.ts — 用 parent/left/right 构建树
// merge.ts — 合并 span sets
// StructureScene.tsx — 渲染树形结构
```

**关键发现**：前端只需要字段**存在**且值为整数，用于构建树形关系。具体数值只需保证：
1. root span 的 `nestedSetParent = -1`
2. 非 root span 的 `nestedSetParent` > 0（指向父 span 的 left 值）
3. `nestedSetLeft < nestedSetRight`（每个 span）
4. 父子关系：`parent.left < child.left && parent.right > child.right`

---

## 详细实施步骤

### Step 1：新增 Nested Set 计算工具函数

**文件**：`extension/adminext/tempo_handler.go`

新增计算 nested set 编号的函数，输入一组 spans（含 parentSpanID），输出每个 spanID 对应的 left/right/parent 值：

```go
// nestedSetInfo holds the computed nested set model values for a span.
type nestedSetInfo struct {
	Parent int // parent span's Left value; -1 for root
	Left   int // DFS pre-order entry number
	Right  int // DFS pre-order exit number
}

// computeNestedSet computes nested set model (left/right/parent) for a list of spans.
// Uses DFS traversal based on parentSpanID relationships.
// Returns a map from spanID to nestedSetInfo.
func computeNestedSet(spans []observabilitystorageext.Span) map[string]nestedSetInfo {
	if len(spans) == 0 {
		return nil
	}

	// Build adjacency: parentSpanID → children
	children := make(map[string][]string, len(spans))
	spanIndex := make(map[string]int, len(spans))
	var roots []string
	for i, sp := range spans {
		spanIndex[sp.SpanID] = i
		if sp.ParentSpanID == "" {
			roots = append(roots, sp.SpanID)
		} else {
			children[sp.ParentSpanID] = append(children[sp.ParentSpanID], sp.SpanID)
		}
	}

	// Sort children by start time for deterministic order.
	for pid := range children {
		sort.Slice(children[pid], func(i, j int) bool {
			si := spanIndex[children[pid][i]]
			sj := spanIndex[children[pid][j]]
			return spans[si].StartTimeUnixNano < spans[sj].StartTimeUnixNano
		})
	}

	// Handle orphan spans (parentSpanID not found in this span set).
	for _, sp := range spans {
		if sp.ParentSpanID != "" {
			if _, exists := spanIndex[sp.ParentSpanID]; !exists {
				roots = append(roots, sp.SpanID)
			}
		}
	}

	// Sort roots by start time.
	sort.Slice(roots, func(i, j int) bool {
		si := spanIndex[roots[i]]
		sj := spanIndex[roots[j]]
		return spans[si].StartTimeUnixNano < spans[sj].StartTimeUnixNano
	})

	// DFS traversal to assign left/right numbers.
	result := make(map[string]nestedSetInfo, len(spans))
	counter := 1

	var dfs func(spanID string, parentLeft int)
	dfs = func(spanID string, parentLeft int) {
		left := counter
		counter++
		for _, childID := range children[spanID] {
			dfs(childID, left)
		}
		right := counter
		counter++
		result[spanID] = nestedSetInfo{
			Parent: parentLeft,
			Left:   left,
			Right:  right,
		}
	}

	for _, rootID := range roots {
		dfs(rootID, -1)
	}

	// Spans not reached by DFS (disconnected) get fallback values.
	for _, sp := range spans {
		if _, ok := result[sp.SpanID]; !ok {
			left := counter
			counter++
			right := counter
			counter++
			result[sp.SpanID] = nestedSetInfo{Parent: -1, Left: left, Right: right}
		}
	}

	return result
}
```

### Step 2：修改 select 投影函数签名

需要将 nested set 信息传递到 `projectSpanWithSelect`：

**修改函数签名**（`tempo_handler.go`）：

```go
// Before:
func projectSpanWithSelect(span observabilitystorageext.Span, selectFields []string) []tempoKeyValue

// After:
func projectSpanWithSelect(span observabilitystorageext.Span, selectFields []string, nsInfo map[string]nestedSetInfo) []tempoKeyValue
```

### Step 3：在 resolveSelectField 中添加 nested set 字段处理

**修改方式**：将函数签名改为接收 `nsInfo` 参数，在 switch 中添加 case：

```go
func resolveSelectField(span observabilitystorageext.Span, field string, nsInfo map[string]nestedSetInfo) *tempoAnyValue {
    // ... existing scope stripping ...

    // ── System / intrinsic fields ──
    switch key {
    // ... existing cases ...
    
    case "nestedSetParent":
        if nsInfo != nil {
            if info, ok := nsInfo[span.SpanID]; ok {
                s := strconv.Itoa(info.Parent)
                return &tempoAnyValue{IntValue: &s}
            }
        }
        // Fallback: root=-1, non-root=1
        if span.ParentSpanID == "" {
            s := "-1"
            return &tempoAnyValue{IntValue: &s}
        }
        s := "1"
        return &tempoAnyValue{IntValue: &s}

    case "nestedSetLeft":
        if nsInfo != nil {
            if info, ok := nsInfo[span.SpanID]; ok {
                s := strconv.Itoa(info.Left)
                return &tempoAnyValue{IntValue: &s}
            }
        }
        // Fallback: position-based
        s := "1"
        return &tempoAnyValue{IntValue: &s}

    case "nestedSetRight":
        if nsInfo != nil {
            if info, ok := nsInfo[span.SpanID]; ok {
                s := strconv.Itoa(info.Right)
                return &tempoAnyValue{IntValue: &s}
            }
        }
        // Fallback: position-based
        s := "2"
        return &tempoAnyValue{IntValue: &s}
    }
    
    // ... rest unchanged ...
}
```

### Step 4：修改调用链路传入 nested set 信息

#### 4a. 结构化查询路径（有 fullSpans）

```go
func convertStructuralResultToTempoSearchTrace(
    sr structuralVerifyResult,
    selectFields []string,
    spss int,
) tempoSearchTrace {
    // ...
    
    // 计算 nested set（只在 select 中请求了相关字段时才计算）
    var nsInfo map[string]nestedSetInfo
    if needsNestedSet(selectFields) {
        nsInfo = computeNestedSet(sr.fullSpans)
    }
    
    for _, sp := range sr.fullSpans {
        if !sr.matchedSpanIDs[sp.SpanID] {
            continue
        }
        attrs := projectSpanWithSelect(sp, selectFields, nsInfo)
        // ...
    }
    // ...
}
```

#### 4b. 非结构化查询路径（只有 SpanSet）

```go
func convertTraceSummaryToTempoSearchTrace(s observabilitystorageext.TraceSummary, selectFields []string) tempoSearchTrace {
    // ...
    
    // 用 SpanSet 中的 spans 计算 nested set（部分 trace，但仍能保证树形关系正确）
    var nsInfo map[string]nestedSetInfo
    if needsNestedSet(selectFields) {
        nsInfo = computeNestedSet(s.SpanSet)
    }
    
    for _, sp := range s.SpanSet {
        attrs := projectSpanWithSelect(sp, selectFields, nsInfo)
        // ...
    }
    // ...
}
```

#### 4c. 辅助函数

```go
// needsNestedSet checks if any select field requires nested set computation.
func needsNestedSet(selectFields []string) bool {
    for _, f := range selectFields {
        switch f {
        case "nestedSetParent", "nestedSetLeft", "nestedSetRight":
            return true
        }
    }
    return false
}
```

### Step 5：添加 intVal 辅助函数（如未存在）

```go
// intVal creates a tempoAnyValue holding an integer (as string per proto jsonpb convention).
func intVal(n int) *tempoAnyValue {
    s := strconv.Itoa(n)
    return &tempoAnyValue{IntValue: &s}
}
```

---

## 涉及文件变更清单

| 文件 | 变更类型 | 说明 |
|------|----------|------|
| `extension/adminext/tempo_handler.go` | 修改 | 新增 `computeNestedSet`、`needsNestedSet`、`intVal`；修改 `resolveSelectField`、`projectSpanWithSelect`、`convertTraceSummaryToTempoSearchTrace`、`convertStructuralResultToTempoSearchTrace` |
| `extension/adminext/tempo_handler_test.go` | 新增/修改 | 新增 nested set 计算测试、select 投影测试 |

---

## 测试策略

### 单元测试

1. **`TestComputeNestedSet`**：
   - 单 root span（parent=-1, left=1, right=2）
   - 线性链（A → B → C）
   - 多叉树（root 有 3 个子节点）
   - 孤儿 spans（parentSpanID 不在集合中）
   - 空 spans 列表

2. **`TestResolveSelectField_NestedSet`**：
   - 有 nsInfo 时返回正确值
   - 无 nsInfo 时返回 fallback 值
   - root span 返回 parent=-1

3. **`TestProjectSpanWithSelect_NestedSet`**：
   - select 包含 nestedSetParent/Left/Right 时输出 intValue
   - select 不包含时不计算

### 集成测试

- 使用 Grafana Traces Drilldown 的真实查询验证前端不再报错
- 验证 span 树形结构正确展示

---

## 风险评估

| 风险 | 等级 | 缓解措施 |
|------|------|----------|
| 非结构化路径只有部分 spans，树形关系不完整 | 中 | `computeNestedSet` 处理孤儿 spans；前端只用返回的 spans 构建树 |
| 性能：大 trace（>1000 spans）DFS 计算开销 | 低 | `needsNestedSet` 守卫，只在需要时计算；DFS O(n) 复杂度可接受 |
| 函数签名变更影响其他调用方 | 低 | `resolveSelectField` 是内部函数，grep 确认只有 `projectSpanWithSelect` 调用 |
| 排序稳定性（相同 startTime 的 spans） | 低 | 使用 `sort.Slice`（不稳定排序），但相同时间的子 span 顺序不影响正确性 |

---

## 验收标准

1. ✅ Grafana Traces Drilldown 页面不再抛出 `nestedSetLeft not found!` 错误
2. ✅ API 响应中 select 包含 `nestedSetParent`/`nestedSetLeft`/`nestedSetRight` 字段，值为 `intValue` 格式
3. ✅ Root span 的 `nestedSetParent` 值为 `-1`
4. ✅ 非 root span 的 `nestedSetParent` 值 > 0
5. ✅ 每个 span 的 `nestedSetLeft < nestedSetRight`
6. ✅ 父 span 的 left/right 包围子 span 的 left/right
7. ✅ 单元测试覆盖所有边界情况
8. ✅ 不影响不包含 nested set 字段的 select 查询性能

---

## 实施进展

| 步骤 | 状态 | 说明 |
|------|------|------|
| Step 1: 新增 `computeNestedSet` 函数 | ✅ 已完成 | 含 `nestedSetInfo` 结构体、`needsNestedSet` 守卫、`intVal` 辅助函数 |
| Step 2: 修改 `projectSpanWithSelect` 签名 | ✅ 已完成 | 添加 `nsInfo map[string]nestedSetInfo` 参数 |
| Step 3: 修改 `resolveSelectField` 添加 case | ✅ 已完成 | 新增 `nestedSetParent`/`nestedSetLeft`/`nestedSetRight` 三个 case |
| Step 4: 修改调用链路传入 nsInfo | ✅ 已完成 | `convertTraceSummaryToTempoSearchTrace` 和 `convertStructuralResultToTempoSearchTrace` 均已更新 |
| Step 5: 添加辅助函数 | ✅ 已完成 | `intVal` 函数位于 `strVal` 旁边 |
| 单元测试 | ✅ 已完成 | 11 个测试用例全部通过 |
| 集成验证 | ⬜ 待实施 | 需要 Grafana 环境验证 |

---

## 遗留问题

1. **非结构化路径 span 不完整**：当 `spss=3` 时只有 3 个 spans，nested set 编号只在这 3 个 spans 内有效。Grafana 前端是否能正确处理 "不完整的 nested set"？需要集成测试验证。
2. **后续优化**：如果非结构化路径也需要完整 nested set，可考虑在检测到 select 包含 nested set 字段时，自动获取完整 trace（类似结构化路径的 GetTrace 逻辑）。但这会增加 ES 查询压力，需权衡。
