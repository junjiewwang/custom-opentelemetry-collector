# Grafana Traces Drilldown ↔ Custom Collector Tempo API — 覆盖度测试报告

测试环境:minikube `custom-otlp-collector.default.svc.cluster.local:8088`
Tempo base URL:`/api/v2/tempo`,认证 `X-API-Key: your-api-key-1`
测试数据:真实运行中的 Java 服务链路(8 个服务,~6 spans/s,含 exception 事件)
测试脚本:`/tmp/tempo-drilldown-test.py`

> **状态:3 个 P0 + 1 个 P1 已修复并在集群验证通过。** 修复详情见文末「修复记录」。
> 剩余未做:`compare()`(P1-5)、streaming、exemplars。

---

## 一、总体结论(初测)

Search / 结构化查询 这条线**覆盖得很好**;Metrics(RED 面板)这条线**有严重正确性缺陷**。

最关键的一点:**14 个 metrics 查询全部返回 HTTP 200**,所以从"接口通不通"的角度看是全绿的。
但其中 3 个返回的是**错误的数据**——UI 会正常渲染出图表,不报错,只是数字是错的。这类问题比 500 更危险。

| 分类 | 结果 |
|---|---|
| Search / TraceQL 过滤 | ✅ 10/10 通过 |
| 结构化算子 `&>>` `>` | ✅ 正确,有区分度(非恒真) |
| nestedSet 三元组 | ✅ 数值正确(left<right,parent 链正确) |
| tag names / values | ✅ 良好,`q=` scoping 生效 |
| Metrics HTTP 状态 | ✅ 13/14 200 |
| **Metrics 数值正确性** | ❌ **quantile 完全错误** |
| **Metrics 响应格式** | ❌ **label 序列化污染;histogram 形状不对** |
| Exceptions 页签 | ❌ 事件属性取不到 |
| compare() | ❌ 未实现 |

---

## 二、必须修的问题

### P0-1. `quantile_over_time` 百分位数值完全错误 🔴

**最严重的问题。** RED 面板的 Duration 图目前显示的是接近最小值的数字。

根因有两处,在 `extension/observabilitystorageext/provider/elasticsearch/trace_metrics.go`:

**(a) 百分比刻度未转换** — `trace_metrics.go:219-223`
```go
// ES percentiles expects values in [0, 100].
for _, p := range query.Percentiles {
    percs = append(percs, p)   // ← 注释说要 0-100,代码直接透传
}
```
TraceQL 用小数 `0.9`,ES 用 `90`。传 `0.9` 给 ES = 请求第 0.9 百分位,基本就是最小值。

实测(真实数据 p90=47ms,p99=471ms):

| 查询 | 返回值 | 应为 |
|---|---|---|
| `quantile_over_time(duration, 0.9)` | **0.096 ms** | ~47 ms |
| `quantile_over_time(duration, 0.99)` | **0.097 ms** | ~471 ms |
| `quantile_over_time(duration, 90)`(手工传 90) | 60.6 ms | ✅ 合理 |

误差约 **500 倍**,且 p50/p90/p99 三条线几乎重合——正是"全取到最小值"的特征。

**(b) 多百分位被平均成一个数** — `trace_metrics.go:460-466`
```go
if len(result.Values) > 0 {
    var sum float64
    for _, v := range result.Values { sum += v }
    return sum / float64(len(result.Values)), nil   // ← 把 p50/p90/p99 平均了
}
```
`quantile_over_time(duration, 0.5, 0.9, 0.99)` 实测只返回 **1 条 series**,Grafana 期望 3 条(每个百分位一条,带 `p` label)。

另有 `tempo_handler.go:1723-1728` `metricsFuncParam()` 只取 `Percentiles[0]`,是同一问题在 MetricReader 路径的副本。

---

### P0-2. Metrics label 值序列化污染 🔴

所有 `by(...)` 分组查询的 label 都带了一层冗余嵌套:

```json
{"key":"resource.service.name",
 "value":{"stringValue":"customcol",
          "Value":{"string_value":"customcol"}}}   ← 多余
```

