# MCP 可观测性查询工具实施记录

> 记录需求、决策、实施进展、未完成任务与遗留问题。文档不含凭据或敏感信息。

## 1. 需求与决策

用户问：MCP 是否可以支持 metric / trace / log 查询，metric 走 Prometheus、trace 走 Tempo、log 走 Loki。

**语义澄清（调研确证）**：仓库里没有直连外部 Prometheus / Tempo / Loki 服务端的客户端。这三个名字是 `adminext` 已实现的 Grafana 数据源协议兼容层，底层由 `observability_storage` 扩展提供：metric → VictoriaMetrics，trace / log → Elasticsearch（`config/template/config.yaml:154-180`），PromQL / LogQL / TraceQL 为自研解析引擎（`README.md:876-878`）。

| 编号 | 决策 |
| --- | --- |
| D1 | 复用现有 All-in-One 存储层的 `MetricReader` / `TraceReader` / `LogReader`，不新建外部数据源客户端 |
| D2 | 一期只做只读、结构化参数查询，不暴露裸 PromQL / LogQL / TraceQL 表达式 |
| D3 | 做 `metric:read` / `trace:read` / `log:read` scope；`tk_` 空 scopes 视为无权限，全局 key 放行 |
| D4 | 三类信号分别开关；代码默认关闭，部署模板显式配置启用 |

## 2. 现状调研结论

- `mcpext` 此前最多 24 个工具（17 个基础 + 7 个动态插桩），`addTool` 是唯一注册入口并包装 `authorizeToolCall`。
- `tenantauth.WithIdentity` 会把 TenantID 写入 `tenantctx`，`WithHTTPContextFunc` 转发到工具 ctx；读路径用 `TenantIDFromContext` / VM account / ES `withTenantFilter` 隔离。
- `adminext/extension.go:565-592` 通过 `host.GetExtensions()` 断言 `*observabilitystorageext.ObservabilityStorage` 取三个 Reader，并在 `:581-584` 桥接 `SetMetricAccountResolver`。
- 结构化 V2 handler 的 `TenantID` 由服务端 `TenantIDFromContext` 注入，不信任请求参数。
- **必须补的缺口**：`mcpext/tool_authz.go` 的 `authorizeToolCall` 对不带 `agent_id` / `session_id` 的工具直接 `return nil`，查询工具会被裸放行。
- `mcpext` 目录内此前对 observability Reader 的引用数为 0。

## 3. 实施结果

全部完成。

| 项 | 状态 | 说明 |
| --- | --- | --- |
| Reader 接口与查询模型签名核对 | 已完成 | `MetricQuery` / `MetricRangeQuery` / `TraceQuery` / `LogQuery` 等 |
| `ObservabilityConfig` 与依赖注入、启动顺序 | 已完成 | `mcpext/config.go`、`extension.go`；`Dependencies()` 补 `observability_storage` 与 `admin`（保证 VM account resolver 先桥接） |
| 查询工具门禁 | 已完成 | `tool_authz.go` 的 `authorizeObservabilityQuery`：能力/Reader fail-closed、禁止 `tenant_id` 参数、`app_id` 归属校验、按信号 scope |
| 只读查询工具与结果格式化、条件注册 | 已完成 | 9 个工具（metric 4 / trace 3 / log 2），统一精简 JSON 信封 + `truncated` 标记 |
| 前端 scope 勾选、配置模板、README、技能包 | 已完成 | 运营端创建 key 可授予四个 scope |
| 单测、无缓存全量回归、前端构建 | 已完成 | `go test -count=1 ./...`、`npm run build` 通过 |
| minikube 端到端验证 | 已完成 | 见第 4 节 |

**改动文件清单**

