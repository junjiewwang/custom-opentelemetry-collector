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

	// L1: HTTP 入口
	root := tdg.HTTPServerCase(s.serviceName, "GET", "/api/v1/products", 8080)
	tdg.WithAttributes(root, map[string]string{"product.id": productID})

	// L2: 中间件层 - 鉴权
	authMiddleware := tdg.MiddlewareCase(s.serviceName, "auth", map[string]string{
		"auth.type": "api_key",
	})

	// L2: Controller 层 - handleGetProduct
	controller := tdg.ControllerCase(s.serviceName, "handleGetProduct", map[string]string{
		"product.id": productID,
	})

	// L3: Service 层 - ProductService.getProductById（Cache-Aside 模式）
	serviceMethod := tdg.ServiceMethodCase(s.serviceName, "ProductService.getProductById", map[string]string{
		"product.id": productID,
	})

	// L4: 缓存层 - CacheService.getProductCache → Redis GET
	cacheGet := tdg.ServiceMethodCase(s.serviceName, "CacheService.getProductCache", map[string]string{
		"cache.key": fmt.Sprintf("product:cache:%s", productID),
	})
	cacheGet.Children = []*tdg.FlowStep{
		tdg.RedisCase(s.serviceName, "GET",
			fmt.Sprintf("GET product:cache:%s", productID)),
	}

	// L4: 缓存未命中 - Repository 回源 → MySQL
	repoFind := tdg.RepositoryCase(s.serviceName, "ProductRepository.findById", map[string]string{
		"cache.hit": "false",
	})
	repoFind.Children = []*tdg.FlowStep{
		tdg.MySQLCase(s.serviceName, "product_db", "SELECT", "products",
			fmt.Sprintf("SELECT id, name, price, description, stock FROM products WHERE id = '%s'", productID)),
	}

	// L4: 回填缓存 - CacheService.setProductCache → Redis SET
	cacheSet := tdg.ServiceMethodCase(s.serviceName, "CacheService.setProductCache", nil)
	cacheSet.Children = []*tdg.FlowStep{
		tdg.RedisCase(s.serviceName, "SET",
			fmt.Sprintf("SET product:cache:%s {json} EX 1800", productID)),
	}

	// 组装 Service 层子步骤
	serviceMethod.Children = []*tdg.FlowStep{
		cacheGet,
		repoFind,
		cacheSet,
	}

	// L3: Service 层 - RecommendService.recordView（记录热门浏览）
	recommendService := tdg.ServiceMethodCase(s.serviceName, "RecommendService.recordView", map[string]string{
		"product.id": productID,
	})
	recommendService.Children = []*tdg.FlowStep{
		tdg.RedisCase(s.serviceName, "ZINCRBY",
			fmt.Sprintf("ZINCRBY product:popular 1 %s", productID)),
		tdg.RedisCase(s.serviceName, "ZREVRANGE",
			"ZREVRANGE product:popular 0 9 WITHSCORES"),
	}

	// L3: 序列化
	buildResp := tdg.InternalCase(s.serviceName, "buildResponse", map[string]string{
		"product.id":      productID,
		"recommendations": "10",
	})

	// 组装 Controller 层子步骤
	controller.Children = []*tdg.FlowStep{
		serviceMethod,
		recommendService,
		buildResp,
	}

	// 组装根节点
	root.Children = []*tdg.FlowStep{
		authMiddleware,
		controller,
	}

	td := tdg.ExecuteFlow([]*tdg.FlowStep{root}, s.errorRate)
	return td, nil
}
