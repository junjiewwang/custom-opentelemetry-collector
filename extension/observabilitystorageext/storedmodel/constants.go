// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package storedmodel

// Backend provider names. Used by hybrid routing config and factory.
const (
	BackendES  = "elasticsearch"
	BackendPG  = "postgresql"
)

// Signal routing keys used by hybrid provider's routing map.
const (
	SignalTrace = "trace"
	SignalMetric = "metric"
	SignalLog   = "log"
	SignalAdmin = "admin"
)
