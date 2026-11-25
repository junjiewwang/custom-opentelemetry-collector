# MCP 支持动态插桩（dynamic_instrument）实施记录

> 文档性质：需求与实施进展记录。不包含任何密钥、令牌、账号等敏感信息。

## 1. 需求

让 AI Agent 能通过 MCP 工具管理动态插桩规则，而不是绕过规则中心直接下发 `dynamic_instrument` 任务。

### 已确认的决策

| 编号 | 决策 | 来源 |
| --- | --- | --- |
| D1 | 一期开放写工具（create / pause / resume / delete） | 用户拍板 |
| D2 | 作用范围固定为 service 对应的实例，不开放按实例 ID 指定目标 | 用户拍板 |
| D3 | 目标实例上限可配置，默认 100 | 用户拍板 |
| D4 | 仅允许管理 MCP 自己创建的规则（默认） | 上轮方案默认值，用户未提出异议 |
| D5 | 本期不把 InstrumentationManager 的归属从 adminext 迁移到 controlplaneext | 上轮方案默认值，用户未提出异议 |
| D6 | 代码内置默认值保持安全关闭；部署模板按 D1 显式开启（含 `allow_write`） | 对「开放」的实施解释，见「遗留问题 L1」 |
| D7 | 显式开启插桩能力但依赖不可用时，启动报错而不是静默降级 | 实施决策，便于快速发现配置错误 |

### 范围外

- 不提供 `instrument_rule_update`（mgr 的 UpdateRule 是 PUT 语义，容易误清字段）。
- 不提供直接下发原始 `dynamic_instrument` 任务的工具。
- 不改 protobuf、不改探针协议、不改 taskengine 的任务类型常量。

## 2. 现状与架构

- `extension/mcpext/` 原有 17 个工具，全部为 Agent 管理 / Arthas 命令 / 异步 Session 类，工具注册集中在 `registerTools()`。
- 动态插桩后端能力已经具备：`instrumentationmanager.InstrumentationManager`（规则 CRUD、目标状态、运行时快照、reconcile）与 `/api/v2/instrumentation/rules` REST 接口。
- `InstrumentationManager` 由 `adminext` 在 `Start()` 中唯一创建，并启动 reconcile / GC 后台协程。`mcpext` **复用该实例**，不自行创建，避免数据隔离或重复下发。

### 依赖注入

- 在 `instrumentationmanager` 包定义中立接口 `Provider`，由 `adminext.Extension` 实现；`mcpext` 只依赖接口，不 import `adminext`。
- `mcpext` 通过 `Dependencies()` 声明对 `admin` 的条件依赖，保证「admin 先启动、mcp 先关闭」。
- 目标实例数校验：优先通过可选接口 `GetAgentRegistry()` 精确调用 `GetInstancesByService`；不可用时退化为按 `GetOnlineAgents` 过滤计数。
- 调用者身份：API Key 摘要派生为 `mcp:key-<摘要前 8 位>`，由鉴权中间件写入 request context，再经 `server.WithHTTPContextFunc` 传导到 MCP 工具 handler。

### 工具集

| 类别 | Tool | 对应 mgr 方法 |
| --- | --- | --- |
| 只读 | `instrument_rule_list` | `ListRules` |
| 只读 | `instrument_rule_get` | `GetRule` + `ListTargetStatuses` |
| 只读 | `instrument_rule_snapshot` | `GetRuleRuntimeSnapshot` / `RefreshRuleRuntimeSnapshot` |
| 写 | `instrument_rule_create` | `CreateRule` |
| 写 | `instrument_rule_pause` | `PauseRule` |
| 写 | `instrument_rule_resume` | `ResumeRule` |
| 写 | `instrument_rule_delete` | `DeleteRule` |

三个写状态变更工具共用 `handleInstrumentRuleAction`，避免重复实现。

### 安全护栏

- `enabled` / `allow_write` 两级开关；关闭写开关时写工具不注册（AI 侧不可见）。
- 作用域固定 `service`；创建前校验目标实例数不超过 `max_targets`。
- 默认禁止 JDK / OTel 自身类前缀插桩，支持配置白名单。
- `force`、`capture_args`、`capture_return` 默认禁用，避免采集敏感参数。
- `manage_only_own_rules` 默认开启：写操作先校验 `CreatedBy` 是否为 MCP 前缀。
- 调用者标识由 API Key 的摘要派生，不记录、不落库密钥原文；写操作记录结构化审计日志。

## 3. 改动清单

| 文件 | 类型 | 说明 |
| --- | --- | --- |
| `extension/controlplaneext/instrumentationmanager/provider.go` | 新增 | 中立 `Provider` 接口 |
| `extension/adminext/instrumentation_provider.go` | 新增 | `Extension` 实现 Provider |
| `extension/mcpext/config.go` | 修改 | `InstrumentationConfig`、默认值、校验 |
| `extension/mcpext/extension.go` | 修改 | 字段、条件依赖、依赖发现 |
| `extension/mcpext/mcp_server.go` | 修改 | HTTP context 注入、条件注册工具 |
| `extension/mcpext/auth.go` | 修改 | 调用者标识注入 context |
| `extension/mcpext/instrumentation_policy.go` | 新增 | 作用域 / 类前缀 / 上限 / 归属策略 |
| `extension/mcpext/instrumentation_formatter.go` | 新增 | MCP 侧精简结果视图 |
| `extension/mcpext/tools_instrumentation.go` | 新增 | 7 个工具的定义与 handler |
| `config/template/config.yaml` | 修改 | 工具清单注释与 instrumentation 配置段 |
| `extension/mcpext/*_test.go` | 新增/修改 | 策略、handler、配置、鉴权的单测 |
| `ai-skills/mcpext-arthas-diagnostic/references/tool-catalog.md` | 修改 | 补充可选插桩工具说明 |
| `README.md` | 修改 | MCP 能力清单补充 |

## 4. 实施进展

