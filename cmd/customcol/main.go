// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

// Package main implements a custom OpenTelemetry Collector with control plane capabilities.
package main

import (
	"log"

	"go.opentelemetry.io/collector/component"
	"go.opentelemetry.io/collector/confmap"
	"go.opentelemetry.io/collector/confmap/provider/envprovider"
	"go.opentelemetry.io/collector/confmap/provider/fileprovider"
	"go.opentelemetry.io/collector/confmap/provider/httpprovider"
	"go.opentelemetry.io/collector/confmap/provider/httpsprovider"
	"go.opentelemetry.io/collector/confmap/provider/yamlprovider"
	"go.opentelemetry.io/collector/otelcol"
)

func main() {
	info := component.BuildInfo{
		Command:     "customcol",
		Description: "Custom OpenTelemetry Collector with Control Plane",
		Version:     "1.0.0",
	}

	set := otelcol.CollectorSettings{
		BuildInfo: info,
		Factories: components,
		ConfigProviderSettings: otelcol.ConfigProviderSettings{
			ResolverSettings: confmap.ResolverSettings{
				ProviderFactories: []confmap.ProviderFactory{
					envprovider.NewFactory(),
					fileprovider.NewFactory(),
					httpprovider.NewFactory(),
					httpsprovider.NewFactory(),
					yamlprovider.NewFactory(),
				},
			},
		},
	}

	if err := run(set); err != nil {
		log.Fatal(err)
	}
}

func run(set otelcol.CollectorSettings) error {
	return runInteractive(set)
}

func runInteractive(set otelcol.CollectorSettings) error {
	cmd := otelcol.NewCommand(set)
	return cmd.Execute()
}
