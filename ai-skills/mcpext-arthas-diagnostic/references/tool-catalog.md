## 工具速查表

本表严格基于当前 `mcpext` 实现，事实来源：

- `extension/mcpext/mcp_server.go`
- `extension/mcpext/tools_arthas_lifecycle.go`
- `extension/mcpext/tools_arthas_commands.go`
- `extension/mcpext/tools_arthas_session.go`
- `extension/mcpext/tools_instrumentation.go`

### Agent 管理

- **`list_agents`**：列出当前在线 Java Agent。用于诊断入口，不需要参数。
- **`agent_info`**：查看指定 `agent_id` 的详细信息，用于确认 JVM / 应用上下文。
- **`arthas_status`**：返回 `agent_online` / `arthas_runtime` / `tunnel_registered` 三个正交状态。`tunnel_not_registered` 不影响结构化命令。

### Arthas 生命周期

- **`arthas_attach`**：预热 JVM 内 Arthas Runtime 或建立交互式 Web Terminal 所需的 Tunnel 会话；两段式返回，不是命令前置条件。
- **`arthas_detach`**：停止目标 Agent 上的 Arthas，释放资源。适用于诊断结束后的清理阶段。

### Arthas 同步命令

- **`arthas_exec`**：执行任意 Arthas 命令。适用于当前无专用 MCP 封装的命令；不要优先使用。
- **`arthas_trace`**：追踪方法内部调用路径与耗时。输入：`agent_id`、`class_name`、`method_name`，可选 `condition`、`skip_jdk`。
- **`arthas_watch`**：观察入参、返回值、异常。输入：`agent_id`、`class_name`、`method_name`，可选 `express`、`condition`。
- **`arthas_jad`**：反编译运行时代码。输入：`agent_id`、`class_name`，可选 `method_name`。
- **`arthas_sc`**：搜索已加载类。输入：`agent_id`、`pattern`，可选 `detail`。
- **`arthas_thread`**：查看线程情况。输入：`agent_id`，可选 `thread_id`、`top_n`、`find_deadlock`。
- **`arthas_stack`**：查看某方法被调用时的调用栈。输入：`agent_id`、`class_name`、`method_name`，可选 `condition`。

### Arthas 异步 Session

- **`arthas_session_open`**：为某个 `agent_id` 创建异步会话，可选 `ttl_seconds`、`idle_timeout_seconds`。
- **`arthas_session_exec`**：在 `session_id` 中启动异步 Arthas 命令。
- **`arthas_session_pull`**：从 `session_id` 拉取增量结果，可选 `wait_timeout_seconds`。
- **`arthas_session_interrupt`**：中断当前 `session_id` 中运行的命令，可选 `reason`。
- **`arthas_session_close`**：关闭 `session_id` 并释放资源，可选 `reason`、`force`。

### 动态插桩（可选能力）

仅在 `mcp.instrumentation.enabled=true` 时注册；写工具还要求 `allow_write=true`。
这组工具走规则中心，与 WebUI 的动态插桩工作台共用同一份规则数据，不要绕过它直接下发任务。

- **`instrument_rule_list`**：列出规则，可按 `app_id` / `service_name` / `instrument_type` / `desired_state` / `search` 过滤。
- **`instrument_rule_get`**：查看单条规则与各目标实例的执行状态，创建后用它轮询生效进度。
- **`instrument_rule_snapshot`**：查看运行时是否真正生效；`refresh=true` 主动刷新快照。
- **`instrument_rule_create`**：创建规则。作用域固定为 `service` 下的全部实例，不适用于按 Agent ID 指定目标；目标实例数受 `max_targets` 限制。
- **`instrument_rule_pause`** / **`instrument_rule_resume`** / **`instrument_rule_delete`**：暂停 / 恢复 / 软删除规则。默认只能操作由 MCP 创建的规则。

推荐流程：`instrument_rule_list` → `instrument_rule_create` → `instrument_rule_get`（轮询）→ `instrument_rule_snapshot`（确认生效）。

限制：JDK / OTel 内部类前缀默认禁止插桩；`force` 与参数/返回值采集默认关闭；`instrument_rule_update` 暂不提供。

### 动态插桩完整调用样例

前置条件（由部署与运营端共同决定）：

- `mcp.instrumentation.enabled=true` 且 `allow_write=true` 时写工具才注册。
- 租户 key（`tk_`）需要 `instrumentation:write` scope 才能调用写工具；静态 super / `ok_` 全局 key 不受限。
- 目标应用所属租户须在运营端「Tenants → Allow dynamic instrumentation」开启；未显式设置时继承部署默认。
- 只能操作自己租户的 Agent 与规则；`tk_` key 带 `X-Tenant-Id` 不会改变身份。

推荐流程：

1. `list_agents` 找到目标 Agent，记下 `app_id` / `service_name`。
2. 用 `arthas_sc` 或 `arthas_exec(sm <class>)` 确认一个**普通业务类**及其方法，避开 JDK / Spring 框架类与 CGLIB / JDK 代理类（JVM 重定义易失败）。
3. `instrument_rule_list` 检查是否已有同目标规则，避免重复。
4. `instrument_rule_create`：
   ```json
   {"app_id":"<app_id>","service_name":"<service_name>","class_name":"com.example.demo.controller.HealthController","method_name":"health","instrument_type":"trace","name":"ai-slow-order-probe"}
   ```
   返回 `rule_id`，生效是异步的。
5. `instrument_rule_get`（可轮询 2–3 次）查看 `summary` 与各目标 `state` / `task_status`。
6. `instrument_rule_snapshot`（`refresh=true`）确认 `summary.effective_targets` 与在线目标 `is_effective`；若为 false，阅读 `diagnostic_message` / `last_error_message`，优先怀疑类名、方法或 JVM 重定义限制。
7. 诊断结束后 `instrument_rule_delete`（或先 `pause` 保留规则），避免长期插桩影响业务。

常见失败与处理：

| 现象 | 原因 | 处理 |
| --- | --- | --- |
| 工具列表没有 `instrument_rule_*` | 部署未启用或 `allow_write=false` | 检查 `mcp.instrumentation` |
| 「缺少 instrumentation:write scope」 | `tk_` key 未授权写 | 运营端创建 key 时勾选该 scope |
| 「is disabled by tenant capability」 | 租户开关关闭 | 运营端 Tenants 页面开启 |
| 「调用者租户与目标应用所属租户不一致」 | 跨租户操作 | 换本租户的 `app_id` |
| 「禁止跨租户操作 Agent」 | 越权访问 Agent | 换本租户 Agent |
| 快照 `is_effective=false` | 类不可重定义或任务失败 | 换普通业务类，查看诊断信息 |

### 关键使用规则

- **先发现后诊断**：优先 `list_agents` → `agent_info` → `arthas_status`
- **先专用后兜底**：已有专用工具时，不要直接用 `arthas_exec`
- **长命令走会话**：等待触发的 `trace` / `watch` / `stack` 更适合 `arthas_session_*`
- **诊断后清理**：会话用完执行 `arthas_session_close`，整轮诊断结束考虑 `arthas_detach`
