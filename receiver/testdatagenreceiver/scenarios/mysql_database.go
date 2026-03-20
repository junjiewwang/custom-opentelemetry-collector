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

	// 构建调用树：HTTP 入口 → 校验 → MySQL 查重 → MySQL 插入 → Redis 缓存
	root := tdg.HTTPServerCase(s.serviceName, "POST", "/api/v1/users", 8080)
	tdg.WithAttributes(root, map[string]string{
		"user.id": userID,
	})

	root.Children = []*tdg.FlowStep{
		tdg.InternalCase(s.serviceName, "validateUserInput", map[string]string{
			"validation.type": "user_create",
		}),
		tdg.MySQLCase(s.serviceName, s.dbName, "SELECT", s.table,
			fmt.Sprintf("SELECT id FROM %s WHERE email = '%s' LIMIT 1", s.table, email)),
		tdg.MySQLCase(s.serviceName, s.dbName, "INSERT", s.table,
			fmt.Sprintf("INSERT INTO %s (id, name, email, created_at) VALUES ('%s', '%s', '%s', NOW())", s.table, userID, userName, email)),
		tdg.MySQLCase(s.serviceName, s.dbName, "SELECT", s.table,
			fmt.Sprintf("SELECT id, name, email, avatar, status FROM %s WHERE id = '%s'", s.table, userID)),
		tdg.RedisCase(s.serviceName, "SET",
			fmt.Sprintf("SET user:cache:%s {json} EX 3600", userID)),
		tdg.InternalCase(s.serviceName, "serializeResponse", map[string]string{
			"serializer": "json",
			"user.id":    userID,
		}),
	}

	td := tdg.ExecuteFlow([]*tdg.FlowStep{root}, s.errorRate)
	return td, nil
}
