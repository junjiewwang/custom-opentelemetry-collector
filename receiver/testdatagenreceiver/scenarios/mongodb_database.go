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

	// L1: HTTP 入口
	root := tdg.HTTPServerCase(s.serviceName, "POST", "/api/v1/articles", 8080)
	tdg.WithAttributes(root, map[string]string{
		"article.id": articleID,
		"author.id":  authorID,
	})

	// L2: 中间件层 - 鉴权
	authMiddleware := tdg.MiddlewareCase(s.serviceName, "auth", map[string]string{
		"auth.type": "bearer_token",
	})

	// L2: Controller 层 - handleCreateArticle
	controller := tdg.ControllerCase(s.serviceName, "handleCreateArticle", map[string]string{
		"article.id": articleID,
		"author.id":  authorID,
	})

	// L3: 参数校验
	validateStep := tdg.InternalCase(s.serviceName, "validateArticle", map[string]string{
		"validation.type": "article_create",
	})

	// L3: Service 层 - ArticleService.publishArticle
	serviceMethod := tdg.ServiceMethodCase(s.serviceName, "ArticleService.publishArticle", map[string]string{
		"article.id": articleID,
		"author.id":  authorID,
	})

	// L4: Repository 层 - ArticleRepository.save → MongoDB insert
	repoSave := tdg.RepositoryCase(s.serviceName, "ArticleRepository.save", nil)
	repoSave.Children = []*tdg.FlowStep{
		tdg.MongoDBCase(s.serviceName, s.dbName, s.collection, "insert",
			fmt.Sprintf(`{"insert": "%s", "documents": [{"_id": "%s", "author_id": "%s", "title": "Article Title", "status": "published"}]}`, s.collection, articleID, authorID)),
	}

	// L4: Repository 层 - AuthorRepository.incrementArticleCount → MongoDB update
	repoUpdateAuthor := tdg.RepositoryCase(s.serviceName, "AuthorRepository.incrementArticleCount", nil)
	repoUpdateAuthor.Children = []*tdg.FlowStep{
		tdg.MongoDBCase(s.serviceName, s.dbName, "authors", "update",
			fmt.Sprintf(`{"update": "authors", "updates": [{"q": {"_id": "%s"}, "u": {"$inc": {"article_count": 1}}}]}`, authorID)),
	}

	// L4: Repository 层 - ArticleRepository.findById → MongoDB find（回查确认）
	repoFind := tdg.RepositoryCase(s.serviceName, "ArticleRepository.findById", nil)
	repoFind.Children = []*tdg.FlowStep{
		tdg.MongoDBCase(s.serviceName, s.dbName, s.collection, "find",
			fmt.Sprintf(`{"find": "%s", "filter": {"_id": "%s"}}`, s.collection, articleID)),
	}

	// L4: 缓存层 - CacheService.setArticleCache → Redis
	cacheSet := tdg.ServiceMethodCase(s.serviceName, "CacheService.setArticleCache", nil)
	cacheSet.Children = []*tdg.FlowStep{
		tdg.RedisCase(s.serviceName, "SET",
			fmt.Sprintf("SET article:cache:%s {json} EX 3600", articleID)),
		tdg.RedisCase(s.serviceName, "LPUSH",
			fmt.Sprintf("LPUSH author:%s:articles %s", authorID, articleID)),
	}

	// 组装 Service 层子步骤
	serviceMethod.Children = []*tdg.FlowStep{
		repoSave,
		repoUpdateAuthor,
		repoFind,
		cacheSet,
	}

	// L3: 序列化
	buildResp := tdg.InternalCase(s.serviceName, "buildResponse", map[string]string{
		"article.id": articleID,
	})

	// 组装 Controller 层子步骤
	controller.Children = []*tdg.FlowStep{
		validateStep,
		serviceMethod,
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