| 阶段 | 状态 |
| --- | --- |
| 需求确认与方案评审 | 已完成 |
| 关键代码核对（Provider 位置、启停顺序、作用域解析、mcp-go context API） | 已完成 |
| 中立 Provider 接口与 adminext 实现 | 已完成 |
| mcpext 配置与依赖注入 | 已完成 |
| 策略模块与工具实现 | 已完成 |
| 鉴权身份与审计日志 | 已完成 |
| 单元测试与相关包测试 | 已完成 |
| 配置模板、技能目录、README 同步 | 已完成 |
| 集成验证（minikube 实际调用） | 已完成（见第 9 节） |

## 5. 验证记录

- `go build ./extension/mcpext/... ./extension/adminext/... ./extension/controlplaneext/instrumentationmanager/...`：通过。
- `go test ./extension/mcpext/...`：通过。新增用例覆盖策略表驱动、实例计数两条路径、上限拒绝、JDK 类拒绝、写标志拒绝、归属校验、工具注册数量（关闭 0 / 只读 3 / 读写 7）、身份摘要与 context 注入。
- `go test ./extension/adminext/... ./extension/controlplaneext/...`：通过。
- IDE 静态诊断：本次改动文件无新增告警。
- minikube 端到端验证：已完成，详见第 9 节（工具集、规则链路、操作人与审计、归属护栏、Ingress 健康检查、业务类插桩生效与撤销）。
- MCP 动态租户 key 鉴权：已完成，详见第 10 节（静态 super / `tk_` / `ok_` 三类 key 的权限边界、KeyID 审计、跨租户拒绝与只读过滤）。
- 复核（第 11 节）：`go test -count=1 ./...` 通过；minikube 复验 `instrument_rule_get` / `snapshot` 跨租户拒绝，并发现 R1–R5 偏差与 L11 高危问题。
- 修复与复验（第 12 节）：L11 与 R1–R4 全部修复，无缓存全量测试通过，minikube 逐条复验通过；顺带修复一个既有时序脆弱测试。
- P2 租户能力开关（第 13 节）：`go test -count=1 ./...` 与前端 `npm run build` 通过；minikube 验证关闭/恢复、全局 key 与 Web API 受约束、Pause/Delete 清理路径。
- P3 / scope / AI 样例（第 14 节）：`go test -count=1 ./...` 与前端构建通过；minikube 验证部署默认 false/true 的继承行为、显式覆盖优先、写工具 scope 授权。

## 6. 未完成任务

- 已完成：minikube 端到端验证（含业务类插桩真正生效与撤销），见第 9 节。
- 已完成：L11 跨租户隔离、R1–R4 鉴权兼容性与启动兼容修复，见第 12 节。
- 已完成：P2 `Tenant.Capabilities` 运营端开关（仅 redis 模式生效），见第 13 节。
- 已完成：P3 部署级默认兜底、scope 级写权限、面向 AI 的调用流程样例，见第 14 节。
- 已完成：清除租户能力回继承态、技能包触发描述补齐，见第 15 节。
- 可选：把 Arthas 命令类工具纳入独立 scope（如 `arthas:exec`），需单独评估现有 `tk_` key 迁移。

## 7. 遗留问题

- **L1**：D6 是对「开放」的解释：代码默认值关闭、部署模板开启。若期望「默认配置即开启写工具」，需调整 `createDefaultConfig()` 默认值。
- **L2**：已解决。minikube 端到端验证已完成（第 9 节），覆盖部署链路、工具集、规则一致性、操作人与审计、归属护栏，以及业务类插桩生效与撤销；L9 亦已解决。
- **L3**：已解决。规则审计与五个写入口的操作人已通过 `Actor` context 补齐（见第 8 节 P0），不再只有 MCP 侧结构化日志。
- **L4**：目标实例数的校验发生在 MCP 侧创建时刻，与服务实例动态伸缩之间存在时间窗口；超限后由规则中心自身的状态流转处理，不做额外补偿。
- **L5**：`InstrumentationManager` 归属仍在 `adminext`，长期看应迁入 `controlplaneext`（D5 延后）。因 `mcpext` 只依赖 `Provider` 接口，迁移时仅需调整配置。
- **L6**：历史设计文档 `docs/2026-03-12/mcp-extension-design.md` 的 Phase 3 仍标记为未完成，且其中的 `instrument_add/remove/list` 与本次落地语义不同，本次未改动，避免产生误导性「已完成」标记。

## 8. 新增需求与本期实施范围（已收到「开始实施」指令）

### 本期决策与范围

| 编号 | 决策 |
| --- | --- |
| D8 | 关闭租户能力时**不需要撤销**存量规则：只拦截新增/恢复等会产生插桩的写入口；reconcile 保持原行为，存量规则继续按现有状态机维护 |
| D9 | P2 只保证 `TenantRepository` 的 redis 模式生效，memory 模式保持原样，并在文档标注限制 |
| D10 | 本期只实施 P0（需求 B 全部）+ P1（`CapabilityProvider` 预留接口与检查点），不实现租户能力字段、运营端开关与前端 |

本期验收口径：Provider 未注入时外部行为不变（所有能力检查跳过），但审计与规则操作人字段补齐；`CapabilityProvider` 支持可选注入且检查点可由单测验证。

### 需求 A：插桩能力迁到运营端，按租户控制「该租户下 Agent 允许被插桩」

**已确认**：语义为被插桩 Agent 所属租户的授权（用户拍板）；载体为 `Tenant.Capabilities`（用户拍板）。

