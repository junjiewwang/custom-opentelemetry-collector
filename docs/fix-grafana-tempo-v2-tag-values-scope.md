# Fix: Grafana Tempo V2 Tag Values Scope 前缀未剥离

## 问题描述

Grafana 通过 Tempo 数据源请求 tag values 时返回空结果：

```
GET /api/v2/search/tag/resource.service.name/values?limit=5000
→ {"tagValues":null,"metrics":{"inspectedTraces":0,"inspectedBytes":"0"}}

GET /api/v2/search/tag/span.http.method/values?q=%7B%7D&limit=5000
→ {"tagValues":null,"metrics":{"inspectedTraces":0,"inspectedBytes":"0"}}
```

## 根因分析

### Grafana V2 Tag Name 格式

Grafana Tempo V2 API 使用 `{scope}.{tagKey}` 格式请求 tag values：
- `resource.service.name` = scope 为 `resource`，实际 key 为 `service.name`
- `span.http.method` = scope 为 `span`，实际 key 为 `http.method`

### Bug 位置

`handleTempoV2SearchTagValues` 直接将含 scope 前缀的 tagName 传给下游：

1. **`resolveTagValues("resource.service.name")`** — switch 匹配的是 `"service.name"` 而非 `"resource.service.name"`，走到 default 返回 `nil`
2. **`fetchTempoTagValues("resource.service.name")`** — 遍历 scope 拼接字段名：
   - scope="span" → `attributes.resource.service.name` (ES 中不存在)
   - scope="resource" → `resource.resource.service.name` (ES 中不存在)

### 正确的 ES 字段映射

```
scope=resource, tagKey=service.name → ES field: resource.service.name ✓
scope=span,     tagKey=http.method  → ES field: attributes.http.method ✓
```

## 修复方案

### 核心改动

在 `extension/adminext/tempo_handler.go` 中：

1. **新增 `parseScopedTagName` 函数** — 从 Grafana V2 格式 `{scope}.{key}` 中提取 scope 和 key
2. **更新 `handleTempoV2SearchTagValues`** — 调用 `parseScopedTagName` 解析后再分发
3. **更新 `fetchTempoTagValues` 签名** — 接受 scope 参数，若已知 scope 则只查该 scope

### 修改文件

| 文件 | 改动 |
|------|------|
| `extension/adminext/tempo_handler.go` | 新增 `parseScopedTagName`，更新 handler 和 fetch 函数 |
| `extension/adminext/tempo_handler_test.go` | 新增 `TestParseScopedTagName` 单元测试 |

### 请求处理流程（修复后）

```
Grafana 请求: /api/v2/search/tag/resource.service.name/values
         ↓
parseScopedTagName("resource.service.name") → scope="resource", key="service.name"
         ↓
resolveTagValues(r, "service.name") → 匹配 case "service.name" → 返回 services 列表 ✓
```

```
Grafana 请求: /api/v2/search/tag/span.http.method/values
         ↓
parseScopedTagName("span.http.method") → scope="span", key="http.method"
         ↓
resolveTagValues(r, "http.method") → default → nil (无快捷路径)
         ↓
fetchTempoTagValues(r, "http.method", "span") → 查询 ES: attributes.http.method ✓
```

## 状态

- [x] 修复实现
- [x] 单元测试通过
- [ ] 部署验证
