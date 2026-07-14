# Fix: Root Span 查询返回空结果

## 问题描述

TraceQL 查询 `{nestedSetParent<0 && true}` 通过 Tempo 兼容 API (`/api/search`) 查询时返回空结果 `{"traces":[]}` 。

## 根因分析

### 架构层级

| 层 | 包 | 职责 |
|---|---|---|
| 存储转换层 | `storedmodel` | OTLP → StoredSpan 转换 (write path) |
| 查询模型层 | `observabilitystorageext` | 定义 TraceQuery（Provider 无关） |
| 存储实现层 | `provider/elasticsearch` | 将 TraceQuery 翻译为 ES DSL (read path) |

### 根因

**Reader 的 ES 查询语义错误**：

- `TraceQuery.IsRoot = true` 时，`buildTraceSearchQuery` 生成 `term(parentSpanId, "")`
- 但 `StoredSpan.ParentSpanID` 有 `omitempty` tag — root span 的 parentSpanId 为空字符串，JSON 序列化时**字段不存在**
- ES `keyword` 类型字段不存在时，`term` 查询匹配空字符串 `""` 是**无法命中**的
- 正确做法应该用 `must_not { exists { field: parentSpanId } }`

### 附带发现

`toParentID` 中有一个死代码：比较 15 个零字符 `"000000000000000"`，但 `pcommon.SpanID.String()` 对全零值直接返回 `""`（不是 16 个字符的 hex 表示）。修正为 16 个零作为防御性检查。

## 修复方案

### 设计原则

- **每层修自己的问题**：writer 修防御性代码，reader 修查询语义
- **高内聚低耦合**：reader 不需要知道 writer "曾经有 bug"，它只需要正确表达 "root span 在 ES 中的查询方式"
- **可独立测试**：两个修复各自有独立单元测试
- **健壮性/兼容性**：reader 的 `should` 查询同时覆盖"字段不存在"和"字段值为零"两种情况

### 修改清单

| 文件 | 修改内容 |
|------|----------|
| `storedmodel/stored_span.go` | `toParentID`: 15个0 → 16个0（防御性修复） |
| `provider/elasticsearch/query/builder.go` | 新增 `MustNot`、`ExistsQ`、`MustNotQ` 便捷函数 |
| `provider/elasticsearch/trace_reader.go` | `IsRoot` 查询: `term("")` → `should(must_not(exists), term("0000000000000000"))` |
| `storedmodel/stored_span_test.go` | 新增 `TestToParentID` 测试 |
| `provider/elasticsearch/trace_reader_query_test.go` | 新增 `TestBuildTraceSearchQuery_IsRoot*` 测试 |

### ES 查询变更

**Before:**
```json
{"term": {"parentSpanId": ""}}
```

**After:**
```json
{
  "bool": {
    "should": [
      {"bool": {"must_not": [{"exists": {"field": "parentSpanId"}}]}},
      {"term": {"parentSpanId": "0000000000000000"}}
    ],
    "minimum_should_match": 1
  }
}
```

## 测试验证

- `TestToParentID` — 验证 writer 对 root span 的转换正确性
- `TestBuildTraceSearchQuery_IsRoot` — 验证 ES DSL 包含正确的 exists/term 条件
- `TestBuildTraceSearchQuery_IsRoot_StructureCheck` — 验证 DSL 结构完整性
- `TestBuildTraceSearchQuery_NotRoot` — 验证非 root 查询不添加额外过滤

## 状态

- [x] 根因分析
- [x] 修复 toParentID 防御性代码
- [x] 新增 esq.ExistsQ / MustNotQ 查询构建函数
- [x] 修复 reader 的 IsRoot 查询语义
- [x] 单元测试通过
- [ ] 集成测试（需要 ES 环境）
- [ ] 部署验证
