## 需求记录

### 背景

希望在仓库中维护一套可归档、可持续演进的 skill source，用于指导 LLM 基于 `mcpext` 当前真实支持的 MCP 能力来使用 Arthas，减少盲猜、错误选型以及错误命令生成。

### 目标

- 在 Git 仓库内维护 skill source，而不是依赖被忽略的目录
- 以 `mcpext` 当前已实现的工具能力为准，不脱离真实实现
- 后续可继续补充 Arthas 官方命令知识，但要映射到当前 MCP 支持范围
- 让模型优先学会“如何在 `mcpext` 约束下正确使用 Arthas”
- 生成一个可被 Skill 导入器正确识别的安装包

### 当前实施范围

- 创建仓库内 skill source 目录：`docs/ai-skills/mcpext-arthas-diagnostic/`
- 建立 `SKILL.md` 与维护 `README.md`
- 建立基于当前实现的参考文档：工具速查、映射、工作流、命令卡片、失败恢复、安全边界
- 生成并修正可安装的 skill 压缩包结构

### 当前进展

- [x] 梳理 `mcpext` 当前已支持的工具分组与参数边界
- [x] 创建仓库内 skill source 基础结构
- [x] 创建 `SKILL.md`
- [x] 创建 `README.md`
- [x] 创建参考文档集合
- [x] 生成可安装的 skill 压缩包：`docs/ai-skills/mcpext-arthas-diagnostic.zip`
- [x] 修复安装包目录层级，使 `SKILL.md` 位于 zip 根目录
- [x] 执行编译验证（`go build ./...`）
- [ ] 根据未来新增 MCP tool 持续演进文档

### 关键事实来源

- `extension/mcpext/mcp_server.go`
- `extension/mcpext/tools_arthas_lifecycle.go`
- `extension/mcpext/tools_arthas_commands.go`
- `extension/mcpext/tools_arthas_session.go`

### 结构示意

```mermaid
flowchart TD
    A[mcpext 实现] --> B[mcp_server.go 注册工具]
    A --> C[tools_arthas_lifecycle.go]
    A --> D[tools_arthas_commands.go]
    A --> E[tools_arthas_session.go]

    B --> F[docs/ai-skills/mcpext-arthas-diagnostic/SKILL.md]
    C --> G[references/tool-catalog.md]
    D --> H[references/arthas-mcp-mapping.md]
    D --> I[references/arthas-command-cards.md]
    E --> J[references/workflows.md]
    E --> K[references/safety.md]
    E --> L[references/arthas-failure-recovery.md]
```

### 打包产物

- 安装包路径：`docs/ai-skills/mcpext-arthas-diagnostic.zip`
- 当前发行结构：zip 根目录直接包含 `SKILL.md`、`README.md` 和 `references/`
- 打包说明：已过滤 macOS 额外元数据，适合作为分发安装包

### 本次修复说明

- 初版安装包将仓库路径 `docs/ai-skills/mcpext-arthas-diagnostic/` 一并打进 zip，导致导入器无法在压缩包根目录直接找到 `SKILL.md`
- 导入器报错“压缩包中未找到 `SKILL.md` 文件”并非文件缺失，而是目录层级不符合其识别约定
- 本次已将打包方式修正为在 skill 目录内执行打包，确保导入器解压后可直接在根目录看到 `SKILL.md`

### 未完成任务

- 后续视需要补充 Arthas 官方命令知识的版本化来源与更多命令卡片
- 未来如新增 `sm` / `monitor` / `tt` 等专用 MCP tool，需要同步更新 skill 文档

### 遗留问题

- 当前 `sm`、`monitor`、`tt`、`ognl` 等尚未有专用 MCP tool，技能中只能明确为 fallback 范围
- 还没有自动化校验脚本来对比 `registerTools()` 和文档是否漂移
- 后续如需重复发布安装包，建议补一个固定打包脚本，避免再次出现目录层级错误