- **授权点变更（重要）**：上一版按「MCP 会话/工具可用性」设计，授权落在 `mcpext`；按新语义必须**前移到规则中心**。Web 工作台与 MCP 都调用同一个 `InstrumentationManager`，只有放在 mgr 层才不会被 Web 端绕过。`mcpext` 保留自己的安全基线（类前缀、`max_targets`、写开关），且**不再需要接入租户 Key 校验**。
- **归属链路（已核对）**：`AgentInfo` 没有租户字段，但 `appmanager.AppInfo.TenantID` 是 app→tenant 归属的单一事实来源；规则带 `AppID`，`resolveTargets` 按 appID 过滤实例，因此 `rule.AppID → AppManager.GetApp → TenantID → Tenant.Capabilities` 一次查询即可，无需逐实例解析。
- **载体设计（P2）**：`Tenant.Capabilities` 中用三态指针字段（nil = 继承部署级默认值，true/false = 覆盖），保证老租户数据与现有部署行为不变；`UpdateTenantRequest` 增加该字段，`TenantRepository.Save` 全量覆盖 JSON，天然兼容。运营端在租户管理页加开关，`/api/v2/tenants` 仍在 `requireAdminMiddleware` 下。不塞进 `opsconfig`，避免污染下发给探针的配置 schema。
- **注入方式（P1）**：在 `instrumentationmanager` 定义中立 `CapabilityProvider`（`IsInstrumentationAllowed(ctx, appID) (bool, error)`），经 `NewInstrumentationManager` 的 Option / `ComponentFactory.CreateInstrumentationManager` **可选注入**（nil = 不启用，行为不变）。P2 再由 `adminext` 用 `tenantMgr.Tenants` + `appmanager.AppManager` 组装实现。`mcpext` 无需改动。
- **检查点（P1）**：`CreateRule` / `UpdateRule` / `ResumeRule` 在写存储前检查；Pause / Delete 属于取消插桩，不拦截，保证能力关闭后仍可清理；reconcile 不检查（D8）。fail-closed：Provider 返回错误时拒绝。
- **关闭能力后的存量规则（D8）**：不主动 pause、不下发 `dynamic_uninstrument`，即不新增 reconcile 撤销分支；只拦新增/恢复/更新等人工写入口，存量规则惰性保留。
- **多副本一致性（D9）**：P2 仅保证 redis 模式生效；memory 模式保持原样，需在文档标注「运营端开关不保证跨副本一致」。redis 模式每次下发查库成本可用短 TTL 缓存 + 变更失效缓解。
- **关于「规则没有租户归属」的澄清**：`Rule` 本身带 `AppID` 与 `ServiceName`，`ServiceInfo` 按 `AppID` 分组存储，`AppInfo.TenantID` 是 app→tenant 的单一事实来源，因此租户归属可由 `Rule.AppID` **权威派生**，无需在规则里冗余存 `tenant_id`。之前的表述不准确，实际是「无冗余字段」而非「无归属」；冗余字段反而会在 `SetTenantID`（app 迁移租户且不回填历史数据）后与当前归属不一致。
- **两种租户语义要分开**：授权判定用**目标 Agent 的当前租户**（实时按 `AppInfo.TenantID` 派生，app 迁移后立即生效）；创建者/审计归属若要落库，应在创建时**快照**，不能依赖实时派生，否则历史审计会随 app 迁移漂移。
- **另列的遗留问题**：`manage_only_own_rules` 仍只按 `CreatedBy` 的 `mcp:` 前缀判断，没有把「调用者租户」与「规则目标租户（派生）」做比对，MCP 侧仍可能跨租户管理规则。修复方式不是给规则加 `tenant_id`，而是复用 admin 已有的租户 Key 校验链路（`KeyValidator` / `NewTenantAuthMiddleware`，`tk_` 硬绑定租户、`ok_`/`sk_` 全局）把 TenantID 写入 MCP context，再与 `rule.AppID → AppInfo.TenantID` 的派生结果比对；`Actor.TenantID` 字段已在 P0 备好。当前 `mcpext` 只做静态 key 集合比对，且部署配置里的 MCP key 是无前缀静态串，尚未接入该链路。

### 需求 B：context 传递操作人补齐

- 现状：`RuleAuditEntry` 无操作人字段；`newAuditEntry` 是集中构造点但只填来源/动作/状态；`PauseRule` / `ResumeRule` / `DeleteRule` 只更新 `UpdatedAtMillis`，不改 `UpdatedBy`；后台 reconcile 使用 `context.Background()`。
- 可复用范式：`adminext` 有 `WithKeyType` / `KeyTypeFromContext` / `TenantIDFromContext`，`opsconfig.ChangeLog` 已有 `Operator` 字段，admin handler 用 `KeyTypeFromContext` 作为 operator。
- 建议在 `instrumentationmanager` 定义中立 `Actor`（ID / Type / TenantID）与 `WithActor` / `ActorFromContext`；在审计条目汇总处统一填 `Operator`（新增 `omitempty` 字段，兼容旧数据）。MCP 侧在 `instrumentationContext()` 一处注入；admin 侧在 `/instrumentation` 路由加中间件注入；reconcile 无 actor 时审计标 `system`，且不覆盖规则上的操作人字段。
- **ctx 权威 actor 覆盖（用户明确要求）**：五个写入口 `CreateRule` / `UpdateRule` / `PauseRule` / `ResumeRule` / `DeleteRule` 一律以 `ActorFromContext(ctx)` 为准，防止客户端伪造操作人：
  - `CreateRule`：`CreatedBy` 与 `UpdatedBy` 都写 actor.ID，`CreateRuleRequest.CreatedBy` 降级为兼容回落；
  - `UpdateRule`：`UpdatedBy` 写 actor.ID，`UpdateRuleRequest.UpdatedBy` 降级为兼容回落；
  - `PauseRule` / `ResumeRule` / `DeleteRule`：除现有 `UpdatedAtMillis` 外**补写 `UpdatedBy`**；
  - `RuleAuditEntry.Operator` 复用同一个 actor.ID，保证规则详情与审计一致（单一事实来源）。
  - 优先级：ctx actor > 请求体字段 > 空。ctx 无 actor 时保留旧行为以兼容内部调用，但所有外部入口必须注入 actor，并逐入口做测试覆盖。
