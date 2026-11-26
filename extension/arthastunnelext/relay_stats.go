// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package arthastunnelext

import (
	"sync/atomic"
	"time"

	"go.uber.org/zap"
)

// relayStats counts frames/bytes flowing in each direction of a relay session.
// It is diagnostic-only: the relay forwarding logic never reads these values.
// srcName matches the pump names in relayWebSocketPair: "browser" or "tunnel".
type relayStats struct {
	browserFrames atomic.Int64
	browserBytes  atomic.Int64
	tunnelFrames  atomic.Int64
	tunnelBytes   atomic.Int64

	// startedAtNano and firstTunnelFrameAtNano are unix nanoseconds.
	// firstTunnelFrameAtNano stays 0 when the agent never sent a frame.
	startedAtNano          atomic.Int64
	firstTunnelFrameAtNano atomic.Int64
}

func newRelayStats(start time.Time) *relayStats {
	s := &relayStats{}
	s.startedAtNano.Store(start.UnixNano())
	return s
}

func (s *relayStats) record(srcName string, n int) {
	switch srcName {
	case "browser":
		s.browserFrames.Add(1)
		s.browserBytes.Add(int64(n))
	case "tunnel":
		s.tunnelFrames.Add(1)
		s.tunnelBytes.Add(int64(n))
		s.firstTunnelFrameAtNano.CompareAndSwap(0, time.Now().UnixNano())
	}
}

// firstFrameLatency returns how long the agent took to produce its first frame.
// ok is false when the agent never wrote anything to the tunnel connection.
func (s *relayStats) firstFrameLatency() (time.Duration, bool) {
	first := s.firstTunnelFrameAtNano.Load()
	start := s.startedAtNano.Load()
	if first == 0 || start == 0 || first < start {
		return 0, false
	}
	return time.Duration(first - start), true
}

// fields returns the per-direction counters as log fields. tunnel_frames == 0
// together with zero browser downlink frames proves the agent never wrote any
// byte on its openTunnel connection.
func (s *relayStats) fields() []zap.Field {
	fields := []zap.Field{
		zap.Int64("browser_frames", s.browserFrames.Load()),
		zap.Int64("browser_bytes", s.browserBytes.Load()),
		zap.Int64("tunnel_frames", s.tunnelFrames.Load()),
		zap.Int64("tunnel_bytes", s.tunnelBytes.Load()),
	}
	if d, ok := s.firstFrameLatency(); ok {
		fields = append(fields, zap.Duration("first_agent_frame_latency", d))
	}
	return fields
}
