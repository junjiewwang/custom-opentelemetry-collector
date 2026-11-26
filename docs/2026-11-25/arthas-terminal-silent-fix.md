# Arthas Terminal 假就绪/静默修复实施记录

## 需求

修复初次连接 Arthas Terminal 时，`[+] Connected successfully, terminal is ready`
之后终端空白、无 banner/提示符的问题。诊断结论见
`docs/2026-11-25/arthas-tunnel-relay-silence.md`。

## 根因（源码级，已坐实）

Arthas 4.1.5 `ArthasBootstrap.bind()`：

- `core/.../server/ArthasBootstrap.java:401-410` 先 `tunnelClient.start()` 注册到 collector；
- `core/.../server/ArthasBootstrap.java:484` 才 `shellServer.listen()` 完成 `LocalAddress` 绑定。

`openTunnel` 的数据通道依赖
`tunnel-client/.../ForwardClientSocketClientHandler.java:91-106` 连接 Netty
`LocalChannel`（`ArthasConstants.NETTY_LOCAL_ADDRESS`）。若 `startTunnel` 落在两者之间，
`LocalAddress` 尚未 bind，ForwardClient 建链失败，openTunnel 连接静默。

`ForwardClientSocketClientHandler.java:71` 的 `ws://127.0.0.1:8563/ws` 仅为占位 URI，
真实传输是进程内 LocalChannel，不走 TCP 端口。

## 决策与边界

- **Arthas 源码不可改**（用户约束），修复只在 collector 前端/后端做。
- **只做 1+2**：
  1. 加 silent 会话指标（观测能力，不改产品行为）；
  2. 只做 P0 前端修复（不加后端看门狗、不做后端自愈）。
- 第 3 项（Java Agent `ReadinessGate` 增加 `shellServerReady`）**只出独立实施文档**
  `docs/2026-11-25/readiness-gate-shellserver-probe.md`，由用户在别处实施；
  本次**未改 Java Agent 代码**。
- 进度状态**继续在终端交互里显示**，不删除、不挪出终端；只修正"假就绪"语义。
- 用户可见的终端进度/错误文案**统一英文**；代码注释中的中文按用户要求保持不动。
- 文档不记录任何 token、密码或密钥值。

## 实施进展

- [x] 建立本文档与第 3 项独立实施文档。
- [x] silent 会话指标。
- [x] P0 前端修复。
- [x] 全量验证。
- [x] 用户可见进度文案统一英文（含后端 ready 帧）。
- [x] 更新本文档。

## 变更清单

| 文件 | 改动摘要 |
|---|---|
| `extension/arthastunnelext/relay_metrics.go`（新） | `otelcol_arthas_relay_sessions` 计数器，标签仅 `result=ok\|silent`；会话不足 3s 不计入；无 MeterProvider 时 no-op |
| `extension/arthastunnelext/relay_metrics_test.go`（新） | 4 个用例：分类、短会话/nil 排除、no-op 安全、observer option |
| `extension/arthastunnelext/arthasuri_relay.go` | `relayWebSocketPair` 增加变参 `relaySessionObserver`，会话结束通知；不动其它调用点 |
| `extension/arthastunnelext/arthasuri_compat.go` | 新增 `compatOption` / `withRelayObserver`，构造时应用 option，relay 调用传入 observers；`statusReady` 帧文案改为 `Relay channel established`（不再宣称 terminal is ready），同步英文注释里的协议示例 |
| `extension/arthastunnelext/extension.go` | 从 `settings.TelemetrySettings.MeterProvider` 创建 `relayMetrics` 并注入 |
| `extension/adminext/webui-react/src/components/Terminal/terminalReadiness.ts`（新） | 纯函数：状态帧识别、ready 帧识别、重试决策、6s 阈值与文案常量（独立于 React，便于单测） |
| `extension/adminext/webui-react/src/components/Terminal/TerminalPanel.tsx` | 首帧看门狗、假就绪文案、自动重连 1 次、同步状态守卫、统一帧处理（消除 string/Blob 分支重复）、关闭/卸载时清理所有定时器 |
| `docs/2026-11-25/arthas-terminal-silent-fix.md` | 本记录 |
| `docs/2026-11-25/readiness-gate-shellserver-probe.md` | 第 3 项独立实施文档 |

### 前端修复要点

- `[+]` 帧不再显示 "terminal is ready"，替换为
  "通道已建立，等待 Arthas 交互会话就绪…"，**进度状态仍在终端里**。
- 收到 `[+]` 后启动 6s 首帧看门狗；收到非状态帧（真实 Arthas 输出）立即取消。
  阈值依据：正常样本 relay→首帧实测 1025ms / 1560ms / 1082ms。
- 超时动作：`decideWatchdogAction()` 决定重试或放弃；最多重试 1 次，间隔 200ms；
  二次超时给出可操作错误文案，不无限重连。