| 文件 | 说明 |
| --- | --- |
| `extension/mcpext/config.go` | `ObservabilityConfig` / `ObservabilitySignals` / `ObservabilityLimits`、默认值、`Validate()`、`queryTimeout()` / `maxTimeRange()` |
| `extension/mcpext/extension.go` | 三个 Reader 字段、`Dependencies()`、`discoverObservabilityReaders()` |
| `extension/mcpext/mcp_server.go` | `registerTools()` 条件注册 |
| `extension/mcpext/tool_authz.go` | `observabilityQueryTools` 映射与 `authorizeObservabilityQuery` |
| `extension/mcpext/tools_observability.go`（新增） | 9 个工具注册与 handler、RFC3339 时间窗、limit 上限、服务端租户覆写 |
| `extension/mcpext/observability_formatter.go`（新增） | 精简视图、截断与统一信封 |
| `extension/mcpext/tools_observability_test.go`（新增） | fake Reader、注册数量、scope 矩阵、租户覆写、时间窗/截断、9 个工具返回值 |
| `extension/observabilitystorageext/provider/elasticsearch/{trace_reader,log_reader,metric_reader}.go` | 补齐无 TenantID 参数方法的按 ctx 租户过滤（见第 6 节） |
| `extension/observabilitystorageext/reader_adapter.go` | 修复空 span 列表 panic；新增 `reader_adapter_test.go` |
| `extension/adminext/webui-react/src/pages/TenantsPage.tsx`、`dist/` | 创建 key 时勾选 `metric:read` / `trace:read` / `log:read` |
| `config/template/config.yaml`、`README.md`、`ai-skills/mcpext-arthas-diagnostic/{SKILL.md,references/tool-catalog.md}` | 配置、说明与调用样例 |

## 4. 验收记录

- `go build ./...`：通过；`go test -count=1 ./...`：通过；`npm run build`（含 `tsc -b`）：通过。
- 渲染产物：YAML 可解析、无残留占位符，`mcp.observability.enabled=true`，三个信号均开启。
- 部署：`make docker-build` → `make k8s-deploy` → `rollout restart`，Pod `1/1 Running`；日志出现 `Discovered observability readers`（metric/trace/log 均 true）。
- 工具集：`tools/list` 为 **33 个 = 原有 24 + 新增 9**；无 key → 401。
- **真实数据查询**（静态 super key）：`metric_list_names` 返回 162 个指标并截断到 50；`trace_services` 返回 2 个服务；`trace_search` total 1622；`log_search` total 288；`metric_label_values`、`trace_get`、`log_context` 均返回真实结果。首次验证时 `metric_query` / `metric_query_range` 为空结果，原因是所选指标名 `http_server_duration_milliseconds_count` 在当前数据里不存在；后续用真实指标名复测，`metric_query` 返回 count=3 / count=1、`metric_query_range` 返回 12 / 10 个数据点，工具本身无缺陷（补测记录见第 8 节）。
- **scope 矩阵**：`tk_` 无 scope 时三个信号分别被拒（错误含对应 scope）；带三个 scope 时均可查且信封 `tenant_id=admin`；静态 super 无需 scope；工具参数传 `tenant_id` 被拒；`X-Tenant-Id` 对 `tk_` 无效（信封仍为 `admin`）。
- **跨租户隔离**：用另一租户的 `tk_` key 访问 admin 租户的 trace / log，`trace_get` 返回 `查询失败: trace ... not found`、`log_context` 返回 not found、`log_search` / `metric_list_names` 返回空；admin 租户 key 访问同一 trace 正常。修复后无 `http: panic serving`。
- 清理：3 个测试 key 全部 revoke；端口转发释放；含明文密钥的临时文件删除；Pod 保持 Running。

## 5. 实施中发现的既有缺陷与修复

以下均为**既有**问题，被本次 MCP 查询的隔离要求暴露，已修复并复验：

1. **无 TenantID 参数的 Reader 方法漏过滤**：ES `GetTraceSpans`（hybrid 实际调用路径）、`GetLogContext`、`ListMetricNames`、`ListLabelValuesForMetric` 不设置 `SearchRequest.TenantID`，按 ctx 补上（与既有 `GetServices` 同模式）。`GetTrace` 也一并补齐。
2. **hybrid 适配器绕过**：`traceReaderAdapter.GetTrace` 调用 `inner.GetTraceSpans` 而非 `GetTrace`，最初只修 `GetTrace` 无效；minikube 复验发现另一租户仍能读到 trace 后定位并修正 `GetTraceSpans`。
3. **空结果 panic**：租户过滤返回 0 个 span 时，`buildTraceFromStoredSpans` 直接访问 `spans[0]` 导致 HTTP 连接断开；在适配器里返回「not found」，并给构造函数加空输入防御，附单测。