出处:`tempo_handler.go:98-113`,`tempoAnyValue` 带了个 `Value *tempoAnyValueAlt` 字段,注释写着:

> `// proto backward-compatible fallback format`
> `// used as a fallback by Grafana traces-drilldown for older backends`

**这个注释是错的。** 我在插件源码里核对过:
- `src/types.ts:40-43` 确实定义了 snake_case 类型
- 但只有 `trace-merge/tree-node.ts:78` 和 `trace-merge/utils.ts:7,19` 读它,**全部在 trace 合并路径(Structure 页签)**
- **metrics 响应路径从不读 `Value.string_value`**

所以对 metrics 而言这是纯冗余,徒增 payload。Structure 路径保留即可,metrics label 应该用扁平结构。

影响:Breakdown 页签所有分组(by service / by span attr / by name / by status)的图例。

---

### P0-3. `histogram_over_time` 形状不对,热力图无法渲染 🔴

我们返回 **1 条 series = 每个时间桶的总计数**。

Grafana 需要**每个 duration 桶一条 series**,并且从 series 名字里解析桶边界 —— `REDPanel.tsx:370-372`:
```ts
export const getYBuckets = (series: DataFrame[]) => {
  return series.map((s) => parseFloat(s.fields[1].name)).sort((a, b) => a - b);
};
```
只有 1 条无名 series ⇒ `yBuckets` 为空 ⇒ `REDPanel.tsx:112` 的 `if (yBuckets?.length)` 不成立 ⇒ **热力图 + Duration 区间选择器直接失效**。

根因 `trace_metrics.go:231-239`:注释里已经承认了
```go
// Grafana Tempo displays this as a time-series of counts (simplified heatmap).
return map[string]any{"value_count": ...}
```
需要改成 ES `histogram` / `range` 聚合,按 duration 分桶,每桶产出一条带桶边界名的 series。

---

### P1-4. Exceptions 页签取不到异常信息 🟠

插件查询(`src/components/Explore/queries/exceptions.ts:6`):
```
{... && status = error} | select(resource.service.name, event.exception.message,
                                 event.exception.stacktrace, event.exception.type)
                          with(most_recent=true)
```
实测返回 20 条 trace,但 span attributes **只有 `service.name`**,三个 exception 字段全部缺失。

而数据本身是存在的 —— `/api/traces/{id}` 原始响应里有:
```json
{"name":"exception","attributes":[
  {"key":"exception.type","value":{"stringValue":"com.tencent...DeliveryException"}},
  {"key":"exception.message","value":{"stringValue":"配送失败"}} ]}
```

**是 `select()` 不支持 event scope 投影,不是没数据。** 修复成本低、收益高。

顺带两个相关缺陷:
- **event 过滤器被静默忽略**:`{event.exception.type != nil}` 和 `{event.exception.type="NOPE_DOES_NOT_EXIST"}` 都返回 100 条,与 `{true}` 完全一致 —— 条件没生效却不报错,属于"静默错误结果"
- **`with(most_recent=true)` 未实现**:去重前后都是 100 条(`ast.go:214` 只有 `Sample` 字段,没有 `MostRecent`)

---

### P1-5. `compare()` 未实现 → Comparison 页签整个不可用 🟠

```
{...} | compare({status=error})
→ HTTP 400 {"error":"not a TraceQL metrics query: missing rate()/count_over_time()/
             quantile_over_time()/histogram_over_time()"}
```
`AttributesComparisonScene.tsx:286` 是唯一入口。lexer 里没有 `compare` token。
这是**唯一一个明确报错**的缺口 —— 相对好办,至少用户能看到错误而不是错误数据。

---

## 三、工作正常的部分

Search 侧质量确实不错,值得说明:

- **结构化算子有真实区分度**(不是恒真返回):
  | 查询 | 结果 |
  |---|---|
  | `{true}` | 100 |
  | `({kind=server} &>> {kind=server})` | 8 |
  | `({kind=server} &>> {kind=server && name="NONEXISTENT_XYZ"})` | **0** ✅ |
  | `({kind=server} > {kind=client})` | 9 |