- **防伪造边界**：actor.ID 统一 trim + 长度上限 + 控制字符过滤（`normalizeActorID`），避免请求体写入超长值或日志注入内容；`TenantID` 与 actor.ID 分开存，不能拿租户当操作人；不写入原始密钥（MCP 用 key 摘要，admin 用 key 类型/租户）。
- **兼容性**：`RuleAuditEntry.Operator` 用 `omitempty`，老数据反序列化不报错；`cloneRuleAuditEntry` 是值拷贝，自动带上新字段。
- **测试**：伪造请求体 `CreatedBy=admin` 但 ctx actor 为 MCP 摘要时断言被覆盖；五个写方法都断言 `UpdatedBy`；无 actor 回落路径；旧 JSON 反序列化。

### 分阶段与本期进展

| 阶段 | 内容 | 本期状态 |
| --- | --- | --- |
| P0 | `Actor` context + `RuleAuditEntry.Operator` + 五个写入口权威覆盖与 `UpdatedBy` | 已完成 |
| P1 | `CapabilityProvider` 中立接口 + 可选注入 + 检查点（未接租户实现） | 已完成 |
| P2 | `Tenant.Capabilities` + `/api/v2/tenants` + 运营端开关（仅 redis 模式） | 已完成（第 13 节） |
| P3 | 静态配置精简为部署级默认值/兜底，配置模板与文档更新 | 已完成（第 14 节） |

### 本期验收清单

- [x] `Actor` 载体与归一化（trim / 长度上限 / 控制字符过滤）。
- [x] `RuleAuditEntry.Operator` 字段与两处审计汇总点填充（manual 用 actor，reconcile 用 system）。
- [x] 五个写入口以 ctx actor 覆盖请求体操作人，并补齐 `UpdatedBy`。
- [x] MCP 与 admin 两个外部入口注入 actor。
- [x] `CapabilityProvider` 接口、Option 注入、三个检查点（fail-closed）。
- [x] 单元测试与相关包回归测试通过。

### 本期实施结果

**改动文件**

| 文件 | 说明 |
| --- | --- |
| `extension/controlplaneext/instrumentationmanager/actor.go` | 新增 `Actor`、`WithActor` / `ActorFromContext`、`normalizeActorID`、规则与审计操作人取值 |
| `extension/controlplaneext/instrumentationmanager/capability.go` | 新增 `CapabilityProvider`、函数式 `Option` / `WithCapabilityProvider`、fail-closed 检查 |
| `extension/controlplaneext/instrumentationmanager/service.go` | 五个写入口权威覆盖与 `UpdatedBy`；审计汇总填 Operator；三个能力检查点 |
| `extension/controlplaneext/instrumentationmanager/reconcile.go` | `RuleAuditEntry.Operator`（omitempty）；reconcile 审计标 system |
| `extension/controlplaneext/instrumentationmanager/factory.go` | `NewInstrumentationManager` 支持 Option，保持既有调用点不变 |
| `extension/controlplaneext/component_factory.go` | `CreateInstrumentationManager` 透传 Option |
| `extension/mcpext/tools_instrumentation.go` | `instrumentationContext()` 注入 MCP actor |
| `extension/adminext/instrumentation_actor.go`、`router.go` | `/instrumentation` 路由注入 admin actor |
| `actor_test.go` / `service_actor_test.go` / `instrumentation_actor_test.go` / `tools_instrumentation_test.go` | 单测 |

**验证记录**

- `go build ./...`：通过。
- `go test ./...`：通过（无失败项）。
- `go test ./extension/controlplaneext/instrumentationmanager/... ./extension/mcpext/... ./extension/adminext/...`：通过。
- IDE 静态诊断：相关目录无新增告警。
- 新增用例覆盖：actor 归一化与 context、伪造请求体被覆盖、五个写方法 `UpdatedBy`、无 actor 回落、旧审计 JSON 兼容、Provider 拒绝/出错/放行/nil 四条路径、Pause/Delete 不受能力限制、MCP 与 admin 入口注入。

**本期未做（D10）**

- 未改 `tenantmanager.Tenant`、未加运营端开关与前端页面；`CapabilityProvider` 当前没有生产实现，`adminext` 创建 mgr 时未传 Option，因此线上行为与实施前一致。
- 未改 `mcpext` 的配置结构与工具集合，未改配置模板。

**后续遗留**

- **L7**：已解决（第 13 节）。`adminext` 已提供基于 `tenantMgr` + `appmanager` 的 `CapabilityProvider` 实现并在 `initInstrumentationManager` 注入；memory 模式按 D9 不启用该检查，文档标注限制。
- **L8**：已解决（第 10、12 节）。`mcpext` 复用共享租户 key 校验链路，写工具比对「调用者租户」与 `rule.AppID → AppInfo.TenantID` 派生的目标租户，只读工具按租户过滤；`X-Tenant-Id` 对 `tk_` 无效。静态 super key 为全权限超级管理员，`ok_` 为全局权限。其余 17 个工具曾实测可跨租户执行 Arthas 命令（L11），已在第 12 节通过统一工具 guard 修复并复验。

## 9. minikube 端到端验证记录

### 环境与部署链路

- 按既有链路执行：配置模板 → `config-template-render` 渲染 → `config/build/config.yaml` → `make k8s-deploy` 生成 ConfigMap / Deployment 并 apply → `kubectl rollout restart` → Pod `1/1 Running`。
- 镜像在 `eval $(minikube docker-env)` 下由 `make docker-build` 构建；镜像生成时间晚于本轮源码修改时间，且运行时行为验证了本轮新增特性。
- 渲染产物校验：YAML 可解析、无残留占位符，`mcp.instrumentation` 的 `enabled=true` / `allow_write=true` / `max_targets=100` 均生效。
- 启动日志出现 `Discovered InstrumentationManager`，确认 Provider 注入与扩展启停顺序生效。
- MCP 与 admin 通过 `kubectl port-forward` 在本机访问；Ingress 以 `Host` 请求头访问 `/health` 返回 200。验证结束后端口转发已释放。

