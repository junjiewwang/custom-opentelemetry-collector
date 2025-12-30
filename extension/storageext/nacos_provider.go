// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package storageext

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/nacos-group/nacos-sdk-go/v2/clients"
	"github.com/nacos-group/nacos-sdk-go/v2/clients/config_client"
	"github.com/nacos-group/nacos-sdk-go/v2/clients/naming_client"
	"github.com/nacos-group/nacos-sdk-go/v2/common/constant"
	"github.com/nacos-group/nacos-sdk-go/v2/vo"
)

// createNacosClients creates Nacos config and naming clients based on configuration.
func (e *Extension) createNacosClients(cfg NacosConfig) (config_client.IConfigClient, naming_client.INamingClient, error) {
	// Parse server address
	serverConfigs, err := parseNacosServerAddr(cfg.ServerAddr)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to parse server address: %w", err)
	}

	// Set up directories
	logDir := cfg.LogDir
	if logDir == "" {
		logDir = filepath.Join(os.TempDir(), "nacos", "log")
	}

	cacheDir := cfg.CacheDir
	if cacheDir == "" {
		cacheDir = filepath.Join(os.TempDir(), "nacos", "cache")
	}

	logLevel := cfg.LogLevel
	if logLevel == "" {
		logLevel = "warn"
	}

	// Create client config
	clientConfig := constant.ClientConfig{
		NamespaceId:         cfg.Namespace,
		TimeoutMs:           uint64(cfg.Timeout.Milliseconds()),
		NotLoadCacheAtStart: true,
		LogDir:              logDir,
		CacheDir:            cacheDir,
		LogLevel:            logLevel,
	}

	if cfg.Username != "" {
		clientConfig.Username = cfg.Username
		clientConfig.Password = cfg.Password
	}

	// Create config client
	configClient, err := clients.NewConfigClient(
		vo.NacosClientParam{
			ClientConfig:  &clientConfig,
			ServerConfigs: serverConfigs,
		},
	)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to create config client: %w", err)
	}

	// Create naming client
	namingClient, err := clients.NewNamingClient(
		vo.NacosClientParam{
			ClientConfig:  &clientConfig,
			ServerConfigs: serverConfigs,
		},
	)
	if err != nil {
		configClient.CloseClient()
		return nil, nil, fmt.Errorf("failed to create naming client: %w", err)
	}

	return configClient, namingClient, nil
}

// parseNacosServerAddr parses the server address string into server configs.
// Supports formats:
// - "host:port"
// - "host1:port1,host2:port2"
func parseNacosServerAddr(addr string) ([]constant.ServerConfig, error) {
	var configs []constant.ServerConfig

	parts := strings.Split(addr, ",")
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}

		hostPort := strings.Split(part, ":")
		if len(hostPort) != 2 {
			return nil, fmt.Errorf("invalid server address format: %s", part)
		}

		port, err := strconv.ParseUint(hostPort[1], 10, 64)
		if err != nil {
			return nil, fmt.Errorf("invalid port number: %s", hostPort[1])
		}

		configs = append(configs, constant.ServerConfig{
			IpAddr: hostPort[0],
			Port:   port,
		})
	}

	if len(configs) == 0 {
		return nil, fmt.Errorf("no valid server address found")
	}

	return configs, nil
}