- **nestedSet 三元组数值正确** —— Structure 页签依赖它:
  ```
  {parent:-1, left:1, right:6}   ← 根
  {parent: 1, left:2, right:3}
  {parent: 1, left:4, right:5}
  ```
- **`select()` 对 span/resource 属性生效**:`select(span.http.route)` → 只返回 `http.route`
- **tag values `q=` scoping 生效**:无过滤 8 个服务 → `q={status=error}` 6 个
- **`rootName`/`rootServiceName` 可以取值**(42 / 7 个)
- 响应字段齐全:`traceID` `rootServiceName` `rootTraceName` `startTimeUnixNano` `durationMs` `spanSets`

### 顺手发现:代码注释与实现不符

`tempo_handler.go:233-235` 写着:
```go
//	❌ rootName        — not stored in ES; would require trace root span derivation (TODO)
//	❌ rootServiceName — not stored in ES; would require trace root span derivation (TODO)
```
但 `tempo_handler.go:1474-1481` 已经通过 `ListRootSpanNames` / `ListRootSpanServices` 实现了,实测也能返回值。**注释是过期的**,建议一并订正,避免误导后续排查。

---

## 四、未覆盖项(影响较小)

| 项 | 状态 | 说明 |
|---|---|---|
| Streaming | 未实现 | 无 gRPC/SSE。插件会回退到非流式,功能可用,大时间范围体感慢 |
| exemplars | 未实现 | 响应只有 `series`/`metrics`。RED 面板失去"从指标跳 trace"的能力 |
| `statusMessage` 取值 | 未实现 | ES text 字段无 `.keyword`,无法 terms 聚合。属真实限制,可接受 |
| `completedJobs`/`totalJobs` | 未实现 | 仅进度显示 |
| 嵌套括号 OR | 报错 | `parser.go:210` 显式拒绝 |

---

## 五、建议修复顺序

1. **P0-1 quantile 刻度 + 多百分位**(`trace_metrics.go:219-223, 460-466` + `tempo_handler.go:1723`)
   数据错误且无声,优先级最高;刻度转换本身是一行改动
2. **P0-2 label 序列化**(`tempo_handler.go:98-113`)
   metrics 路径去掉 `Value` 嵌套,Structure 路径保留
3. **P0-3 histogram 分桶**(`trace_metrics.go:231-239`)
   改 ES range/histogram 聚合,恢复热力图
4. **P1-4 event scope**(`select()` 投影 + 过滤下推 + `most_recent`)
5. **P1-5 `compare()`**

前三项都集中在 metrics 链路,建议一起做。

---

## 六、复现方式

```bash
python3 /tmp/tempo-drilldown-test.py metrics   # 14 条 metrics 查询
python3 /tmp/tempo-drilldown-test.py search    # 10 条 search 查询
```

判定逻辑区分 `OK` / `ZERO`(200 但全零)/ `EMPTY` / `FAIL`,并打印 label 结构与 span 属性 key,便于回归比对。

---

## 七、修复记录(已完成并在集群验证)

### 已修 P0-1 — quantile 百分位刻度 + 多百分位

`elasticsearch/trace_metrics.go`
- 新增 `quantileToPercent()`:TraceQL 小数 `0.9` → ES 百分比 `90`(≤1 视为分数,>1 原样透传,兼容已传百分比的调用方)
- 默认百分位改为分数 `{0.5, 0.95, 0.99}`,与显式传入的走同一转换
- `extractMetricValue` → `extractMetricValues`,返回 `[]metricValue`,每个百分位一条 series,label `p`(对齐 Tempo `engine_metrics.go:2286`),不再求平均

验证(真实数据 p50≈0ms / p90≈47ms / p99≈471ms):

