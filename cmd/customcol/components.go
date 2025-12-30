// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"go.opentelemetry.io/collector/connector"
	"go.opentelemetry.io/collector/connector/forwardconnector"
	"go.opentelemetry.io/collector/exporter"
	"go.opentelemetry.io/collector/exporter/debugexporter"
	"go.opentelemetry.io/collector/exporter/otlpexporter"
	"go.opentelemetry.io/collector/exporter/otlphttpexporter"
	"go.opentelemetry.io/collector/extension"
	"go.opentelemetry.io/collector/extension/memorylimiterextension"
	"go.opentelemetry.io/collector/extension/zpagesextension"
	"go.opentelemetry.io/collector/otelcol"
	"go.opentelemetry.io/collector/processor"
	"go.opentelemetry.io/collector/processor/batchprocessor"
	"go.opentelemetry.io/collector/processor/memorylimiterprocessor"
	"go.opentelemetry.io/collector/receiver"
	"go.opentelemetry.io/collector/receiver/nopreceiver"
	"go.opentelemetry.io/collector/receiver/otlpreceiver"

	// Contrib exporters
	"github.com/open-telemetry/opentelemetry-collector-contrib/exporter/prometheusremotewriteexporter"

	// Contrib connectors
	"github.com/open-telemetry/opentelemetry-collector-contrib/connector/spanmetricsconnector"

	// Custom components
	"go.opentelemetry.io/collector/custom/extension/adminext"
	"go.opentelemetry.io/collector/custom/extension/controlplaneext"
	"go.opentelemetry.io/collector/custom/extension/storageext"
	"go.opentelemetry.io/collector/custom/processor/tokenauthprocessor"
	"go.opentelemetry.io/collector/custom/receiver/enhancedotlpreceiver"
)

func components() (otelcol.Factories, error) {
	var err error
	factories := otelcol.Factories{}

	// Extensions - include both built-in and custom
	factories.Extensions, err = extension.MakeFactoryMap(
		memorylimiterextension.NewFactory(),
		zpagesextension.NewFactory(),
		// Custom extensions
		storageext.NewFactory(),
		controlplaneext.NewFactory(),
		adminext.NewFactory(),
	)
	if err != nil {
		return otelcol.Factories{}, err
	}

	// Receivers - include both built-in and custom
	factories.Receivers, err = receiver.MakeFactoryMap(
		nopreceiver.NewFactory(),
		otlpreceiver.NewFactory(),
		// Custom enhanced OTLP receiver
		enhancedotlpreceiver.NewFactory(),
	)
	if err != nil {
		return otelcol.Factories{}, err
	}

	// Exporters
	factories.Exporters, err = exporter.MakeFactoryMap(
		debugexporter.NewFactory(),
		otlpexporter.NewFactory(),
		otlphttpexporter.NewFactory(),
		// Prometheus Remote Write exporter for metrics
		prometheusremotewriteexporter.NewFactory(),
	)
	if err != nil {
		return otelcol.Factories{}, err
	}

	// Processors
	factories.Processors, err = processor.MakeFactoryMap(
		batchprocessor.NewFactory(),
		memorylimiterprocessor.NewFactory(),
		// Custom token auth processor
		tokenauthprocessor.NewFactory(),
	)
	if err != nil {
		return otelcol.Factories{}, err
	}

	// Connectors
	factories.Connectors, err = connector.MakeFactoryMap(
		forwardconnector.NewFactory(),
		// Span metrics connector - generates metrics from traces
		spanmetricsconnector.NewFactory(),
	)
	if err != nil {
		return otelcol.Factories{}, err
	}

	return factories, nil
}
