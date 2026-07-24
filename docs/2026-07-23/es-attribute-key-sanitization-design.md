# ES Attribute Key Sanitization 设计方案

> 文档创建日期：2026-07-23
> 状态：已实施
> 关联问题：`mapper_parsing_exception: object mapping for [attributes.peer.service] tried to parse field as object, but found a concrete value`

---

## 实施记录 (2026-07-23)

### 改动文件清单

| 文件 | 操作 | 改动说明 |
|------|------|---------|
| `storedmodel/sanitize_key.go` | 新建 | `SanitizeKey()` 纯函数 |
| `storedmodel/sanitize_key_test.go` | 新建 | 20 个测试用例 + 幂等性 + 首段保留测试 |
| `storedmodel/stored_span.go:152` | 修改 | `pcommonMapToFlat()` 调用 `SanitizeKey(k)` |
| `elasticsearch/trace_reader.go:690` | 修改 | `resolveTagFieldPaths()` sanitize plainKey |
| `elasticsearch/trace_reader.go:718` | 修改 | `resolveTagESFields()` sanitize plainKey |
| `elasticsearch/label_translator.go:101` | 修改 | `translateLabelKey()` 统一应用 SanitizeKey |
| `elasticsearch/metric_reader.go:281` | 修改 | `ListLabelValues()` sanitize label |
| `elasticsearch/label_translator_test.go` | 修改 | 更新 2 个测试用例的期望值（3段 key → sanitized） |

### 翻译逻辑影响分析

`translateLabelKey()` 是 metric 查询中 Prometheus key → OTel key → ES field path 的 **唯一转换点**。在此处统一应用 `SanitizeKey` 后：

| key | Prometheus | translateLabelKey 输出 | ES field path |
|-----|-----------|----------------------|---------------|
| `peer_service` | ✅ | `peer.service` (2段, unchanged) | `labels.peer.service` |
| `http_method` | ✅ | `http.method` (2段, unchanged) | `labels.http.method` |
| `rpc_grpc_status_code` | ✅ | `rpc.grpc_status_code` (3段→sanitized) | `labels.rpc.grpc_status_code` |
| `net_peer_name` | ✅ | `net.peer_name` (3段→sanitized) | `labels.net.peer_name` |

所有已知 Prometheus label key 均为 1-2 段，仅 `rpc_grpc_status_code` 和 `net_peer_name` 的 OTel 对应 key 为 3 段。其余不受影响。

自定义 key 不受影响（无点号，从 Prometheus 传入已是 `_` 格式）。

### 测试结果

```
ok  extension/observabilitystorageext/storedmodel        (SanitizeKey tests)
ok  extension/observabilitystorageext/provider/elasticsearch  (label_translator tests)
ok  extension/adminext
ok  extension/adminext/logql
ok  extension/adminext/traceql
```

全部测试通过，无回归。

---

## 1. 问题陈述

### 1.1 错误现象

```
mapper_parsing_exception: object mapping for [attributes.peer.service]
tried to parse field [peer.service] as object, but found a concrete value
```

516 / 606 条 trace 写入失败（**85% 失败率**），大量数据丢失。

### 1.2 根因

ES `attributes` 字段使用 `dynamic: true` 映射，ES 将 JSON key 中的 `.` 解释为嵌套路径分隔符。

| 写入顺序 | 文档 attribute key | ES mapping 行为 | 结果 |
|---------|-------------------|----------------|------|
| 先写入 | `peer.service.source` | 创建 `attributes.peer`(对象) → `service`(对象) → `source`(keyword) | ✅ 正常 |
| 后写入 | `peer.service` | 尝试写入 `attributes.peer.service`(已为对象) = 字符串 | ❌ 冲突 |

**核心矛盾**：ES 中 `peer.service`（2 段点号 key）与 `peer.service.source`（3 段点号 key）产生嵌套层级歧义——前者期望 `peer.service` 是叶子节点，后者要求它是中间对象节点。

### 1.3 目标

- 消除所有因点号 key 导致的 ES mapping 冲突
- 写入侧和读取侧的 key 转换一致且可预测
- 改动最小化，不修改 ES template

---

## 2. 核心算法

### 2.1 规则

```
1 段：保持不变          (e.g. "kind"      → "kind")
2 段：保持不变          (e.g. "http.method" → "http.method")
≥3 段：保留第一个 .，其余替换为 _ (e.g. "peer.service.source" → "peer.service_source")
```

### 2.2 伪代码

