# LogQL OR 表达式支持 设计方案

> 文档创建日期：2026-07-23
> 状态：方案设计
> 关联问题：LogQL 查询中的 `OR` 操作符导致解析失败或结果为空

---

## 1. 问题陈述

### 1.1 当前行为

Grafana Loki Explore 插件生成的查询包含 `OR` 语法：

```logql
{service_name="test-java-stock-service"} 
  | label_format log_line_contains_trace_id=`{{ contains "..." __line__ }}`
  | log_line_contains_trace_id="true"
  OR trace_id="5d5f4cc370174374aefdeedff111973e"
```

**Problem 1 — Log Query**：`parse()` 在遇到 `OR` 时（不匹配 `|` 或 `!`）break 循环 → 后半段被**静默丢弃** → ES 查不到匹配文档 → 空结果。

**Problem 2 — Metric Query**：`parseMetric()` → 内层 `parse()` 同样在 `OR` 处 break → 后续 `parseRangeVector()` 期望 `[` 但遇到 `O` → 500 报错。

### 1.2 Loki OR 语义

```logql
{app="foo"} | json | level="error" OR level="warn"
```

等价于两条独立查询取并集：

```
Branch 1: {app="foo"} | json | level="error"
Branch 2: {app="foo"} | json | level="warn"
```

- 分支间 **共享 stream selector 和 OR 之前的 pipeline**
- 每个分支独立执行
- 结果合并：去重（按 entry ID）、排序（按 timestamp）、截断（按 limit）

### 1.3 两种 OR 形式

| 形式 | 示例 | 说明 |
|------|------|------|
| 完整分支 | `{a="1"} \| json OR {b="2"} \| logfmt` | 各有独立 stream selector |
| 共享前缀 | `{a="1"} \| json \| level="err" OR level="warn"` | 共享 stream selector + pipeline |

**MVP 仅支持共享前缀形式**（Grafana Explore 插件生成的形式），完整分支作为后续迭代。

---

## 2. 核心设计

### 2.1 数据流

```
Before:
  Parse(q) → LogQLQuery → Evaluator → LogQuery → ES → Result

After:
  ParseExpression(q) → []LogQLQuery 
    → for each branch: Evaluator → LogQuery → ES → per-branch result
    → MergeResults → Result
```

### 2.2 新增类型

```go
// ast.go

// LogQLExpression is a composite expression containing OR-connected branches.
// For simple queries (no OR), Branches has exactly 1 element.
type LogQLExpression struct {
    Branches []*LogQLQuery
}
```

### 2.3 解析流程

```
parseExpression():
  ┌─ parse() shared prefix: {selector} | pipeline1 | pipeline2 ...
  │   → sharedQuery
  │
  │   if not followed by "OR": return [sharedQuery]
  │
  ├─ branches = [sharedQuery]
  │
  ├─ while peek keyword "OR":
  │   ├─ skip "OR"
  │   ├─ if next token is '{': parse full branch (stream selector + pipeline)
  │   │   └─ append to branches
  │   └─ else (no '{', inherits stream selector):
  │       ├─ clone sharedQuery.StreamSelector + pre-OR pipeline stages
  │       └─ parse branch-specific filters/pipeline
  │           └─ append branch Query to shared prefix → append to branches
  │
  │   // Parse tail: pipeline after all OR branches (e.g. | drop __error__)
  │   ├─ parse remaining pipeline stages
  │   └─ append tail to ALL branches
  │
  └─ return LogQLExpression{Branches: branches}
```

### 2.4 示例分解

输入：
```logql
{service_name="test-java-stock-service"} 
  | label_format log_line_contains_trace_id=`...`
  | log_line_contains_trace_id="true"
  OR trace_id="5d5f4cc370174374aefdeedff111973e"
  | drop __error__
```

分解结果（2 个分支）：

```
Branch 1:
  StreamSelector: {service_name="test-java-stock-service"}
  Pipeline: [label_format..., log_line_contains_trace_id="true", drop __error__]

Branch 2:
  StreamSelector: {service_name="test-java-stock-service"}          ← 继承
  Pipeline: [label_format..., trace_id="5d5f4cc...", drop __error__]
```

每个分支独立构造 ES 查询：

