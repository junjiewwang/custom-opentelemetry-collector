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

	// L1: HTTP 入口
	root := tdg.HTTPServerCase(s.serviceName, "GET", "/api/v1/users", 8080)

	// L2: 中间件层 - 鉴权
	authMiddleware := tdg.MiddlewareCase(s.serviceName, "auth", map[string]string{
		"auth.type": "bearer_token",
	})

	// L2: Controller 层 - handleGetUser
	controller := tdg.ControllerCase(s.serviceName, "handleGetUser", map[string]string{
		"user.id": userID,
	})

	// L3: Service 层 - UserService.getUserById
	serviceMethod := tdg.ServiceMethodCase(s.serviceName, "UserService.getUserById", map[string]string{
		"user.id": userID,
	})

	// L4: Repository 层 - UserRepository.findById → MySQL
	repoFind := tdg.RepositoryCase(s.serviceName, "UserRepository.findById", nil)
	repoFind.Children = []*tdg.FlowStep{
		tdg.MySQLCase(s.serviceName, "user_db", "SELECT", "users",
			fmt.Sprintf("SELECT id, name, email, avatar FROM users WHERE id = '%s'", userID)),
	}

	// L4: 缓存层 - CacheService.setUserCache → Redis
	cacheSet := tdg.ServiceMethodCase(s.serviceName, "CacheService.setUserCache", map[string]string{
		"cache.key": fmt.Sprintf("user:cache:%s", userID),
	})
	cacheSet.Children = []*tdg.FlowStep{
		tdg.RedisCase(s.serviceName, "SET",
			fmt.Sprintf("SET user:cache:%s {json} EX 300", userID)),
	}

	// 组装 Service 层子步骤
	serviceMethod.Children = []*tdg.FlowStep{
		repoFind,
		cacheSet,
	}

	// L2: 序列化响应
	serializeResp := tdg.InternalCase(s.serviceName, "serializeResponse", map[string]string{
		"serializer": "json",
		"user.id":    userID,
	})

	// 组装 Controller 层子步骤
	controller.Children = []*tdg.FlowStep{
		tdg.InternalCase(s.serviceName, "validateRequest", map[string]string{
			"validation.type": "request_params",
		}),
		serviceMethod,
		serializeResp,
	}

	// 组装根节点
	root.Children = []*tdg.FlowStep{
		authMiddleware,
		controller,
	}

	td := tdg.ExecuteFlow([]*tdg.FlowStep{root}, s.errorRate)
	return td, nil
}
