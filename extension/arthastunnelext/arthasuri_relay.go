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

func relayWebSocketPair(ctx context.Context, logger *zap.Logger, a, b *websocket.Conn, observers ...relaySessionObserver) {
	if a == nil || b == nil {
		return
	}

	startTime := time.Now()
	stats := newRelayStats(startTime)

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
		logger.Debug("Relay pump started",
			zap.String("from", srcName),
			zap.String("to", dstName),
		)
		loggedWait := false
		for {
			select {
			case <-ctx.Done():
				return
			case <-done:
				return
			default:
			}

			if !loggedWait {
				logger.Debug("Relay pump waiting for frame", zap.String("from", srcName))
				loggedWait = true
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

			stats.record(srcName, len(data))
			if srcName == "tunnel" && stats.tunnelFrames.Load() == 1 {
				if latency, ok := stats.firstFrameLatency(); ok {
					logger.Info("Relay first agent frame received",
						zap.Duration("wait", latency),
						zap.Int("bytes", len(data)),
					)
				}
			}
			logger.Debug("Relay frame read",
				zap.String("from", srcName),
				zap.String("to", dstName),
				zap.Int("message_type", mt),
				zap.Int("bytes", len(data)),
			)

			if err := dst.safeWriteMessage(mt, data); err != nil {
				logger.Debug("Relay write error",
					zap.String("dst", dstName),
					zap.Error(err),
				)
				return
			}
			logger.Debug("Relay frame forwarded",
				zap.String("from", srcName),
				zap.String("to", dstName),
				zap.Int("bytes", len(data)),
			)
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

	// Log relay session summary. The counters are emitted at Info so a silent
	// session can be diagnosed even when Debug logging is disabled.
	duration := time.Since(startTime)
	fields := append([]zap.Field{
		zap.Duration("duration", duration),
		zap.String("reason", closeReason),
	}, stats.fields()...)
	logger.Info("Relay session ended", fields...)

	for _, observe := range observers {
		if observe != nil {
			observe(stats, duration)
		}
	}
}
