# Fix: Resource-Scoped 属性过滤 Scope 信息丢失导致查询无数据

## 问题描述

Grafana 发送的 TraceQL 查询包含 `resource.app_id` 过滤条件时返回空数据：

```json
{
  "query": "{nestedSetParent<0 && resource.app_id=\"z1l8pFk0vpogGHVL\"} | rate() with(sample=true)",
  "queryType": "traceql"
}
```

所有包含 `resource.app_id` 的查询（`rate()`、`histogram_over_time(duration)`、`rate() by(resource.service.name)`）均返回空结果。

## 根因分析

### 数据流

```
TraceQL: resource.app_id="z1l8pFk0vpogGHVL"
    ↓ Parser
Condition{ Scope: "resource", Key: "app_id", Value: "z1l8pFk0vpogGHVL" }
    ↓ Planner (extractCondition default 分支)
p.Tags["app_id"] = "z1l8pFk0vpogGHVL"   ← 🐛 scope 信息丢失！
    ↓ buildMetricsFilter
resolver.Resolve("app_id")
    ↓ AttributeResolver
parseScopeAndKey("app_id") → scope="", key="app_id"
resolveWithScope("", "app_id") → "attributes.app_id"  ← ❌ 错误字段！
```

### Bug 位置

`extension/adminext/traceql/planner.go` 的 `extractCondition` default 分支：

```go
// 修复前：scope 信息被丢弃
p.Tags[key] = valStr  // key="app_id"，丢失了 scope="resource"
```

### 正确的 ES 字段映射

| TraceQL 表达式 | 期望 ES 字段 | 修复前实际 ES 字段 |
|---|---|---|
| `resource.app_id="xxx"` | `resource.app_id` | `attributes.app_id` ❌ |
| `resource.cluster!="staging"` | `resource.cluster` | `attributes.cluster` ❌ |
| `span.http.method="GET"` | `attributes.http.method` | `attributes.http.method` ✅ |

## 修复方案

### 核心思路

在 planner 中存入 Tags 时保留 scope 前缀（如 `"resource.app_id"`），这样 `AttributeResolver.Resolve("resource.app_id")` 能正确解析 scope 并映射到 ES 字段 `"resource.app_id"`。

### 新增 helper 函数

```go
// scopedKey returns "scope.key" if scope is non-empty, otherwise just "key".
func scopedKey(scope, key string) string {
    if scope != "" {
        return scope + "." + key
    }
    return key
}
```

### 修改点

| 位置 | 修改内容 |
|------|----------|
| `extractCondition` default 分支 | `p.Tags[key]` → `p.Tags[scopedKey(cond.Scope, key)]`，同理 TagsNot/TagsExists/TagsRegex |
| `extractFromSpanFilter` OrGroups | `branchMap[cond.Key]` → `branchMap[scopedKey(cond.Scope, cond.Key)]` |
| `extractOrConditions` | `m[cond.Key]` → `m[scopedKey(cond.Scope, cond.Key)]` |

### 修复后数据流

```
TraceQL: resource.app_id="z1l8pFk0vpogGHVL"
    ↓ Parser
Condition{ Scope: "resource", Key: "app_id" }
    ↓ Planner (extractCondition default + scopedKey)
p.Tags["resource.app_id"] = "z1l8pFk0vpogGHVL"   ← ✅ 保留 scope
    ↓ buildMetricsFilter
resolver.Resolve("resource.app_id")
    ↓ AttributeResolver
parseScopeAndKey("resource.app_id") → scope="resource", key="app_id"
resolveWithScope("resource", "app_id") → "resource.app_id"  ← ✅ 正确字段
```

## 修改文件

| 文件 | 改动 |
|------|------|
| `extension/adminext/traceql/planner.go` | 新增 `scopedKey` helper，修复 3 处 scope 丢失 |
| `extension/adminext/traceql/traceql_test.go` | 新增 4 个 resource-scoped 测试，更新 1 个 span-scoped 测试 |
| `extension/adminext/tempo_handler_test.go` | 更新 2 个 span-scoped 属性的期望值 |

## 同步修复：histogram_over_time 单位转换 Bug

在排查过程中还修复了 `unitconv` 包的一个 bug：

- **问题**：`histogram_over_time(duration)` 返回的是 span 计数值（value_count 聚合），不是 duration 值，不应做纳秒→秒的转换
- **修复**：从 `IsDurationFunction` 中移除 `histogram_over_time`
- **文件**：`extension/observabilitystorageext/unitconv/unitconv.go` + `unitconv_test.go`

## 状态

- [x] 修复 IsDurationFunction 移除 histogram_over_time
- [x] 修复 planner scope 丢失
- [x] 更新所有相关单元测试
- [x] 全量测试通过
- [ ] 部署验证（需要构建镜像并部署到集群）

## 遗留问题

1. **部署验证**：需要构建 Docker 镜像部署到 minikube 集群验证实际查询返回数据
2. **EventTags**：event scope 的属性目前不带 scope 前缀（`p.EventTags[key]`），因为 event 有独立的存储和处理路径，当前逻辑正确