- 4 个重试陷阱全部处理：
  1. 用 `connectingRef` / `connectedRef` 同步守卫替代异步 state，避免重试被 guard 吞掉；
  2. 重试只关闭旧 ws，**不调用 `handleClose`、不卸载面板**；
  3. 重试跳过重复 attach，且 `agentId` 用 `instance.agent_id`（不依赖旧 `tunnelAgentId` prop）；
  4. 重试期间抑制 `onclose` 的 "Connection closed" 与 500ms 列表刷新。
- 附带修正：原有 15s 超时只在 `readyState !== OPEN` 时触发，静默时无效，
  因此新增独立首帧看门狗；所有定时器在关闭/卸载时统一清理。

## 文案统一（追加需求）

用户可见文案全部改为英文，中文仅保留在代码注释里（按用户要求不动）。

| 位置 | 英文文案 |
|---|---|
| `terminalReadiness.ts` `RELAY_BOUND_MESSAGE` | `[*] Channel established, waiting for Arthas interactive session...` |
| `TerminalPanel.tsx` 触发重连 | `[System] Arthas did not respond, reconnecting...` |
| `TerminalPanel.tsx` 二次超时 | `[System] Arthas interactive session did not become ready in time. Close this terminal and retry, or restart the target application.` |
| `arthasuri_compat.go:969` 后端 ready 帧 | `[+] Relay channel established` |

ready 帧检测基于 `[+]` 图标前缀（`isRelayReadyFrame`、跨节点 `connectToAgentCrossNode`），
不依赖消息文本，缩短文案不影响识别；帧长度由 56 变为 40 字节。

## 验收记录

| 项目 | 结果 |
|---|---|
| `go test ./...` | exit 0，40 个包 ok，FAIL 0 |
| 新增 Go 单测 | `TestRelayMetrics*` / `TestWithRelayObserver` 全部 PASS；`arthastunnelext` 全量 PASS |
| `npm run build` | 通过（`tsc -b` + Vite，736 modules） |
| IDE 静态诊断 | 修改的 Go / TS 文件 0 问题 |
| `git diff --check` | 通过 |
| 镜像重建 + 部署 | 成功，Pod Running，health 200 |
| **正常路径 CDP 回归** | reload 加载新 chunk 后：`[arthas@1]$` 出现、旧 "terminal is ready" 消失、新等待文案显示、无重连误触发 |
| **静默路径 CDP 回归** | 用 CDP 注入受控静默（仅测试期，产品代码未改）：`created=2`（原连接 + 1 次自动重连），首次超时写"正在重建通道…"，二次超时写明确错误并停止，无无限循环；全程进度状态在终端显示 |
| 现场恢复 | 注入已随 reload 清除（`window.__probe === undefined`）；`level: info`、debug 日志 0 条、health 200 |
| **文案统一回归** | `go test ./...` exit 0 / 40 包；`npm run build` 通过；镜像重建部署 health 200 |
| 正常路径 CDP（英文） | 后端帧 `[+] Relay channel established`（40B），终端显示 `Channel established, waiting for Arthas interactive session...`，banner + `[arthas@1]$` 正常，旧 "terminal is ready" 消失，终端内无中文 |
| 静默路径 CDP（英文） | 受控静默后 `created=2`（重连 1 次）、显示 `Arthas did not respond, reconnecting...`、二次超时显示 `did not become ready in time`，中文文案残留 0，注入已清理 |

## 未完成任务

| # | 任务 | 状态 |
|---|------|------|
| 1 | 第 3 项：Java Agent `ReadinessGate` 增加 `shellServerReady` | **按用户决定不在本轮实施**，已出独立文档 |
| 2 | 前端等 attach 任务真正完成（替代固定 2 秒） | 留给第 3 项一并做，本轮未改 |
| 3 | 提交并推送本批改动 | 用户未要求，未执行 |

## 遗留与风险

- 静默是 Arthas 初始化窗口竞态，本修复是**前端兜底**，不会消除竞态本身；
  消除需第 3 项或 Arthas 上游调整 `bind()` 顺序（均不在本次范围）。
- 6s 阈值在异常慢环境可能误触发重连；发生率可通过新增指标长期观测。
- 指标标签只有低基数 `result`，未放 agent_id 等用户可控值；会话 <3s 不计入，
  避免用户立刻关面板稀释发生率。
- 诊断日志（`Relay session ended` 带方向计数）保留，Info 级每会话一条。
- **新发现（不在本轮范围）**：`Agent registered` 日志中 `app_name` 字段是明文凭据
  （`arthasuri_compat.go` 附近），属于另一个脱敏遗漏，建议后续处理。
- 运行时日志级别已恢复 `info`；仓库模板/元数据未修改；重新渲染会保持 `info`。
- 临时采样脚本 `/tmp/arthas_sample.py`、样本日志 `/tmp/arthas_sample_*.log`、
  反编译临时目录 `/tmp/arthas_probe` `/tmp/arthas_all` 保留作证据。
- Chrome CDP 9222 仍运行，用完可关闭。
- 本批改动**未提交**；`1cd` 等既有未跟踪文件未动。
