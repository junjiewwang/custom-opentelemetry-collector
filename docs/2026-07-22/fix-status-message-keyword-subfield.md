# Fix: status.message 添加 .keyword 子字段以支持 terms aggregation

## 需求背景

Grafana TraceQL 查询 `{statusMessage != nil} | rate() by(statusMessage)` 无法返回数据。

**根因**：`status.message` 在 ES 中是 `text` 类型（由动态映射自动分配），没有 `.keyword` 子字段。`text` 字段无法做 terms aggregation（ES 报错：`Text fields are not optimised for operations that require per-document field data`）。

尝试 `fielddata=true` 后 terms agg 返回的是**分词 token**（"失"、"败"、"failed"、"send"），非完整消息，不满足需求。

## 解决方案

**方案 A（推荐）**：在 ES 索引模板中为 `status.message` 添加 `fields.keyword` 多字段子映射。由于其他按需处理，仅需再追加一个 `.keyword` 子字段。

- `status.message`（text）→ 全文搜索（match query）
- `status.message.keyword`（keyword）→ 精确匹配 + terms aggregation

## ES 实际验证

**环境**：`9.134.106.132:9200`，索引 `otel-traces-CIKmanlvG3sfdnpr-2026.07.22`

### 1. 添加 .keyword 子字段映射

```bash
PUT /otel-traces-CIKmanlvG3sfdnpr-2026.07.22/_mapping
{
  "properties": {
    "status": {
      "properties": {
        "message": {
          "type": "text",
          "fields": {
            "keyword": { "type": "keyword", "ignore_above": 256 }
          }
        }
      }
    }
  }
}
```

→ `{"acknowledged": true}` ✅

### 2. 写入测试文档

```bash
POST /otel-traces-CIKmanlvG3sfdnpr-2026.07.22/_doc
{
  "status": { "message": "配送认证失败", "code": "Error" }
}
```

→ `"result": "created"` ✅

### 3. 验证 terms aggregation

```bash
GET /otel-traces-CIKmanlvG3sfdnpr-2026.07.22/_search
{
  "aggs": {
    "by_msg": { "terms": { "field": "status.message.keyword", "size": 20 } }
  }
}
```

**结果**：

```
"配送认证失败" : 1
"配送失败"     : 1
```

✅ 返回完整消息，非分词 token。

### 4. 与其他方案对比

| 方案 | 方法 | terms agg 结果 | 正确性 |
|------|------|---------------|--------|
| A（推荐） | `status.message.keyword` | "配送认证失败" | ✅ 完整消息 |
| B | 应用层聚合（Go 端 map） | — | ✅ 数据量 ≤ 10000 |
| C（不推荐） | `fielddata=true` | "失", "败", "failed"… | ❌ 分词 token |

## 实施步骤

### Step 1：查询层 — AttributeResolver + buildMetricsAggTree

`buildMetricsAggTree` 中 `by(statusMessage)` 当前使用 `resolver.Resolve("statusMessage").ESField` → `"status.message"`（text 字段）。

修复：对 `statusMessage` / `status.message` 的 `by()` 聚合使用 `status.message.keyword`。

**文件**：`extension/observabilitystorageext/provider/elasticsearch/trace_metrics.go`

在 `buildMetricsAggTree` 中：
```go
resolver := &AttributeResolver{}
for i := len(query.ByLabels) - 1; i >= 0; i-- {
    label := query.ByLabels[i]
    field := resolveAggField(resolver, label)
    // ...
}

// resolveAggField 返回正确的 ES 聚合字段
func resolveAggField(resolver *AttributeResolver, label string) string {
    resolved := resolver.Resolve(label)
    // status.message 是 text 字段，需要用 .keyword 子字段做 terms agg
    if resolved.ESField == FieldStatus+".message" {
        return resolved.ESField + ".keyword"
    }
    return resolved.ESField
}
```

### Step 2：索引模板 — 添加 .keyword 多字段

在索引模板中添加 `status.message.keyword` 多字段定义，确保新写入的文档自动生成 keyword 子字段。

**文件**：`extension/observabilitystorageext/provider/elasticsearch/trace_writer.go`（或模板初始化代码）

在 trace index template 的 mappings 中：
```json
{
  "status": {
    "properties": {
      "code": { "type": "keyword" },
      "message": {
        "type": "text",
        "fields": {
          "keyword": { "type": "keyword", "ignore_above": 256 }
        }
      }
    }
  }
}
```

### Step 3（可选）：Reindex 旧索引

更新模板后，新写入的文档自动生效。旧数据在 `_update_by_query` 后也能填充 `.keyword`：

```bash
POST /otel-traces-*/_update_by_query?refresh
```

或者直接重建下游索引。

### 变更文件

| 文件 | 变更 | 说明 |
|------|------|------|
| `extension/.../elasticsearch/trace_metrics.go` | `buildMetricsAggTree` 加 `resolveAggField` | `by(statusMessage)` → `status.message.keyword` |
| `extension/.../elasticsearch/trace_writer.go` 或模板代码 | 模板 mapping 加 `status.message.keyword` | 新文档自动有 keyword 子字段 |

## 状态

- [x] ES 验证通过（实际集群测试）
- [x] 方案对比完成（A/B/C 三方案）
- [x] Step 1 实施：`by(statusMessage)` → `status.message.keyword`
- [x] Step 2 实施：索引模板加 `.keyword` 多字段
- [x] 全量编译 + 测试通过（4 个新单元测试 + ES 验证）

### 实施摘要

**变更文件（2 个）**：

| 文件 | 变更 |
|------|------|
| `extension/.../elasticsearch/trace_metrics.go` | +`metricsAggField()` helper，`buildMetricsAggTree` 用 `.keyword` 子字段 |
| `extension/.../elasticsearch/admin.go` | 索引模板中 `status.message` 加 `keywords.Fields` 多字段映射 |
| `extension/.../elasticsearch/trace_metrics_test.go` | +4 个单元测试 |

**ES 验证**：

| 步骤 | 结果 |
|------|------|
| 索引模板更新 | `acknowledged: true` ✅ |
| 写入测试文档 `status.message = "模板验证-配送超时"` | `"result": "created"` ✅ |
| `terms agg on status.message.keyword` | `"模板验证-配送超时": 1` ✅ 完整消息 |