```go
func SanitizeKey(key string) string {
    // 找到第一个 . 的位置
    firstDot := strings.IndexByte(key, '.')
    if firstDot < 0 {
        return key  // 1 段，无点号
    }
    // 查找第二个点号
    secondDot := strings.IndexByte(key[firstDot+1:], '.')
    if secondDot < 0 {
        return key  // 2 段，不变
    }
    // ≥3 段：保留第一个 .，后续点号替换为 _
    prefix := key[:firstDot+secondDot+1]   // "peer.service"
    suffix := key[firstDot+secondDot+2:]   // "source"
    return prefix + "_" + strings.ReplaceAll(suffix, ".", "_")
}
```

### 2.3 示例

| 输入 | 输出 | ES 嵌套深度 | 冲突消除？ |
|------|------|:----------:|:---------:|
| `kind` | `kind` | 1 | ✅ |
| `http.method` | `http.method` | 2 | ✅ |
| `peer.service` | `peer.service` | 2 | ✅ |
| `peer.service.source` | `peer.service_source` | 2 | ✅ |
| `db.operation.name` | `db.operation_name` | 2 | ✅ |
| `net.peer.name` | `net.peer_name` | 2 | ✅ |
| `a.b.c.d` | `a.b_c_d` | 2 | ✅ |

**关键**：所有 key 的 ES 嵌套深度 ≤ 2，相同第一段（如 `peer`）的 key 都是兄弟节点，不再有父子层级冲突。

### 2.4 确定性保证

```
∀ key: SanitizeKey(key) 总是返回相同结果
∀ query: resolveTagESFields(SanitizeKey(key)) 路径 = ES 中的存储路径
```

不存在运行时状态依赖，多实例天然安全。

---

## 3. 改动设计

### 3.1 改写侧：`pcommonMapToFlat()` + 递归

**文件**：`extension/observabilitystorageext/storedmodel/stored_span.go`（行 152-163）

```go
// 改动前
func pcommonMapToFlat(attrs pcommon.Map) map[string]any {
    result := make(map[string]any, attrs.Len())
    attrs.Range(func(k string, v pcommon.Value) bool {
        result[k] = valueToAny(v)         // 原始 key，含点号
        return true
    })
    return result
}

// 改动后
func pcommonMapToFlat(attrs pcommon.Map) map[string]any {
    result := make(map[string]any, attrs.Len())
    attrs.Range(func(k string, v pcommon.Value) bool {
        result[SanitizeKey(k)] = valueToAny(v)  // sanitize key
        return true
    })
    return result
}
```

`valueToAny()` 中 `pcommon.ValueTypeMap` 分支递归调用 `pcommonMapToFlat()`（行 176-177），无需额外改动——嵌套 Map 的属性 key 也会被自动 sanitize。

### 3.2 影响范围：此单一入口覆盖全部属性转换

`pcommonMapToFlat()` 是**三个信号类型共用的单入口**：

| 调用点 | 信号 | 数据结构 |
|--------|------|---------|
| `stored_span.go:114` | Trace | `span.Attributes` |
| `stored_span.go:115` | Trace | `resource.Attributes` |
| `stored_span.go:112` | Trace | `scope.Scope().Attributes()` |
| `stored_span.go:220` | Trace | `event.Attributes` |
| `stored_span.go:238` | Trace | `link.Attributes` |
| `stored_log.go:40` | Log | `lr.Attributes` |
| `stored_log.go:41` | Log | `resource.Attributes` |
| `stored_metric.go:40` | Metric | `resource.Attributes` |
| `stored_metric.go:67,89,107` | Metric | `dp.Attributes` (→ `Labels`) |

**改写侧改动量**：1 个函数内部 + 1 个新增工具函数，约 20 行。

### 3.3 读改侧：`resolveTagESFields()` + `resolveTagFieldPaths()`

**文件**：`extension/observabilitystorageext/provider/elasticsearch/trace_reader.go`

两个函数在构造通用 attribute 的 ES 查询路径时，拼接 `FieldAttributes + "." + plainKey`。需要将 `plainKey` 也 sanitize：

```go
// resolveTagESFields (行 718)
// 改动前：
return []string{FieldAttributes + "." + plainKey, FieldResource + "." + plainKey}, val
// 改动后：
sanitized := storedmodel.SanitizeKey(plainKey)
return []string{FieldAttributes + "." + sanitized, FieldResource + "." + sanitized}, val

// resolveTagFieldPaths (行 690)
// 改动前：
return []string{FieldAttributes + "." + plainKey, FieldResource + "." + plainKey}
// 改动后：
sanitized := storedmodel.SanitizeKey(plainKey)
return []string{FieldAttributes + "." + sanitized, FieldResource + "." + sanitized}
```

**读侧改动量**：2 处，约 4 行。

### 3.4 不需要改动的文件

| 文件 | 原因 |
|------|------|
| `label_translator.go` | 映射的是 Prometheus label → OTel key，由消费方 sanitize |
| `log_reader.go` | `logLabelFieldMap` 映射的是顶级字段（`serviceName` 等），不经 `attributes.*` 路径 |
| `admin.go` (ES template) | `dynamic: true` 的行为满足 ≤2 层嵌套需求 |