## 6. 未完成任务与遗留问题

- **已解决（抽象层修复，见第 7 节）**：ES metric 路径的租户透传缺口已在中立层补齐，MCP 不感知底层后端；配置模板中「切回 ES 不要开放 metric 工具」的错误使用限制已删除。
- 裸表达式查询（PromQL / LogQL / TraceQL）、写入类可观测性工具、MCP Resources/Prompts 不在本期范围。
- 外部 Prometheus / Tempo / Loki 集群直连属新增工作，现有代码零基础。
- 浏览器 WebUI 未做交互截图验证；已验证 TypeScript 编译、dist 产物包含新 scope 与限额配置文案、后端 API 透传。
- 工具数量口径：metric 4 + trace 3 + log 2 = **9 个**；方案讨论中曾写「10 个」为笔误，以实际实现为准。
- 单信号关闭已在集群验证（第 8 节）；**Reader 为 nil、数据源停机、查询超时的真实集群注入仍未做**（第 8 节如实记录），仅由单测覆盖。
- 运营端保存非法限额值返回 HTTP 500（既有 handler 统一错误映射），功能上确实拒绝保存，但语义上应为 400，属既有错误码问题，未在本批修改。

## 7. 抽象层租户透传修复（已完成）

### 背景与纠错

用户指出：metric / trace / log 应通过抽象逻辑层查询，MCP 不该关注底层存储。核实后确认**上一批把问题放错了层**：公共 `MetricQuery` / `MetricRangeQuery` 本就带 `TenantID`（`observabilitystorageext/types.go:247,266`），VM Reader 用 `query.TenantID` 做 account 隔离，ES 也具备 `tenantId.keyword` 过滤能力且 `StoredMetricDataPoint` 带 `TenantID`。缺口是**抽象层自身**：

- ES 本地 `MetricQuery` / `MetricRangeQuery` 没有 TenantID 字段（对比 ES `LogQuery` / `LogStatsQuery` 有）；
- `metricReaderAdapter.Query/QueryRange` 转换公共模型时把 TenantID 丢掉；
- ES `Client.Search` 只在 `SearchRequest.TenantID != ""` 时过滤，不能兜住「本地模型没有该字段」的方法。

因此这不是「后端不支持」，也不该变成 MCP 侧的配置使用限制。上一批写入模板注释和本文档第 6 节的「切回 ES 不要开放 metric 工具」已删除。

### 修复内容（MCP 工具层零改动）

| 文件 | 说明 |
| --- | --- |
| `provider/elasticsearch/types_reader.go` | ES 本地 `MetricQuery` / `MetricRangeQuery` 补 `TenantID` |
| `provider/elasticsearch/metric_reader.go` | `Query` / `QueryRange` 的 `SearchRequest` 带 `TenantID`；`queryRangeMixed` 的 `oldReq` / `recentReq` 一并补齐 |
| `provider/elasticsearch/client_search.go` | 新增 `searchTenantID(ctx, req)`：显式 `SearchRequest.TenantID` 优先，否则回落到 `tenantctx.TenantIDFromContext(ctx)`；`Client.Search` 统一注入 `withTenantFilter`，覆盖所有未显式设置 TenantID 的 Reader 方法（含 `QueryRaw` / `QueryFlat` / 各类 `List*`）；后台 context 保持全局 |
| `reader_adapter.go` | `metricReaderAdapter.Query/QueryRange` 显式透传 `TenantID` |
| `mcpext/observability_formatter.go` | `metricSeriesViews` 跳过无数据点的占位序列（ES 在租户过滤后可能产生空序列，避免 `count=1` 误导调用方） |
| `config/template/config.yaml`、本文档第 6 节 | 删除错误限制，改为「抽象层统一隔离、MCP 后端无关」 |

**单测**：`searchTenantID` 四态（显式优先 / ctx 兜底 / 后台全局 / 后台显式）、`metricReaderAdapter` 用 capturing searcher 断言公共 `TenantID` 到达 ES `SearchRequest`（Query 与 QueryRange）、空序列过滤；`go test -count=1 ./...` 通过。

### minikube 验证（实际切换后端）

