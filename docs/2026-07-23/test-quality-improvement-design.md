# 测试质量改进方案

> 文档创建日期：2026-07-24
> 关联问题：TraceQL metrics 3 个 bug 均为已有单元测试未覆盖的假设错误

---

## 一、问题复盘

3 个 bug 的共性：**代码假设了 ES 的数据格式（大写 status、keyword 字段、.keyword 后缀），但这个假设是错的。单元测试同样 encode 了这些错误假设，形成了「代码和测试一起错」的闭环。**

| Bug | 代码假设 | ES 实际 | 单元测试能发现吗 | 为什么不能 |
|-----|---------|--------|:---:|------|
| capitalizeFirst status | ES 存 `"Error"` | ES 存 `"error"` | ❌ | 没有写测试 |
| resource.app_id NoKeyword | 是 keyword 字段 | 是 text 字段 | ❌ | 测试断言了「不需要 .keyword」 |
| status.code 不加 .keyword | 已处理 | 未处理 | ❌ | 测试断言了「不应该有 .keyword」 |

---

## 二、三层测试体系

```
┌──────────────────────────────────────────────┐
│  Layer 3: ES 集成验证 (IntegrationTest)       │  ← 新增，防止假设错误
│  对真实 ES 做断言：mapping 类型 + 查询计数     │
├──────────────────────────────────────────────┤
│  Layer 2: Golden File 快照 (SnapshotTest)     │  ← 新增，防止查询结构退化
│  生成 ES query JSON → diff 对比已知正确版本    │
├──────────────────────────────────────────────┤
│  Layer 1: 单元测试 (UnitTest)                  │  ← 增强，覆盖边界条件
│  纯函数测试 + 边界场景 + ES 查询结构验证       │
└──────────────────────────────────────────────┘
```

### Layer 1: 增强单元测试

**改造原则**：从「验证实现」→「验证行为」。不再断言「不应该有 .keyword」，而断言「生成的具体 ES 查询字段是什么」。

```go
// 改造前：验证实现（错）
assert.NotContains(t, field, ".keyword")

// 改造后：验证行为（对）
assert.Equal(t, "status.code.keyword", field)
```

**补充缺失的测试**：

```go
// capitalizeFirst 单元测试（之前完全没有）
func TestCapitalizeFirst(t *testing.T) {
    tests := []struct{ input, want string }{
        {"", ""},
        {"server", "Server"},
        {"error", "Error"},
        {"ok", "Ok"},
    }
    for _, tt := range tests {
        assert.Equal(t, tt.want, capitalizeFirst(tt.input))
    }
}

// metricsAggField 对所有 intrinsic 的回归
func TestMetricsAggField_AllIntrinsics(t *testing.T) {
    tests := []struct{
        label    string
        want     string
    }{
        {"status",       "status.code.keyword"},    // text → .keyword
        {"statusMessage", "status.message.keyword"}, // text → .keyword
        {"status.message","status.message.keyword"}, // text → .keyword  
        {"kind",         "kind"},                    // keyword → no suffix
        {"name",         "name"},                    // keyword → no suffix
        {"rootName",     "rootName"},                // keyword → no suffix
        {"resource.app_id", "resource.app_id.keyword"}, // text → .keyword
        {"resource.host.name", "resource.host.name"},   // keyword → no suffix
    }
    for _, tt := range tests {
        t.Run(tt.label, func(t *testing.T) {
            got := metricsAggField(resolver, tt.label)
            assert.Equal(t, tt.want, got)
        })
    }
}
```

### Layer 2: Golden File 快照测试

**目的**：捕获 ES 查询 JSON 结构，防止代码改动导致查询退化。

**实现方式**：

