## 工具速查表

本表严格基于当前 `mcpext` 实现，事实来源：

- `extension/mcpext/mcp_server.go`
- `extension/mcpext/tools_arthas_lifecycle.go`
- `extension/mcpext/tools_arthas_commands.go`
- `extension/mcpext/tools_arthas_session.go`

### Agent 管理

- **`list_agents`**：列出当前在线 Java Agent。用于诊断入口，不需要参数。
- **`agent_info`**：查看指定 `agent_id` 的详细信息，用于确认 JVM / 应用上下文。
- **`arthas_status`**：检查目标 Agent 是否已具备可用的 Arthas 连接状态。状态重点关注：`not_attached`、`tunnel_registered`。

### Arthas 生命周期

- **`arthas_attach`**：在目标 Agent 上启动 Arthas，并引导其连接到 Tunnel。适用于开始诊断前的准备阶段。
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

### 关键使用规则

- **先发现后诊断**：优先 `list_agents` → `agent_info` → `arthas_status`
- **先专用后兜底**：已有专用工具时，不要直接用 `arthas_exec`
- **长命令走会话**：等待触发的 `trace` / `watch` / `stack` 更适合 `arthas_session_*`
- **诊断后清理**：会话用完执行 `arthas_session_close`，整轮诊断结束考虑 `arthas_detach`