| 查询 | 修复前 | 修复后 |
|---|---|---|
| `quantile_over_time(duration, 0.5)` | 0.098 ms | **0.29 ms** |
| `quantile_over_time(duration, 0.9)` | 0.102 ms | **51.78 ms** |
| `quantile_over_time(duration, 0.99)` | 0.103 ms | **60123 ms** |
| `quantile_over_time(duration, 0.5, 0.9, 0.99)` | 1 条 series | **3 条**,`p=0.5/0.9/0.99` |

p50 < p90 < p99 单调性恢复。

### 已修 P0-2 — metrics label 序列化

`adminext/tempo_handler.go` `stringToTempoAnyValue()` 去掉 `Value` 嵌套。
核对过插件源码:`Value.string_value` 只在 `trace-merge/tree-node.ts:78`、`utils.ts:7,19`(Structure 页签)被读,metrics 路径从不读 —— 原注释"drilldown fallback"是错的。
`anyToTempoValue()`(span 属性,Structure 依赖)保持不变,并加测试锁定这一区别。

```json
// 修复前                                   // 修复后
{"stringValue":"customcol",                {"stringValue":"customcol"}
 "Value":{"string_value":"customcol"}}
```

### 已修 P0-3 — histogram 分桶(热力图恢复)

`elasticsearch/trace_metrics.go` 改用 ES keyed `range` 聚合,桶上界为 2 的幂,复刻 Tempo `Log2Bucketize`(`engine_metrics.go:2345`);每桶一条 series,label `__bucket`,值为**秒**(对齐 `ast_metrics.go:288` "Bucket is in seconds")。空桶丢弃。

选 `range` 而非 script/histogram 聚合:避免依赖 ES scripting(常被禁用)。

验证:series 数 **1 → 21**,`getYBuckets()`(`REDPanel.tsx:370`)拿到 21 个桶,热力图 + Duration 区间选择器恢复。分布形态合理(0.26ms 峰值 3431 条,长尾到秒级)。

### 已修 P1-4 — event scope(Exceptions 页签)

这条链路上有**三个独立缺陷**,逐层暴露:

1. **`select()` 不支持 event 投影** — `tempo_handler.go` `resolveSelectField()` 解析了 `event.` scope 却没有对应分支。补上 event 分支,并**置于 intrinsic switch 之前** —— 否则 `event:name` 会被 intrinsic `name` 抢先返回 span 名(这个顺序问题是测试先发现的)。event scope 与 span/resource 严格隔离,互不串读。

2. **适配层静默丢字段** — `observabilitystorageext/reader_adapter.go` 三处手写 struct literal 逐字段拷贝,**漏了 `EventTags`/`EventTagsOr`/`TagsNotOr`/`TagsRegexOr`/`TagsNotExists`**。planner 正确解析并填充,到适配层被丢弃 —— 这是过滤器"看似生效实则 match-all"的真正根因。提取统一的 `toStoredTraceQuery()`,并加反射测试:所有输入字段非零时输出不得有零值字段,且公有结构体新增字段必须有对应项,防止再次漏拷。

3. **ES 字段路径 + `.keyword`** — 事件属性存在 `events.attributes.<key>`(不是 `events.<key>`),且走 dynamic template 映射为 `text`+`.keyword`,精确 `term` 必须打 `.keyword`,否则 analyzed 文本永远匹配不上 `java.lang.IllegalArgumentException` 这种带点的类名。`events.name` 显式映射为 keyword,不加后缀。这与近期 commit `b6d51be`/`cfe94ff` 对 span 属性的修复是同一类问题。

验证:

| 查询 | 修复前 | 修复后 |
|---|---|---|
| `{true}`(基线) | 100 | 100 |
| `{event.exception.type="NOPE_XYZ"}` | 100 ❌ | **0** ✅ |
| 8 个真实存在的 exception type | 100(全部) | **全部 >0 且各不相同** ✅ |
| `{event.name="NOPE"}` | 100 ❌ | **0** ✅ |
| Exceptions 页签 `select(event.exception.*)` | 只有 `service.name` | **message/type/stacktrace 全部返回** ✅ |

### 关于初测「case 9 ZERO」

