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

const mongodbDatabaseName = "mongodb_database"

func init() {
	tdg.Register(mongodbDatabaseName, func() tdg.Scenario {
		return &MongoDBDatabaseScenario{}
	})
}

// MongoDBDatabaseScenario MongoDB 数据库场景
// 模拟内容管理服务的文章操作流程：
//
//	HTTP POST /api/v1/articles → MongoDB insert(articles) → MongoDB update(authors) → Redis SET(缓存)
type MongoDBDatabaseScenario struct {
	tdg.BaseScenario

	serviceName string
	dbName      string
	collection  string
	errorRate   float64
}

func (s *MongoDBDatabaseScenario) Name() string      { return mongodbDatabaseName }
func (s *MongoDBDatabaseScenario) Type() tdg.DataType { return tdg.DataTypeTraces }

func (s *MongoDBDatabaseScenario) Init(cfg map[string]interface{}) error {
	s.serviceName = tdg.ParseString(cfg, "service_name", "content-service")
	s.dbName = tdg.ParseString(cfg, "db_name", "content_db")
	s.collection = tdg.ParseString(cfg, "collection", "articles")
	s.errorRate = tdg.ParseFloat64(cfg, "error_rate", 0.03)
	return nil
}

func (s *MongoDBDatabaseScenario) GenerateTraces() (ptrace.Traces, error) {
	r := rand.New(rand.NewSource(time.Now().UnixNano()))
	articleID := fmt.Sprintf("ART-%d", 10000+r.Intn(90000))
	authorID := fmt.Sprintf("AUTH-%d", 1000+r.Intn(9000))

	// 构建调用树：HTTP 入口 → 校验 → MongoDB 插入文章 → MongoDB 更新作者统计 → Redis 缓存
	root := tdg.HTTPServerCase(s.serviceName, "POST", "/api/v1/articles", 8080)
	tdg.WithAttributes(root, map[string]string{
		"article.id": articleID,
		"author.id":  authorID,
	})

	root.Children = []*tdg.FlowStep{
		tdg.InternalCase(s.serviceName, "validateArticle", map[string]string{
			"validation.type": "article_create",
		}),
		tdg.MongoDBCase(s.serviceName, s.dbName, s.collection, "insert",
			fmt.Sprintf(`{"insert": "%s", "documents": [{"_id": "%s", "author_id": "%s", "title": "Article Title", "status": "published"}]}`, s.collection, articleID, authorID)),
		tdg.MongoDBCase(s.serviceName, s.dbName, "authors", "update",
			fmt.Sprintf(`{"update": "authors", "updates": [{"q": {"_id": "%s"}, "u": {"$inc": {"article_count": 1}}}]}`, authorID)),
		tdg.MongoDBCase(s.serviceName, s.dbName, s.collection, "find",
			fmt.Sprintf(`{"find": "%s", "filter": {"_id": "%s"}}`, s.collection, articleID)),
		tdg.RedisCase(s.serviceName, "SET",
			fmt.Sprintf("SET article:cache:%s {json} EX 3600", articleID)),
		tdg.RedisCase(s.serviceName, "LPUSH",
			fmt.Sprintf("LPUSH author:%s:articles %s", authorID, articleID)),
		tdg.InternalCase(s.serviceName, "buildResponse", map[string]string{
			"article.id": articleID,
		}),
	}

	td := tdg.ExecuteFlow([]*tdg.FlowStep{root}, s.errorRate)
	return td, nil
}
