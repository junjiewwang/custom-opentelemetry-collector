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

const mysqlDatabaseName = "mysql_database"

func init() {
	tdg.Register(mysqlDatabaseName, func() tdg.Scenario {
		return &MySQLDatabaseScenario{}
	})
}

// MySQLDatabaseScenario MySQL 数据库场景
// 模拟用户管理服务的 CRUD 操作：
//
//	HTTP POST /api/v1/users → 参数校验 → MySQL SELECT(查重) → MySQL INSERT(创建) → Redis SET(缓存) → 返回
type MySQLDatabaseScenario struct {
	tdg.BaseScenario

	serviceName string
	dbName      string
	table       string
	errorRate   float64
}

func (s *MySQLDatabaseScenario) Name() string      { return mysqlDatabaseName }
func (s *MySQLDatabaseScenario) Type() tdg.DataType { return tdg.DataTypeTraces }

func (s *MySQLDatabaseScenario) Init(cfg map[string]interface{}) error {
	s.serviceName = tdg.ParseString(cfg, "service_name", "user-service")
	s.dbName = tdg.ParseString(cfg, "db_name", "user_db")
	s.table = tdg.ParseString(cfg, "table", "users")
	s.errorRate = tdg.ParseFloat64(cfg, "error_rate", 0.05)
	return nil
}

func (s *MySQLDatabaseScenario) GenerateTraces() (ptrace.Traces, error) {
	r := rand.New(rand.NewSource(time.Now().UnixNano()))
	userID := fmt.Sprintf("USR-%d", 10000+r.Intn(90000))
	userName := fmt.Sprintf("user_%s", tdg.RandomPick([]string{"alice", "bob", "charlie", "diana", "evan"}))
	email := fmt.Sprintf("%s@example.com", userName)

	// L1: HTTP 入口
	root := tdg.HTTPServerCase(s.serviceName, "POST", "/api/v1/users", 8080)
	tdg.WithAttributes(root, map[string]string{"user.id": userID})

	// L2: 中间件层 - 鉴权
	authMiddleware := tdg.MiddlewareCase(s.serviceName, "auth", map[string]string{
		"auth.type": "bearer_token",
	})

	// L2: Controller 层 - handleCreateUser
	controller := tdg.ControllerCase(s.serviceName, "handleCreateUser", map[string]string{
		"user.id": userID,
	})

	// L3: 参数校验
	validateStep := tdg.InternalCase(s.serviceName, "validateUserInput", map[string]string{
		"validation.type": "user_create",
	})

	// L3: Service 层 - UserService.createUser
	serviceMethod := tdg.ServiceMethodCase(s.serviceName, "UserService.createUser", map[string]string{
		"user.id":   userID,
		"user.name": userName,
	})

	// L4: Repository 层 - UserRepository.checkDuplicate → MySQL
	repoCheckDup := tdg.RepositoryCase(s.serviceName, "UserRepository.findByEmail", nil)
	repoCheckDup.Children = []*tdg.FlowStep{
		tdg.MySQLCase(s.serviceName, s.dbName, "SELECT", s.table,
			fmt.Sprintf("SELECT id FROM %s WHERE email = '%s' LIMIT 1", s.table, email)),
	}

	// L4: Repository 层 - UserRepository.save → MySQL INSERT
	repoSave := tdg.RepositoryCase(s.serviceName, "UserRepository.save", nil)
	repoSave.Children = []*tdg.FlowStep{
		tdg.MySQLCase(s.serviceName, s.dbName, "INSERT", s.table,
			fmt.Sprintf("INSERT INTO %s (id, name, email, created_at) VALUES ('%s', '%s', '%s', NOW())", s.table, userID, userName, email)),
	}

	// L4: Repository 层 - UserRepository.findById → MySQL SELECT
	repoFind := tdg.RepositoryCase(s.serviceName, "UserRepository.findById", nil)
	repoFind.Children = []*tdg.FlowStep{
		tdg.MySQLCase(s.serviceName, s.dbName, "SELECT", s.table,
			fmt.Sprintf("SELECT id, name, email, avatar, status FROM %s WHERE id = '%s'", s.table, userID)),
	}

	// L4: 缓存层 - CacheService.setUserCache → Redis
	cacheSet := tdg.ServiceMethodCase(s.serviceName, "CacheService.setUserCache", nil)
	cacheSet.Children = []*tdg.FlowStep{
		tdg.RedisCase(s.serviceName, "SET",
			fmt.Sprintf("SET user:cache:%s {json} EX 3600", userID)),
	}

	// 组装 Service 层子步骤
	serviceMethod.Children = []*tdg.FlowStep{
		repoCheckDup,
		repoSave,
		repoFind,
		cacheSet,
	}

	// L3: 序列化响应
	serializeResp := tdg.InternalCase(s.serviceName, "serializeResponse", map[string]string{
		"serializer": "json",
		"user.id":    userID,
	})

	// 组装 Controller 层子步骤
	controller.Children = []*tdg.FlowStep{
		validateStep,
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
