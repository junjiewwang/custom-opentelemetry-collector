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

func relayWebSocketPair(ctx context.Context, logger *zap.Logger, a, b *websocket.Conn) {
	if a == nil || b == nil {
		return
	}

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

	pump := func(src, dst *websocket.Conn, dir string) {
		defer closeBoth("relay closed")
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
						zap.String("dir", dir),
						zap.Int("code", ce.Code),
						zap.String("text", ce.Text),
					)
				} else {
					logger.Debug("Relay read error",
						zap.String("dir", dir),
						zap.Error(err),
					)
				}
				return
			}

			dst.SetWriteDeadline(time.Now().Add(30 * time.Second))
			if err := dst.WriteMessage(mt, data); err != nil {
				logger.Debug("Relay write error",
					zap.String("dir", dir),
					zap.Error(err),
				)
				return
			}
		}
	}

	go pump(a, b, "a->b")
	go pump(b, a, "b->a")

	select {
	case <-ctx.Done():
		closeBoth("relay context done")
	case <-done:
		return
	}
}
