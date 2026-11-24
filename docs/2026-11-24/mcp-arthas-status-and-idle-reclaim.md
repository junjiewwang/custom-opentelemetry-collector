# MCP Arthas 状态语义修复与控制面空闲回收实施记录

## 需求

修复 `not_attached` 被误读为「attach 失败、无法诊断」的问题：MCP 命令实际走控制面任务链路（`auto_attach=true`、`require_tunnel_ready=false`），Tunnel 注册不是命令前置条件。同时把 Arthas 空闲回收从 Tunnel 扩展迁移到控制面，避免 Runtime 常驻泄漏或正在使用的会话被误回收。

## 决策

- auto_detach 改为控制面空闲回收。
- 默认支持所有 Arthas 命令，本次不引入白名单/分级拦截。
- Tunnel 注册失败排查后续再做，本次只修语义。
- 状态模型：`agent_online` / `arthas_runtime` / `tunnel_registered` 三个正交维度；`not_attached` 改为 `tunnel_not_registered`，不保留兼容别名。
- 控制面 Tracker 作为 Runtime 与活动时间的唯一来源；`UNKNOWN` 不回收。
- 旧 `arthas_tunnel.auto_detach` 配置保留解析并标记 deprecated，模板中已置为 disabled；新配置段的值由渲染元数据维护。
- 文档不记录任何凭据值。

## 实施进展

- [x] 建立本文档。
- [x] P0-a：统一 MCP 工具描述、AI skill 决策树、README/旧文档对齐。
- [x] P0-c：控制面 Arthas 生命周期 Tracker + 空闲回收 Sweeper + 配置迁移。
- [x] P0-b：arthas_status 三态语义、attach 确认语义与测试修正。
- [x] 模板/元数据渲染 + Go 全量测试。
- [x] 重建镜像部署 minikube 并验证。
- [x] 更新本文档。

## 变更清单

| 位置 | 改动摘要 |
|---|---|
| `extension/mcpext/tool_descriptions.go`（新增） | 抽取三份共享描述常量，收敛所有"必须已 attach"文案 |
| `extension/mcpext/tools_arthas_commands.go`、`tools_arthas_session.go`、`tools_arthas_lifecycle.go` | 命令工具描述改为控制面任务链路；attach 改为"预热/Terminal 会话"、两段式确认；修正已注册时返回文案 |
| `extension/mcpext/mcp_server.go`、`formatter.go`、`formatter_test.go` | `arthas_status` 输出三态 + 能力提示；`not_attached` 改为 `tunnel_not_registered`；测试覆盖"无 Tunnel 但 Runtime RUNNING" |
| `extension/mcpext/arthas_orchestrator.go` | attach 确认提示不再要求死等 `tunnel_registered` |
| `extension/controlplaneext/arthaslifecycle/`（新增） | `Config` / `State` / `Tracker` / `Manager`：内存与 Redis 两种 Tracker、任务提交与结果钩子、会话计数、空闲扫描、跨副本冷却锁、detach 任务构造、6 个回归测试 |
| `extension/controlplaneext/config.go`、`extension.go` | 新增 `arthas_lifecycle` 配置段、可选 `ArthasLifecycleProvider` 接口、`GetArthasLifecycle` / `RecordArthasActivity`；Submit/Report/Register 三处钩子；Start/Shutdown 生命周期 |
| `extension/arthastunnelext/arthasuri_compat.go`、`extension.go` | 删除旧 auto-detach 循环与提交逻辑（含无用时间戳字段和 import）；Tunnel 生命周期事件改为上报控制面活动时间；旧配置启动警告改为 deprecated 提示 |
| `extension/arthastunnelext/compat_autodetach_test.go` | 删除（只测试已移除的旧回收器）；`compat_connect_test.go` 同步移除失效字段 |
| `ai-skills/mcpext-arthas-diagnostic/**` | 通用入口改为 `list_agents → agent_info → 轻量命令探测`；失败恢复不再把 `not_attached → attach` 当硬门禁；工具速查与映射同步 |
| `README.md` | 时序图改为控制面任务链路；修正 status/attach 定位；补齐 5 个 session 工具（共 17 个）；写明命令不依赖 Tunnel |
| `docs/2026-03-12/mcp-extension-design.md`、`docs/2026-03-31/mcp-extension-config.md` | 加"历史架构"标注，指向新架构文档 |
| `config/template/config.yaml` | 新增 `controlplane.arthas_lifecycle` 配置段（`[[ .Service.* ]]` 占位符）；旧 `auto_detach` 标记 deprecated 并禁用 |
| `config-template-render/config/tcs/service_provider.yaml` | 新增 `controlplane.arthas_lifecycle` 元数据段，维护全部阈值 |
| `docs/2026-11-24/mcp-arthas-status-and-idle-reclaim.md` | 本实施记录 |

