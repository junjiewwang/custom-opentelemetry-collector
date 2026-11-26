# 第 3 项独立实施文档：ReadinessGate 增加交互会话可输出确认

> 本文档供在 Java Agent 仓库独立实施使用，**Arthas 源码不可修改**。
> 本轮 collector 仓库未实施此项，也未改动 Java Agent 代码。

## 目标

让 `ArthasReadinessGate.TERMINAL_READY` 不再在"Arthas 本地交互服务尚未完成 listen"时误报。
现状只检查 `tunnelConnected && tunnelRegistered && (RUNNING|IDLE)`，其中 tunnel 状态来自
`ArthasTunnelStatusBridge` 反射 `TunnelClient.isConnected()` / `getId()`，
只反映 `tunnelClient.start()` 完成，不反映 `shellServer.listen()` 完成。

## 源码依据（Arthas 4.1.5，路径 `/root/IdeaProjects/github/arthas`）

| 事实 | 位置 |
|---|---|
| `bind()` 先 `tunnelClient.start()` | `core/src/main/java/com/taobao/arthas/core/server/ArthasBootstrap.java:401-410` |
| `shellServer.listen()` 在后 | 同文件 `:484` |
| `isBindRef` 在 `bind()` 开头即置 true | 同文件 `:380` |
| `isBind()` 只是 `isBindRef.get()` | 同文件 `:841-843` |
| `BindHandler` 仅在失败时把标志改回 false | `core/.../shell/handlers/BindHandler.java:22-28` |
| **所有 TermServer listen 完成后 `setClosed(false)`** | `core/.../shell/handlers/server/TermServerListenHandler.java:36-46` |
| `ShellServerImpl.closed` 初值 `true` | `core/.../shell/impl/ShellServerImpl.java:56` |
| `setClosed(boolean)` 是 public，但**无 public `isClosed()`** | 同文件 `:180-182` |
| `ArthasBootstrap.getShellServer()` 是 public | `ArthasBootstrap.java:1021` |
| `HttpTermServer.listen()` 内完成 LocalAddress bind 才回调成功 | `core/.../shell/term/impl/HttpTermServer.java:48-58` |
| LocalAddress 绑定点 | `core/.../shell/term/impl/http/NettyWebsocketTtyBootstrap.java:90-107` |
| `ForwardClient` 通过 LocalChannel 连该地址 | `tunnel-client/.../ForwardClientSocketClientHandler.java:91-106` |

**关键结论**：`isBind()` **不能**用作就绪信号（从 bind 第一行起就是 true）。
可靠信号是 `ShellServerImpl.closed == false`，它在全部 TermServer listen 完成后才成立，
精确对应竞态窗口终点。

## 实施方案

### 1. `ArthasTunnelStatusBridge` 新增探测

- 复用现有反射初始化模式（`:305-365`，已有 `getTunnelClient()` / `getMethod` 兼容多版本 Arthas）。
- 新增 `isShellServerListening()`：
  1. 通过 `ArthasBootstrap.getShellServer()`（public）拿到 `ShellServer` 实例；
  2. 实例实际类型是 `ShellServerImpl`，反射 `getDeclaredField("closed")` + `setAccessible(true)`；
  3. 返回 `!closed`（即 listen 全部完成）；
  4. **任何异常（类/字段不存在、InaccessibleObjectException、SecurityManager）一律保守返回 false**，
     并写 `startupLogCollector` WARN，不要抛到状态轮询线程。
- 字段名做版本兼容：先试 `closed`，失败可试 `listening` 之类备选，但**不能把"读不到"当成 true**。

### 2. 状态总线增加新维度

- `ArthasStateEventBus.State` 增加 `shellServerReady`（保持现有 `tunnelConnected` /
  `tunnelRegistered` / `arthasState` 不变，是**追加**不是替换）。
- 由 `ArthasTunnelStatusBridge` 的轮询把 `isShellServerListening()` 结果并入状态更新。
- 轮询周期沿用现有隧道状态轮询，不要新起线程。

### 3. `ArthasReadinessGate` 追加条件

- `evaluateNow()`（`arthas/ArthasReadinessGate.java:148-161`）在现有
  `tunnelReady && arthasReady` 基础上**再与** `s.isShellServerReady()`：

```java
boolean terminalUsable = s.isShellServerReady();
if (tunnelReady && arthasReady && terminalUsable) {
  return new Result(Readiness.TERMINAL_READY, ReasonCode.NONE, snapshot);
}
```

- 细分原因新增一个 `ReasonCode`（例如 `TERMINAL_NOT_LISTENING`），
  用于"隧道/Arthas 都 OK，但本地交互服务未就绪"的等待态，便于 `awaitReady()` 继续等待
  而不是直接失败。
- `awaitReady()`（`:213-225` 附近）的判定同步加上 `terminalUsable`，
  确保等待循环能等到 ③ 完成。

### 4. 关联改动（建议一并做）

- 前端 `attachArthas()` 后固定等 2 秒（collector `TerminalPanel.tsx`）
  应改为**等待 attach 任务真正完成**，否则新的就绪条件虽然更准，浏览器仍会在 ③ 之前打开终端。
- MCP 的 `tunnelReady` / `arthas_status` 语义可随之对齐（可选）。

## 单测要求

| 用例 | 期望 |
|---|---|
| ShellServer 未 listen（`closed=true`） | `isShellServerListening()==false`，`evaluateNow()` 不是 `TERMINAL_READY` |
| listen 完成（`closed=false`）+ 其余条件满足 | `TERMINAL_READY` |
| 反射失败（字段不存在 / 抛异常） | 保守 false，不抛异常，状态细分原因明确 |
| `awaitReady()` | 能从 not-ready 等到 ready；超时走明确错误码 |
| 现有用例回归 | 既有 `TERMINAL_READY` / `ARTHAS_READY_BUT_TUNNEL_NOT_READY` 等枚举语义不变 |

建议用 mock/fake `ShellServer` 或可控反射桩，不启动真实 Arthas。

## 风险与边界

- `closed` 是私有字段，Arthas 升级可能改名 → 必须版本兼容 + 失败兜底 false。
- 该改动只修正**状态语义**（attach 任务、`arthas_status`、MCP `tunnelReady` 更准），
  **不会消除终端连接竞态本身**：collector 的 `connectArthas` 在 `agentRegister` 出现即发
  `startTunnel`，不看 attach 任务结果。彻底消除需同时改前端等待任务完成，
  或保留 collector 前端的首帧看门狗 + 自动重连兜底。
- 反射访问 JDK 强封装模块可能受 `--add-opens` 影响；Arthas 运行在应用 ClassLoader，
  现有 `ArthasTunnelStatusBridge` 已用同款方式，风险可控但需实测。
- 本项改动在 Java Agent 仓库；此前 Gradle 依赖下载受 Maven Central 429 限制，
  实施前需确认构建环境可用。

## 不做

- 不改 Arthas 源码（用户约束）。
- 不重新打包内嵌 `arthas-core.jar`。
- 不把 `isBind()` 当作就绪信号（已验证无效）。
- 不替换现有三个就绪条件，只追加一层。
