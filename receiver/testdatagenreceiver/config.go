// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package testdatagenreceiver

import (
	"errors"
	"fmt"
	"time"
)

// Config 定义 TestDataGen Receiver 的配置
// 一个场景对应一个完整的 Collector 配置文件，通过 --config 参数指定
type Config struct {
	// Interval 数据生成间隔，默认 10s
	Interval time.Duration `mapstructure:"interval"`

	// Systems 业务系统声明（定义业务系统、应用及其归属关系）
	Systems []SystemConfig `mapstructure:"systems"`

	// Flows 声明式业务流列表（通过配置定义调用链拓扑）
	Flows []DeclarativeFlowConfig `mapstructure:"flows"`
}

// SystemConfig 业务系统配置
// 一个业务系统对应一个 Token，包含多个应用
type SystemConfig struct {
	// Name 业务系统名称
	Name string `mapstructure:"name"`

	// Namespace 业务系统命名空间，映射到 service.namespace 资源属性
	Namespace string `mapstructure:"namespace"`

	// Applications 该业务系统下的应用列表
	Applications []ApplicationConfig `mapstructure:"applications"`
}

// ApplicationConfig 应用配置
type ApplicationConfig struct {
	// Name 应用名称，映射到 service.name
	Name string `mapstructure:"name"`

	// Type 应用类型：http, grpc
	Type string `mapstructure:"type"`

	// Version 应用版本，默认 "1.0.0"
	Version string `mapstructure:"version"`
}

// DeclarativeFlowConfig 声明式业务流配置
// 通过 YAML 配置定义调用链拓扑，引擎自动生成 Trace 数据
type DeclarativeFlowConfig struct {
	// Name 业务流唯一名称
	Name string `mapstructure:"name"`

	// Enabled 是否启用
	Enabled bool `mapstructure:"enabled"`

	// Description 业务流描述
	Description string `mapstructure:"description"`

	// ErrorRate 全局错误率 (0.0 ~ 1.0)
	ErrorRate float64 `mapstructure:"error_rate"`

	// EntryPoint 入口配置
	EntryPoint *EntryPointConfig `mapstructure:"entry_point"`

	// Topology 调用拓扑（图的邻接表表示，支持 internal_steps）
	Topology map[string]*TopologyNode `mapstructure:"topology"`
}

// EntryPointConfig 入口点配置
type EntryPointConfig struct {
	// App 入口应用名称
	App string `mapstructure:"app"`

	// Protocol 入口协议：http, grpc
	Protocol string `mapstructure:"protocol"`

	// Method HTTP 方法（如 POST, GET）或 gRPC 方法
	Method string `mapstructure:"method"`

	// Route HTTP 路由（如 /api/v1/process）
	Route string `mapstructure:"route"`
}

// TopologyNode 拓扑节点（一个应用在拓扑中的完整描述）
type TopologyNode struct {
	// InternalSteps 应用内部行为步骤（按顺序执行）
	InternalSteps []InternalStepConfig `mapstructure:"internal_steps"`

	// Calls 该应用调用的下游列表
	Calls []TopologyEdge `mapstructure:"calls"`
}

// InternalStepConfig 应用内部行为步骤配置
type InternalStepConfig struct {
	// Type 步骤类型：middleware, service_method, repository, redis, mysql, mongodb, kafka_send, kafka_receive, internal
	Type string `mapstructure:"type"`

	// Name 步骤名称（middleware/internal 使用）
	Name string `mapstructure:"name"`

	// Method 方法名（service_method/repository 使用）
	Method string `mapstructure:"method"`

	// Operation 操作类型（redis: GET/SET, mysql: SELECT/INSERT/UPDATE/DELETE, mongodb: find/insert/update）
	Operation string `mapstructure:"operation"`

	// Database 数据库名称（mysql/mongodb 使用）
	Database string `mapstructure:"database"`

	// Table 表名（mysql 使用）
	Table string `mapstructure:"table"`

	// Collection 集合名（mongodb 使用）
	Collection string `mapstructure:"collection"`

	// KeyPattern Redis key 模式（redis 使用）
	KeyPattern string `mapstructure:"key_pattern"`

	// Topic 消息主题（kafka 使用）
	Topic string `mapstructure:"topic"`

	// Attributes 自定义属性
	Attributes map[string]string `mapstructure:"attributes"`
}

