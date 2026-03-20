// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package testdatagenreceiver

import "fmt"

// ============================================================================
// SpanCase 可复用原子操作用例
//
// 每个 Case 是一个工厂函数，返回 *FlowStep，封装了完整的 OTel 语义属性。
// 业务流和独立场景都可以通过组合 Case + ExecuteFlow 来生成逼真的 Trace。
//
// 使用示例：
//
//	root := HTTPServerCase("api-gateway", "POST", "/api/v1/orders", 443)
//	root.Children = []*FlowStep{
//	    GRPCCallCase("api-gateway", "order-service", "OrderService", "CreateOrder", 50051),
//	}
//	td := ExecuteFlow([]*FlowStep{root}, 0.05)
// ============================================================================

// --------------------------------------------------------------------------
// HTTP
// --------------------------------------------------------------------------

// HTTPServerCase 创建一个 HTTP Server 入口 span（SpanKind=Server）
// 模拟接收到外部 HTTP 请求
func HTTPServerCase(service, method, route string, port int) *FlowStep {
	return &FlowStep{
		ServiceName:   service,
		SpanName:      fmt.Sprintf("HTTP %s %s", method, route),
		Kind:          SpanKindEntry,
		MinDurationMs: 30,
		MaxDurationMs: 200,
		ScopeName:     "io.opentelemetry.http-1.28",
		Attributes: map[string]string{
			"http.method":    method,
			"http.route":     route,
			"http.scheme":    "https",
			"http.flavor":    "1.1",
			"net.host.name":  fmt.Sprintf("%s.example.com", service),
			"http.user_agent": "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36",
		},
		IntAttributes: map[string]int64{
			"http.status_code": 200,
			"net.host.port":    int64(port),
		},
		ResourceAttributes: map[string]string{
			"service.version": "1.0.0",
		},
	}
}

// HTTPClientCase 创建一个 HTTP Client 出站 span（SpanKind=Client）
// 模拟调用外部 HTTP 服务
func HTTPClientCase(service, method, url, targetHost string, port int) *FlowStep {
	return &FlowStep{
		ServiceName:   service,
		SpanName:      fmt.Sprintf("HTTP %s", method),
		Kind:          SpanKindCall,
		MinDurationMs: 10,
		MaxDurationMs: 100,
		ScopeName:     "io.opentelemetry.http-1.28",
		Attributes: map[string]string{
			"http.method":    method,
			"http.url":       url,
			"server.address": targetHost,
		},
		IntAttributes: map[string]int64{
			"http.status_code": 200,
			"server.port":      int64(port),
		},
	}
}

// --------------------------------------------------------------------------
// gRPC
// --------------------------------------------------------------------------

// GRPCCallCase 创建一对 gRPC 调用 span（client + server）
// 自动生成 caller 端的 Client span 和 callee 端的 Server span，形成跨服务调用对
// 返回的是 caller 端的 Client span，其 Children 中已包含 callee 端的 Server span
func GRPCCallCase(callerService, calleeService, rpcService, rpcMethod string, port int) *FlowStep {
	serverSpan := &FlowStep{
		ServiceName:   calleeService,
		SpanName:      fmt.Sprintf("grpc.server %s/%s", rpcService, rpcMethod),
		Kind:          SpanKindEntry,
		MinDurationMs: 5,
		MaxDurationMs: 50,
		ScopeName:     "io.opentelemetry.grpc-1.28",
		Attributes: map[string]string{
			"rpc.system":  "grpc",
			"rpc.service": rpcService,
			"rpc.method":  rpcMethod,
		},
		IntAttributes: map[string]int64{
			"rpc.grpc.status_code": 0,
		},
		ResourceAttributes: map[string]string{
			"service.version": "1.0.0",
		},
	}

	clientSpan := &FlowStep{
		ServiceName:   callerService,
		SpanName:      fmt.Sprintf("grpc.client %s/%s", rpcService, rpcMethod),
		Kind:          SpanKindCall,
		MinDurationMs: 8,
		MaxDurationMs: 60,
		ScopeName:     "io.opentelemetry.grpc-1.28",
		Attributes: map[string]string{
			"rpc.system":     "grpc",
			"rpc.service":    rpcService,
			"rpc.method":     rpcMethod,
			"server.address": fmt.Sprintf("%s.default.svc.cluster.local", calleeService),
		},
		IntAttributes: map[string]int64{
			"server.port":          int64(port),
			"rpc.grpc.status_code": 0,
		},
		Children: []*FlowStep{serverSpan},
	}

	return clientSpan
}

