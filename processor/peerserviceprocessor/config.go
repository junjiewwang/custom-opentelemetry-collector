// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package peerserviceprocessor

import (
	"errors"
	"time"

	"go.opentelemetry.io/collector/component"
)

// StoreConfig defines the configuration for the peer pairing store.
type StoreConfig struct {
	MaxItems int           `mapstructure:"max_items"`
	TTL      time.Duration `mapstructure:"ttl"`
}

// Config defines the configuration for the peer service processor.
type Config struct {
	Enabled               bool          `mapstructure:"enabled"`
	DBPeerPriority        []string      `mapstructure:"db_peer_priority"`
	MessagingPeerPriority []string      `mapstructure:"messaging_peer_priority"`
	Store                 StoreConfig   `mapstructure:"store"`
}

var _ component.Config = (*Config)(nil)

func (cfg *Config) Validate() error {
	if cfg.Store.MaxItems < 1 {
		return errors.New("store.max_items must be greater than 0")
	}
	if cfg.Store.TTL <= 0 {
		return errors.New("store.ttl must be greater than 0")
	}
	return nil
}

func createDefaultConfig() component.Config {
	return &Config{
		Enabled: true,
		DBPeerPriority: []string{
			"db.name", "db.instance", "db.system", "db.type", "server.address",
		},
		MessagingPeerPriority: []string{
			"messaging.destination.name", "messaging.destination", "messaging.system",
		},
		Store: StoreConfig{
			MaxItems: 10000,
			TTL:      10 * time.Second,
		},
	}
}
