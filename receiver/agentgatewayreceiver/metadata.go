// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package agentgatewayreceiver

import (
	"context"
	"net"

	"go.opentelemetry.io/collector/custom/receiver/agentgatewayreceiver/longpoll"
)

// gatewayMetadataProvider implements longpoll.ServerMetadataProvider to inject
// runtime information from the gateway into agent configurations.
type gatewayMetadataProvider struct {
	config *Config
}

var _ longpoll.ServerMetadataProvider = (*gatewayMetadataProvider)(nil)

func (p *gatewayMetadataProvider) Name() string {
	return "gateway"
}

func (p *gatewayMetadataProvider) ProvideMetadata(_ context.Context, _ *longpoll.PollRequest) map[string]string {
	metadata := make(map[string]string)
	if p.config.HTTP != nil {
		_, port, err := net.SplitHostPort(p.config.HTTP.Endpoint)
		if err == nil {
			metadata["http_port"] = port
		}
	}
	return metadata
}
