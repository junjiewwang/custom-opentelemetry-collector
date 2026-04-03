## Arthas 命令与 MCP 映射

本文件的目标不是复述完整 Arthas 文档，而是把**当前仓库已经支持的 mcpext MCP 工具**映射到常见 Arthas 诊断意图，帮助模型减少盲猜。

### 优先级规则

- **优先专用 MCP tool**
- **其次异步 session 组合**
- **最后才是 `arthas_exec` fallback**

### 已有专用映射

| 诊断意图 | 优先 MCP 工具 | 说明 |
|---|---|---|
| 列出在线实例 | `list_agents` | 不是 Arthas 原生命令，但这是 mcpext 的诊断入口 |
| 查看实例详细信息 | `agent_info` | 用于先确认 JVM / 应用上下文 |
| 检查 Arthas 是否可用 | `arthas_status` | 用于判断是否要 attach |
| 启动 Arthas | `arthas_attach` | 诊断前准备 |
| 停止 Arthas | `arthas_detach` | 诊断后清理 |
| 查慢方法内部耗时 | `arthas_trace` | 默认会拼接 `-n 1`；更长观察请用 session |
| 看入参与返回值 | `arthas_watch` | 默认表达式为 `{params, returnObj, throwExp}` |
| 看方法调用来源 | `arthas_stack` | 默认单次观察；长时间等待触发可转 session |
| 看线程、热点线程、死锁 | `arthas_thread` | 支持 `thread_id`、`top_n`、`find_deadlock` |
| 查类是否已加载 | `arthas_sc` | 支持 `pattern` 与 `detail` |
| 看运行时代码 | `arthas_jad` | 支持按类反编译，也可限定方法 |

### 需要异步 session 的情况

当命令满足以下特征时，应优先用 `arthas_session_open` + `arthas_session_exec` + `arthas_session_pull`：

- 命令需要等待真实请求命中
- 结果可能分多批返回
- 需要在观察过程中手动中断
- 需要在一个会话里多轮查看结果

典型命令：

- `trace <class> <method>`
- `watch <class> <method> ...`
- `stack <class> <method>`

### 当前无专用封装，使用 `arthas_exec` 兜底

以下命令在当前仓库中**没有专用 MCP tool**，必要时才使用 `arthas_exec`：

- `sm`
- `monitor`
- `tt`
- `ognl`
- `dashboard`
- `classloader`
- `profiler`
- `vmoption`
- `sysprop`
- `sysenv`

### 明确禁止的误用

- 不要在已有 `arthas_trace` 时仍然手写 `trace ...` 给 `arthas_exec`
- 不要把原生命令参数直接当成 MCP 参数名
- 不要假设 session 工具会自动帮你清理；结束后仍需显式 close
- 不要跳过 `arthas_status` / `arthas_attach` 检查就假设 Arthas 可用
