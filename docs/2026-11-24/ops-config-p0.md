# 运营配置 P0 实施记录

## 需求

管理员通过运营端管理全局运维配置（首个配置项：Arthas Tunnel Server 地址），在线探针通过控制面 config poll 自动获取，无需探针启动参数。

## 决策

- 优先级：本地 `-D` > 运营端下发 > OTLP/control endpoint 推导。
- 运营配置（global）新建 Redis OpsConfig；app 应用配置走 appmanager，service 服务配置走 Nacos，三者互不覆盖。
- P0 只做"能改能下发"，不做历史/回滚 UI、灰度、app/service 级运维配置。
- revision 只纳入 ETag，不污染业务 Version；探针侧 `server_metadata` 按内容独立去重。
- 多副本使用 Redis pub/sub 唤醒所有 waiter，纳入 P0。
- 来源展示（本地覆盖 / 运营下发 / 推导 / 未知）纳入 P0：运营页汇总条 + 实例页徽标与筛选，不单独建列表页。
- 不做可达性探测，不做地址白名单；保留变更日志（操作者、旧值、新值、时间）。
- WS 日志脱敏同批完成。
- 评审修正集（共享 Provider、schema 注册表、仅唤醒语义、集成测试）在落地时一并完成。
- 文档不记录任何 token、密码或密钥值。

## 实施进展

- [x] 建立本文档。
- [x] 新建共享 Redis OpsConfig 存储包。
- [x] collector 后端接入。
- [x] Java 探针接入。
- [x] 前端运营配置页与实例来源展示。
- [x] 构建与联调验证（Java 编译/单测按用户要求跳过）。
- [x] 评审修正集 1-4。
- [x] 更新本文档。

## 变更清单

### collector 后端

| 文件 | 改动摘要 |
|---|---|
| `extension/opsconfig/store.go`（新） | Redis 存储：`Start/Stop` 用独立 context、原子 Lua 脚本（INCR + HSET/HDEL + 审计 + PUBLISH）、本地快照缓存、变更通知广播、`Done()` 优雅退出、`Snapshot/Revision/RevisionTag/Audit` |
| `extension/opsconfig/schema.go`（新） | schema 注册表 `RegisterKey/Validate/Keys`，`ErrValidation` 可判定错误；首个 key 为 URL 类型，仅允许 ws/wss、禁止 userinfo/query/fragment、固定路径 |
| `extension/opsconfig/provider.go`（新） | 共享 `Provider` + 引用计数，消除 adminext / receiver 双初始化、双 pub/sub 订阅 |
| `extension/opsconfig/store_test.go`（新） | 纯函数 + miniredis 集成测试：写入/删除/审计、校验失败不改 revision、跨副本 pub/sub、共享 Provider |
| `receiver/agentgatewayreceiver/ops_metadata.go`（新） | `opsMetadataProvider` 注入 `ops.arthas.tunnel_endpoint` 与 `ops.revision` |
| `receiver/agentgatewayreceiver/ops_metadata_test.go`（新） | 下发内容与 nil 安全单测 |
| `receiver/agentgatewayreceiver/longpoll/config_handler.go` | `SetOpsRevisionProvider` + `effectiveEtag`（revision 只进 ETag）、`NotifyOpsChanged` 全量唤醒并**立即携带新 metadata 且保持业务 Version 不变**、`ConfigWaiter.lastConfigVersion`、`injectMetadata` 保持空骨架安全 |
| `receiver/agentgatewayreceiver/longpoll/ops_etag_test.go`（新） | 复合 ETag、唤醒不降级版本、空缓存仍带 metadata 三个用例 |
| `receiver/agentgatewayreceiver/manager_init.go`、`receiver.go` | 复用共享 Provider 初始化 store，注册 metadata provider / revision provider，pub/sub 变更桥接到 `NotifyOpsChanged`，Shutdown 优雅停止 |
| `extension/adminext/handlers_opsconfig.go`（新） | `GET/PUT/DELETE /api/v2/ops/configs`、`/keys`、`/audit`；校验错误 400、存储不可用 503、其它错误 503；变更日志记录操作者 |
| `extension/adminext/handlers_opsconfig_test.go`（新） | 读取含 schema、写入校验与持久化、删除、存储不可用 |
| `extension/adminext/router.go` | 注册 adminOnly 运维配置路由组 |
| `extension/adminext/extension.go`、`admin_deps.go` | 复用已有 storage 扩展初始化共享 store，Start/Shutdown 生命周期 |
| `extension/adminext/handlers.go` | `instanceResponse` 增加 `arthas_tunnel_source`、`ops_revision_applied`，从心跳 Labels 取值 |
| `extension/arthastunnelext/query_log.go`（新） | `sanitizeQueryForLog` / `sanitizeRequestURIForLog`，敏感 query 键统一替换为占位符 |
| `extension/arthastunnelext/query_log_test.go`（新） | 脱敏单测 3 个 |
| `extension/arthastunnelext/arthasuri_compat.go` | 三处 `query` 日志 + 三处 `request_uri` 日志全部脱敏 |

### Java 探针