1. **当前配置（VM）基线**：新镜像部署后 `tools/list` = 33，`metric_list_names` total=162。
2. **临时把 `observability_storage.hybrid.metric` 改为 `elasticsearch`**（只改渲染产物，不重渲染模板），`make k8s-deploy` + 重启：
   - MCP **零改动**：工具集仍 33 个，工具参数与配置不变；
   - `metric_list_names`：全局 key total=253→279（写入持续变化）、admin 租户 `tk_` total=1→28（仅自己数据）、另一租户 `tk_` total=0；信封 `tenant_id` 分别为空 / `admin` / 对应租户；
   - `metric_query_range`（admin 自己的指标）：全局 key 1 序列 2 个数据点、admin 租户 1 序列 2 个数据点、另一租户 **count=0、0 个数据点**；
   - collector 日志无 panic。
3. **切回 VM**：重新渲染模板 + 部署重启，`tools/list` = 33、`metric_list_names` total=162，恢复正常。
4. 清理：两个测试 key 全部 revoke，端口转发释放，含明文密钥的临时文件删除，渲染产物恢复模板值。

### 验证中发现并修复的两个额外问题

- `QueryRange` 的 mixed（跨 raw/rollup）分支会走 `queryRangeMixed`，它另发两个 `SearchRequest` 且不带 TenantID；首次复验时另一租户虽拿不到数据点，却返回一条空序列（`count=1`）。已给两个请求补 `TenantID`，并在 MCP formatter 过滤空序列，复验后另一租户 `count=0`。
- 首次 `metric_query_range` 复验的「另一租户 count=1」经检查是**空序列占位**而非数据泄露（values 长度为 0），如实记录，未把它当成验证通过。

### 仍存的实现不对称（如实记录）

ES 本地 `MetricRawQuery` / `MetricFlatQuery` / `FlatDensityQuery` 等仍没有 TenantID 字段，`metricReaderAdapter` 的对应方法也未显式透传公共模型的 TenantID。这些路径**当前由 `Client.Search` 的 ctx 兜底保证隔离**（MCP 的 ctx 一定带认证租户），且不属于本期 9 个 MCP 工具的调用路径；但若将来出现「ctx 无租户、仅公共 query.TenantID 非空」的内部调用，这些路径不会被过滤，需补显式透传。此项未在本批修改，也未在集群上逐一验证。

## 8. 租户级查询限额与验证矩阵补齐（已完成）

### 决策

| 编号 | 决策 |
| --- | --- |
| D5 | 部署级 6 个值是**纯默认**，租户配置**允许放大**（管理员配置，风险可控），不做 clamp |
| D6 | 限额按**调用者租户**（认证身份 `TenantID`）判定，与「被插桩 Agent 所属租户」的能力开关语义分开 |
| D7 | 请求语义为**整体替换 + 每字段省略即清除**，另提供整体清除标志；`signals` 不租户化 |
| D8 | 补齐上一轮列出的真实测试缺口，并同步文档里过时的 `metric_query` 空结果记录 |

### 语义边界

- 全局 `super` / `ok_` key（TenantID 为空）与 `super` 的 `X-Tenant-Id` impersonation 身份**不应用**目标租户限额，一律使用部署默认，避免超级管理员被小限额反向限制。
- 租户存储不可用、记录不存在、字段非法 → 回落部署默认并告警（限额是成本/上下文控制，不是授权门）；查询不会被阻断。
- 每次工具调用实时读取租户配置，**不加缓存**，运营端改动下一次调用即生效；代价是多一次 `GetTenant`。
- 技术边界：`query_timeout` 可放大，但 Go `context.WithTimeout` 取父 ctx 与子 ctx 中最早的 deadline，且 HTTP `WriteTimeout` 仍是外层上限，租户值不能突破进程级超时。

### 改动清单

