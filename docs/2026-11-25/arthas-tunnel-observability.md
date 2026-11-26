# Arthas Tunnel 可观测性完善实施记录

## 需求

完善 collector 侧 Arthas Tunnel 的可观测性。背景见
`docs/2026-11-25/arthas-terminal-silent-fix.md` 与
`docs/2026-11-25/arthas-tunnel-relay-silence.md`。

## 决策与边界

- 范围 **A+B**：
  - A：安全脱敏 + 首帧信号；
  - B：活跃量指标 + 连接阶段耗时与失败归因。
- **不开** 8888 pull 端点：内部自监控指标已走
  `telemetry.metrics.readers.periodic → otlp http://localhost:4318 → metrics pipeline → observability_storage`。
- **不做** 前端上报、`/api/v2/arthas/status` 诊断端点、探针侧 `shellServerReady` 上报。
- **不改** 配置模板与元数据（无需新增配置项/端口）。
- 指标标签**只低基数**：禁止 agent_id、app_name、session_id、reason 原文。
- 文档不记录任何 token、密码或凭据值。

## 现状盘点（实施前已实测）

| 能力 | 结论 |
|---|---|
| 内部指标 reader | 可用；`otelcol_arthas_relay_sessions{result=ok}` 已落库 |
| 既有指标 | 仅 `otelcol_arthas_relay_sessions{result=ok\|silent}` |
| 既有日志 | `Relay session ended`（Info，4 个方向计数）；帧级日志为 Debug；connectArthas 各阶段 Info |
| 敏感信息 | `arthasuri_compat.go` 的 `Agent registered` 日志里 `app_name` 为明文凭据（WS 用 appName 当 token） |
| 接口限制 | `pending.PendingStore` 接口**没有** `Count()`，`RedisPendingStore` 也没有；`Count()` 只在 `LocalPendingStore` 上 |
| 复用点 | `ListAgents()` 存在且在 distributed 模式走 Redis registry、失败回落本地 |

**设计调整 1**：因 `PendingStore` 无全局 `Count()`，pending 指标改用 UpDownCounter
（`CreateWithBrowserConn` 后 +1 / defer −1），语义为"本节点进行中的 connectArthas 请求"。
**设计调整 2**：`registered_agents` 的 gauge 回调读本地内存 map（加锁 O(1)），
不在采集路径上做 Redis 网络 IO。

## 实施进展

- [x] 建立本文档。
- [x] A1 脱敏。
- [x] A2 首帧信号。
- [x] B1 活跃量。
- [x] B2 阶段耗时与失败归因。
- [x] 全量验证。
- [x] 更新本文档。

## 变更清单

| 文件 | 改动摘要 |
|---|---|
| `extension/arthastunnelext/arthasuri_compat.go` | A1 两处 `app_name` → `<redacted>`；新增 `metrics` 字段、`withRelayMetrics` option、`localAgentCount()` / `mode()`；`handleConnectArthas` 埋入阶段耗时与失败归因；pending 计数用 defer 配对；relay 调用带 `session_id` 日志字段 |
| `extension/arthastunnelext/relay_stats.go` | 新增 `startedAtNano` / `firstTunnelFrameAtNano`（CAS 只记一次）、`newRelayStats()`、`firstFrameLatency()`；`fields()` 追加 `first_agent_frame_latency` |
| `extension/arthastunnelext/relay_metrics.go` | 7 个 instrument 集中管理；no-op 安全；注册失败回落 noop 不中断连接 |
| `extension/arthastunnelext/arthasuri_relay.go` | 首个 `from=tunnel` 帧打一条 Info（每会话最多一条）；`newRelayStats(startTime)` |
| `extension/arthastunnelext/extension.go` | 统一 `withRelayMetrics` 注入并绑定 gauge 计数源 |
| `extension/arthastunnelext/relay_metrics_test.go` | 8 个用例：会话分类、首帧 Histogram、短会话排除、活跃会话平衡、gauge 注入、阶段/失败/pending、no-op 安全、option 注入 |
| `extension/arthastunnelext/relay_stats_test.go` | 补首帧 CAS、浏览器帧不误判、fields 字段数用例 |
| `extension/arthastunnelext/query_log_test.go` | 新增源码级脱敏回归用例 |
| `docs/2026-11-25/arthas-tunnel-observability.md` | 本记录 |

### 指标清单

