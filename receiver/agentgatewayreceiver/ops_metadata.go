// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package agentgatewayreceiver

import (
	"context"
	"strconv"

	"go.opentelemetry.io/collector/custom/extension/opsconfig"
	"go.opentelemetry.io/collector/custom/receiver/agentgatewayreceiver/longpoll"
)

// opsMetadataProvider injects admin-managed global runtime config into the
// config-poll response. Metadata keys are namespaced (ops.*) so they never
// collide with Nacos business config or gateway runtime metadata.
type opsMetadataProvider struct {
	store *opsconfig.Store
}

var _ longpoll.ServerMetadataProvider = (*opsMetadataProvider)(nil)

func (p *opsMetadataProvider) Name() string { return "ops_config" }

func (p *opsMetadataProvider) ProvideMetadata(_ context.Context, _ *longpoll.PollRequest) map[string]string {
	if p == nil || p.store == nil {
		return nil
	}
	values, revision := p.store.Snapshot()
	meta := make(map[string]string, len(values)+1)
	for k, v := range values {
		meta[k] = v
	}
	meta["ops.revision"] = strconv.FormatInt(revision, 10)
	return meta
}