| 分支 | ES 查询 |
|------|--------|
| Branch 1 | `serviceName=test-java-stock-service` + `attributes.log_line_contains_trace_id="true"` |
| Branch 2 | `serviceName=test-java-stock-service` + `attributes.trace_id="5d5f4cc..."` |

### 2.5 向后兼容

现有 API 不变：

```go
// Parse 在无 OR 时行为不变；有 OR 时返回第一个分支（兼容调用方）
func Parse(input string) (*LogQLQuery, error) {
    p := &parser{input: input}
    branches, err := p.parseExpression()
    if err != nil {
        return nil, err
    }
    return branches[0], nil
}

// ParseExpression 对外暴露 OR 分支
func ParseExpression(input string) (*LogQLExpression, error) {
    p := &parser{input: input}
    branches, err := p.parseExpression()
    if err != nil {
        return nil, err
    }
    return &LogQLExpression{Branches: branches}, nil
}
```

同样，`MetricExpr` 扩展：

```go
type MetricExpr struct {
    Aggregation   string
    By            []string
    Function      string
    RangeDuration time.Duration
    
    // Inner: single branch (backward compatible, nil if OR branches used)
    Inner          *LogQLQuery
    // InnerBranches: OR branches inside metric function (nil if simple query)
    InnerBranches  []*LogQLQuery
}
```

### 2.6 结果合并（Merge）

```go
// mergeLogResults 合并多个分支的日志查询结果
func mergeLogResults(branchResults []*observabilitystorageext.LogSearchResult, 
                     limit int, direction string) *observabilitystorageext.LogSearchResult {
    // 1. 收集所有 entries
    // 2. 去重：按 entry.ObservedTimeUnixNano + entry.Body hash（简化版 ID）
    // 3. 排序：按 timestamp，direction="backward" 降序
    // 4. 截断：取前 limit 条
    // 5. 返回合并后的 LogSearchResult
}

// mergeMetricResults 合并多个分支的指标查询结果（matrix series）
func mergeMetricResults(branchResults []*observabilitystorageext.LogMetricResult) *observabilitystorageext.LogMetricResult {
    // 1. 收集所有 series
    // 2. 按 (labels + timestamp) 合并：相同组合的计数求和
    // 3. 返回合并后的 LogMetricResult
}
```

---

## 3. 文件结构

```
extension/adminext/logql/
├── ast.go                    # [修改] 新增 LogQLExpression，MetricExpr 新增 InnerBranches
├── parser.go                 # [修改] 新增 parseExpression()，保留 parse() 兼容
├── parser_test.go            # [修改] 新增 OR 解析测试用例
├── evaluator.go              # [不变] 对单个 LogQLQuery 求值

extension/adminext/
├── loki_handler.go           # [修改] handleLokiQuery 调用 ParseExpression，合并结果
├── loki_metric.go            # [修改] handleLokiMetricQuery 支持 InnerBranches
├── result_merger.go          # [新建] mergeLogResults + mergeMetricResults
├── result_merger_test.go     # [新建] 合并逻辑测试
```

---

## 4. 解析器详细设计

### 4.1 `parseExpression()`

```go
func (p *parser) parseExpression() ([]*LogQLQuery, error) {
    // Step 1: Parse shared prefix (stream selector + pre-OR filters/pipeline)
    shared, err := p.parse()
    if err != nil {
        return nil, err
    }
    
    branches := []*LogQLQuery{shared}
    
    // Step 2: Check for OR branches
    for p.peekKeyword("or") {
        p.advanceN(2)
        p.skipWhitespace()
        
        // Check if OR branch has its own stream selector
        if p.match('{') {
            // Full branch: {selector} | filters...
            branch, err := p.parse()
            if err != nil {
                return nil, err
            }
            branches = append(branches, branch)
        } else {
            // Shared prefix branch: inherits stream selector + pre-OR pipeline
            branch := p.buildSharedBranch(shared)
            
            // Parse branch-specific filters/pipeline
            for p.pos < len(p.input) {
                p.skipWhitespace()
                if p.pos >= len(p.input) {
                    break
                }
                // Stop at next OR or end
                if p.peekKeyword("or") {
                    break
                }
                if !p.match('|') && !p.match('!') {
                    break
                }
                
                // Parse filter or pipeline stage
                if p.isLineFilter() {
                    f, err := p.parseLineFilter()
                    if err != nil {
                        return nil, err
                    }
                    branch.LineFilters = append(branch.LineFilters, f)
                } else {
                    break // remaining pipeline will be parsed as tail
                }
            }
            branches = append(branches, branch)
        }
    }
    
    // Step 3: Parse tail pipeline (applied to ALL branches)
    tailPipeline, tailFilters := p.parsePipelineTail()
    if len(tailPipeline) > 0 || len(tailFilters) > 0 {
        for _, b := range branches {
            b.LineFilters = append(b.LineFilters, tailFilters...)
            b.Pipeline = append(b.Pipeline, tailPipeline...)
        }
    }
    
    return branches, nil
}
```

