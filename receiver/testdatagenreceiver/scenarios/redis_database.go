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

const redisDatabaseName = "redis_database"

func init() {
	tdg.Register(redisDatabaseName, func() tdg.Scenario {
		return &RedisDatabaseScenario{}
	})
}

// RedisDatabaseScenario Redis 数据库场景
// 模拟缓存服务的典型操作流程：
//
//	HTTP GET /api/v1/products/:id → Redis GET(查缓存) → MySQL SELECT(缓存未命中) → Redis SET(回填缓存)
type RedisDatabaseScenario struct {
	tdg.BaseScenario

	serviceName string
	errorRate   float64
}

func (s *RedisDatabaseScenario) Name() string      { return redisDatabaseName }
func (s *RedisDatabaseScenario) Type() tdg.DataType { return tdg.DataTypeTraces }

func (s *RedisDatabaseScenario) Init(cfg map[string]interface{}) error {
	s.serviceName = tdg.ParseString(cfg, "service_name", "product-service")
	s.errorRate = tdg.ParseFloat64(cfg, "error_rate", 0.02)
	return nil
}

func (s *RedisDatabaseScenario) GenerateTraces() (ptrace.Traces, error) {
	r := rand.New(rand.NewSource(time.Now().UnixNano()))
	productID := fmt.Sprintf("PROD-%d", 1000+r.Intn(9000))

	// 构建调用树：HTTP 入口 → Redis 查缓存 → MySQL 查库 → Redis 回填缓存
	root := tdg.HTTPServerCase(s.serviceName, "GET", "/api/v1/products", 8080)
	tdg.WithAttributes(root, map[string]string{
		"product.id": productID,
	})

	root.Children = []*tdg.FlowStep{
		// 先查 Redis 缓存
		tdg.RedisCase(s.serviceName, "GET",
			fmt.Sprintf("GET product:cache:%s", productID)),
		// 缓存未命中，查 MySQL
		tdg.InternalCase(s.serviceName, "cacheMissHandler", map[string]string{
			"cache.hit": "false",
		}),
		tdg.MySQLCase(s.serviceName, "product_db", "SELECT", "products",
			fmt.Sprintf("SELECT id, name, price, description, stock FROM products WHERE id = '%s'", productID)),
		// 回填缓存
		tdg.RedisCase(s.serviceName, "SET",
			fmt.Sprintf("SET product:cache:%s {json} EX 1800", productID)),
		// 查热门推荐（利用 Redis sorted set）
		tdg.RedisCase(s.serviceName, "ZINCRBY",
			fmt.Sprintf("ZINCRBY product:popular 1 %s", productID)),
		tdg.RedisCase(s.serviceName, "ZREVRANGE",
			"ZREVRANGE product:popular 0 9 WITHSCORES"),
		tdg.InternalCase(s.serviceName, "buildResponse", map[string]string{
			"product.id":       productID,
			"recommendations":  "10",
		}),
	}

	td := tdg.ExecuteFlow([]*tdg.FlowStep{root}, s.errorRate)
	return td, nil
}