## 验收记录

| 项目 | 结果 |
|---|---|
| 渲染产物校验 | 缺失字段 0、残留占位符 0、YAML 解析通过；`arthas_lifecycle` 配置段正确生成 |
| `go test ./...` | 全量通过（exit 0，FAIL 0）；新增 `arthaslifecycle` 6 个用例通过 |
| 扩展定向测试 | `mcpext` / `controlplaneext` / `arthastunnelext` 全部通过 |
| ConfigMap 与镜像 | 重新渲染并 `kubectl apply`，镜像在 minikube Docker 环境重建，Pod `Running` |
| 生命周期模块启动 | 日志出现 `arthas_lifecycle idle reclaim enabled`（15m 空闲 / 10m 最短 attach / 10m 冷却 / 90s 扫描），`Everything is ready` |
| MCP 工具描述 | 线上 `tools/list` 可见新描述（含 `tunnel_not_registered`、`auto_attach`），旧文案"这是执行任何 Arthas 命令的前提条件"为 0；工具数 17 |
| 实测 no-Tunnel 场景 | 在线 Agent 1 个；未调用 `arthas_attach`、无 Tunnel 注册时 `arthas_exec('version')` 成功返回结构化结果 |
| `arthas_status` 实测 | 输出 `agent_online: true`、`arthas_runtime`、`tunnel_registered: false`、`active_sessions`、`last_activity_at`、`command_channel: control_plane_task`，状态为 `tunnel_not_registered`，并说明"不代表 attach 失败、命令仍可执行" |

## 实施中发现并修正的问题

- 部分 Agent 上报的任务结果状态字段为空但带成功载荷，导致 Runtime 一直显示 `UNKNOWN`。已增加 `isSuccessfulResult()` 兼容判定（失败/超时/取消或带错误码时仍判失败），并补充单测；随后重新构建并部署。
- 复核该补偿逻辑的端到端 MCP 调用命令被工具审批取消两次，未再次执行；补偿逻辑已通过单元测试且新镜像已部署，最终线上确认列为待办。

## 后续阶段（本次不做）

- P1：同步工具独立 context + 上限超时 + 取消时 interrupt；`max_concurrent_sessions` 落地或标 deprecated；`ConnectToAgent` 等死代码清理。
- P2：面向使用者的能力边界指南、Redis Tracker 的集成测试（miniredis）、更完整回归测试。
- Tunnel 注册失败排查（WS 路径、鉴权、Agent 侧配置）。
- 命令白名单/分级拦截（按用户决定，后续有需求再加）。

## 遗留问题与风险

- 空闲回收现在覆盖所有 `RUNNING` 且长期空闲的 Agent；卸载后下次命令会重新 auto attach，首次命令可能变慢。
- `UNKNOWN`（上线前已运行、未被观测）不回收，属保守策略。
- `RegisterOrHeartbeatAgent`（心跳路径）不会重置 Runtime 观测；只有显式 `RegisterAgent` 会重置，极端情况下可能保留过时状态。
- 分布式模式下 `IsAgentOnline` 不校验 liveness，`tunnel_registered` 可能误报；状态输出未承诺其绝对准确。
- Tunnel 活动上报覆盖 pending/tunnel 生命周期事件；若终端长连接有其它活动路径未覆盖，仍可能被空闲回收，后续可补充。
- Redis Tracker 通过 `storage.GetRedis("default")` 复用现有客户端；未做 miniredis 集成测试。
- 旧 `arthas_tunnel.auto_detach` 字段仍保留解析，但值不再生效；启动时会有 deprecated 警告。
- minikube IP 或元数据中的配置值变化需同步更新反代配置/重新渲染。
