---
name: mcpext-arthas-diagnostic
description: 当用户需要通过 custom-opentelemetry-collector 的 mcpext MCP 工具，对 Java Agent 执行 Arthas 运行时诊断、排障、trace、watch、thread、jad、sc、stack、attach/detach 或异步 session 操作时使用。适用于慢请求排查、死锁分析、类加载排查、运行时代码确认，以及指导模型按正确顺序调用 list_agents、agent_info、arthas_status、arthas_attach、专用 Arthas 工具和 session 工具。
---

## 目标

基于 `extension/mcpext` 当前**已经实现**的 MCP 工具能力，指导模型在运行时诊断 Java Agent 时：

- 优先使用**专用工具**，不要直接盲猜 `arthas_exec`
- 按照正确顺序完成**发现、连接、诊断、清理**
- 根据命令特征选择**同步工具**或**异步 session**
- 明确区分**事实**、**推断**和**下一步动作**

## 事实来源

在做判断时，优先以以下实现文件为准：

- `extension/mcpext/mcp_server.go`
- `extension/mcpext/tools_arthas_lifecycle.go`
- `extension/mcpext/tools_arthas_commands.go`
- `extension/mcpext/tools_arthas_session.go`

如需复核工具边界，读取 `references/arthas-mcp-mapping.md` 与 `references/tool-catalog.md`。

## 触发场景

当用户出现以下意图时，应优先使用本技能：

- 提到 `Arthas`、`JVM`、`Java Agent`、`运行时诊断`
- 提到 `trace`、`watch`、`thread`、`jad`、`sc`、`stack`
- 提到慢请求、线程热点、死锁、类加载、运行时代码确认
- 提到需要通过 `mcpext` / MCP 对在线 Agent 做排障
- 需要判断是否应 attach Arthas、是否应使用异步 session

如果用户只是讨论方案、架构设计或纯概念问答，不必强制执行诊断工具调用。

## 核心原则

### 先拿事实，再下结论

始终先确认：

1. 有哪些在线 Agent
2. 目标 Agent 是哪一个
3. Arthas 当前是否已可用
4. 当前问题适合哪种工具

不要在没有 `agent_id` 的前提下假设目标实例。

### 优先专用工具，最后才用通用执行

优先级如下：

1. `list_agents` / `agent_info` / `arthas_status`
2. `arthas_attach` / `arthas_detach`
3. 专用命令工具：`arthas_trace`、`arthas_watch`、`arthas_jad`、`arthas_sc`、`arthas_thread`、`arthas_stack`
4. 异步 session 工具：`arthas_session_*`
5. `arthas_exec` 仅作为**当前无专用封装命令**时的兜底

不要在已有专用工具时仍然手写原生命令交给 `arthas_exec`。

### 不要假设封装层支持所有 Arthas 原生命令参数

当前 `mcpext` 暴露给模型的是 MCP 参数，不是完整原生命令行。生成诊断方案时：

- 先看当前 MCP tool 是否已经封装该能力
- 不要凭空编造 MCP 参数
- 不要把原生命令 flag 直接等价成 MCP 参数
- 只有在专用工具确实没有覆盖时，再考虑 `arthas_exec`

### 长命令优先走异步 session

对需要等待目标流量触发、可能持续输出或需要多轮观察的命令，优先用异步 session：

- 典型命令：`trace`、`watch`、`stack`
- 典型场景：需要等待真实请求命中、需要连续 pull 结果、需要可中断

同步工具更适合：

- `thread`
- `sc`
- `jad`
- 快速返回的一次性命令

## 标准工作流

### 工作流一：通用排障入口

1. 调用 `list_agents`
2. 如果用户没有明确 agent，结合返回结果选择最合适的实例
3. 调用 `agent_info` 了解 JVM / 应用信息
4. 调用 `arthas_status`
5. 若状态为 `not_attached`，调用 `arthas_attach`
6. 再次确认 Arthas 已可用后开始诊断

### 工作流二：同步诊断

适用于 `thread`、`sc`、`jad` 或快速返回的单次命令：

1. 完成通用排障入口
2. 选择专用同步工具
3. 读取结果并区分事实 / 推断
4. 如无需继续诊断，可调用 `arthas_detach`

### 工作流三：异步 session 诊断

适用于等待触发的 `trace` / `watch` / `stack`：

1. 完成通用排障入口
2. 调用 `arthas_session_open`
3. 调用 `arthas_session_exec` 提交命令
4. 多轮调用 `arthas_session_pull`
5. 如需停止，调用 `arthas_session_interrupt`
6. 结束后必须调用 `arthas_session_close`
7. 如果整个诊断已结束且无需保活，再调用 `arthas_detach`

## 命令选择规则

- **想看慢方法内部耗时路径**：优先 `arthas_trace`
- **想看入参、返回值、异常**：优先 `arthas_watch`
- **想看方法是谁调用的**：优先 `arthas_stack`
- **想看线程热点、线程栈、死锁**：优先 `arthas_thread`
- **想确认类是否已加载、被谁加载**：优先 `arthas_sc`
- **想确认线上实际运行代码**：优先 `arthas_jad`
- **当前没有专用工具的命令**：再考虑 `arthas_exec`

## 生成命令与参数时的硬规则

- 类名、方法名不确定时，不要直接猜；先缩小范围或先做发现
- 输出要尽量带约束，避免无限等待和超大结果
- 对 `trace` / `watch` / `stack`，优先思考是否需要 session
- 不要把“命令执行失败”直接解释成“应用有问题”，先检查参数、命中条件和工具适用性
- 不要在一次回答里同时启动多个互相冲突的长命令 session

## 清理要求

完成诊断后：

- 如果使用过异步会话，必须执行 `arthas_session_close`
- 如果整个 Arthas 诊断流程已完成，优先考虑执行 `arthas_detach`

不要遗留长期占用资源的会话或 Arthas 进程。

## 结果表述规范

对用户输出结论时，按以下结构组织：

- **已确认事实**：来自工具结果的直接证据
- **初步判断**：基于事实的合理推断
- **建议动作**：下一步最合适的 MCP / Arthas 操作

不要把未验证的猜测表述成既定事实。

## 参考资料

按需读取以下文件：

- `references/tool-catalog.md`
- `references/arthas-mcp-mapping.md`
- `references/workflows.md`
- `references/arthas-command-cards.md`
- `references/arthas-failure-recovery.md`
- `references/safety.md`