| 文件 | 改动摘要 |
|---|---|
| `core/model/ServerMetadata.java` | 新增 `ops.arthas.tunnel_endpoint` / `ops.revision` 常量、解析与强类型访问器，空值等价 key 删除 |
| `arthas/TunnelUrlGenerator.java` | 新增四参数 / 配置对象重载，运营端值置于本地 `-D` 与推导之间；旧重载保持兼容 |
| `arthas/ArthasIntegration.java` | 保存运营端值与 revision；启动时纳入；`onServerMetadataChanged` 早退判据改为"最终端点是否变化"，支持 key 删除回退；新增来源判定与 `getOpsStatusAttributes()` |
| `dynamic/DynamicConfigManager.java` | `server_metadata` 按内容快照独立去重，先于业务版本去重通知监听器；移除重复通知，空 map 也通知以支持回退 |
| `identity/AgentIdentityProvider.java` | 可注入的动态心跳属性 supplier，合并进 `toProto()` attributes，异常安全 |
| `ControlPlaneManager.java` | 创建 ArthasIntegration 时注册动态属性来源 |
| `arthas/TunnelUrlGeneratorOpsTest.java`、`core/model/ServerMetadataOpsTest.java`（新） | 优先级 A、回退、旧重载兼容、metadata 解析单测 |

### 前端

| 文件 | 改动摘要 |
|---|---|
| `webui-react/src/pages/OpsConfigPage.tsx`（新） | schema 驱动的编辑表单、revision、变更日志表、按来源汇总条（已应用 / 本地覆盖 / 未知） |
| `webui-react/src/App.tsx`、`layouts/Sidebar.tsx` | adminOnly 路由 `/ops-config` 与菜单 |
| `webui-react/src/pages/InstancesPage.tsx` | Arthas Status 增加来源徽标与已应用 revision，新增"Local -D"筛选卡片与统计 |
| `webui-react/src/types/api.ts` | `Instance` 增加两个来源字段 |

### 文档

- `docs/2026-11-24/ops-config-p0.md`：本记录。

## 验收记录

| 项目 | 结果 |
|---|---|
| Go 全量测试 | `go test ./...` **exit 0**，39 个包 ok，FAIL 0；评审中出现的 mcpext 临时编译错误未复现，按用户指示忽略 |
| 定向测试 | `opsconfig`、`adminext`、`arthastunnelext`、`agentgatewayreceiver/...`、`longpoll` 全部通过，含新增 miniredis 集成用例 |
| 前端构建 | `npm run build` 通过（含 TypeScript 类型检查） |
| Java 编译/单测 | **按用户要求跳过**（此前 Gradle 依赖下载受 Maven Central 429 限制）；Java 侧改动保留，未做本地编译验证 |
| 镜像与部署 | 两次重建镜像并 `rollout restart`，Pod 正常 Running |
| 运营配置 API（经 Ingress） | GET 200（schema 1 项）、PUT 合法值 200 且 revision 递增、PUT 非法 URL 400 且 revision 不变、AUDIT 200 且含操作者与旧/新值、DELETE 200、RESTORE 200 |
| WS 日志脱敏（线上） | 带探针标记请求返回 400；日志 `request_uri` 中 `token` / `appName` 均为 `<redacted>`，`secret_leaked=0` |
| 实例列表 | `/api/v2/instances?status=all` 200，3 个实例，返回 `arthas_tunnel_source` 与 `ops_revision_applied` 字段（旧探针为空 → 页面显示"未知"） |
| 运营页路由 | `/ui/ops-config` 200 |
| 静态诊断 | 修改的 Go / TS / Java 文件 IDE 诊断 0 问题 |

## 未完成任务

| # | 任务 | 状态 |
|---|------|------|
| 1 | Java 探针编译与单测执行 | 按用户要求不做；此前环境依赖下载受限 |
| 2 | 升级运行中的 Java Agent jar 并重启，验证真实 `agentRegister` | 待部署侧：Agent 实例不在本机可管理的 Docker/K8s 中 |
| 3 | 端到端验证"改地址 → 在线探针收到新 metadata → 本地 `-D` 仍优先" | 依赖 #2 |
| 4 | 提交并推送本批改动 | 用户未要求，未执行 |

## 遗留问题与风险

- **Java 侧未编译验证**：按用户要求跳过；上线前需在依赖网络可用时跑 `:sdk-extensions:controlplane:test`。
- 探针必须升级 jar 才能消费新 metadata key 并上报来源；老探针忽略未知 key，向后兼容但不会自动获得地址，运营页会显示为"未知"。
- 运营配置存于 Redis；Redis 不可用时 admin API 返回 503，探针回落到现有推导链，不阻断 Arthas。
- 地址变化且 Arthas 正在运行时会停止实例等下次 attach，可能中断正在执行的 session；控制面 lifecycle 状态不会感知这次停止。
- collector 端下发的是 URL 值，不验证 Agent 侧 DNS/网络可达性；`custom-collector.devcloud` 的解析依赖 Agent 侧 hosts/DNS。
- 变更日志的 `operator` 取自 admin key 类型（如 super/operator），不是精确到人的身份；不含任何凭据。
- 唤醒响应保留探针上报的业务 Version；若探针尚未上报版本，则为空，不会降级为骨架 `"0"`。
- `config-template-render` 元数据仍为部署期引导值，运营端下发优先于它；该文件被 gitignore 忽略、未纳入版本控制，是长期隐患。
- 本批改动涉及 collector 与 Java 两个仓库，**均未提交**；`1cd` 等既有未跟踪文件未动。
