# Arthas Terminal 首次连接静默问题诊断记录

## 现象

初次打开 Arthas Terminal（Tunnel 未注册）时，终端显示连接进度后停在
`[+] Connected successfully, terminal is ready`，没有 Arthas banner 和 `[arthas@1]$`
提示符；关闭重开后同一 Agent 立即正常。问题**不是必现**。

## 约束

- 进度状态**继续在终端交互里显示**，不删除、不挪出终端；修复只处理"假就绪 + 静默"。
- 本阶段**只加诊断日志，不改产品逻辑**，抓够样本坐实根因后再谈修复。
- 诊断日志用 Debug 级别，运行时默认 `info` 不输出；验证完已恢复配置。
- 文档不记录任何 token、密码或密钥值。

## 实施进展

- [x] 建立本文档。
- [x] relay pump 加 Debug 字节流日志（仅诊断，转发逻辑零改动）。
- [x] Go 全量测试、重建镜像部署、临时开 Debug。
- [x] 多轮样本采集（r1/r2/r3 + 关闭重开对照）。
- [x] 恢复 `info` 级别、确认 Pod 健康、更新结论。

## 变更清单（仅诊断）

| 文件 | 改动摘要 |
|---|---|
| `extension/arthastunnelext/relay_stats.go`（新） | 纯诊断统计类型，按方向累计帧数/字节数，独立可测、并发安全 |
| `extension/arthastunnelext/relay_stats_test.go`（新） | 4 个用例：方向计数、静默判定、并发安全、日志字段可用 |
| `extension/arthastunnelext/arthasuri_relay.go` | pump 启动/等待/读帧/转发加 Debug；`Relay session ended` 追加方向计数（Info） |
| `docs/2026-11-25/arthas-tunnel-relay-silence.md` | 本记录 |

**转发逻辑一行未改**。诊断日志保留在代码中（Debug 级，默认不输出），会话结束汇总是 Info 级、每会话一条。

## 已确认事实（CDP + 服务端日志）

- `[System]` 4 行来自前端 `TerminalPanel.tsx` 本地 `terminal.write()`。
- `[*]` / `[+]` 4 行来自后端 `sendBrowserStatus()`，ANSI 文本帧，字节长度与帧日志逐条吻合（66/37/55/56）。
- 后端在 `arthasuri_compat.go:950` 发 `[+]`，到 `:967` 才启动 `relayWebSocketPair`；
  `[+]` 只代表**通道绑定成功**，不代表交互会话就绪。
- `arthasuri_relay.go` 是纯字节透传；诊断日志加上之前，成功路径没有任何日志，
  因此"无 relay 日志"本身不具备判别力（正常样本也无日志）。
- 静默时连接始终存活，页面无 `Connection closed` / `WebSocket error`。

## 样本数据

用 Chrome CDP 9222 抓网络层全量 WS 帧（不受前端 JS 过滤影响），同步采集服务端 relay
Debug 日志。采样脚本 `/tmp/arthas_sample.py`，日志样本 `/tmp/arthas_sample_r*.log`。

| 样本 | 路径 | CDP `[+]` 后下行帧 | relay `from=tunnel` | 结果 |
|---|---|---|---|---|
| **r1** | 初次 attach | **0** | **0 帧 / 0 字节** | 空白终端 |
| r1_reopen | 同一 Agent 关闭重开 | 30+ | 18 帧 | banner + `[arthas@1]$` |
| **r2** | 重启后初次 attach | 30+ | 19 帧 | banner + `[arthas@1]$` |
| **r3** | 重启后初次 attach | **0** | **0 帧 / 0 字节** | 空白终端 |
| r3 二次采集 | 刷新后 Tunnel 已注册 | 29 | 16 帧 | banner + `[arthas@1]$` |

**r1 会话结束汇总**（Info 级，72.48 秒后浏览器关闭）：

```
duration=72.48s  reason=browser closed
browser_frames=1  browser_bytes=39
tunnel_frames=0   tunnel_bytes=0
```

**r3 会话结束汇总**（20.33 秒）：`browser_frames=1, browser_bytes=39, tunnel_frames=0, tunnel_bytes=0`。

