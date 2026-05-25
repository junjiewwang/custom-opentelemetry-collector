// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package arthastunnelext

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"go.uber.org/zap"
)

// Relay heartbeat constants.
const (
	// relayPingInterval is the interval between ping frames sent to each relay connection.
	// Set below common NAT/LB idle timeouts (30-60s) to keep the connection alive.
	relayPingInterval = 15 * time.Second

	// relayPongWait is the maximum time to wait for a pong response.
	// If no pong is received within this duration, the connection is considered dead.
	// Set to 2× pingInterval to tolerate one missed ping.
	relayPongWait = 30 * time.Second

	// relayWriteWait is the write deadline for data frames during relay.
	relayWriteWait = 30 * time.Second
)

// relayConn wraps a websocket.Conn with a write mutex to protect against concurrent
// writes from the pump goroutine (WriteMessage) and the ping goroutine (WriteControl).
type relayConn struct {
	*websocket.Conn
	writeMu sync.Mutex
}

func (rc *relayConn) safeWriteMessage(mt int, data []byte) error {
	rc.writeMu.Lock()
	defer rc.writeMu.Unlock()
	_ = rc.Conn.SetWriteDeadline(time.Now().Add(relayWriteWait))
	return rc.Conn.WriteMessage(mt, data)
}

func (rc *relayConn) safeWriteControl(mt int, data []byte, deadline time.Time) error {
	rc.writeMu.Lock()
	defer rc.writeMu.Unlock()
	return rc.Conn.WriteControl(mt, data, deadline)
}

func relayWebSocketPair(ctx context.Context, logger *zap.Logger, a, b *websocket.Conn) {
	if a == nil || b == nil {
		return
	}

	startTime := time.Now()

	connA := &relayConn{Conn: a}
	connB := &relayConn{Conn: b}

	done := make(chan struct{})
	var once sync.Once
	var closeReason string
	closeBoth := func(reason string) {
		once.Do(func() {
			closeReason = reason
			_ = writeClose(a, 2000, reason)
			_ = writeClose(b, 2000, reason)
			_ = a.Close()
			_ = b.Close()
			close(done)
		})
	}

	// Setup pong handlers with ReadDeadline renewal for both connections.
	setupHeartbeat := func(conn *websocket.Conn) {
		_ = conn.SetReadDeadline(time.Now().Add(relayPongWait))
		conn.SetPongHandler(func(string) error {
			_ = conn.SetReadDeadline(time.Now().Add(relayPongWait))
			return nil
		})
	}

	setupHeartbeat(a)
	setupHeartbeat(b)

	pump := func(src *websocket.Conn, dst *relayConn, srcName, dstName string) {
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
				// Normal close is expected.
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

			if err := dst.safeWriteMessage(mt, data); err != nil {
				logger.Debug("Relay write error",
					zap.String("dst", dstName),
					zap.Error(err),
				)
				return
			}
		}
	}

	// pingLoop sends periodic ping frames to keep the connection alive
	// and allow ReadDeadline-based dead connection detection.
	pingLoop := func(conn *relayConn, name string) {
		ticker := time.NewTicker(relayPingInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-done:
				return
			case <-ticker.C:
				deadline := time.Now().Add(10 * time.Second)
				if err := conn.safeWriteControl(websocket.PingMessage, nil, deadline); err != nil {
					logger.Debug("Relay ping failed, closing connection",
						zap.String("target", name),
						zap.Error(err),
					)
					closeBoth(name + " ping failed")
					return
				}
			}
		}
	}

	go pump(a, connB, "browser", "tunnel")
	go pump(b, connA, "tunnel", "browser")
	go pingLoop(connA, "browser")
	go pingLoop(connB, "tunnel")

	select {
	case <-ctx.Done():
		closeBoth("relay context done")
	case <-done:
	}

	// Log relay session summary.
	duration := time.Since(startTime)
	logger.Info("Relay session ended",
		zap.Duration("duration", duration),
		zap.String("reason", closeReason),
	)
}
