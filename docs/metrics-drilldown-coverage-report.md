# Grafana Metrics Drilldown ↔ Custom Collector Prometheus API — 覆盖度测试报告

测试环境:minikube `custom-otlp-collector.default.svc.cluster.local:8088`
Prometheus base URL:`/api/v2/prometheus`,端点在 `/api/v1` 下,认证 `X-API-Key`
测试数据:真实运行中的 Java 服务链路(241 个指标,8 个服务,含 JVM / spanmetrics / kafka)
测试脚本:`scripts/metrics-drilldown/suite.py`(64 项断言,全部通过)

> **状态:两轮共发现并修复 12 个缺陷,均已在集群验证。** 上一版报告中"已修复"的 4 项里,有 3 项实际上从未编译通过(见第一节)。

---

## 一、首先:上一版报告的结论有一半是错的

开工第一件事是跑 `go build ./extension/...`,**工作区编译不过**:

```
metric_reader_test.go:139:2: expected operand, found ','
prometheus_handler.go:507:19: undefined: expr
```

`handlePromSeries` 里引用了一个根本不存在的 `expr` 变量。也就是说上一版报告声称"已 `make docker-build` 并 rollout,当前 pod 含全部修复"——**这个镜像不可能构建成功**。集群里跑的是更早的版本,报告中那批"验证通过"的数据来自一个并不包含相应代码的二进制。

其中 `!=` / `!~` 负向匹配是半成品:`promqlExpr` 加了 `LabelNot`/`LabelNotMatch` 字段,handler 里也填了,但 parser 不产出、adapter 不透传、ES 层字段名还是错的。四层里三层是断的。

**教训**:先编译、再谈"验证通过"。本轮所有结论都基于 `go build` + `go test ./extension/...` 全绿后重新构建并 rollout 的镜像。

---

## 二、本轮修复(6 项)

### P0-1. 标签匹配器只有 `=` 能用 🔴 影响面最大

Metrics Drilldown 的每一条查询都会插入 `${filters:raw}` ad-hoc 过滤变量。实测:

| 匹配器 | 修复前 | 修复后 |
|---|---|---|
| `=` 精确 | ✅ | ✅ |
| `=~` 正则 | ❌ 0 条 | ✅ |
| `!=` 排除 | ❌ 0 条 | ✅ |
| `!~` 反向正则 | ❌ 0 条 | ✅ |

三个独立的根因,叠在一起:

**(a) ES 字段少了 `.keyword`。** 用一个临时诊断测试打出真实的 ES query 后一目了然:

```
exact  =    {"term":{"labels.span_kind.keyword":"Client"}}   ← 对
regex  =~   {"term":{"labels.span_kind":"Client"}}           ← 错,漏了 .keyword
```

metric labels 是 dynamic 映射的 text+keyword,对 bare text 字段做 term/terms/prefix 永远匹配不上多 token 的值。这正是 traces 那两个 commit(cfe94ff / b6d51be)修过的问题,**没有同步到 metrics**。

**(b) `!~` 在 parser 里被整个吞掉。** `parseSelector` 先 `strings.Index(pair, "=")` 找分隔符——而 `!~` 是唯一不含 `=` 的匹配器,`eqIdx < 0` 于是走进"裸引号字符串 → 当作指标名"的分支,过滤条件凭空消失。改为先探测 `!~`。

**(c) 4 个 adapter 漏传字段。** `reader_adapter.go` 里 `MetricQuery` / `MetricRangeQuery` / `MetricRawQuery` / `MetricFlatQuery` 四个结构体字面量逐字段拷贝,负向匹配字段一个都没拷。`MetricQuery` 连 `LabelMatch` 都漏了。

验证(与真实数据对拍,不只看 HTTP 200):

```
= Client          → 1 个   期望 1   PASS
=~ Client|Server  → 2 个   期望 2   PASS
=~ test-java.*    → 5 个   期望 5   PASS
!= Client         → 4 个   期望 4   PASS
!~ test-java.*    → 3 个   期望 3   PASS
```

### P0-2. `histogram_quantile` 的 θ 被完全忽略 🔴

```
p50 = 0.5727404518518521
p99 = 0.5727404518518521   ← 一模一样
```

instant 模式返回 **95 条原始 series**,而不是 1 个分位值;且 p50 和 p99 逐位相等——分位数根本没算。

