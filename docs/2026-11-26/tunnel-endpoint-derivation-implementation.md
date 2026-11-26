# Arthas Tunnel Endpoint 推导改造 — collector 仓库实施记录

> 记录需求、决策、实施进展、未完成任务与遗留问题。文档不含凭据或敏感信息。
> Java 探针侧由他处实施，实施指引见同目录 `tunnel-endpoint-java-implementation.md`。

## 1. 需求与决策

用户判断 `ops.arthas.tunnel_endpoint` 不该是默认必需项，探针应能按 OTLP/control endpoint 自行推导 tunnel-server 地址。经调研确认：**当前系统本来就是「默认靠探针推导、opsConfig 只做覆盖」**，因此本批不是删除 key，而是补覆盖粒度与统计。

| 编号 | 决策 |
| --- | --- |
| D1 | 采用方案 A：新增单个 `ops.arthas.tunnel_base_url`（scheme + host[:port] + 可选 path 前缀）；完整 URL key 先作过渡逃逸阀，**第二批已一次性迁移并移除** |
| D2 | 推导时按「原 URL 是否显式带 port」分情况决定是否替换 `http_port` |
| D3 | 反代子路径前缀由 base_url 承载，推导逻辑不特殊处理 |
| D4 | `derived` 来源显式上报并单独统计 |
| D5 | 多仓库协作；本批**只实施 collector 仓库**，Java 侧仅产出实施指引 |

## 2. 调研结论（确证）

- 优先级链 `local -D > ops config > 探针推导` 已成立；删除 ops key、Redis 不可用时回落到 derived（`extension/opsconfig/store.go` 注释、`receiver/agentgatewayreceiver/manager_init.go`）。
- 服务端**不感知**探针的 OTLP protocol / exporter endpoint：`ConfigRequest`、`PollRequest`、`StatusRequest` 均无此字段；gateway metadata 只下发 `http_port`（`receiver/agentgatewayreceiver/metadata.go:25-33`，从 `0.0.0.0:4318` 拆出）。因此推导必须留在探针侧。
- tunnel WS `/v1/arthas/ws` 与 OTLP/HTTP 同挂 4318 HTTP router；Ingress `/v1/` Prefix → 4318。gRPC 4317 无 Ingress 路由。
- `agentgatewayreceiver` 控制面**同时实现 HTTP 与 gRPC**（`server_setup.go:48-53` 注册 `ControlPlaneService`，`proto/controlplane/v1/service.proto` 6 个 unary rpc，无 streaming），但**Arthas Tunnel 只有 HTTP/WS 版本**，任何 protocol 的探针都必须 HTTP/WS 可达。
- `derived` 在服务端是**原样透传**：`extension/adminext/handlers.go:49` 直接取 `inst.Labels["arthas.tunnel.source"]`；把它并入 `unknown` 的是前端 `OpsConfigPage` 的汇总逻辑。因此 D4 的 collector 侧改动集中在前端，无需改后端聚合。
- Java 探针三层位置已定位：SDK 实现在 `/root/IdeaProjects/github/opentelemetry-java`（`sdk-extensions/controlplane`），打包在 `opentelemetry-java-instrumentation`，发布需 `publishToMavenLocal` 后重建 javaagent。

## 3. 本批改动（collector 仓库）

| 文件 | 说明 |
| --- | --- |
| `extension/opsconfig/store.go` | 新增 `KeyTunnelBaseURL = "ops.arthas.tunnel_base_url"`，原 endpoint key 注释改为「full-URL override」 |
| `extension/opsconfig/schema.go` | 注册新 key（ValueType `base_url`）+ `validateTunnelBaseURL`：scheme 限 http/https/ws/wss、host 必需、禁 userinfo/query/fragment、端口 1–65535、允许 path 前缀但拒绝包含固定 tunnel path 或尾斜杠 |
| `extension/opsconfig/store_test.go` | 表驱动校验、Schema 含双 key、ErrValidation 包装、Store 下发与删除回退 |
| `extension/adminext/webui-react/src/pages/OpsConfigPage.tsx` | 来源统计从三类改四类：`derived / ops / local / unknown`；统计条改 2/4 列响应式 |
| `extension/adminext/webui-react/src/pages/InstancesPage.tsx` | `derived` 徽标单独配色（green），`ops` 保持蓝色体系，`local` 琥珀 |

