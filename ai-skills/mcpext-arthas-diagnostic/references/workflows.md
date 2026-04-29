## 诊断工作流模板

以下工作流严格围绕当前 `mcpext` 已实现工具编排。

### 1. 通用入口工作流

1. 调用 `list_agents`
2. 根据应用名、服务名、IP 等信息定位目标 Agent
3. 调用 `agent_info`
4. 调用 `arthas_status`
5. 若为 `not_attached`，调用 `arthas_attach`
6. 诊断结束后视情况调用 `arthas_detach`

### 2. 慢请求 / 慢方法排查

#### 快速单次确认

1. 完成通用入口工作流
2. 调用 `arthas_trace`
3. 若结果不足，再切换到异步 session 方案

#### 需要等待真实流量命中

1. 完成通用入口工作流
2. 调用 `arthas_session_open`
3. 调用 `arthas_session_exec` 提交 `trace ...`
4. 多轮调用 `arthas_session_pull`
5. 命令结束或已拿到足够结果后，调用 `arthas_session_close`
6. 整轮排障完成后调用 `arthas_detach`

### 3. 方法入参与返回值核查

1. 完成通用入口工作流
2. 优先使用 `arthas_watch`
3. 如果需要长时间等待方法命中，切换为异步 session 执行 `watch ...`
4. 根据结果区分：真实参数、返回值、异常信息
5. 清理会话并结束 Arthas 连接

### 4. 调用来源分析

1. 完成通用入口工作流
2. 使用 `arthas_stack`
3. 如果调用不易复现，用异步 session 执行 `stack ...`
4. 从返回栈中提取真实调用路径，不要把猜测链路当结论

### 5. 线程热点 / 死锁排查

1. 完成通用入口工作流
2. 使用 `arthas_thread`
3. 常见模式：
   - 指定 `top_n` 查看最忙线程
   - 指定 `thread_id` 查看具体线程栈
   - 指定 `find_deadlock=true` 排查死锁
4. 如果线程问题已明确，可直接收尾清理

### 6. 类加载 / 运行时代码确认

#### 类加载确认

1. 完成通用入口工作流
2. 调用 `arthas_sc`
3. 必要时开启 `detail` 查看 ClassLoader 等信息

#### 运行时代码确认

1. 完成通用入口工作流
2. 调用 `arthas_jad`
3. 对比反编译结果与预期逻辑，判断是否存在版本漂移或加载异常

### 7. 当前无专用工具的高级命令

1. 先确认当前确实无专用 MCP tool
2. 若有必要，调用 `arthas_exec`
3. 命令要尽量具体，避免超大输出与长时间阻塞
4. 对长时间观察型命令，优先考虑 session 执行原生命令
