# Arthas Terminal 空行显示修复（方案 A）

## 需求背景

在 Arthas Terminal 页面执行 `jvm` 等命令时，输出视觉上存在疑似“多余空行”的现象，需要以**不破坏现有架构**的方式修复。

## 目标

- 仅实施 **方案 A**
- 不改动后端 relay 逻辑
- 不在业务组件中散落换行替换逻辑
- 将换行处理能力收敛到终端基础 Hook 中，由具体终端场景按需启用

## 根因分析（当前结论）

- 后端 `relayWebSocketPair` 为透明透传，没有主动插入空行
- `TerminalPanel` 将 WebSocket 文本原样 `terminal.write(...)`
- `useTerminal` 当前未显式配置 `xterm` 的 `convertEol`
- Arthas Terminal 属于非 PTY 本地终端的字符流场景，更适合通过 `convertEol` 控制 `\n` 的渲染语义

## 实施方案

### 方案 A

1. 在 `useTerminal` 的 `UseTerminalOptions` 中增加 `convertEol?: boolean`
2. 创建 `Terminal` 实例时将该配置透传给 `xterm`
3. 仅在 `TerminalPanel` 中启用 `convertEol: true`
4. 保持默认值为 `false`，避免影响其他终端使用场景

## 实施进展

- [x] 创建需求记录文档
- [x] 为 `useTerminal` 增加 `convertEol` 配置能力
- [x] 在 `TerminalPanel` 中启用 `convertEol: true`
- [x] 完成前端诊断/类型检查
- [x] 更新最终结论与遗留事项

## 实施结果

### 本次代码改动

1. `useTerminal.ts`
   - `UseTerminalOptions` 新增 `convertEol?: boolean`
   - 创建 `Terminal` 时透传 `convertEol: options.convertEol ?? false`
   - 默认值保持 `false`，避免影响其他复用场景

2. `TerminalPanel.tsx`
   - 在 `useTerminal(containerRef, ...)` 中启用 `convertEol: true`
   - 未新增任何业务层文本替换逻辑

## 验证结果

- `read_lints` 检查：**无新增诊断问题**
- `cd extension/adminext/webui-react && npx tsc -b`：**通过**

## 未完成任务

- 在真实 Arthas 页面中执行 `jvm` 等命令，确认视觉上的多余空行已消失

## 遗留问题

- 目前尚未对真实运行时输出样本做抓取比对，本次先按最小架构修复落地
- 若 `convertEol` 不能完全覆盖全部异常换行场景，后续再评估更细粒度的数据归一化方案（本次不实施）