**未改动**：`receiver/agentgatewayreceiver/ops_metadata.go`（`ProvideMetadata` 遍历 `store.Snapshot()` 下发所有 key，revision 自动进 ETag，新增 key 零改动生效）；`extension/adminext/handlers.go`（来源已透传）；`config/template/config.yaml`（opsConfig 走 Redis + 代码注册 schema，模板无对应段）。

## 4. 验证记录

- `go test -count=1 ./extension/opsconfig/...`：通过（新增 4 组用例：base_url 合法/非法值、Schema 双 key、ErrValidation 包装、Store 下发与删除回退）。
- `go build ./...`、`go test -count=1 ./...`：全通过。期间修正了既有 `TestGetOpsConfigsIncludesSchema` 的「schema 只有 1 项」假设，改为按 key 断言两个 value_type。
- `npm run build`：通过（TypeScript 与 Vite 产物）。
- IDE 静态诊断：改动目录无新增告警。
- minikube 验证：见第 6 节。

## 5. 未完成任务与遗留问题

- **L1（已解决）**：Java SDK 侧已完成并部署（提交 `f5b17a0d5`）；在线探针已上报 `derived` 并在下发 base_url 后上报 `ops`，端到端链路见第 8 节。
- **L2**：`derived` 计数依赖探针心跳上报；旧探针仍上报 `local / ops / none`，会落入 `unknown`，属预期兼容行为。
- **L3**：`http_port` 是**监听端口**（4318），不是外部可见端口；D2 的「不带 port 则不注入」规则在探针侧实现，服务端无法代劳。
- **L4**：collector 的 validator 允许 base_url 带 path 前缀，但实际 Ingress 是否有对应路由需部署方自行保证；服务端不做连通性探测。
- **L5（已解决）**：`ops.arthas.tunnel_endpoint` 已通过一次性 admin 迁移后移除；**代价**：服务端 `ArthasTunnel.Path` 可配置而 Java 侧 `DEFAULT_ARTHAS_WS_PATH` 硬编码，删除完整 URL key 后不再有手段覆盖非默认 tunnel path。当前模板使用默认值 `/v1/arthas/ws`，无影响；若将来需要自定义 path，须新增 path 覆盖项。

## 6. 第一批 minikube 验证记录（两个 key 并存时）

按既有链路：渲染 → `make docker-build` → `make k8s-deploy` → `kubectl rollout restart`，Pod `1/1 Running`。

| 项 | 结果 |
| --- | --- |
| `GET /api/v2/ops/configs` schema | `['ops.arthas.tunnel_base_url', 'ops.arthas.tunnel_endpoint']` |
| `PUT` 合法 base_url `https://gw.example/otel` | HTTP 200；回读 `configs` 含该值，revision=5 |
| `PUT` 非法值 `ftp://host` | HTTP 400，body `opsconfig validation failed: base URL scheme must be http, https, ws or wss` |
| `DELETE` base_url | HTTP 200；回读不含该 key（回到 derived 模式） |
| 前端 Ops Config 页面 | 按 schema 自动渲染两个配置项（`ops` 分组显示 2 settings）；来源统计条显示四类 `Auto-derived endpoint 0 / Applied from ops 2 / Overridden by local -D 0 / Unknown 0` |
| 截图 | `docs/2026-11-26/ui-preview/ops-config-tunnel.png` |

说明：以上为第一批（两个 key 并存、探针尚未升级）的历史记录，当时的 schema 与页面数据不应与当前状态对比。Java 侧升级、一次性迁移与旧 key 移除后的验证见第 8 节。

## 7. 改动文件清单

| 文件 | 说明 |
| --- | --- |
| `extension/opsconfig/store.go` | `KeyTunnelBaseURL` 常量 |
| `extension/opsconfig/schema.go` | 新 key 注册 + `validateTunnelBaseURL` |
| `extension/opsconfig/store_test.go` | 4 组新增用例 |
| `extension/adminext/handlers_opsconfig_test.go` | 既有 schema 断言适配双 key |
| `extension/adminext/webui-react/src/pages/OpsConfigPage.tsx` | 四类来源统计、2/4 列响应式 |
| `extension/adminext/webui-react/src/pages/InstancesPage.tsx` | derived 徽标单独配色 |
| `extension/adminext/webui-react/dist/` | 已重新构建并打进镜像；该目录被 `.gitignore` 排除，不计入版本改动 |
| `docs/2026-11-26/tunnel-endpoint-derivation-implementation.md` | 本记录 |
| `docs/2026-11-26/tunnel-endpoint-java-implementation.md` | Java 仓库实施指引（供带走） |
| `docs/2026-11-26/ui-preview/ops-config-tunnel.png` | 第一批验证截图 |