根因是一个 **plugin 与存储的形态错配**:

- 两个 executor 都卡在 `expr.HistogramSub == "bucket"`,要求指标名带 `_bucket` 后缀
- 但我们 **241 个指标里 `_bucket` 后缀的有 0 个**(ES 把 bucket 数组存在基础指标文档上)
- 插件读到 metadata `type: histogram`,判定为 **native histogram**,发出的正是 `histogram_quantile(θ, sum(rate(m[5m])))`——无 `_bucket`、无 `by (le)`

于是查询悄悄掉进普通 rate 分支,返回原始速率。

**修复**:改为以 `expr.Aggregation == AggHistogramQuantile` 为判据。

> ⚠️ 中途踩了个坑:我最初写成 `!math.IsNaN(expr.Quantile)`,但 `Quantile` 的零值是 **0.0 而非 NaN**,导致*每一条* rate 查询都被路由进 histogram 分支,`sum by (...) (rate(...))` 全线返回空。部署后立刻被回归检查抓到。已针对这一点补了专门的测试。

同时修了聚合语义:parser 用 histogram_quantile 覆盖了内层的 `sum`,导致分组维度丢失。新增 `InnerAgg` 字段保留它,并按 PromQL 由内向外的求值语义投影标签(`le` 由分位数消费,不出现在输出中)。

验证:

```
p50=1.257  p90=9.446  p99=1837.024      单调递增,1 条 series
by (le, service_name) → 8 条,每服务一条,无 le 标签
```

### P0-3. `/label/{name}/values` 忽略 `match[]` 🟠

```
GET /label/span_kind/values?match[]={__name__="jvm.memory.used"}
→ ['Client','Server','Internal',...]     ← jvm.memory.used 上根本没有这个标签
```

Breakdown 的取值下拉框由此端点驱动,用户选中一个值后面板必然空白。新增 `ListLabelValuesForMetric`,PostgreSQL provider 无法按指标过滤,显式降级为不过滤并在注释里写明。

### P0-4. `{__name__="x"}` 在查询路径被忽略 🟠

```
{__name__="traces_spanmetrics_calls_total"} → 126 条,横跨所有指标
```

存储层按 name 字段做 term 查询,没有 `__name__` 这个标签;把它留在 `Labels` 里就是在过滤一个不存在的标签,ES 直接忽略 → 全量返回。`planSeriesMatch`(用于 `/series`)早就正确路由了,但 `parsePromQL`(用于 `/query`)没有。同一个概念两套实现,只对了一半。

修在 parser 里,所有查询路径一起受益;顺带删掉了 `planSeriesMatchFull` 中因此变成死代码的分支。

### P1-5. `topk` 在 range 模式下不裁剪

`topk(3, ...)` instant 返回 3 条,range 返回 **8 条**——`applyTopK` 只在 instant 路径调用。

PromQL 严格语义下 topk 是逐时间点求值的(series 可以进出集合),但 Grafana 需要稳定的线条集合。实现按**每条 series 在整个区间内的极值**排序取前 K,并在 `dispatchRangeQuery` 的汇合处统一应用(该函数有多个 matrix 返回点)。

### P1-6. `/labels` 全局列表漏标签

`ListLabelNames` 只采样**最近 100 条文档**取标签并集。不带 `match[]` 时这 100 条会被高频指标垄断,低频指标的标签永远采不到——实测**限定单指标返回的 `http_method`,全局列表里反而没有**(子集断言直接失败)。采样量提到 2000 后全局标签数从 12 涨到 **51**。

> 这仍是启发式采样,不是精确扫描。注释里写清楚了:增大样本只能缩小差距,不能消除。真正的修法是 terms 聚合,但那需要改索引映射,超出本轮范围。

---

## 三、第二轮:针对"未覆盖面"的定向排查(再修 6 项)

第一轮 46 项全绿后,把**尚未被断言覆盖的面**单独探了一遍(`scripts/metrics-drilldown/gaps.py`)。结果是:**绿灯本身有误导性——没被断言的地方,几乎都是坏的。**

### P0-7. 热力图 `sum by (le)` 把桶维度压平了 🔴

插件的直方图面板发的正是这条(`getHeatmapQueryRunnerParams.ts:18`):

