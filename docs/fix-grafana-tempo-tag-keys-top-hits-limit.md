# 修复: GetTagKeys top_hits size 超过 ES max_inner_result_window 限制

## 问题描述

Grafana 通过 Tempo API 请求 `/api/v2/search/tags` 获取 tag keys 列表时返回失败，ES 报错：

```
Top hits result window is too large, the top hits aggregator [docs]'s from + size must be less than or equal to: [100] but was [200].
This limit can be set by changing the [index.max_inner_result_window] index level setting.
```

虽然用户实际请求的是带 `q` 条件的 `/api/v2/search/tag/{name}/values`，但 Grafana 会并行发起 `search/tags` 请求，该请求的失败会影响侧边栏标签列表展示。

## 根因分析

`GetTagKeys` 方法中使用了 `sampler` + `top_hits` 聚合来采样文档并提取 tag keys：

```go
"docs": map[string]any{
    "top_hits": map[string]any{
        "size":    200,  // ← 超过 ES 默认限制
        "_source": []string{fieldPrefix},
    },
},
```

Elasticsearch 的 `index.max_inner_result_window` 默认值为 **100**，而代码中设置了 `size: 200`，导致所有分片查询失败。

## 修复方案

将 `top_hits` 的 `size` 从 `200` 改为 `100`，以符合 ES 默认的 `index.max_inner_result_window` 限制。

**修改文件**: `extension/observabilitystorageext/provider/elasticsearch/trace_reader.go`

**影响评估**:
- `top_hits size=100` 配合 `sampler shard_size=500`，每个分片采样 500 个 bucket，每个 bucket 取 top 100 个文档，对于 tag keys 发现已足够
- `GetTagValues` 使用的是 `terms` 聚合，不受此限制影响

## 状态

- [x] 修复代码实施
- [x] lint 检查通过
