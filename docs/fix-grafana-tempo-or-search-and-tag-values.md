# 修复 Grafana Tempo OR 搜索 & Tag Values 时间窗口

## 需求背景

Grafana Explore 使用 Tempo 数据源时，发送包含 `||` (OR) 条件的 TraceQL 查询，以及 tag values 发现请求，均返回空结果。

### 复现请求

1. **OR 搜索请求**（返回 `inspectedTraces: 0`）：
   ```
   GET /api/search?q={(span.span.kind="internal" || span.span.kind="client" || span.span.kind="server" || span.span.kind="producer" || span.span.kind="consumer")}&limit=20&spss=3&start=1783929463&end=1783933063
   ```

2. **Tag Values 请求**（返回 `tagValues: null`）：
   ```
   GET /api/v2/search/tag/span.rpc.system/values?q={}&limit=5000
   ```

## 问题根因

### 问题 1：OR 条件不支持

- `parseTempoSearchParams` 调用 `parseTraceQL()` 处理 `q` 参数
- `parseTraceQL` 只按 `&&` 分割条件，不支持 `||` 语法和括号分组
- Grafana 发送 `{(cond1 || cond2 || ...)}` 时：
  - 整个括号内容被当作一个 token
  - 解析出畸形的 key=`(span.span.kind`，value=`"internal" || ...)`
  - ES 查询匹配不到任何文档

### 问题 2：Tag Values 默认时间窗口太短

- `fetchTempoTagValues` 使用 `parseTempoTimeRange(r)` 获取时间范围
- 当 Grafana 不传 `start`/`end` 时，默认只查最近 1 小时
- Tag values 接口用于发现/自动补全，应覆盖更大范围

## 实施方案

### 修复 1：支持 TraceQL OR 条件

**修改文件**：

| 文件 | 改动 |
|------|------|
| `extension/observabilitystorageext/types.go` | `TraceQuery` 添加 `TagsOr []map[string]string` 字段 |
| `extension/observabilitystorageext/storedmodel/trace_query.go` | 同上 |
| `extension/adminext/tempo_handler.go` | `parseTempoSearchParams` 改用 `parseTraceQLOrFilter`；新增 `stripOuterParens` 处理括号包裹 |
| `extension/observabilitystorageext/reader_adapter.go` | adapter 传递 `TagsOr` 字段 |
| `extension/observabilitystorageext/provider/elasticsearch/trace_reader.go` | `buildTraceSearchQuery` 支持 `TagsOr` 生成 ES `bool.should` |

**核心逻辑**：

1. `parseTraceQLOrFilter` 已有 OR 解析能力（用于 metrics 查询），复用到 search 路径
2. 新增 `stripOuterParens` 处理 Grafana 格式 `{(cond1 || cond2)}`
3. `buildTraceSearchQuery` 中 TagsOr 生成嵌套 `bool.should`，每个 OR 组内部仍用 AND

**生成的 ES 查询结构**：
```json
{
  "bool": {
    "must": [
      { "range": { "startTimeUnixNano": { "gte": ..., "lte": ... } } },
      {
        "bool": {
          "should": [
            { "bool": { "must": [{"bool": {"should": [{"term": {"attributes.span.kind": "internal"}}, {"term": {"resource.span.kind": "internal"}}], "minimum_should_match": 1}}] } },
            { "bool": { "must": [{"bool": {"should": [{"term": {"attributes.span.kind": "client"}}, {"term": {"resource.span.kind": "client"}}], "minimum_should_match": 1}}] } },
            ...
          ],
          "minimum_should_match": 1
        }
      }
    ]
  }
}
```

### 修复 2：优化 Tag Values 默认时间窗口

**修改文件**：`extension/adminext/tempo_handler.go`

新增 `parseTempoTagValuesTimeRange` 函数：
- 有显式 `start`/`end` 参数 → 使用用户指定范围
- 无显式参数 → 默认 7 天（而非 1 小时）

### 修复 3：Tag Values V2 接口支持 `q` 参数过滤

**问题**：`/api/v2/search/tag/{tagName}/values?q={resource.service.name="tapm-api"}` 中 `q` 参数的过滤条件被忽略，`GetTagValues` 只用时间范围过滤，导致返回所有服务的 tag values 而非仅限指定服务的。

**修改文件**：

| 文件 | 改动 |
|------|------|
| `extension/observabilitystorageext/provider.go` | `GetTagValues` 接口增加 `filterTags map[string]string` 参数 |
| `extension/observabilitystorageext/provider/elasticsearch/trace_reader.go` | `GetTagValues` 使用 `filterTags` 构建额外的 `bool.filter` 条件 |
| `extension/observabilitystorageext/reader_adapter.go` | 传递 `filterTags` |
| `extension/observabilitystorageext/pg_reader_adapter.go` | 签名同步 |
| `extension/adminext/tempo_handler.go` | `handleTempoV2SearchTagValues` 解析 `q` 参数；`fetchTempoTagValues` 接收并传递 `filterTags`；新增 `parseTagValuesFilter` |

**核心逻辑**：

1. `parseTagValuesFilter(r)` 从请求的 `q` 参数中解析出 AND 过滤条件
2. 当有过滤条件时，跳过 V1 快速路径（因为快速路径不支持条件过滤）
3. `GetTagValues` 的 ES 查询中加入 filter 条件，确保聚合只在匹配的文档上执行

**生成的 ES 查询结构**（带 `q={resource.service.name="tapm-api"}`）：
```json
{
  "bool": {
    "must": [
      { "range": { "startTimeUnixNano": { "gte": ..., "lte": ... } } },
      {
        "bool": {
          "should": [
            { "term": { "attributes.service.name": "tapm-api" } },
            { "term": { "resource.service.name": "tapm-api" } }
          ],
          "minimum_should_match": 1
        }
      }
    ]
  }
}
```

## 验证

- [x] 编译通过
- [x] 全部现有单元测试通过（无回归）
- [x] 新增单元测试 `TestParseTraceQLOrFilter` — 覆盖 Grafana OR 格式、无括号 OR、AND、空查询
- [x] 新增单元测试 `TestStripOuterParens` — 覆盖完整包裹、非完整包裹、无括号等边界情况
- [x] 新增单元测试 `TestParseTagValuesFilter` — 覆盖 service name 过滤、多条件 AND、空参数

## 变更记录

| 日期 | 内容 | 状态 |
|------|------|------|
| 2026-07-13 | 实施 OR 搜索支持 + Tag Values 时间窗口优化 | ✅ 已完成 |
| 2026-07-13 | Tag Values V2 接口支持 `q` 参数过滤条件 | ✅ 已完成 |