### 验收结果

| 项目 | 结果 |
| --- | --- |
| MCP 鉴权 | 无 key → 401；带 key 完成 `initialize` + `tools/list` |
| 工具集 | 24 个 = 原有 17 个 + 7 个 `instrument_rule_*` |
| 规则创建（MCP） | 成功；`created_by` / `updated_by` 均为 MCP key 摘要身份；admin API 可查询到同一规则 |
| 目标与状态 | 规则解析出 3 个目标（1 在线、2 离线），任务下发；`instrument_rule_get` 返回各目标状态与任务 ID |
| 运行时快照 | `instrument_rule_snapshot` 返回真实快照；业务类插桩时 `effective_targets=1`、`is_effective=true`、`runtime_status=active`，删除规则后变为 `0` / `false` / `reverted` |
| 业务类定位 | 通过 MCP 的 `arthas_sc` 与 `arthas_exec(sm)` 找到 demo 应用的普通业务类 `com.example.demo.controller.HealthController` 及无参方法 `health`，避开 JDK / Spring 框架类与 CGLIB 代理类 |
| 审计 Operator | manual 审计条目为 MCP actor；reconcile 条目为 `system`；pause / resume / delete 后 `updated_by` 保持一致 |
| 防伪造 | admin API 请求体传伪造 `created_by`，落库为 `admin:super`（ctx actor 覆盖） |
| 归属护栏 | MCP 尝试 pause admin 自建规则被拒绝（`manage_only_own_rules`） |
| 清理 | 全部测试规则均软删除，规则列表无残留；端口转发已释放 |

### 结论与遗留

- **L9**：已解决。先前选用 Spring 框架类时 JVM 重定义失败（`attempted to change superclass or interfaces`），改用普通业务类后插桩真正生效：控制面状态 `applied`、任务 `success`，运行时快照 `is_effective=true`；删除规则后探针侧 `reverted`，完整验证了 apply → effective → revert 生命周期。原来的失败来自所选类，不是 MCP / 规则中心缺陷。
- L2 已解决：部署链路、工具集、规则中心一致性、操作人与审计、归属护栏、插桩生效与撤销均通过实际调用验证。

## 10. MCP 动态租户 key 鉴权（已实施）

### 决策

| 编号 | 决策 |
| --- | --- |
| D11 | 保留静态 super key，作为拥有全部权限的超级管理员；`api_keys` 变为可选 |
| D12 | 允许 `ok_` 全局 operator key 调用 MCP（按上轮推荐默认执行，用户未另行指定） |
| D13 | scope 级写权限后置；`KeyID` 本批一起做，进入审计身份 |
| D14 | 租户鉴权中间件抽到中立公共包，admin 与 MCP 共用 |

### 改动清单

| 文件 | 说明 |
| --- | --- |
| `extension/tenantauth/identity.go`、`middleware.go` | 新增中立包：`Identity`（TenantID / KeyType / KeyID / Scopes / Impersonating）、`Validator`、`New` 中间件（`sk_` / `ok_` / `tk_`、impersonation、health / OPTIONS / WS 放行） |
| `extension/tenantauth/middleware_test.go` | 公共包单测 |
| `extension/adminext/tenant_auth.go` | 改为薄适配层，保留原有导出名与 `requireAdminMiddleware`，委托公共包 |
| `extension/adminext/router.go` | `authMiddleware` 改用 `NewTenantAuthMiddlewareWithIdentity`，透传 KeyID / Scopes |
| `extension/controlplaneext/tenantmanager/api_key.go`、`api_key_service.go` | `KeyValidation` 新增 `KeyID`，`ValidateAPIKey` 各分支填充 |
| `extension/mcpext/config.go` | `AuthConfig.DynamicKeys`（默认 true）；`Validate` 放宽静态 key 必填、禁止 `none` + 动态 |
| `extension/mcpext/extension.go` | 通过 `controlplane` 的 `GetMultiTenantManager()` 获取共享 `Keys` 构建 `tenantauth.Validator`；`startHTTPServer` 改用公共中间件 |
| `extension/mcpext/auth.go` | 删除静态 keySet 中间件与摘要 principal，改为由 `Identity` 派生 `mcpPrincipal` |
| `extension/mcpext/mcp_server.go` | `WithHTTPContextFunc` 转发完整 `Identity` 到工具 handler |
| `extension/mcpext/tools_instrumentation.go` | Actor 填充 TenantID；各 handler 接入 fail-closed 租户授权 |
| `extension/mcpext/instrumentation_authz.go` | 新增：目标租户派生（`appID → AppInfo.TenantID`）、写工具授权、只读规则过滤 |
| `extension/mcpext/*_test.go` | 身份 / 授权 / 配置单测 |
| `config/template/config.yaml` | `mcp.auth.dynamic_keys: true`，注释区分静态 super key 与动态租户 key |

### 验收记录

- `go build ./...` / `go test ./...`：通过（无失败项）。
- 配置渲染：YAML 可解析、无残留占位符、`dynamic_keys=true`，静态 super key 仍配置 1 个。
- 镜像与部署：`make docker-build` 生成新镜像，`make k8s-deploy` + `rollout restart` 成功；启动日志出现 `Discovered tenant API key validator`。
- 静态 super key：`initialize` + `tools/list` 成功，24 个工具（含 7 个插桩工具），全权限保持；无 key → 401。
- `tk_`（非 admin 租户）：初始化成功；对 admin 租户应用创建规则被拒（即使带 `X-Tenant-Id: admin` 伪装）；`instrument_rule_list` 看不到其他租户规则。
- `tk_`（admin 租户）：创建规则成功，`created_by` 形如 `mcp:tenant:admin:<KeyID>`，KeyID 进入审计身份。
- `ok_` 全局 key：`instrument_rule_list` 可见全部规则（全局权限）。
- 清理：验证规则已删除、3 个测试 key 已 revoke、端口转发释放、含明文密钥的临时文件已删除。

