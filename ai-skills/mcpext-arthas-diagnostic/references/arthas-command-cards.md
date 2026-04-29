## Arthas 命令卡片

本文件聚焦当前 `mcpext` 已有专用封装的高频命令。

### `trace`

- **适用目标**：定位某个方法内部调用链的耗时热点
- **优先工具**：`arthas_trace`
- **当前 MCP 输入**：`agent_id`、`class_name`、`method_name`，可选 `condition`、`skip_jdk`
- **当前默认行为**：自动追加单次采样约束；默认跳过 JDK 方法
- **适合 session 的情况**：需要等待真实请求命中、需要多轮观察
- **常见误用**：类名/方法名不确定时直接猜；条件过严导致无命中

### `watch`

- **适用目标**：查看方法入参、返回值、异常
- **优先工具**：`arthas_watch`
- **当前 MCP 输入**：`agent_id`、`class_name`、`method_name`，可选 `express`、`condition`
- **当前默认行为**：默认表达式为 `{params, returnObj, throwExp}`，带有限制性输出深度
- **适合 session 的情况**：需要等待请求触发、需要多次观察
- **常见误用**：表达式写得过大、输出对象过深、条件写错导致无结果

### `stack`

- **适用目标**：查看某个方法被调用时的调用来源
- **优先工具**：`arthas_stack`
- **当前 MCP 输入**：`agent_id`、`class_name`、`method_name`，可选 `condition`
- **当前默认行为**：默认单次观察
- **适合 session 的情况**：调用不稳定、需要等流量命中
- **常见误用**：把 stack 当 trace 用；没有命中时误判为无调用

### `thread`

- **适用目标**：看线程状态、热点线程、死锁
- **优先工具**：`arthas_thread`
- **当前 MCP 输入**：`agent_id`，可选 `thread_id`、`top_n`、`find_deadlock`
- **适合场景**：线程异常、CPU 飙高、怀疑死锁
- **常见误用**：同时设置互斥的观察意图；没先确认目标 Agent 就直接抓线程

### `sc`

- **适用目标**：确认类是否已加载、被谁加载、是否存在多个匹配
- **优先工具**：`arthas_sc`
- **当前 MCP 输入**：`agent_id`、`pattern`，可选 `detail`
- **适合场景**：类名不完全确定、怀疑类加载器问题
- **常见误用**：模式写得过宽导致结果噪声过大

### `jad`

- **适用目标**：确认运行时类代码是否符合预期
- **优先工具**：`arthas_jad`
- **当前 MCP 输入**：`agent_id`、`class_name`，可选 `method_name`
- **适合场景**：线上代码与源码不一致、怀疑部署或加载版本漂移
- **常见误用**：在类名未确认时直接 jad；没有先通过 `sc` 缩小范围