```promql
sum by (le) (rate(metric[$__rate_interval]))
```

返回 **1 条 series,且没有 `le` 标签**。原因:**ES 里根本没有 `le` 这个标签**——桶数据存在 `bucket_counts` / `explicit_bounds` 数组里。按一个不存在的标签分组,自然全塌成一条。热力图只能画出一行。

修复:新增 `execHistogramBucketRange`,从数组里**合成 `le` 维度**,输出 Prometheus 风格的**累积桶**(含 `+Inf`)。

验证:

```
sum by (le) (...)               → 27 条,le=2,4,6,...,+Inf,累积单调 ✅
sum by (le, service_name) (...) → 216 条 = 27 桶 × 8 服务 ✅
```

### P0-8. 二元运算返回**左操作数的值**,伪装成比值 🔴

比"不支持"严重得多:

```
sum(rate(m[5m])) / sum(rate(m[5m])) → 2.407
```

同一个指标除以自己,**必须是 1.0**。聚合外壳先吃掉左操作数,剩下的尾巴被 `parseSelector` 当成"指标名",除法被丢弃,左边的值原样返回。**这是错的数据,不是缺失的数据。**

### P0-9. `*_over_time` 是彻底的空操作 🔴

第一轮我把它们判为 PASS,因为返回了数据。**这个判断是错的**,我做了对拍:

```
                 plain      max_over_time   min_over_time   count_over_time
jvm.memory.used  30049280   30049280        30049280        30049280
max == min == count == plain?  全部 True
```

`count_over_time` 返回 30049280(一个内存字节数,不是样本数)。`parseFuncWrapper` 只认识 rate/increase/irate,其余的掉进裸选择器分支,返回原始最新值。

同理,`time() - avg(...)` 返回 6 条(应为 1 条)且忽略了 `time()`;`and > -Inf` 静默丢弃过滤条件。

**处理**:统一改为 **400**。这些从来就没工作过,报错比给错数强。

### P1-10. `stddev` / `stdvar` 未实现却"看起来能用"

不在 `AggFuncs` 里 → 走裸选择器 → 返回 6 条原始 series,而不是 1 个标准差。插件的函数选择器提供这两个,所以**实现**而非拒绝:instant 侧用总体方差(除以 N,与 Prometheus 一致),range 侧走 ES `extended_stats`。

对拍 Python `statistics.pstdev`:

```
expected 33256105.767514404
actual   33256105.767514408   ← 15 位有效数字一致
```

### P1-11. NaN 污染聚合

写 stddev 测试时顺带发现:`strconv.ParseFloat` **认得 `"NaN"` 和 `"+Inf"`**,所以一条 NaN 样本会让整组 `sum`/`avg` 塌成 NaN。这个 bug 在 sum/avg/max/min 里**早就存在**,只是没人测过。已过滤非有限值。

### P1-12. 空聚合产出 `{"metric":null,"value":null}`

窗口短于采集间隔(如 `[30s]`)时样本不足 2 个,rate 产出空,但 `applyAggregation` 仍包了一个零值样本回去,序列化成 **null 字段的畸形 series**。空结果就该是空结果。

---

## 四、验证方式

`scripts/metrics-drilldown/suite.py` —— 64 项断言,全部基于**语义**而非 HTTP 状态码:

| 分组 | 覆盖内容 |
|---|---|
| 1. 发现类端点 | buildinfo / 指标列表 / metadata 类型分布 / `?metric=` 过滤 / series / labels |
| 2. 标签取值作用域 | 正反两个方向:该有的有,不该有的必须为空 |
| 3. 标签匹配器 | 6 种匹配器,逐个与真实标签集对拍 |
| 4. `__name__` 选择器 | 三种写法结果必须一致 |
| 5. 分类型面板查询 | counter / gauge / breakdown,instant 与 range 交叉校验 |
| 6. 直方图分位数 | 单调性、θ 生效、分组维度、`le` 不外泄 |
| 7. 其他查询形态 | topk / min / count / stddev |
| 8. **热力图** | 桶维度展开、`+Inf` 存在、累积单调、分组保留 |
| 9. **不支持的查询必须 400** | 9 种,全部不得返回 200+空 |
| 10. **传输与时长格式** | POST 三个端点、Go 风格复合时长 `1m0s`/`4m30s`/`2h15m0s` |

