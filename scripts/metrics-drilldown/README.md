# Metrics Drilldown ↔ Prometheus API 覆盖度测试

针对 [Grafana Metrics Drilldown](https://github.com/grafana/metrics-drilldown) 插件的
Prometheus 兼容层回归测试。查询形态来自对插件查询构造器的源码走查,不是凭空构造的。

## 运行

```bash
export MD_BASE_URL=http://<collector-host>:8088
export MD_API_KEY=<your-api-key>

python3 scripts/metrics-drilldown/suite.py   # 64 项回归断言,退出码非 0 表示有失败
python3 scripts/metrics-drilldown/gaps.py    # 未覆盖面的探查脚本(只打印,不断言)
```

在 minikube 上取 service ClusterIP:

```bash
export MD_BASE_URL="http://$(kubectl get svc custom-otlp-collector -o jsonpath='{.spec.clusterIP}'):8088"
```

## 为什么断言写成这样

**HTTP 200 不代表数据正确。** 这套测试发现的最严重的几个缺陷——
`histogram_quantile` 忽略 θ、标签匹配器全部失效、`*_over_time` 是空操作、
二元运算返回左操作数——**全都是 HTTP 200 且 JSON 结构合法**。

所以断言一律落在语义上:

- series 条数(`sum()` 必须塌成 1 条,`by (label)` 必须每个标签值一条)
- 标签集合与真实数据对拍(`=~ test-java.*` 的结果必须等于实际以此开头的服务集合)
- 数值关系(p50 ≤ p90 ≤ p99;累积桶单调不减;`stddev` 与 Python `statistics.pstdev` 对拍)
- instant 与 range 交叉校验
- 不支持的查询**必须 400**,不得返回 200+空(否则用户无法区分"没数据"和"不支持")

新增断言时请遵循同一原则:**不要断言"返回了东西",要断言"返回的东西是对的"。**

## 文件

| 文件 | 作用 |
|---|---|
| `probe.py` | HTTP 封装与结果摘要,被另外两个脚本导入 |
| `suite.py` | 64 项回归断言,分 10 组 |
| `gaps.py` | 探查脚本:打印尚未被断言覆盖的面,用于发现新缺陷 |

完整缺陷清单与遗留问题见 [`docs/metrics-drilldown-coverage-report.md`](../../docs/metrics-drilldown-coverage-report.md)。