### 遗留

- **L10**：已解决。MCP 不再把共享静态口令当作租户身份；静态 super key 保留为全权限超级管理员，动态 `tk_` / `ok_` key 由共享租户管理器校验，租户身份进入 context、Actor 与审计。
- **L11**（高危，已实测并修复）：本批租户授权最初仅覆盖 7 个 `instrument_rule_*` 工具；复核时用非 admin 租户的 `tk_` key 实测 `arthas_exec` 可跨租户下发命令，属跨租户远程命令执行。已在第 12 节通过统一工具 guard 修复并复验，状态为已解决。
- scope 级写权限（D13 后置）与 `Tenant.Capabilities`（P2）仍未实施。

## 11. 复核记录（求真务实，未改代码）

### 复核方法

- `go test -count=1 ./...`（禁用缓存）：通过，无失败项。
- 在 minikube 环境重新用运营端临时创建的 `tk_` key 与静态 super key 逐条复验；临时 key、规则、端口转发、含明文密钥的文件在复核后全部清理。

### 确认符合预期

| 项目 | 结果 |
| --- | --- |
| `instrument_rule_get` / `instrument_rule_snapshot` 跨租户 | 非 admin 租户 `tk_` key 均被拒，错误为「调用者租户与目标应用所属租户不一致」（上一轮只验证了 list/create，本轮补齐） |
| 插桩工具租户授权与只读过滤 | 与第 10 节结论一致 |
| 无 key | 401 |

### 发现的不符合预期 / 需修正

| # | 问题 | 证据 | 影响 | 建议修复 |
| --- | --- | --- | --- | --- |
| R1 | `Authorization: bearer`（小写）被拒 | 实测返回 401；原 `mcpext.extractAPIKey` 用 `EqualFold(parts[0],"Bearer")` 支持小写，公共包改为 `HasPrefix(auth,"Bearer ")` | 兼容性回归，部分 MCP 客户端会 401 | 公共包改为大小写不敏感解析 |
| R2 | 请求头优先级反转 | 同时带无效 `X-API-Key` 与有效 `Authorization: Bearer <super key>` 时实测 401；原 MCP 实现 Bearer 优先，公共包为 X-API-Key 优先 | 兼容性回归 | 统一为 Bearer 优先，或明确文档化并升级客户端 |
| R3 | 无效 key 的响应码与响应体变化 | 公共包统一返回 `401` + 纯文本；原 MCP 返回 `403` + JSON 错误体 | 客户端错误处理可能依赖原语义 | 评估是否保留 `403` + JSON |
| R4 | `DynamicKeys` 默认 true 导致无多租户部署启动失败 | `mcpext/extension.go` 在 `DynamicKeys && api_key` 时要求 `controlplane` 提供 tenant manager，否则 `Start` 报错 | 对未启用多租户的部署是破坏性变化 | 改为「检测到 tenant manager 才启用动态校验」，或默认值改 false 并在模板显式开启 |
| R5 | L11 升级为跨租户命令执行 | 实测 `agent_info` / `arthas_exec` 越权成功 | 高危，多租户隔离失效 | 优先于 P2 修复 |

### 上一轮汇报的偏差

- 上一轮写「其余 17 个工具仍可能跨租户列出或操作 Agent」是推断、未实测；本轮实测确认可跨租户执行 Arthas 命令，严重性被低估。
- 上一轮写「静态 super key 全权限保持」，未覆盖小写 Bearer 与双请求头场景，实际存在 R1 / R2 两处兼容性回归。
- 第 10 节的 `go test ./...` 通过结论经 `-count=1` 复跑确认可靠。

### 待办（历史记录，已在第 12 节完成）

- R1–R4 属代码修复，按用户规则需明确「开始实施」后再改。
- R5（L11）建议先于 P2 处理；P2（`Tenant.Capabilities` 运营端开关）仍未开始。

## 12. L11 与 R1–R4 修复、验证（已完成）

### 实施范围

按第 11 节复核结论实施：**先修 L11 高危越权，再修 R1–R4**，P2 未纳入本批。

### 改动清单

| 文件 | 说明 |
| --- | --- |
| `extension/tenantauth/middleware.go` | 新增 `Option`：`WithBearerPriority()`（R2）、`WithMCPErrorResponses()`（R3）；`extractAPIKey` 的 Bearer scheme 改为大小写不敏感（R1）；admin 保持原有优先级与纯文本响应 |
| `extension/tenantauth/middleware_test.go` | R1 / R2 / R3 单测（大小写、双请求头、401/403 与 JSON 响应体） |
| `extension/mcpext/extension.go` | `startHTTPServer` 传入两个兼容 Option |
| `extension/mcpext/config.go` | `DynamicKeys` 默认值改为 `false`（R4），模板 `dynamic_keys: true` 显式开启，未启用多租户的部署不再启动失败 |
| `extension/mcpext/config_test.go` | 默认值与校验用例同步 |
| `extension/mcpext/mcp_server.go` | 新增 `addTool`：全部 22 处工具注册统一经过租户 guard；`handleListAgents` 按租户过滤 |
| `extension/mcpext/tool_authz.go` | 新增统一 `authorizeToolCall`：`agent_id` / `session_id` 归属校验，`authorizeAgent` 按 `agent.AppID → AppInfo.TenantID` 派生目标租户；全局身份放行，无身份 fail-closed |
| `extension/mcpext/tool_authz_test.go` | Agent / Session 归属、跨租户拒绝、全局放行、列表过滤单测 |
| `extension/mcpext/tools_arthas_*.go`、`tools_instrumentation.go` | 注册入口统一改为 `w.addTool` |
| `extension/controlplaneext/instrumentationmanager/reconcile_test.go` | 修复既有时序脆弱测试（见下） |

### 顺带修复的既有测试问题