```go
// trace_metrics_golden_test.go
func TestBuildMetricsFilter_GoldenSnapshot(t *testing.T) {
    query := TraceMetricsQuery{
        Status:  "error",
        IsRoot:  true,
        SpanKind: "server",
    }
    
    esQuery := reader.buildMetricsFilter(query)
    got, _ := json.MarshalIndent(esQuery, "", "  ")
    
    // 与 golden file 对比
    goldenPath := "testdata/trace_metrics_filter.json"
    if *update {
        os.WriteFile(goldenPath, got, 0644)
    }
    want, _ := os.ReadFile(goldenPath)
    assert.JSONEq(t, string(want), string(got))
}
```

**工作流**：
```
代码改动 → go test → golden file diff → 审查查询变化 → 接受(更新 golden) 或 拒绝(回退)
```

这会在 CI 中暴露 `capitalizeFirst("error")→"Error"` 这类查询内容变化。

### Layer 3: ES 集成验证测试

**目的**：对真实 ES 做断言，验证 mapping 和查询结果。

**设计**：使用 build tag `integration` 隔离，不在 CI 自动运行，手动或按需执行。

```go
//go:build integration

// es_integration_test.go
func TestESMapping_StatusFields(t *testing.T) {
    // 验证 status.code 的实际 mapping 类型
    mapping := getESFieldMapping(t, "otel-traces-*", "status.code")
    assert.Equal(t, "text", mapping.Type)
    assert.True(t, mapping.HasKeyword, "must have .keyword sub-field")
}

func TestESMapping_FieldsUsedInAggregation(t *testing.T) {
    // 检查所有 metricsAggField 中可能用到的字段
    fields := esFieldsUsedInMetricsAggregation()
    for _, f := range fields {
        mapping := getESFieldMapping(t, "otel-traces-*", f)
        isAggregatable := mapping.Type == "keyword" ||
            mapping.Type == "long" ||
            mapping.Type == "double" ||
            (mapping.Type == "text" && mapping.HasKeyword)
        assert.True(t, isAggregatable,
            "field %s (type=%s) must support aggregation", f, mapping.Type)
    }
}

func TestESQuery_StatusFilterHasResults(t *testing.T) {
    // 真实查询验证
    count := esCount(t, esq.T("status.code.keyword", "error"))
    assert.Greater(t, count, 0, "must have error status documents")
}

func TestESQuery_CapitalizeFirstWouldFail(t *testing.T) {
    // 显式验证：capitalized 值匹配不到数据
    countLower := esCount(t, esq.T("status.code.keyword", "error"))
    countUpper := esCount(t, esq.T("status.code.keyword", "Error"))
    assert.Greater(t, countLower, countUpper,
        "lowercase 'error' should match more docs than capitalized 'Error'")
}
```

**运行方式**：
```bash
# 需要 ES 连接时手动运行
ES_HOST=http://9.134.106.132:9200 go test -tags=integration ./extension/.../
```

---

## 三、实施优先级

| 优先级 | 内容 | 覆盖的 bug 类型 | 工作量 |
|:---:|------|------|:---:|
| **P0** | Layer 1: 增强现有单元测试（补充 capitalizeFirst + metricsAggField 全 intrinsic） | capitalizeFirst 假设错误 | ~50 行 |
| **P0** | Layer 2: Golden file 快照测试（buildTraceSearchQuery + buildMetricsFilter） | 查询结构退化 | ~80 行 |
| **P1** | Layer 1: 改造现有测试从「验证实现」到「验证行为」 | .keyword 后缀、NoKeyword 列表 | ~30 行 |
| **P1** | Layer 3: ES 集成验证测试（mapping + count） | 所有 ES 假设错误 | ~100 行 |

---

## 四、防止再犯的流程规则

1. **任何新字段/新映射加到代码中时，同步加 golden test** — 生成并提交 golden file
2. **任何对 ES 数据格式的假设（如"ES 存大写"），必须写 ES 集成测试验证**
3. **CR 时检查：测试是「断言了具体值」还是「断言了反面（不应该有 X）」** — 后者是 red flag
4. **没有对应 ES mapping 验证的查询改动不通过 CR**
