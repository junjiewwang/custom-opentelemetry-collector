## 技能文档

### 基本信息
- 技能名: `mcpext-arthas-diagnostic`
- 创建人: @junjiewwang (junjiewwang@tencent.com)
- 版本: v1.0.0
- 更新时间: 2026-04-03

### 适用场景
当仓库内的 `mcpext` 提供的 MCP 工具被用于 Java Agent 运行时诊断时，使用此技能指导模型按当前实现边界执行 Arthas 相关操作，包括 Agent 发现、Arthas attach/detach、同步命令和异步 session 诊断。

### 前置条件
- `custom-opentelemetry-collector` 已包含 `mcpext` 实现
- 目标运行环境存在在线 Java Agent
- `mcpext` 的当前工具注册与 `extension/mcpext/mcp_server.go` 保持一致
- 使用者需要优先遵循仓库内 `extension/mcpext/*.go` 的真实实现，而不是外部文档猜测

### 使用示例
```
请通过 mcpext 的 MCP 工具帮我排查某个 Java Agent 的慢请求

请用 arthas trace/watch 相关能力看看这个方法的入参与耗时

请确认目标 Agent 是否已经 attach Arthas，并给我正确的下一步操作
```

### 注意事项
⚠️ 本技能只描述 **当前仓库里已经支持的 mcpext 工具能力**，不等价于完整 Arthas 原生命令全集。  
⚠️ 如 `mcpext` 新增、修改或删除工具，需要同步更新 `SKILL.md` 与 `references/` 下的映射文档。  
⚠️ 异步 session 使用完毕后必须关闭，避免残留运行时资源。  
⚠️ `arthas_exec` 是兜底能力，不应覆盖已有专用工具。

### 已知问题
- [ ] `sm`、`monitor`、`tt`、`ognl` 等尚无专用 MCP tool，只能通过 `arthas_exec` 兜底
- [ ] 尚未提供自动校验脚本，暂时需要人工比对 `registerTools()` 与文档内容
- [x] v1.0.0：已建立仓库内可归档的 skill source 与参考资料

### 相关技能
- `skill-creator`: 技能创建与维护规范
