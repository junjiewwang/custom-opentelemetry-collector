// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package testdatagenreceiver

import (
	"errors"
	"fmt"
	"time"
)

// Config 定义 TestDataGen Receiver 的配置
type Config struct {
	// Interval 数据生成间隔，默认 10s
	Interval time.Duration `mapstructure:"interval"`

	// Scenarios 场景列表（单独场景模式，各场景独立生成数据）
	Scenarios []ScenarioConfig `mapstructure:"scenarios"`

	// Flows 业务流列表（业务流模式，生成跨多服务的完整调用链 Trace）
	Flows []FlowConfig `mapstructure:"flows"`
}

// FlowConfig 单个业务流的配置
type FlowConfig struct {
	// Name 业务流名称，需与注册中心的名称一致
	Name string `mapstructure:"name"`

	// Enabled 是否启用此业务流
	Enabled bool `mapstructure:"enabled"`

	// Config 业务流特有配置
	Config map[string]interface{} `mapstructure:"config"`
}

// ScenarioConfig 单个场景的配置
type ScenarioConfig struct {
	// Name 场景名称，需与注册中心的名称一致
	Name string `mapstructure:"name"`

	// Enabled 是否启用此场景
	Enabled bool `mapstructure:"enabled"`

	// Config 场景特有配置（键值对），由各场景自行解析
	Config map[string]interface{} `mapstructure:"config"`
}

// Validate 校验配置合法性
func (cfg *Config) Validate() error {
	if cfg.Interval <= 0 {
		return errors.New("interval must be positive")
	}
	if len(cfg.Scenarios) == 0 && len(cfg.Flows) == 0 {
		return errors.New("at least one scenario or flow must be configured")
	}

	nameSet := make(map[string]struct{})
	for i, sc := range cfg.Scenarios {
		if sc.Name == "" {
			return fmt.Errorf("scenario[%d]: name must not be empty", i)
		}
		if _, exists := nameSet[sc.Name]; exists {
			return fmt.Errorf("scenario[%d]: duplicate name %q", i, sc.Name)
		}
		nameSet[sc.Name] = struct{}{}
	}

	flowNameSet := make(map[string]struct{})
	for i, fl := range cfg.Flows {
		if fl.Name == "" {
			return fmt.Errorf("flow[%d]: name must not be empty", i)
		}
		if _, exists := flowNameSet[fl.Name]; exists {
			return fmt.Errorf("flow[%d]: duplicate name %q", i, fl.Name)
		}
		flowNameSet[fl.Name] = struct{}{}
	}
	return nil
}