| 指标 | 类型 | 标签 |
|---|---|---|
| `otelcol_arthas_relay_sessions` | Counter | `result=ok\|silent`（既有，保留） |
| `otelcol_arthas_relay_first_frame_seconds` | Histogram | `result=ok\|timeout` |
| `otelcol_arthas_registered_agents` | ObservableGauge | 无 |
| `otelcol_arthas_active_relay_sessions` | UpDownCounter | 无 |
| `otelcol_arthas_pending_connections` | UpDownCounter | `mode=local\|distributed` |
| `otelcol_arthas_connect_duration_ms` | Histogram | `phase=wait_agent\|start_tunnel\|wait_open_tunnel\|relay_setup` |
| `otelcol_arthas_connect_failures` | Counter | `stage=lookup\|start_tunnel\|open_tunnel\|timeout\|proxy` |

### 设计约束落实情况

- meter 为空时 no-op 安全；instrument 注册失败不影响终端连接。
- observer 保持变参注入，所有现有 `relayWebSocketPair` 调用点不变。
- `relayStarted` / `relayFinished` 在调用点用 defer 配对，避免 relay 提前返回导致 gauge 泄漏。
- 统计逻辑独立可单测，转发逻辑零改动。
- `session_id` 只进日志，不进指标标签。
- 未改产品转发逻辑、未改前端、未开端口、未改配置模板/元数据。

## 验收记录

| 项目 | 结果 |
|---|---|
| `go test ./...` | exit 0，40 包 ok，FAIL 0 |
| 新增单测 | `arthastunnelext` 全量 PASS；首帧 CAS / 脱敏 / 指标分类 / gauge 注入均 PASS |
| `npm run build` | 通过（前端未改，仅回归） |
| IDE 静态诊断 | 修改目录 0 问题 |
| `git diff --check` | 通过 |
| 镜像重建 + 部署 | 成功，health 200，无 panic / metric 注册错误 |
| **指标落库** | 查询 API 的 names 中出现全部新增指标（7 个基础名 + Histogram 的 `_bucket/_count/_sum`） |
| 指标数据 | `first_frame_seconds_count{result=ok}=1`、`connect_duration_ms` 4 个 phase、`registered_agents=1`、`active=0`、`pending=0`（配对无泄漏）、`relay_sessions{result=ok}=1` |
| **A1 运行时** | `Agent registered` 日志为 `"app_name": "<redacted>"`；源码级回归用例 PASS |
| **首帧日志** | `Relay first agent frame received` 每会话 1 条，带 `session_id` / `wait=0.967024833` / `bytes`；`Relay session ended` 带 `first_agent_frame_latency` |
| 不刷屏 | Info 级 `Relay frame read` 计数 0（帧级仍是 Debug） |
| CDP 回归 | `[arthas@1]$` 正常、英文等待文案正常、无回归 |
| 日志级别 | `info` 未改；配置模板/元数据未改 |

注：`otelcol_arthas_connect_failures` 当前 0 序列属正常（验证窗口内无失败请求）；
`first_frame_seconds` 基础名直接查询为空是 Histogram 的固有表现，需查 `_count/_sum/_bucket`。

## 未完成任务

| # | 任务 | 状态 |
|---|------|------|
| 1 | 提交并推送本批改动 | 用户未要求，未执行 |
| 2 | 阶段 C（诊断端点、前端上报、8888 pull） | 按用户决定不做 |
| 3 | 探针侧 `shellServerReady` 上报 | 按用户决定不做（Java 仓库，见独立文档） |

## 遗留与风险

- `registered_agents` 是**本节点**内存注册数，不是分布式全局数；跨节点总数需查各副本或 Redis registry。
- `first_frame_seconds` 的 `timeout` 样本只在会话时长 ≥3s 时记录，避免用户立刻关面板稀释。
- 静默发生率仍依赖 `relay_sessions{result=silent}` 与首帧 timeout；需长期观测才能给出稳定基线。
- 内部指标当前仅经 ES/查询 API 可读，8888 pull 端点按用户决定保持关闭。
- 采样脚本 `/tmp/arthas_sample.py` 的 ready 帧识别仍按旧文案 `Connected successfully`，
  其 `AFTER_READY_downlink` 统计不准（产品无关）；若继续使用需改成 `[+]` 前缀。
- Chrome CDP 9222 仍运行；临时采样日志在 `/tmp/arthas_sample_*.log`。
- 本批改动**未提交**；`TerminalPanel.tsx`、`terminalReadiness.ts` 等此前修复也未提交。