// TopologyEdge 拓扑边（调用关系）
type TopologyEdge struct {
	// Target 目标应用名称
	Target string `mapstructure:"target"`

	// Protocol 调用协议：grpc, http, kafka, rocketmq, rabbitmq
	Protocol string `mapstructure:"protocol"`

	// Service gRPC 服务名（protocol=grpc 时使用）
	Service string `mapstructure:"service"`

	// Method gRPC 方法名或 HTTP 方法
	Method string `mapstructure:"method"`

	// Topic 消息主题（protocol=kafka/rocketmq/rabbitmq 时使用）
	Topic string `mapstructure:"topic"`

	// Exchange 交换机名（protocol=rabbitmq 时使用）
	Exchange string `mapstructure:"exchange"`

	// RoutingKey 路由键（protocol=rabbitmq 时使用）
	RoutingKey string `mapstructure:"routing_key"`

	// ConsumerGroup 消费者组（kafka/rocketmq 使用）
	ConsumerGroup string `mapstructure:"consumer_group"`
}

// Validate 校验配置合法性
func (cfg *Config) Validate() error {
	if cfg.Interval <= 0 {
		return errors.New("interval must be positive")
	}

	if len(cfg.Systems) == 0 && len(cfg.Flows) == 0 {
		return errors.New("at least one system with flows must be configured")
	}

	// 校验业务系统
	appNameSet := make(map[string]string) // app name → system namespace
	for i, sys := range cfg.Systems {
		if sys.Name == "" {
			return fmt.Errorf("systems[%d]: name must not be empty", i)
		}
		if sys.Namespace == "" {
			return fmt.Errorf("systems[%d]: namespace must not be empty", i)
		}

		for j, app := range sys.Applications {
			if app.Name == "" {
				return fmt.Errorf("systems[%d].applications[%d]: name must not be empty", i, j)
			}
			if existingNs, exists := appNameSet[app.Name]; exists {
				return fmt.Errorf("systems[%d].applications[%d]: duplicate app name %q (already in namespace %q)", i, j, app.Name, existingNs)
			}
			appNameSet[app.Name] = sys.Namespace
		}
	}

	// 校验声明式业务流
	flowNameSet := make(map[string]struct{})
	for i, fl := range cfg.Flows {
		if fl.Name == "" {
			return fmt.Errorf("flows[%d]: name must not be empty", i)
		}
		if _, exists := flowNameSet[fl.Name]; exists {
			return fmt.Errorf("flows[%d]: duplicate flow name %q", i, fl.Name)
		}
		flowNameSet[fl.Name] = struct{}{}

		if fl.EntryPoint == nil {
			return fmt.Errorf("flows[%d] %q: entry_point must be configured", i, fl.Name)
		}
		if fl.EntryPoint.App == "" {
			return fmt.Errorf("flows[%d] %q: entry_point.app must not be empty", i, fl.Name)
		}

		if len(fl.Topology) == 0 {
			return fmt.Errorf("flows[%d] %q: topology must not be empty", i, fl.Name)
		}

		// 校验拓扑中的应用是否都已声明
		if len(cfg.Systems) > 0 {
			if _, exists := appNameSet[fl.EntryPoint.App]; !exists {
				return fmt.Errorf("flows[%d] %q: entry_point.app %q not found in any system", i, fl.Name, fl.EntryPoint.App)
			}
			for appName, node := range fl.Topology {
				if _, exists := appNameSet[appName]; !exists {
					return fmt.Errorf("flows[%d] %q: topology references unknown app %q", i, fl.Name, appName)
				}
				if node != nil {
					for _, edge := range node.Calls {
						if _, exists := appNameSet[edge.Target]; !exists {
							return fmt.Errorf("flows[%d] %q: topology edge target %q not found in any system", i, fl.Name, edge.Target)
						}
					}
				}
			}
		}
	}

	return nil
}

// GetAppNamespace 根据应用名称获取其所属的业务系统 namespace
func (cfg *Config) GetAppNamespace(appName string) string {
	for _, sys := range cfg.Systems {
		for _, app := range sys.Applications {
			if app.Name == appName {
				return sys.Namespace
			}
		}
	}
	return ""
}

// GetAppConfig 根据应用名称获取应用配置
func (cfg *Config) GetAppConfig(appName string) *ApplicationConfig {
	for _, sys := range cfg.Systems {
		for i, app := range sys.Applications {
			if app.Name == appName {
				return &sys.Applications[i]
			}
		}
	}
	return nil
}