### 4.2 关键词匹配

```go
func (p *parser) peekKeyword(kw string) bool {
    saved := p.pos
    p.skipWhitespace()
    if p.pos+len(kw) <= len(p.input) && 
       strings.EqualFold(p.input[p.pos:p.pos+len(kw)], kw) {
        p.pos = saved
        return true
    }
    p.pos = saved
    return false
}

func (p *parser) isLineFilter() bool {
    if !p.match('|') && !p.match('!') {
        return false
    }
    if p.pos+1 < len(p.input) {
        next := p.input[p.pos+1]
        return next == '=' || next == '~'
    }
    return false
}
```

---

## 5. 设计原则体现

| 原则 | 体现 |
|------|------|
| **SRP** | `parseExpression` 负责拆分支，`resultMerger` 负责合并，互不干扰 |
| **OCP** | 新增 OR 支持不修改 `Evaluator`、`parse()` 内部逻辑 |
| **DIP** | `resultMerger` 依赖 `LogSearchResult`/`LogMetricResult` 接口类型 |
| **高内聚** | `parseExpression` + OR 解析逻辑集中在 parser 内 |
| **低耦合** | 分支解析、求值、合并三个步骤通过类型边界解耦 |
| **健壮性** | 任一分支失败不影响其他分支（partial success），清晰错误信息 |
| **可测试** | `parseExpression` 纯函数可测，`mergeResults` 纯函数可测，mock 分支结果可测 |

---

## 6. 测试设计

### 6.1 Parser 测试

| 输入 | 期望分支数 | 说明 |
|------|:--------:|------|
| `{a="1"} \|= "err" OR \|= "warn"` | 2 | 基础 OR |
| `{a="1"}` | 1 | 无 OR，向后兼容 |
| `{a="1"} \| json \| l="e" OR l="w" \| drop __err__` | 2 | 带 tail pipeline |
| `{a="1"} \| json \| l="e" OR {b="2"} \| logfmt` | 2 | 完整分支（后续迭代） |
| `{a="1"} \|~ "(?i)err" OR \|~ "(?i)warn"` | 2 | regex filter OR |

### 6.2 合并测试

| 场景 | 输入 | 期望 |
|------|------|------|
| 去重 | Branch1: [entry-A, entry-B]; Branch2: [entry-A, entry-C] | [A, B, C] (去重) |
| 排序 | mixed timestamps | 按 direction 排序 |
| 截断 | 合并后 > limit | 截取 limit 条 |
| 空结果 | 两个分支都空 | 空结果 |

---

## 7. 实施计划

| Sprint | 内容 | 验证 |
|--------|------|------|
| Sprint 1 | `LogQLExpression` + `parseExpression` + 单元测试 | parser 测试 |
| Sprint 2 | Handler 侧合并逻辑 + unit test | 合并测试 |
| Sprint 3 | Metric query OR 支持 | metric 端到端测试 |
| Sprint 4 | ES 端到端验证 | 真实 Grafana 请求 |

---

## 8. 遗留问题

1. **完整独立分支 OR**（`{a} \| f1 OR {b} \| f2`）：当前设计预留了 `p.match('{')` 分支，但完整 stream selector 解析需要额外测试验证。
2. **去重 ID 选取**：Loki 使用 entry timestamp + line hash 作为去重 key。我们可用 `observedTimeUnixNano + body` 作为近似的去重标识。
3. **性能**：OR 分支数 × ES 查询数。对于常见用户场景（2-3 个分支），增量可接受。