| 文件 | 说明 |
| --- | --- |
| `extension/controlplaneext/tenantmanager/model.go` | `TenantObservabilityQueryConfig`（6 个指针字段，nil=继承）；`TenantCapabilities.ObservabilityQuery`；`UpdateTenantRequest.ObservabilityQuery` / `ClearObservabilityQuery`；`Validate()` 正数、时长与 fail-fast 校验 |
| `extension/controlplaneext/tenantmanager/service.go` | `UpdateTenant` 整体替换与整体清除（clear 优先），保持「替换而非原地修改」 |
| `extension/controlplaneext/tenantmanager/capabilities_test.go` | 整体替换、省略字段清除、clear 优先、非法值拒绝、老 JSON 兼容 |
| `extension/mcpext/extension.go` | 保存 `tenantManager tenantmanager.TenantManager`（复用已发现的 `GetMultiTenantManager()`，无新增扩展依赖） |
| `extension/mcpext/observability_settings.go`（新增） | `observabilitySettings`、context 注入/读取、`resolveObservabilitySettings`（按调用者租户、允许放大、失败回落） |
| `extension/mcpext/mcp_server.go` | `addTool` 对 9 个查询工具统一注入本次调用的 settings，handler 内不再散落解析 |
| `extension/mcpext/tools_observability.go` | 15 处部署级配置读取收敛为 `observabilitySettings(ctx)`；时间窗错误显示当前生效值 |
| `extension/mcpext/observability_settings_test.go`（新增） | 继承、放大、部分覆盖、invalid duration 回落、租户存储失败、全局/impersonation、context 注入 |
| `webui-react/src/types/api.ts`、`api/client.ts`、`pages/TenantsPage.tsx`、`dist/` | 类型与 `updateTenant` 字段；编辑租户表单新增「Observability query limits」分组：整体继承开关 + 6 个字段各自的 inherit 勾选与输入 |

### minikube 验证记录

**租户限额**（`admin` 租户设 `query_timeout=90, max_time_range=72h, metric_series=3, traces=2, logs=2, log_context_lines=2`）：

| 场景 | 结果 |
| --- | --- |
| `tk_` 收紧 | `metric_list_names` count=3（total 51）、`trace_search` count=2、`log_search` count=2、`log_context` before=2（传 `lines=50` 也被限制） |
| `tk_` 放大 | 48h 时间窗的 `metric_query_range` 成功；同参数用全局 key 返回「查询时间窗不能超过 24h」 |
| 全局 key 不受影响 | `metric_list_names` count=50（部署默认），信封 `tenant_id` 为空 |
| 非法值保存 | `query_timeout=0` 被拒（返回 `query_timeout must be positive`，HTTP 500，见第 6 节） |
| 部分覆盖 | 只提交 `traces:1` 后，capabilities 仅保留该字段，其余回到继承 |
| 清除后立即生效 | `clear_observability_query` 后 `metric_list_names` count 恢复 50、48h 时间窗被拒；**未重启**，证明无缓存 |

**补齐上一轮真实测试缺口**：

| 项 | 结果 |
| --- | --- |
| `tk_` 下 `metric_label_values` / `trace_services` / `log_context` / `metric_query` 成功路径 | 全部真实调用成功（`metric_query` count=3） |
| 另一租户 `trace_search` / `trace_services` 隔离 | 均 count=0，信封 `tenant_id` 为该租户 |
| ES 后端下 `metric_query` / `metric_label_values` | 临时切 `hybrid.metric=elasticsearch` 后：全局 key count=11/1、admin `tk_` count=9/1（租户隔离仍生效）；已切回 VM |
| 单信号关闭 | 临时设 `signals.metric=false` 后 `tools/list`=29（metric 4 个工具不注册，trace/log 5 个保留）；已恢复为 33 |
| `metric_query` 真实数据 | VM 后端用真实指标名 `active_requests` count=3、`demo_customer_pass_qps` count=1；`metric_query_range` 12 / 10 个数据点 |
| 恢复确认 | 重新渲染模板后 `tools/list`=33、`metric_list_names` total=162，Pod `1/1 Running` |

**未在真实集群验证（如实记录）**：Reader 为 nil、数据源停机、查询超时的注入。其中查询超时做了一次尝试（租户 `query_timeout=1` + 20h 范围查询），查询在 1 秒内完成并返回 count=0，**未触发超时**，因此不能声称该路径已验证；这三项目前只有单测/代码路径保证。浏览器 WebUI 仍未做交互截图验证（已验证 TypeScript 编译、dist 产物与后端 API 透传）。

**清理**：2 个测试 key 全部 revoke，`admin` 租户 `capabilities` 恢复为 `null`，渲染产物恢复模板值（VM + 三信号开启），端口转发与含明文密钥的临时文件全部清理。