无缓存全量测试暴露 `TestReconcileRuleReappliesRecoveredOnlineTarget` 稳定失败：共享 helper 使用 2ms 心跳 TTL + 1ms offline checker，reconcile 时 Agent 已被标记 offline，因此不会重新下发。该问题与本次业务改动无关，仓库中同类测试已采用「较长 HeartbeatTTL 避免 offline detection 竞态」的写法。新增 `newTestInstrumentationServiceWithAgentTTL` helper，让该用例使用 5s TTL / 1s 检查间隔；`go test -count=3 ./extension/controlplaneext/instrumentationmanager/...` 稳定通过。

### 验收记录

- `go build ./...`：通过。
- `go test -count=1 ./...`：通过（无失败项）；`go test -count=3 ./extension/controlplaneext/instrumentationmanager/...`：通过。
- 配置渲染：`dynamic_keys=true`；`make docker-build` + `make k8s-deploy` + `rollout restart` 成功；日志出现 `Discovered tenant API key validator`。
- R1：`Authorization: bearer <super key>` → 200（修复前 401）。
- R2：同时带无效 `X-API-Key` 与有效 Bearer → 200（修复前 401）；admin 仍保持 X-API-Key 优先（单测覆盖）。
- R3：无 key → 401 + `{"error":"unauthorized"...}`；无效 key → 403 + `{"error":"forbidden"...}`。
- R4：代码默认 `DynamicKeys=false`（单测覆盖），模板显式 true，部署正常启动。
- L11（`tk_` 非 admin 租户 key）：`list_agents` 过滤后为空；`agent_info` / `arthas_exec` / `arthas_session_open` 对 admin 租户 Agent 全部返回「租户授权失败: 禁止跨租户操作 Agent」；`instrument_rule_list` 为空；`instrument_rule_get` 对 admin 租户规则被拒。
- 全局身份回归：静态 super key 的 `list_agents` 可见 Agent、`arthas_exec version` 成功、`instrument_rule_create` / `get` 正常。
- 清理：验证规则删除、临时 key revoke、端口转发释放、含明文密钥的临时文件删除。

### 状态

- **L11 / R1–R5**：已解决。
- 后续批次：P2 已完成（第 13 节）；剩余 P3（静态配置精简）、scope 级写权限、面向 AI 的调用流程样例。

## 13. P2 租户能力开关（已实施）

### 决策

按第 8 节 P2 方案与推荐默认执行：只做 `dynamic_instrumentation` 单开关；`nil` 默认允许（继承部署默认）；全局 key 也受目标租户能力约束；前端开关一起做；不做 P3 配置精简。D8（关闭能力不撤销存量）、D9（仅 redis 生效）继续遵守。

### 改动清单

| 文件 | 说明 |
| --- | --- |
| `extension/controlplaneext/tenantmanager/model.go` | 新增 `TenantCapabilities{DynamicInstrumentation *bool}`、`Tenant.Capabilities`、`UpdateTenantRequest.DynamicInstrumentation`（三态：nil=不变/继承，false=关闭，true=开启） |
| `extension/controlplaneext/tenantmanager/service.go` | `UpdateTenant` 应用开关，替换而非原地修改 Capabilities（避免绕过 memory repo 的 Save） |
| `extension/controlplaneext/tenantmanager/capabilities_test.go` | 老 JSON 兼容、三态更新、不原地修改存储对象 |
| `extension/controlplaneext/tenantmanager/manager.go` | `MultiTenantManager.Type` 暴露 repository 类型，供 D9 判定 |
| `extension/adminext/instrumentation_capability.go` | `tenantCapabilityProvider`：`appID → AppInfo.TenantID → Tenant.Capabilities`，nil=允许，查不到 app/租户返回 error（fail-closed）；`instrumentationCapabilityOptions` 仅 redis 注入 |
| `extension/adminext/extension.go` | `initInstrumentationManager` 通过 `WithCapabilityProvider` 注入 |
| `extension/adminext/instrumentation_capability_test.go` | provider 允许/拒绝/继承/空 appID/未知 app/后端错误、memory 模式不注入 |
| `extension/adminext/webui-react/src/types/api.ts`、`api/client.ts`、`pages/TenantsPage.tsx` | 租户列表显示 Instrumentation 状态、编辑表单开关、`updateTenant` 字段 |
| `extension/adminext/webui-react/dist/` | 重新 `npm run build` 生成前端产物 |

`tenant_handlers.go` 使用 `decodeJSON[UpdateTenantRequest]`，字段自动透传，无需改动。

### 验收记录

- `go build ./...`、`go test -count=1 ./...`：通过；前端 `npm run build`（`tsc -b && vite build`）：通过，dist 含新开关。
- 部署：重新渲染配置、构建镜像、`make k8s-deploy` + `rollout restart` 成功；日志 `Tenant instrumentation capability check enabled` 且 `tenant_manager_type=redis`。
- 运营端 API：`PUT /api/v2/tenants/{id}` 传 `dynamic_instrumentation:false` 返回 `capabilities.dynamic_instrumentation=false`；老租户记录初始 `capabilities=null`（继承默认允许）。
- **关闭能力后**：静态 super key（全局）通过 MCP 创建规则被拒（`instrumentation is disabled by tenant capability for app ...`）；Web 工作台同源 admin API 也被拒（400）。证明按被插桩 Agent 所属租户判定，与调用者身份无关。
- **D8 清理路径**：Pause 成功、Resume 被拒（恢复插桩受能力约束）、Delete 成功；reconcile 未新增撤销逻辑。
- **重新开启后**：创建规则成功；验证规则均已删除。
- memory 模式不注入 Provider（单测覆盖），文档标注限制。

### 环境状态与遗留

- 当时 `admin` 租户被验证流程显式置为 `true`（因当时缺少清除 API）；该状态已在第 15 节通过 `clear_dynamic_instrumentation` 清除回继承态。
- 浏览器 UI 未做交互截图验证；已验证 TypeScript 编译、dist 产物包含开关、后端字段透传。
- **L7 / P2 / P3**：已解决。scope 级写权限与面向 AI 的调用流程样例也已完成，见第 14 节。