// GRPCServerCase 创建单独的 gRPC Server 入口 span（不包含 client 端）
// 适用于消费者侧入口等不需要 client span 的场景
func GRPCServerCase(service, rpcService, rpcMethod string) *FlowStep {
	return &FlowStep{
		ServiceName:   service,
		SpanName:      fmt.Sprintf("grpc.server %s/%s", rpcService, rpcMethod),
		Kind:          SpanKindEntry,
		MinDurationMs: 5,
		MaxDurationMs: 50,
		ScopeName:     "io.opentelemetry.grpc-1.28",
		Attributes: map[string]string{
			"rpc.system":  "grpc",
			"rpc.service": rpcService,
			"rpc.method":  rpcMethod,
		},
		IntAttributes: map[string]int64{
			"rpc.grpc.status_code": 0,
		},
		ResourceAttributes: map[string]string{
			"service.version": "1.0.0",
		},
	}
}

// --------------------------------------------------------------------------
// MySQL
// --------------------------------------------------------------------------

// MySQLCase 创建一个 MySQL 操作 span（SpanKind=Client）
// 遵循 OTel Database Semantic Conventions
func MySQLCase(service, db, operation, table, statement string) *FlowStep {
	return &FlowStep{
		ServiceName:   service,
		SpanName:      fmt.Sprintf("mysql %s", operation),
		Kind:          SpanKindCall,
		MinDurationMs: 2,
		MaxDurationMs: 20,
		ScopeName:     "io.opentelemetry.jdbc-1.28",
		Attributes: map[string]string{
			"db.system":      "mysql",
			"db.name":        db,
			"db.operation":   operation,
			"db.statement":   statement,
			"db.sql.table":   table,
			"server.address": "mysql-primary.default.svc.cluster.local",
			"db.user":        "app_svc",
		},
		IntAttributes: map[string]int64{
			"server.port": 3306,
		},
	}
}

// MySQLCaseWithAddr 创建一个 MySQL 操作 span，可指定服务器地址和端口
func MySQLCaseWithAddr(service, db, operation, table, statement, serverAddr string, port int) *FlowStep {
	step := MySQLCase(service, db, operation, table, statement)
	step.Attributes["server.address"] = serverAddr
	step.IntAttributes["server.port"] = int64(port)
	return step
}

// --------------------------------------------------------------------------
// Redis
// --------------------------------------------------------------------------

// RedisCase 创建一个 Redis 操作 span（SpanKind=Client）
// 遵循 OTel Database Semantic Conventions
func RedisCase(service, operation, statement string) *FlowStep {
	return &FlowStep{
		ServiceName:   service,
		SpanName:      fmt.Sprintf("redis %s", operation),
		Kind:          SpanKindCall,
		MinDurationMs: 1,
		MaxDurationMs: 5,
		ScopeName:     "io.opentelemetry.redis-1.28",
		Attributes: map[string]string{
			"db.system":      "redis",
			"db.operation":   operation,
			"db.statement":   statement,
			"server.address": "redis-cluster.default.svc.cluster.local",
		},
		IntAttributes: map[string]int64{
			"server.port":             6379,
			"db.redis.database_index": 0,
		},
	}
}

// RedisCaseWithAddr 创建一个 Redis 操作 span，可指定服务器地址和端口
func RedisCaseWithAddr(service, operation, statement, serverAddr string, port, dbIndex int) *FlowStep {
	step := RedisCase(service, operation, statement)
	step.Attributes["server.address"] = serverAddr
	step.IntAttributes["server.port"] = int64(port)
	step.IntAttributes["db.redis.database_index"] = int64(dbIndex)
	return step
}

// --------------------------------------------------------------------------
// MongoDB
// --------------------------------------------------------------------------