静默发生率：本次 3 轮初次连接中 2 轮静默（r1、r3），1 轮正常（r2）。

## 根因结论（已坐实）

**静默发生在 Agent 侧：首次 attach 建立的 `openTunnel` WebSocket 连接上，Agent 自始至终没有写入任何字节。**

证据链（r1/r3 一致）：

```
Relay pump started             (browser 方向)
Relay pump started             (tunnel  方向)
Relay pump waiting for frame   from=browser
Relay pump waiting for frame   from=tunnel
Relay frame read               from=browser → to=tunnel, bytes=39   (前端 resize)
Relay frame forwarded          from=browser → to=tunnel
（此后整段会话 from=tunnel 0 帧，直到浏览器关闭）
```

- 两个 pump 都正常启动并在 `ReadMessage` 上等待；
- 前端 resize 被完整读到并转发给 Agent；
- `from=tunnel` 零帧零字节，**不是延迟**（会话持续 72s / 20s 都是 0）；
- 无 `Relay write error` / `read error` / `ping failed`，连接未异常关闭。

**已排除**：relay 丢帧、前端 JSON 过滤、WS 断开、协议错配、浏览器渲染问题。

**已证伪的假设**：服务端时序竞态。r1（静默）与 r2（正常）的
`agentRegister → startTunnel` 间隔分别是 1.028s / 1.010s，
`startTunnel → openTunnel` 0.34s / 0.50s，几乎一致，结果却相反。变量不在 collector 侧。

**仍未坐实**：Arthas 官方 `TunnelClient` 内部为什么有时不写字节——该实现不在这个仓库，
采集到这一层已到边界。推测与"刚 attach 起来的 TunnelClient，首次 openTunnel 连接是否绑定到可交互 TUI 会话"有关，属非确定性。

## 对修复的指导

无论 Arthas 内部根因如何，collector/前端必须有兜底，否则用户会一直看到假就绪的空终端：

1. **前端兜底（最小代价）**：`[+]` 后不写 "terminal is ready"，改为"通道已建立，等待 Arthas 就绪"；
   加"等待首帧 + 超时自动 close/reopen 一次"。关闭重开已在 r1_reopen / r3 二次采集验证 100% 有效。
2. **后端语义修正**：`statusReady` 从"openTunnel 到达"推迟到"relay 收到第一帧 agent 数据"，超时发明确错误。
3. **进度状态保留**：按约束，上述所有状态提示继续在终端交互里显示，只是不能提前宣称就绪。
4. 误导性代码（本次未改）：前端 `attachArthas()` 后固定等 2 秒，实际注册耗时 5~10 秒。

## 验收记录

| 项目 | 结果 |
|---|---|
| `go test ./...` | exit 0，40 个包 ok |
| `git diff --check` | 通过 |
| 镜像重建 + 部署 | 成功，Pod Running |
| Debug 级别临时开启 | 生效（日志含 `Relay frame read/forwarded`、`from` 方向字段） |
| 样本采集 | r1/r2/r3 + 2 个对照，静默轮均有 `tunnel_frames=0` 汇总证据 |
| 配置恢复 | `level: info`、debug 日志 0 条、ConfigMap 40205 字节 YAML 可解析、无 PLACEHOLDER |
| 恢复后健康 | health 200、`Everything is ready`、无 error/panic |

## 遗留与风险

- 诊断日志保留在代码中：Debug 级默认不输出；`Relay session ended` 每会话一条 Info，新增 4 个数值字段。
- Chrome CDP 9222 仍运行（用于采样），用完可关闭。
- 临时文件 `/tmp/arthas_sample.py`、`/tmp/arthas_sample_r*.log`、`/tmp/cm-*.json` 未清理，保留作证据。
- 采样时 r3 的命令重复执行了脚本，第二次覆盖了该轮的 relay 日志文件；但 r3 静默会话的
  `Relay session ended` 汇总仍保留在第二次日志中，不影响结论。
- 运行时日志级别改动只作用于 ConfigMap，仓库模板/元数据**未修改**；重新渲染会保持 `info`。
- 本批改动**未提交**；本轮未写任何修复补丁。
