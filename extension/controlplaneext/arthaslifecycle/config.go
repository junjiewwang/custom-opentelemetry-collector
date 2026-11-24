// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package arthaslifecycle

import (
	"errors"
	"time"
)

// Config controls Control Plane owned Arthas idle reclaim.
type Config struct {
	Enabled          bool          `mapstructure:"enabled"`
	IdleThreshold    time.Duration `mapstructure:"idle_threshold"`
	SweepInterval    time.Duration `mapstructure:"sweep_interval"`
	MinAttachAge     time.Duration `mapstructure:"min_attach_age"`
	Cooldown         time.Duration `mapstructure:"cooldown"`
	TaskTimeout      time.Duration `mapstructure:"task_timeout"`
	MaxTasksPerSweep int           `mapstructure:"max_tasks_per_sweep"`
}

// DefaultConfig returns conservative defaults, matching the retired
// arthas_tunnel.auto_detach behaviour.
func DefaultConfig() Config {
	return Config{
		Enabled:          true,
		IdleThreshold:    15 * time.Minute,
		SweepInterval:    90 * time.Second,
		MinAttachAge:     10 * time.Minute,
		Cooldown:         10 * time.Minute,
		TaskTimeout:      60 * time.Second,
		MaxTasksPerSweep: 200,
	}
}

// Validate rejects configurations that would make the sweeper ineffective.
func (c Config) Validate() error {
	if !c.Enabled {
		return nil
	}
	if c.IdleThreshold <= 0 {
		return errors.New("arthas_lifecycle.idle_threshold must be positive")
	}
	if c.SweepInterval <= 0 {
		return errors.New("arthas_lifecycle.sweep_interval must be positive")
	}
	if c.TaskTimeout <= 0 {
		return errors.New("arthas_lifecycle.task_timeout must be positive")
	}
	if c.MaxTasksPerSweep <= 0 {
		return errors.New("arthas_lifecycle.max_tasks_per_sweep must be positive")
	}
	return nil
}
