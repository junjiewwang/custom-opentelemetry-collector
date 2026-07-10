# 前端 Metrics 查询时间范围优化方案

## 1. 问题描述

前端查询指标的 names、labels、values 三个 API **没有传递时间范围参数**，导致后端只能使用默认值（最近 1 小时）查询，造成指标列表不完整。

## 2. 数据链路分析

### 当前状态

```
前端 API 调用                    后端 Handler                    ES 查询层
───────────────────────   →   ──────────────────────────   →   ────────────────
GET /metrics/names              parseTimeRange(r)              timeRangeQuery(tr)
GET /metrics/labels               ↓                              ↓
GET /metrics/labels/X/values    start=nil → 默认 now()-1h     range: {timeUnixMilli:
  (均无 query params)           end=nil   → now                 {gte, lte}}
```

### 各层传递情况

| 层级 | 是否传递时间范围 | 详情 |
|------|:---:|------|
| **前端 API 调用** | ❌ | `getMetricNames()` / `getMetricLabels()` / `getMetricLabelValues()` 均未传 `?start=&end=` |
| **后端 V2 Handler** | ✅ 已支持 | `parseTimeRange(r)` 解析 `?start=xxx&end=xxx` |
| **parseTimeRange 默认值** | ⚠️ | 前端不传时默认**最近 1 小时** |
| **Reader 实现** | ✅ | ES/PG 都使用 timeRange 做实际查询过滤 |

### 关键代码（后端已就绪）

**`observability_handler_v2.go`**：
```go
func (e *Extension) handleMetricNamesV2(w http.ResponseWriter, r *http.Request) {
    timeRange := parseTimeRange(r)  // 已支持 ?start=&end=
    names, err := e.storageMetricReader.ListMetricNames(r.Context(), timeRange)
    // ...
}

func parseTimeRange(r *http.Request) observabilitystorageext.TimeRange {
    now := time.Now()
    tr := observabilitystorageext.TimeRange{
        Start: now.Add(-1 * time.Hour),  // 默认: 1小时前
        End:   now,
    }
    if v := r.URL.Query().Get("start"); v != "" {
        tr.Start = parseTimeParam(v, tr.Start)
    }
    if v := r.URL.Query().Get("end"); v != "" {
        tr.End = parseTimeParam(v, tr.End)
    }
    return tr
}
```

**前端（当前）**：
```typescript
getMetricNames(): Promise<{ data: string[] }> {
  return this.request<{ data: string[] }>('GET', '/observability/metrics/names');
}
getMetricLabels(): Promise<{ data: string[] }> {
  return this.request<{ data: string[] }>('GET', '/observability/metrics/labels');
}
getMetricLabelValues(labelName: string): Promise<{ data: string[] }> {
  return this.request<{ data: string[] }>(
    'GET', `/observability/metrics/labels/${encodeURIComponent(labelName)}/values`
  );
}
```

## 3. 问题场景

1. 用户在页面选择「最近 7 天」的时间范围
2. 前端调用 `getMetricNames()` 加载指标名列表（用于自动补全）
3. 后端只查最近 1 小时 → 如果某指标在最近 1 小时无数据但过去 7 天有数据，就不会出现在列表里
4. 用户无法选择到该指标 → **功能缺失**

## 4. 方案对比

| 方案 | 描述 | 优点 | 缺点 |
|------|------|------|------|
| **A. 传递当前页面时间范围** | 前端把时间选择器的值传给 API | 语义精确；查询可控；后端零改动 | 前端改动；切换时间需重新加载列表 |
| B. 加大默认范围 | 后端默认改为 30 天 | 零前端改动 | 查询量大；返回已停止上报的指标 |
| C. 全量 match_all | 不加时间过滤 | 列表最完整 | 性能差；噪声大 |

## 5. 推荐方案：A（传递当前页面时间范围）

### 5.1 理由

1. **后端接口已就绪** — `parseTimeRange` 支持 `?start=&end=`，零后端改动
2. **语义正确** — 指标列表与用户选择的时间范围一致
3. **性能可控** — 不产生无界全量查询
4. **改动小** — 仅前端 3 个 API 方法加 query params

### 5.2 前端改动设计

**`api/client.ts`**：
```typescript
/** 获取所有 metric 名称 */
getMetricNames(start?: number, end?: number): Promise<{ data: string[] }> {
  const params = this.buildTimeParams(start, end);
  return this.request<{ data: string[] }>('GET', `/observability/metrics/names${params}`);
}

/** 获取所有 label 名称 */
getMetricLabels(start?: number, end?: number): Promise<{ data: string[] }> {
  const params = this.buildTimeParams(start, end);
  return this.request<{ data: string[] }>('GET', `/observability/metrics/labels${params}`);
}

/** 获取指定 label 的值 */
getMetricLabelValues(labelName: string, start?: number, end?: number): Promise<{ data: string[] }> {
  const params = this.buildTimeParams(start, end);
  return this.request<{ data: string[] }>(
    'GET', `/observability/metrics/labels/${encodeURIComponent(labelName)}/values${params}`
  );
}

/** 构建时间范围 query string */
private buildTimeParams(start?: number, end?: number): string {
  const params = new URLSearchParams();
  if (start) params.set('start', String(start));
  if (end) params.set('end', String(end));
  const qs = params.toString();
  return qs ? `?${qs}` : '';
}
```

**调用侧（Hooks）**：
```typescript
// useMetricAvailability.ts / useRedPanels.ts
const { start, end } = useTimeRange();  // 从页面时间选择器获取
const names = await apiClient.getMetricNames(start, end);
```

### 5.3 交互设计

- 时间选择器变化时，重新加载 names/labels 列表
- 加载中显示 loading 状态
- 支持 debounce（避免快速切换时重复请求）

## 6. 实施计划

| 步骤 | 内容 | 验收标准 |
|------|------|---------|
| 1 | `client.ts` 三个方法增加 `start/end` 可选参数 + `buildTimeParams` | 编译通过；向后兼容（不传时行为不变） |
| 2 | Hooks 调用处传入当前页面时间范围 | 请求 URL 带 `?start=&end=` |
| 3 | 时间范围变化时触发列表重新加载 | 切换时间范围后列表刷新 |
| 4 | 端到端验证 | 7 天范围下指标列表更完整 |

## 7. 变更日志

| 日期 | 版本 | 说明 |
|------|------|------|
| 2026-07-10 | v1.0 | 初始方案设计 |