---

## 4. 文件结构

```
extension/observabilitystorageext/storedmodel/
├── stored_span.go            # [修改] pcommonMapToFlat + 新增 SanitizeKey
├── sanitize_key.go           # [新建] SanitizeKey() + 单元测试
├── sanitize_key_test.go      # [新建] 单元测试

extension/observabilitystorageext/provider/elasticsearch/
├── trace_reader.go           # [修改] 2 处 resolve* 函数加 sanitize
```

**共 4 文件**：1 新建 + 2 修改 + 1 测试。

---

## 5. 测试设计

### 5.1 `SanitizeKey()` 单元测试

```go
func TestSanitizeKey(t *testing.T) {
    tests := []struct {
        input    string
        expected string
    }{
        {"", ""},
        {"kind", "kind"},
        {"http.method", "http.method"},          // 2段不变
        {"peer.service", "peer.service"},        // 2段不变
        {"peer.service.source", "peer.service_source"},       // 3段
        {"db.operation.name", "db.operation_name"},           // 3段
        {"net.peer.port", "net.peer_port"},                   // 3段
        {"a.b.c.d", "a.b_c_d"},                               // 4段
        {"rpc.grpc.status_code", "rpc.grpc_status_code"},     // 3段
        {"messaging.destination.name", "messaging.destination_name"},// 3段
        {"..", ".."},                                          // 边界
        {"...", ".._"},                                        // 边界
    }
    for _, tt := range tests {
        t.Run(tt.input, func(t *testing.T) {
            got := SanitizeKey(tt.input)
            if got != tt.expected {
                t.Errorf("SanitizeKey(%q) = %q, want %q", tt.input, got, tt.expected)
            }
        })
    }
}
```

### 5.2 `pcommonMapToFlat()` 集成测试

验证嵌套 map 递归 sanitize、空 map、多属性同时转换等场景。

### 5.3 `resolveTagESFields()` 测试

验证 `http.method` → `attributes.http.method`（2段不变），`peer.service.source` → `attributes.peer.service_source`（3段转换）。

---

## 6. 与 3 个信号类型的兼容性

| 信号 | attributes 路径 | 受影响？ | 说明 |
|------|----------------|:------:|------|
| Trace | `attributes.*` `resource.*` `links.*.attributes.*` `events.*.attributes.*` | ✅ | 所有入口 |
| Log | `attributes.*` `resource.*` | ✅ | 数量少，点号 key 少 |
| Metric | `labels.*` `resource.*` | ✅ | 主要是 `service.name` 等 2 段 key，不受影响 |

---

## 7. 安全性

| 保证项 | 说明 |
|--------|------|
| 确定性 | `SanitizeKey` 是纯函数，无外部依赖 |
| 幂等性 | `SanitizeKey(SanitizeKey(x)) == SanitizeKey(x)`（输出不含点号的那个段已经替换为 `_`，再次替换不改变） |
| 无碰撞 | 假设 OTel 不会同时使用 `.` 和 `_` 作为同一段的分隔（实际情况：`.` 用于命名空间分隔，`_` 用于单词分隔） |
| 多实例安全 | 无共享状态，所有实例独立计算，结果一致 |
| 并发安全 | 纯函数，无锁 |

---

## 8. 实施步骤

| 步骤 | 内容 | 验证 |
|------|------|------|
| 1 | 新建 `sanitize_key.go` + `sanitize_key_test.go` | `go test` |
| 2 | 修改 `pcommonMapToFlat()` 调用 `SanitizeKey` | 运行已有测试 + 新增集成测试 |
| 3 | 修改 `trace_reader.go` 两处 `resolve*` 函数 | 运行 reader 测试 |
| 4 | 全量编译 + 运行所有测试 | `go build ./... && go test ./...` |
| 5 | 部署到测试环境，验证写入无报错 | 观察 ES `mapper_parsing_exception` 消失 |
| 6 | 验证 Tempo 查询正常 | 查询已知 attribute 确认有结果 |

---

## 9. 遗留问题

1. **3 段 key 的语义表示变化**：用户通过 Tempo UI 查看 tag key 时，`peer.service.source` 显示为 `peer.service_source`。对大多数 Tempo 使用场景影响较小（自定义 tag 通常不在 UI 面板中展示）。
2. **已有 ES 旧数据**：旧索引中 `attributes.peer.service.source`（3 层嵌套）与新索引中 `attributes.peer.service_source`（2 层，sanitized）不兼容。跨日期范围查询可能漏数据。通过 ILM 数据过期自然淘汰，短期可接受。
3. **OTel 新增属性**：未来 OTel semantic convention 可能引入新的 ≥3 段属性 key，sanitization 自动生效，无需额外改动。
