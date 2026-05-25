# Arthas Terminal WebSocket Relay 心跳修复方案

## 1. 问题现象

用户连接 Arthas Terminal 后，偶尔出现以下现象：

```
[System] Authenticating...
[System] Connecting to Arthas...
[*] Connecting to agent...
[*] Waiting for agent to establish tunnel...
[+] Connected successfully, terminal is ready
```

之后**再无输出，键入也无用**。关闭重新打开后就恢复正常。

## 2. 根因分析

### 2.1 架构回顾

Arthas Terminal 的连接链路：

```
Browser (xterm.js WebSocket)
    ↕ pump(browser→tunnel)
Server: relayWebSocketPair()
    ↕ pump(tunnel→browser)  
Agent (Arthas tunnel WebSocket)
```

`relayWebSocketPair` 是核心 relay 函数，位于 `extension/arthastunnelext/arthasuri_relay.go`。

### 2.2 关键代码问题

```go
// arthasuri_relay.go - pump 函数
pump := func(src, dst *websocket.Conn, srcName, dstName string) {
    defer closeBoth(srcName + " closed")
    for {
        select {
        case <-ctx.Done():
            return
        case <-done:
            return
        default:
        }

        mt, data, err := src.ReadMessage()  // ← 问题点：无 ReadDeadline，无限阻塞
        if err != nil {
            // ... error handling
            return
        }

        dst.SetWriteDeadline(time.Now().Add(30 * time.Second))
        if err := dst.WriteMessage(mt, data); err != nil {
            // ... error handling
            return
        }
    }
}
```

**核心问题：`src.ReadMessage()` 无 ReadDeadline 且无 ping/pong 心跳机制。**

### 2.3 对比：Agent 控制连接有心跳

Agent 注册的控制连接 (`runAgentControlLoops`) **已有完善的心跳机制**：

```go
func (s *arthasURICompat) runAgentControlLoops(ctx *compatConnContext, a *compatAgent) {
    livenessTimeout := s.livenessTimeout()
    _ = ctx.conn.SetReadDeadline(time.Now().Add(livenessTimeout))
    ctx.conn.SetPongHandler(func(string) error {
        _ = ctx.conn.SetReadDeadline(pongTime.Add(livenessTimeout))
        return nil
    })
    
    // Ping loop
    go func() {
        t := time.NewTicker(s.pingInterval())  // 默认 20s
        for {
            select {
            case <-t.C:
                _ = a.safeWriteControl(websocket.PingMessage, nil, ...)
            }
        }
    }()
}
```

但 **relay 连接（browser↔tunnel）完全没有心跳保活**，这是设计遗漏。

### 2.4 故障时序

```
T+0s    Browser ←→ Server ←→ Agent (relay 正常工作)
T+30s   中间网络设备（NAT/LB/Proxy）空闲超时，静默丢弃连接映射
T+30s+  Browser 发送数据 → 到达 Server relay pump
        pump 调用 dst.WriteMessage() → 写入 Agent tunnel WebSocket
        但 Agent 端 TCP 连接已被中间设备丢弃
        → TCP 重传等待（默认 ~15min 才会 timeout）
        → 或者直接 half-open 无响应
T+∞     pump 永远阻塞在 src.ReadMessage() 或 dst.WriteMessage()
        用户看到：连接成功但无任何输入输出
```

### 2.5 为什么关闭重开能恢复

前端 `useTerminal.ts` 中 `disconnect()` 调用 `ws.close()`：
- 浏览器端 WebSocket 被关闭
- Server 的 `pump(browser→tunnel)` 收到 CloseError
- 触发 `closeBoth()`，关闭两端连接
- 重新打开走全新的连接流程，网络路径重新建立

## 3. 修复方案

### 3.1 方案概述：Relay 连接添加双向 Ping/Pong 心跳

在 `relayWebSocketPair` 中为 **两个 WebSocket 连接** 分别添加 ping/pong 心跳机制，确保：
- 定期发送 ping 保活，防止 NAT/代理超时
- 设置 ReadDeadline，一旦超时自动断开死连接
- pong 响应时续期 ReadDeadline

### 3.2 具体实现