// MongoDBCase 创建一个 MongoDB 操作 span（SpanKind=Client）
// 遵循 OTel Database Semantic Conventions
func MongoDBCase(service, db, collection, operation, statement string) *FlowStep {
	return &FlowStep{
		ServiceName:   service,
		SpanName:      fmt.Sprintf("mongodb %s", operation),
		Kind:          SpanKindCall,
		MinDurationMs: 2,
		MaxDurationMs: 15,
		ScopeName:     "io.opentelemetry.mongodb-1.28",
		Attributes: map[string]string{
			"db.system":             "mongodb",
			"db.name":               db,
			"db.operation":          operation,
			"db.mongodb.collection": collection,
			"db.statement":          statement,
			"server.address":        "mongo-rs.default.svc.cluster.local",
		},
		IntAttributes: map[string]int64{
			"server.port": 27017,
		},
	}
}

// MongoDBCaseWithAddr 创建一个 MongoDB 操作 span，可指定服务器地址和端口
func MongoDBCaseWithAddr(service, db, collection, operation, statement, serverAddr string, port int) *FlowStep {
	step := MongoDBCase(service, db, collection, operation, statement)
	step.Attributes["server.address"] = serverAddr
	step.IntAttributes["server.port"] = int64(port)
	return step
}

// --------------------------------------------------------------------------
// Kafka
// --------------------------------------------------------------------------

// KafkaSendCase 创建一个 Kafka Producer span（SpanKind=Producer, IsAsync=true）
// 异步消息发送，consumer 端通过 SpanLink 关联
func KafkaSendCase(service, topic, clientID string) *FlowStep {
	return &FlowStep{
		ServiceName:   service,
		SpanName:      fmt.Sprintf("%s send", topic),
		Kind:          SpanKindProduce,
		MinDurationMs: 2,
		MaxDurationMs: 10,
		ScopeName:     "io.opentelemetry.kafka-1.28",
		IsAsync:       true,
		Attributes: map[string]string{
			"messaging.system":           "kafka",
			"messaging.destination.name": topic,
			"messaging.operation":        "send",
			"messaging.client_id":        clientID,
		},
	}
}

// KafkaReceiveCase 创建一个 Kafka Consumer span（SpanKind=Consumer）
func KafkaReceiveCase(service, topic, consumerGroup, clientID string) *FlowStep {
	return &FlowStep{
		ServiceName:   service,
		SpanName:      fmt.Sprintf("%s receive", topic),
		Kind:          SpanKindConsume,
		MinDurationMs: 3,
		MaxDurationMs: 15,
		ScopeName:     "io.opentelemetry.kafka-1.28",
		Attributes: map[string]string{
			"messaging.system":                "kafka",
			"messaging.destination.name":      topic,
			"messaging.operation":             "receive",
			"messaging.kafka.consumer.group":  consumerGroup,
			"messaging.client_id":             clientID,
		},
		ResourceAttributes: map[string]string{
			"service.version": "1.0.0",
		},
	}
}

// --------------------------------------------------------------------------
// RocketMQ
// --------------------------------------------------------------------------

// RocketMQSendCase 创建一个 RocketMQ Producer span（SpanKind=Producer, IsAsync=true）
func RocketMQSendCase(service, topic, clientGroup, messageType, tag string) *FlowStep {
	return &FlowStep{
		ServiceName:   service,
		SpanName:      fmt.Sprintf("%s send", topic),
		Kind:          SpanKindProduce,
		MinDurationMs: 2,
		MaxDurationMs: 10,
		ScopeName:     "io.opentelemetry.rocketmq-1.28",
		IsAsync:       true,
		Attributes: map[string]string{
			"messaging.system":                "rocketmq",
			"messaging.destination.name":      topic,
			"messaging.operation":             "send",
			"messaging.rocketmq.client_group": clientGroup,
			"messaging.rocketmq.message_type": messageType,
			"messaging.rocketmq.message_tag":  tag,
			"messaging.rocketmq.namespace":    "production",
		},
	}
}

// RocketMQReceiveCase 创建一个 RocketMQ Consumer span（SpanKind=Consumer）
func RocketMQReceiveCase(service, topic, clientGroup string) *FlowStep {
	return &FlowStep{
		ServiceName:   service,
		SpanName:      fmt.Sprintf("%s receive", topic),
		Kind:          SpanKindConsume,
		MinDurationMs: 3,
		MaxDurationMs: 15,
		ScopeName:     "io.opentelemetry.rocketmq-1.28",
		Attributes: map[string]string{
			"messaging.system":                "rocketmq",
			"messaging.destination.name":      topic,
			"messaging.operation":             "receive",
			"messaging.rocketmq.client_group": clientGroup,
		},
		ResourceAttributes: map[string]string{
			"service.version": "1.0.0",
		},
	}
}