```
TOTAL: 64/64 passed
```

关键设计:**判定 `OK` 不等于数据正确**。两轮里最严重的四个问题(θ 被忽略、匹配器失效、`*_over_time` 空操作、二元运算返回左值)**都是 HTTP 200 + 结构合法**。所以断言全部落在 series 条数、标签集合、数值单调性、instant/range 一致性、以及**与 Python 独立计算结果对拍**上。

Go 侧新增测试:

- `prometheus_histogram_grouping_test.go` — 分组投影语义、`InnerAgg` 保留、**普通 rate 查询不得进入 histogram 分支**
- `promql_parse_matchers_test.go` — `__name__` 路由、`!~` 解析、4 种匹配器互不串味
- `prometheus_topk_matrix_test.go` — 按极值排序、按峰值而非末值、空 series 排最后
- `prometheus_heatmap_test.go` — 拒绝清单 / 接受清单、`le` 系列构建、`*_over_time` 不得复活为空操作
- `prometheus_stddev_test.go` — 总体方差、NaN 不污染、count 仍计入不可解析样本、空聚合不产出 null
- `metric_filter_keyword_test.go` — 所有 `labels.*` 引用必须带 `.keyword`
- `reader_adapter_matchers_test.go` — AST 层面校验 4 个结构体字面量都赋了全部匹配器字段

最后这个值得说一下:adapter 漏字段是**静默**的,类型系统不管、测试也照过。所以写了个 AST 测试直接解析 `reader_adapter.go`,检查每个 `elasticsearch.Metric*Query{}` 字面量有没有赋全。并且实际删掉一个字段验证过它会失败——不会失败的断言等于没写。

顺带把 `TestValidAggregations` 从"断言个数等于 11"改成断言具体名字:一个裸数字既不说明加了什么,也不说明该有的在不在。

`go build` / `go test ./extension/...` / `go vet` 全绿。

---

## 五、仍未解决(按建议优先级)

| 项 | 现状 | 建议做法 |
|---|---|---|
| **二元运算 / 比率面板** | 已 400,不再返回错数 | Knowledge Graph 的错误率面板要用。需要真正的表达式树:先切分顶层二元运算符(注意括号深度),左右各自递归求值,再按标签集做向量匹配。工作量最大但价值最高 |
| **`*_over_time`** | 已 400 | 数据已在 `QueryFlat` 里拿到,只差在窗口内做 max/min/avg/sum/count 归约——`AggregateHistogramSamples` 已有同款滑窗逻辑可参照。**性价比最高的下一步** |
| `time() - avg(...)` | 已 400 | age 类指标面板需要。属于二元运算的子集,可在实现标量运算时一并解决 |
| `and`/`unless`/`> -Inf` | 已 400 | 集合运算符,同样依赖表达式树 |
| `sum without (...)` | 已 400 | 相对简单:`without` 是 `by` 的补集,拿到全部标签名后取差即可 |
| `group by (__name__) ... unless ...` | range 返回畸形 series | "最近新增指标"过滤器用。依赖 `unless` |
| `/labels` 采样 | 已从 100 提到 2000 | 仍是启发式。精确解需改用 terms 聚合,但 `labels` 是 object 映射,需先改索引模板 |
| `limit` 参数 | 三个发现类端点均忽略 | 改动小:在 handler 里截断即可。指标多时对 UI 有实际意义 |
| Ruler API `/rules` | 404 | "firing alerts" 徽章不可用。若不打算做告警,可长期搁置 |
| `help` / `unit` 元数据 | 空 | OTel 不带 help;unit 有但未索引 |
| 指标名正则 | 仅支持 `|` 字面量 | 存储层按 name 做 term 查询,通配需先列举再匹配 |
| 历史数据指标类型 | 仅新写入正确 | 需要回填或按索引版本兼容读取 |

---

## 六、部署状态

已 `DOCKER_CONTEXT=minikube make docker-build` 并 rollout,当前 pod 包含全部 12 项修复,上述数据均为**修复后**实测。

复现:

```bash
python3 scripts/metrics-drilldown/suite.py   # 64 项回归断言
python3 scripts/metrics-drilldown/gaps.py    # 未覆盖面的探查脚本
```
