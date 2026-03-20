// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package scenarios

import (
	"fmt"
	"math/rand"
	"time"

	"go.opentelemetry.io/collector/pdata/ptrace"

	tdg "go.opentelemetry.io/collector/custom/receiver/testdatagenreceiver"
)

const basicTraceName = "basic_trace"

func init() {
	tdg.Register(basicTraceName, func() tdg.Scenario {
		return &BasicTraceScenario{}
	})
}

// BasicTraceScenario 基础链路场景
// 模拟一个典型的用户服务查询请求：
//
//	HTTP GET /api/users/:id → validateRequest → MySQL SELECT → Redis SET(缓存) → serializeResponse
type BasicTraceScenario struct {
	tdg.BaseScenario

	serviceName string
	errorRate   float64
}

func (s *BasicTraceScenario) Name() string        { return basicTraceName }
func (s *BasicTraceScenario) Type() tdg.DataType   { return tdg.DataTypeTraces }

func (s *BasicTraceScenario) Init(cfg map[string]interface{}) error {
	s.serviceName = tdg.ParseString(cfg, "service_name", "user-service")
	s.errorRate = tdg.ParseFloat64(cfg, "error_rate", 0.0)
	return nil
}

func (s *BasicTraceScenario) GenerateTraces() (ptrace.Traces, error) {
	r := rand.New(rand.NewSource(time.Now().UnixNano()))
	userID := fmt.Sprintf("USR-%d", 10000+r.Intn(90000))

	// 构建调用树：HTTP 入口 → 内部校验 → MySQL 查询 → Redis 缓存 → 序列化
	root := tdg.HTTPServerCase(s.serviceName, "GET", "/api/v1/users", 8080)
	root.Children = []*tdg.FlowStep{
		tdg.InternalCase(s.serviceName, "validateRequest", map[string]string{
			"validation.type": "request_params",
		}),
		tdg.MySQLCase(s.serviceName, "user_db", "SELECT", "users",
			fmt.Sprintf("SELECT id, name, email, avatar FROM users WHERE id = '%s'", userID)),
		tdg.RedisCase(s.serviceName, "SET",
			fmt.Sprintf("SET user:cache:%s {json} EX 300", userID)),
		tdg.InternalCase(s.serviceName, "serializeResponse", map[string]string{
			"serializer": "json",
			"user.id":    userID,
		}),
	}

	td := tdg.ExecuteFlow([]*tdg.FlowStep{root}, s.errorRate)
	return td, nil
}