`{...resource.service.name="unknown_service:java"} | rate()` 返回全零 —— 复查确认该服务在测试数据中**根本不存在**(实际只有 customcol、java-user-service 等 8 个)。是我的测试固定值选错了,**不是缺陷**。

### 顺带订正

`tempo_handler.go:233-235` 注释标 `rootName`/`rootServiceName` 为 ❌ TODO,但 `:1474-1481` 早已通过 `ListRootSpanNames`/`ListRootSpanServices` 实现,实测能返回 42/7 个值 —— 过期注释。

### 新增测试

- `elasticsearch/trace_metrics_buckets_test.go` — 11 个:百分位刻度、多百分位 fan-out、NaN、log2 桶边界、histogram 不塌缩、grouped 场景 label 合并、rate 保持单 series
- `elasticsearch/trace_reader_events_test.go` — event 字段路径 / `.keyword` / OR 组 / `events.name` 不加后缀
- `adminext/tempo_event_select_test.go` — Exceptions 投影、event scope 隔离、两种 AnyValue 序列化差异
- `observabilitystorageext/reader_adapter_query_test.go` — 字段拷贝完整性 + 结构体字段对等(反射)

`go build ./extension/...` 通过,`go test ./extension/...` 全绿。

### 自查阶段又发现并修掉的 3 个问题

修完之后我自己 review + 实测挑战了一遍假设,又抓到三个自己引入/遗漏的缺陷:

**(a) `p` label 浮点漂移** — 我原本用 ES 回显的百分比字符串除以 100 反推分位:`99.9/100 = 0.9990000000000001`,label 就脏了。改成拿 ES 百分比去匹配调用方**原始请求**的分位值,匹配不上才回退除法。实测 `p=0.999` / `p=0.9999` 现在 label 精确。

**(b) series 排序用字符串** — `%g` 格式化小数会出科学计数法(`3.2768e-05`),字典序 ≠ 数值序。给 `metricValue` 加了独立的 `sortKey float64`,两条 fan-out 路径都按数值排。

**(c) 跨时间桶的 bucket 排序错乱**(最隐蔽) — 原实现按「首次出现顺序」收集 series。某个 duration 桶如果只在靠后的时间桶才出现,它就被排到响应末尾。实测确实翻车:
```
... 68.719476736, 3.2768e-05, 0.067108864, ...   ← 第 14 位突然跳回最小值
```
改成收集完再按数值排序。补了针对性测试(桶只在第二个时间桶出现)。

Grafana 的 `getYBuckets()` 自己会 `.sort()`,所以 (c) 不至于让热力图崩,但响应本身是错的,别的消费方会踩。

### 一个真实限制:`.keyword` 的 `ignore_above=256`

dynamic template 里 `ignore_above: 256`(`admin.go:238`),超长值不会进 `.keyword` 索引。
实测 `event.exception.stacktrace` 通常 4KB+,**无法做等值过滤**;但 `select()` 投影正常(走 `_source`,不依赖索引)。
Exceptions 页签只用 stacktrace 展示、用 message/type 过滤,所以不影响实际使用。已在代码注释中标明。

---

## 八、未修

| 项 | 说明 |
|---|---|
| **P1-5 `compare()`** | Comparison 页签不可用,仍返回 400。唯一**显式报错**的缺口(用户看到错误,不是错误数据) |
| Streaming | 插件自动回退非流式,功能可用 |
| exemplars | 失去"指标跳 trace" |
| `statusMessage` 取值 | ES text 字段无 `.keyword`,真实限制 |
| stacktrace 等值过滤 | `ignore_above=256`,见上 |

---

**部署状态:** 已 `make docker-build`(DOCKER_CONTEXT=minikube)并 `kubectl rollout restart`,当前 pod 运行含全部修复的镜像,上述数据均为修复后实测。

**最终回归:** metrics 14 条(12 OK / 1 ZERO 属数据缺失非缺陷 / 1 FAIL 为未实现的 `compare()`),search 10 条全 OK。`go build` / `go test ./extension/...` / `go vet` 全部通过。
