// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package storageext

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"go.opentelemetry.io/collector/custom/extension/storageext/blobstore"
)

// Config defines the configuration for the storage extension.
type Config struct {
	// Redis holds named Redis connection configurations.
	Redis map[string]RedisConfig `mapstructure:"redis"`

	// Nacos holds named Nacos client configurations.
	Nacos map[string]NacosConfig `mapstructure:"nacos"`

	// BlobStore holds the blob store configuration.
	BlobStore blobstore.Config `mapstructure:"blob_store"`
}

// RedisConfig holds Redis connection configuration.
type RedisConfig struct {
	// Standalone mode
	Addr string `mapstructure:"addr"`

	// Cluster mode
	Addrs []string `mapstructure:"addrs"`

	// Sentinel mode
	MasterName    string   `mapstructure:"master_name"`
	SentinelAddrs []string `mapstructure:"sentinel_addrs"`

	// Authentication
	Password string `mapstructure:"password"`

	// Database number (standalone mode only)
	DB int `mapstructure:"db"`

	// Connection pool settings
	PoolSize     int           `mapstructure:"pool_size"`
	DialTimeout  time.Duration `mapstructure:"dial_timeout"`
	ReadTimeout  time.Duration `mapstructure:"read_timeout"`
	WriteTimeout time.Duration `mapstructure:"write_timeout"`
}

// NacosConfig holds Nacos client configuration.
type NacosConfig struct {
	// ServerAddr is the Nacos server address (e.g., "nacos:8848")
	ServerAddr string `mapstructure:"server_addr"`

	// Namespace is the Nacos namespace ID
	Namespace string `mapstructure:"namespace"`

	// Authentication
	Username string `mapstructure:"username"`
	Password string `mapstructure:"password"`

	// Timeout for requests
	Timeout time.Duration `mapstructure:"timeout"`

	// LogDir is the directory for Nacos SDK logs
	LogDir string `mapstructure:"log_dir"`

	// CacheDir is the directory for Nacos SDK cache
	CacheDir string `mapstructure:"cache_dir"`

	// LogLevel is the log level for Nacos SDK (debug, info, warn, error)
	LogLevel string `mapstructure:"log_level"`
}

// ApplyDefaults sets reasonable default values and initializes nil maps.
// It is safe to call multiple times.
func (cfg *Config) ApplyDefaults() {
	if cfg == nil {
		return
	}
	if cfg.Redis == nil {
		cfg.Redis = make(map[string]RedisConfig)
	}
	if cfg.Nacos == nil {
		cfg.Nacos = make(map[string]NacosConfig)
	}

	for name := range cfg.Redis {
		rc := cfg.Redis[name]
		rc.ApplyDefaults()
		cfg.Redis[name] = rc
	}
	for name := range cfg.Nacos {
		nc := cfg.Nacos[name]
		nc.ApplyDefaults()
		cfg.Nacos[name] = nc
	}
}

// Validate checks if the configuration is valid.
func (cfg *Config) Validate() error {
	if cfg == nil {
		return errors.New("config is nil")
	}

	// Validate Redis configurations
	for name, redisCfg := range cfg.Redis {
		if err := redisCfg.Validate(); err != nil {
			return fmt.Errorf("redis.%s: %w", name, err)
		}
	}

	// Validate Nacos configurations
	for name, nacosCfg := range cfg.Nacos {
		if err := nacosCfg.Validate(); err != nil {
			return fmt.Errorf("nacos.%s: %w", name, err)
		}
	}

	// Validate BlobStore configuration
	if err := cfg.BlobStore.Validate(); err != nil {
		return fmt.Errorf("blob_store: %w", err)
	}

	return nil
}

// ApplyDefaults sets default values for RedisConfig.
func (cfg *RedisConfig) ApplyDefaults() {
	if cfg == nil {
		return
	}
	if cfg.PoolSize <= 0 {
		cfg.PoolSize = 10
	}
	if cfg.DialTimeout <= 0 {
		cfg.DialTimeout = 5 * time.Second
	}
	if cfg.ReadTimeout <= 0 {
		cfg.ReadTimeout = 3 * time.Second
	}
	if cfg.WriteTimeout <= 0 {
		cfg.WriteTimeout = 3 * time.Second
	}
}

// ApplyDefaults sets default values for NacosConfig.
func (cfg *NacosConfig) ApplyDefaults() {
	if cfg == nil {
		return
	}
	if cfg.LogDir == "" {
		cfg.LogDir = filepath.Join(os.TempDir(), "nacos", "log")
	}
	if cfg.CacheDir == "" {
		cfg.CacheDir = filepath.Join(os.TempDir(), "nacos", "cache")
	}
	if cfg.LogLevel == "" {
		cfg.LogLevel = "warn"
	}
}

// Validate checks if the Redis configuration is valid.
func (cfg *RedisConfig) Validate() error {
	hasStandalone := cfg.Addr != ""
	hasCluster := len(cfg.Addrs) > 0
	hasSentinel := cfg.MasterName != "" && len(cfg.SentinelAddrs) > 0

	count := 0
	if hasStandalone {
		count++
	}
	if hasCluster {
		count++
	}
	if hasSentinel {
		count++
	}

	if count == 0 {
		return errors.New("one of addr, addrs, or sentinel configuration is required")
	}
	if count > 1 {
		return errors.New("only one of addr, addrs, or sentinel configuration should be specified")
	}

	return nil
}

// Validate checks if the Nacos configuration is valid.
func (cfg *NacosConfig) Validate() error {
	if cfg.ServerAddr == "" {
		return errors.New("server_addr is required")
	}
	return nil
}

// createDefaultConfig creates the default configuration.
func createDefaultConfig() *Config {
	return &Config{
		Redis:     make(map[string]RedisConfig),
		Nacos:     make(map[string]NacosConfig),
		BlobStore: blobstore.DefaultConfig(),
	}
}