```go
func relayWebSocketPair(ctx context.Context, logger *zap.Logger, a, b *websocket.Conn) {
    if a == nil || b == nil {
        return
    }

    const (
        pingInterval     = 15 * time.Second  // 发送 ping 的间隔
        pongWait         = 30 * time.Second  // 等待 pong 的超时时间
        writeWait        = 30 * time.Second  // 写超时
    )

    done := make(chan struct{})
    var once sync.Once
    closeBoth := func(reason string) {
        once.Do(func() {
            _ = writeClose(a, 2000, reason)
            _ = writeClose(b, 2000, reason)
            _ = a.Close()
            _ = b.Close()
            close(done)
        })
    }

    // 为每个连接设置 pong handler 和初始 ReadDeadline
    setupHeartbeat := func(conn *websocket.Conn, name string) {
        _ = conn.SetReadDeadline(time.Now().Add(pongWait))
        conn.SetPongHandler(func(string) error {
            _ = conn.SetReadDeadline(time.Now().Add(pongWait))
            return nil
        })
    }

    setupHeartbeat(a, "browser")
    setupHeartbeat(b, "tunnel")

    pump := func(src, dst *websocket.Conn, srcName, dstName string) {
        defer closeBoth(srcName + " closed")
        for {
            select {
            case <-ctx.Done():
                return
            case <-done:
                return
            default:
            }

            mt, data, err := src.ReadMessage()
            if err != nil {
                var ce *websocket.CloseError
                if errors.As(err, &ce) {
                    logger.Debug("Relay read close",
                        zap.String("src", srcName),
                        zap.Int("code", ce.Code),
                        zap.String("text", ce.Text),
                    )
                } else {
                    logger.Debug("Relay read error",
                        zap.String("src", srcName),
                        zap.Error(err),
                    )
                }
                return
            }

            dst.SetWriteDeadline(time.Now().Add(writeWait))
            if err := dst.WriteMessage(mt, data); err != nil {
                logger.Debug("Relay write error",
                    zap.String("dst", dstName),
                    zap.Error(err),
                )
                return
            }
        }
    }

    // Ping loop：向两端定期发送 ping
    pingLoop := func(conn *websocket.Conn, name string) {
        ticker := time.NewTicker(pingInterval)
        defer ticker.Stop()
        for {
            select {
            case <-ctx.Done():
                return
            case <-done:
                return
            case <-ticker.C:
                deadline := time.Now().Add(10 * time.Second)
                if err := conn.WriteControl(websocket.PingMessage, nil, deadline); err != nil {
                    logger.Debug("Relay ping failed",
                        zap.String("target", name),
                        zap.Error(err),
                    )
                    closeBoth(name + " ping failed")
                    return
                }
            }
        }
    }

    go pump(a, b, "browser", "tunnel")
    go pump(b, a, "tunnel", "browser")
    go pingLoop(a, "browser")
    go pingLoop(b, "tunnel")

    select {
    case <-ctx.Done():
        closeBoth("relay context done")
    case <-done:
        return
    }
}
```

### 3.3 参数选择依据

| 参数 | 值 | 依据 |
|------|------|------|
| pingInterval | 15s | < 常见 NAT/LB 超时（30-60s），保证在超时前发送保活 |
| pongWait | 30s | 2× pingInterval，容忍一次 ping 丢失 |
| writeWait | 30s | 与现有 WriteDeadline 保持一致 |

### 3.4 WriteControl 并发安全

`gorilla/websocket` 的 `WriteControl` 是并发安全的（与 `WriteMessage` 不冲突），但两个 `WriteControl`（ping 和 close）可能并发。这里因为 `closeBoth` 使用了 `sync.Once`，且 `WriteControl` 自身是线程安全的，所以无需额外加锁。

但需要注意：`pump` 函数中的 `dst.WriteMessage()` 和 `pingLoop` 中的 `conn.WriteControl()` 操作的是**不同方向的连接**：
- `pump(a→b)` 写 `b`，`pingLoop(a)` 控制写 `a` → 无冲突
- `pump(b→a)` 写 `a`，`pingLoop(b)` 控制写 `b` → 无冲突

如果 `pingLoop(a)` 和 `pump(b→a)` 都写 `a`，则存在并发写。**需要对每个连接加写锁**：

```go
type guardedConn struct {
    *websocket.Conn
    writeMu sync.Mutex
}

func (g *guardedConn) safeWriteMessage(mt int, data []byte) error {
    g.writeMu.Lock()
    defer g.writeMu.Unlock()
    return g.Conn.WriteMessage(mt, data)
}

func (g *guardedConn) safeWriteControl(mt int, data []byte, deadline time.Time) error {
    g.writeMu.Lock()
    defer g.writeMu.Unlock()
    return g.Conn.WriteControl(mt, data, deadline)
}
```

