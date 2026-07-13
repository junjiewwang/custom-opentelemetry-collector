# Fix: Grafana Tempo V2 Tag Values — Intrinsic 字段带 Filter 返回空

## 问题描述

Grafana 通过 Tempo 数据源请求 `name` tag 的 values，同时附带 `q` filter 参数时返回空结果：

```
GET /api/v2/search/tag/name/values?q=%7Bresource.service.name%3D%22tapm-api%22%7D&limit=5000
→ {"tagValues":null,"metrics":{"inspectedTraces":0,"inspectedBytes":"0"}}
```

**注意**：不带 `q` 参数时（`/api/v2/search/tag/name/values`）能正常返回数据。

## 根因分析

### 执行流程（Bug 路径）

```
请求: /api/v2/search/tag/name/values?q={resource.service.name="tapm-api"}
         ↓
parseScopedTagName("name") → scope="", tagKey="name"
         ↓
filterTags = {"service.name": "tapm-api"} (非空)
         ↓
len(filterTags) != 0 → 跳过 V1 fast path（跳过了 resolveTagValues 中对 "name" 的特殊处理）
         ↓
fetchTempoTagValues(r, "name", "", filterTags)
         ↓
scope="" → 尝试 ["span", "resource"]
         ↓
GetTagValues("name", ..., "span", ...) → ES 查询聚合字段: attributes.name → 不存在！
GetTagValues("name", ..., "resource", ...) → ES 查询聚合字段: resource.name → 不存在！
         ↓
两次都返回空 → tagValues: null
```

### 核心问题

`name` 是一个 **intrinsic 字段**（span name / operation name），存储在 ES 顶层字段 `name`，
而**不是** `attributes.name` 或 `resource.name`。

- **无 filter 时**：V1 fast path 的 `resolveTagValues` 走 `case "name"` → `fetchAllOperations` → 正确
- **有 filter 时**：fast path 被跳过，直接走通用的 `fetchTempoTagValues` → 查 `attributes.name` → 空

### Intrinsic 字段到 ES 字段的映射

| Tempo Intrinsic | ES 字段 | 正确获取方式 |
|---|---|---|
| `name` | `name` (顶层) | `GetOperations` terms 聚合 |
| `kind` | `kind` (顶层) | 静态列表 |
| `status` | `status` (顶层) | 静态列表 |
| `duration` | `durationNano` (顶层) | 不返回值 |

## 修复方案

### 核心改动

在 `extension/adminext/tempo_handler.go` 中：

1. **新增 `resolveIntrinsicTagValuesWithFilter` 方法** — 在 `fetchTempoTagValues` 之前拦截 intrinsic 字段
2. 对 `"name"` tag：根据 filterTags 中的 `service.name` 调用 `GetOperations` 获取该 service 的操作名
3. 对 `"kind"` / `"status"` 等静态值 tag：直接返回静态列表（filter 不影响可选值）

### 修改文件

| 文件 | 改动 |
|------|------|
| `extension/adminext/tempo_handler.go` | 新增 `resolveIntrinsicTagValuesWithFilter`，在 handler 中调用 |

### 请求处理流程（修复后）

```
请求: /api/v2/search/tag/name/values?q={resource.service.name="tapm-api"}
         ↓
parseScopedTagName("name") → scope="", tagKey="name"
         ↓
filterTags = {"service.name": "tapm-api"} (非空)
         ↓
len(filterTags) != 0 → 跳过 V1 fast path
         ↓
resolveIntrinsicTagValuesWithFilter(r, "name", filterTags)
         ↓
tagKey == "name" → filterTags 中有 "service.name"="tapm-api"
         ↓
GetOperations(ctx, "tapm-api", timeRange) → ES terms 聚合顶层 "name" 字段，filter by serviceName ✓
         ↓
返回 tapm-api 服务的所有 operation names ✓
```

### 方案优点

1. **不修改 TraceReader 接口** — 复用已有的 `GetOperations` 方法
2. **精准过滤** — 有 service.name filter 时只返回该 service 的 operations（比无 filter 时的全量查询更高效）
3. **向后兼容** — 非 intrinsic 字段仍走原有的通用路径
4. **可扩展** — 未来如需支持其他 intrinsic 字段带 filter，只需在 switch 中添加 case

## 状态

- [x] 根因分析
- [x] 修复实现
- [x] 编译通过
- [ ] 部署验证