// --------------------------------------------------------------------------
// RabbitMQ
// --------------------------------------------------------------------------

// RabbitMQSendCase 创建一个 RabbitMQ Producer span（SpanKind=Producer, IsAsync=true）
func RabbitMQSendCase(service, exchange, routingKey string) *FlowStep {
	return &FlowStep{
		ServiceName:   service,
		SpanName:      fmt.Sprintf("%s send", exchange),
		Kind:          SpanKindProduce,
		MinDurationMs: 2,
		MaxDurationMs: 10,
		ScopeName:     "io.opentelemetry.rabbitmq-1.28",
		IsAsync:       true,
		Attributes: map[string]string{
			"messaging.system":                               "rabbitmq",
			"messaging.destination.name":                     exchange,
			"messaging.operation":                            "send",
			"messaging.rabbitmq.destination.routing_key":     routingKey,
		},
	}
}

// RabbitMQReceiveCase 创建一个 RabbitMQ Consumer span（SpanKind=Consumer）
func RabbitMQReceiveCase(service, queue string) *FlowStep {
	return &FlowStep{
		ServiceName:   service,
		SpanName:      fmt.Sprintf("%s receive", queue),
		Kind:          SpanKindConsume,
		MinDurationMs: 3,
		MaxDurationMs: 15,
		ScopeName:     "io.opentelemetry.rabbitmq-1.28",
		Attributes: map[string]string{
			"messaging.system":           "rabbitmq",
			"messaging.destination.name": queue,
			"messaging.operation":        "receive",
		},
		ResourceAttributes: map[string]string{
			"service.version": "1.0.0",
		},
	}
}

// --------------------------------------------------------------------------
// Internal
// --------------------------------------------------------------------------

// InternalCase 创建一个内部处理步骤 span（SpanKind=Internal）
// 模拟服务内部的业务逻辑处理
func InternalCase(service, name string, attrs map[string]string) *FlowStep {
	step := &FlowStep{
		ServiceName:   service,
		SpanName:      name,
		Kind:          SpanKindInternal,
		MinDurationMs: 1,
		MaxDurationMs: 15,
		ScopeName:     "io.opentelemetry.auto",
		Attributes:    make(map[string]string),
	}
	for k, v := range attrs {
		step.Attributes[k] = v
	}
	return step
}

// --------------------------------------------------------------------------
// Helper: 设置额外属性到已创建的 FlowStep
// --------------------------------------------------------------------------

// WithAttributes 给 FlowStep 追加字符串属性
func WithAttributes(step *FlowStep, attrs map[string]string) *FlowStep {
	if step.Attributes == nil {
		step.Attributes = make(map[string]string)
	}
	for k, v := range attrs {
		step.Attributes[k] = v
	}
	return step
}

// WithIntAttributes 给 FlowStep 追加整数属性
func WithIntAttributes(step *FlowStep, attrs map[string]int64) *FlowStep {
	if step.IntAttributes == nil {
		step.IntAttributes = make(map[string]int64)
	}
	for k, v := range attrs {
		step.IntAttributes[k] = v
	}
	return step
}

// WithResourceAttributes 给 FlowStep 追加 Resource 属性
func WithResourceAttributes(step *FlowStep, attrs map[string]string) *FlowStep {
	if step.ResourceAttributes == nil {
		step.ResourceAttributes = make(map[string]string)
	}
	for k, v := range attrs {
		step.ResourceAttributes[k] = v
	}
	return step
}

// WithDuration 设置 FlowStep 的耗时范围
func WithDuration(step *FlowStep, minMs, maxMs int) *FlowStep {
	step.MinDurationMs = minMs
	step.MaxDurationMs = maxMs
	return step
}

// WithErrorRate 设置 FlowStep 的错误概率
func WithErrorRate(step *FlowStep, rate float64) *FlowStep {
	step.ErrorRate = rate
	return step
}

// WithErrorMessage 设置 FlowStep 的错误消息
func WithErrorMessage(step *FlowStep, msg string) *FlowStep {
	step.ErrorMessage = msg
	return step
}

// WithChildren 设置 FlowStep 的子步骤
func WithChildren(step *FlowStep, children ...*FlowStep) *FlowStep {
	step.Children = children
	return step
}