> **注意**：根据 gorilla/websocket 文档，`WriteControl` 实际上可以与 `WriteMessage` 并发调用（它使用独立的写锁路径）。但为了代码清晰和防御性编程，建议统一用 mutex 保护。

### 3.5 前端可选增强

在 `useTerminal.ts` 中添加应用层心跳检测（可选，作为 P2）：

```typescript
// 应用层心跳：如果 N 秒无数据，显示提示
const HEARTBEAT_TIMEOUT = 45_000; // 45s
let lastDataTime = Date.now();

ws.onmessage = (event) => {
    lastDataTime = Date.now();
    // ... existing handler
};

const heartbeatChecker = setInterval(() => {
    if (Date.now() - lastDataTime > HEARTBEAT_TIMEOUT) {
        // 显示提示：连接可能已断开，建议重连
        terminal.write('\r\n\x1b[33m[!] Connection may be lost, consider reconnecting...\x1b[0m\r\n');
    }
}, 10_000);
```

## 4. 影响评估

### 4.1 性能影响

- 每个 relay 新增 2 个 ping goroutine（轻量，每 15s 一次 WriteControl）
- 额外网络开销：每 15s 发送 2-4 bytes 的 WebSocket control frame，可忽略

### 4.2 兼容性

- **Agent 端**：Arthas tunnel agent 使用标准 WebSocket 库，天然支持 ping/pong 协议层自动回复 pong
- **Browser 端**：浏览器原生 WebSocket API **不暴露 ping/pong**（由浏览器内部处理），但 gorilla/websocket 发送的 ping 会被浏览器自动回复 pong
- **跨节点代理**：`proxyConnectArthas` 和 `proxyOpenTunnel` 使用的 WebSocket Dial 默认支持 ping/pong

### 4.3 风险

| 风险 | 概率 | 缓解措施 |
|------|------|----------|
| 心跳误判断开 | 低 | pongWait=30s 容忍网络抖动 |
| 写锁竞争导致延迟 | 极低 | ping 仅 control frame，耗时微秒级 |
| Agent 不回复 pong | 极低 | 标准 WebSocket 协议要求回复 |

## 5. 实施计划

### P0: Relay 心跳（必须）

- 修改 `extension/arthastunnelext/arthasuri_relay.go`
- 为 relay 双向连接添加 ping/pong + ReadDeadline
- 添加并发写保护（guardedConn 或复用 WriteControl 的并发安全特性）

### P1: 日志增强（推荐）

- relay 结束时记录 duration 和关闭原因
- 区分正常关闭（用户主动 close）和异常关闭（超时/ping 失败）

### P2: 前端提示（可选）

- `useTerminal.ts` 添加无数据超时提示
- 不自动重连（避免状态混乱），仅提示用户手动重连

## 6. 实施状态

- **P0 Relay 心跳**：✅ 已完成 (`arthasuri_relay.go` — relayConn + setupHeartbeat + pingLoop)
- **P1 日志增强**：✅ 已完成 (relay 结束时记录 duration + closeReason)
- **P2 前端提示**：✅ 已完成 (`TerminalPanel.tsx` — 45s 无数据超时提示)
- **单元测试**：✅ 已完成 (`arthasuri_relay_test.go` — 5 个测试用例, race detector 通过)

---

## 附录 A：相关文件

| 文件 | 说明 |
|------|------|
| `extension/arthastunnelext/arthasuri_relay.go` | Relay 核心，修复主位置 |
| `extension/arthastunnelext/arthasuri_compat.go` | Agent 控制连接心跳参考 |
| `extension/adminext/webui-react/src/components/Terminal/useTerminal.ts` | 前端终端 hook |
| `extension/adminext/webui-react/src/components/Terminal/TerminalPanel.tsx` | 前端终端组件 |

## 附录 B：Agent 控制连接心跳对比

| 特性 | Agent 控制连接 (runAgentControlLoops) | Relay 连接 (relayWebSocketPair) |
|------|------|------|
| Ping 发送 | ✅ 每 20s | ❌ 无 |
| Pong 处理 | ✅ 续期 ReadDeadline | ❌ 无 |
| ReadDeadline | ✅ livenessTimeout (90s) | ❌ 无 |
| WriteDeadline | ✅ 10s (control frame) | ✅ 30s (data frame) |
| 连接死亡检测 | ✅ ReadDeadline 超时触发错误 | ❌ 永久阻塞 |
