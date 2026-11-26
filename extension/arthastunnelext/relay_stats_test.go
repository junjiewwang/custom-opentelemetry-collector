// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package arthastunnelext

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"go.uber.org/zap/zaptest"
)

func TestRelayStatsRecordByDirection(t *testing.T) {
	s := &relayStats{}
	s.record("browser", 39)
	s.record("browser", 29)
	s.record("tunnel", 126)
	s.record("unknown", 999)

	require.Equal(t, int64(2), s.browserFrames.Load())
	require.Equal(t, int64(68), s.browserBytes.Load())
	require.Equal(t, int64(1), s.tunnelFrames.Load())
	require.Equal(t, int64(126), s.tunnelBytes.Load())
	require.Len(t, s.fields(), 4)
}

func TestRelayStatsZeroMeansSilentAgent(t *testing.T) {
	s := &relayStats{}
	s.record("browser", 39)
	require.Equal(t, int64(0), s.tunnelFrames.Load(), "no frame from agent tunnel")
	require.Equal(t, int64(0), s.tunnelBytes.Load())
}

func TestRelayStatsConcurrentSafe(t *testing.T) {
	s := &relayStats{}
	done := make(chan struct{})
	for i := 0; i < 20; i++ {
		go func() {
			for j := 0; j < 50; j++ {
				s.record("tunnel", 1)
			}
			done <- struct{}{}
		}()
	}
	for i := 0; i < 20; i++ {
		<-done
	}
	require.Equal(t, int64(1000), s.tunnelFrames.Load())
	require.Equal(t, int64(1000), s.tunnelBytes.Load())
}

func TestRelayStatsFieldsAreUsable(t *testing.T) {
	logger := zaptest.NewLogger(t)
	s := &relayStats{}
	s.record("tunnel", 5)
	logger.Info("Relay session ended", s.fields()...)
}

func TestRelayStatsFirstFrameRecordedOnce(t *testing.T) {
	s := newRelayStats(time.Now().Add(-500 * time.Millisecond))
	_, ok := s.firstFrameLatency()
	require.False(t, ok)

	s.record("browser", 39)
	_, ok = s.firstFrameLatency()
	require.False(t, ok, "browser frames must not count as agent output")

	s.record("tunnel", 126)
	first, ok := s.firstFrameLatency()
	require.True(t, ok)
	require.GreaterOrEqual(t, first, 500*time.Millisecond)

	s.record("tunnel", 2)
	second, ok := s.firstFrameLatency()
	require.True(t, ok)
	require.Equal(t, first, second, "first frame timestamp must not move")
}

func TestRelayStatsFieldsIncludeLatency(t *testing.T) {
	s := newRelayStats(time.Now().Add(-time.Second))
	s.record("tunnel", 126)
	require.Len(t, s.fields(), 5)

	silent := newRelayStats(time.Now().Add(-time.Second))
	silent.record("browser", 39)
	require.Len(t, silent.fields(), 4)
}