## 14. P3 部署级兜底、scope 写权限与 AI 样例（已实施）

### 决策

按上一轮剩余清单实施。`instrumentation:write` 仅约束 4 个动态插桩写工具，Arthas 命令类工具不纳入（其安全由 L11 的 Agent 归属校验覆盖）。super / `ok_` 全局 key 不受 scope 限制；`tk_` 空 scopes 视为无写权限（fail-closed）。

### 改动清单

| 文件 | 说明 |
| --- | --- |
| `extension/controlplaneext/instrumentationmanager/interface.go` | `Config.DefaultTenantInstrumentationEnabled *bool`（nil=true），部署级兜底 |
| `extension/adminext/instrumentation_capability.go` | provider 增加 `defaultAllowed`，capabilities 为 nil 时返回部署默认；日志输出 `default_allowed` |
| `extension/adminext/instrumentation_capability_test.go` | 默认 false 继承、显式覆盖优先 |
| `extension/mcpext/tool_authz.go` | `instrumentation:write` scope 校验：写工具集合 + `identityHasScope`；全局身份放行 |
| `extension/mcpext/tool_authz_test.go` | 无 scope 拒绝写、有 scope 放行、读工具不限、全局 key 不限 |
| `extension/adminext/webui-react/src/pages/TenantsPage.tsx` | 创建 key 时可勾选 `instrumentation:write` |
| `config/template/config.yaml` | `instrumentation_manager.default_tenant_instrumentation_enabled: true`；mcp 段注释说明 scope |
| `ai-skills/mcpext-arthas-diagnostic/references/tool-catalog.md` | 新增完整调用样例（前置条件、7 步流程、常见失败表） |
| `extension/adminext/webui-react/dist/` | 重新构建前端产物 |

### 验收记录

- `go build ./...`、`go test -count=1 ./...`：通过；`npm run build`：通过，dist 含 `instrumentation:write`。
- 部署：重新渲染 + 构建镜像 + `make k8s-deploy` + 重启；日志 `default_allowed=true`。
- **scope**：`tk_` 无 scope key 的 `instrument_rule_create` 被拒（提示缺少 scope），`instrument_rule_list` 正常；带 `instrumentation:write` 的 key 创建成功；静态 super key 不受限；读工具均不受限。
- **P3 部署默认**：临时把渲染产物改为 `default_tenant_instrumentation_enabled: false` 并重启，日志 `default_allowed=false`；显式绑定到无能力租户的临时应用创建规则被拒；admin 租户显式 `true` 仍可创建（覆盖优先）。恢复为 `true` 并重启后，同一应用创建成功。
- 清理：本批创建的 5 条规则、临时应用、2 个测试 key 全部删除 / revoke；端口转发与明文临时文件清理；配置恢复为模板的 `true`。

### 行为变化与遗留

- 现有无 `instrumentation:write` scope 的 `tk_` key 将无法调用动态插桩写工具，运营端需在创建 key 时勾选该 scope（或重建 key）。这是本次有意为之的收紧。
- Arthas 命令类工具（`arthas_exec` 等）未纳入 scope，仅受 L11 的 Agent 租户归属校验；如需细粒度控制可后续扩展 scope 集合。
- `UpdateTenantRequest` 的「清除回继承态」已在第 15 节补齐（`clear_dynamic_instrumentation`），本条已解决。
- 浏览器 UI 未做交互截图验证；已验证 TypeScript 编译、dist 产物包含开关与 scope 文案、后端 API 透传。
- **P3 / scope / AI 样例**：已完成。剩余可选项见第 6 节。

## 15. 清除继承态与技能包触发补齐（已实施）

### 改动清单

| 文件 | 说明 |
| --- | --- |
| `extension/controlplaneext/tenantmanager/model.go` | `UpdateTenantRequest.ClearDynamicInstrumentation bool`，优先于 `DynamicInstrumentation` |
| `extension/controlplaneext/tenantmanager/service.go` | 清除时把 `Capabilities.DynamicInstrumentation` 置 nil；若整个 Capabilities 变空则置 nil；保持「替换而非原地修改」 |
| `extension/controlplaneext/tenantmanager/capabilities_test.go` | 清除后存储与返回均为 nil、clear 与 value 同时传入时 clear 优先 |
| `extension/adminext/webui-react/src/api/client.ts`、`pages/TenantsPage.tsx` | 编辑表单增加「Inherit deployment default」，列表显示 `inherited` / `enabled` / `disabled` 三态 |
| `ai-skills/mcpext-arthas-diagnostic/SKILL.md` | description 与触发场景补充 `instrument_rule_*` / 动态插桩；事实来源补 `tools_instrumentation.go`、`tool_authz.go`、`instrumentation_authz.go`，指向完整调用样例 |
| `extension/adminext/webui-react/dist/` | 重新构建前端产物 |

### 验收记录

- `go build ./...`、`go test -count=1 ./...`、`npm run build`：通过；dist 含 `Inherit deployment default`。
- 部署：重新渲染 + 构建镜像 + `make k8s-deploy` + 重启成功。
- **三态闭环**：`PUT /api/v2/tenants/admin/` 传 `clear_dynamic_instrumentation:true` → `capabilities=null`；部署默认 `true` 时创建规则成功；临时把部署默认改为 `false` 并重启（日志 `default_allowed=false`）→ 创建被拒；恢复 `true` 后日志恢复 `default_allowed=true`。
- **环境恢复**：验证规则删除；`admin` 租户保持 `capabilities=null`（继承态，消除前几批留下的显式 `true`）；配置恢复模板的 `true`；端口转发与临时文件清理。

### 状态

- A（清除继承态）与 C（技能包触发）已完成；第 13、14 节相关遗留条目已同步。
- 剩余可选项：Arthas 命令类工具的独立 scope、浏览器 UI 交互截图验证。