## 8. 第二批：一次性迁移与旧 key 移除（已完成）

### 决策与前提

- Java SDK 侧已实施完成并提交 `f5b17a0d5`（`Arthas Tunnel 改用 ops base_url 覆盖并收敛推导链路`），agent 已重新部署；该模块搜索 `ops.arthas.tunnel_endpoint` / `KEY_OPS_TUNNEL_ENDPOINT` / `opsTunnelEndpoint` **0 命中**。
- 迁移前在线探针上报 `arthas_tunnel_source=derived`、`ops_revision_applied=6`，说明它已忽略仍在下发的旧 key；因此直接删除旧 key 不会造成旧探针断连。
- collector Redis 仍有旧值 `ws://custom-collector.devcloud/v1/arthas/ws`（revision 6）。由于 `getOpsConfigs` 与 `opsMetadataProvider` 都遍历 `Snapshot()`，**必须先迁移数据再删代码**，否则旧值会变成「能显示、能下发、但 PUT/DELETE 被拒」的幽灵配置。

### 一次性 admin 操作（按用户决策）

| 步骤 | 结果 |
| --- | --- |
| `PUT ops.arthas.tunnel_base_url` = `ws://custom-collector.devcloud`（去掉固定 path，无损等价） | 200；探针随后上报 `source=ops`、`ops_revision_applied=8` |
| `DELETE ops.arthas.tunnel_endpoint` | 200；`configs` 只剩 base_url，revision=8 |

### 代码删除

`extension/opsconfig/store.go` 删 `KeyTunnelEndpoint` 常量（保留 `expectedTunnelPath` 供 base_url 校验）；`schema.go` 删旧 key 的 schema item 与 `validateWsEndpoint`；`store_test.go`、`adminext/handlers_opsconfig_test.go`、`receiver/agentgatewayreceiver/ops_metadata_test.go`、`longpoll/ops_etag_test.go` 全部改用 `KeyTunnelBaseURL` 或只用字面量断言「旧 key 不再受支持」；`OpsConfigPage.tsx` 的 placeholder 改为 base_url 示例 `wss://gw.example/otel`。全仓 Go/前端代码中的旧 key 只剩两处「不应再支持」的回归断言。

### 顺带修正的既有缺陷

`store.Delete` 的 unsupported key 错误没有包装 `ErrValidation`（`store.go:243`），导致 `DELETE` 旧 key 返回 **503 后端故障**而 `PUT` 返回 400。已改为 `fmt.Errorf("%w: ...", ErrValidation, key)` 并补单测；修正后旧 key 的 PUT / DELETE 均返回 400 与明确错误信息。

### 验证记录

- `go build ./...`、`go test -count=1 ./...`、`npm run build` 全通过，lint 0。
- 部署后 `GET /api/v2/ops/configs`：`schema=['ops.arthas.tunnel_base_url']`、`configs={'ops.arthas.tunnel_base_url': 'ws://custom-collector.devcloud'}`、revision=8。
- 旧 key `PUT` / `DELETE` 均返回 400（`unsupported key`），无 503。
- **探针端到端来源流转**（探针心跳周期约 30 秒，短窗口会错过中间态，故按 revision 轮询等待）：
  - 迁移后 `source=ops / ops_rev=8`；
  - 删除 base_url（rev 9/11）→ `source=derived / ops_rev=11`；
  - 写回 base_url（rev 12）→ `source=ops / ops_rev=12`。
  - 最终配置恢复为迁移值 `ws://custom-collector.devcloud`，非测试残留。
- 前端页面截图 `docs/2026-11-26/ui-preview/ops-config-single-key.png`：`ops` 分组只剩 1 setting，四类来源统计 `derived 0 / ops 1 / local 0 / unknown 0`，rev 12。
- 审计历史仍保留旧 key 的历史变更条目，属预期（ChangeLog 不做重写）。

### 遗留

- 删除完整 URL key 后，服务端 `ArthasTunnel.Path` 可配置但 Java 侧 `DEFAULT_ARTHAS_WS_PATH` 硬编码，**不再有手段覆盖非默认 tunnel path**。当前模板用默认值 `/v1/arthas/ws`，无影响；若将来需要自定义 path，须新增 path 覆盖项。
- `ops-config-p0.md` 属历史设计文档，仍描述旧 key，未改动。
