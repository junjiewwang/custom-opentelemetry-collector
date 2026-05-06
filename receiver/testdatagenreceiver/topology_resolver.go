// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package testdatagenreceiver

import (
	"fmt"
	"math/rand"
	"strings"
	"time"
)

// TopologyResolver 拓扑解析器
// 将声明式配置中的图拓扑（邻接表）转换为 FlowStep 树，供 ExecuteFlow 引擎执行
type TopologyResolver struct {
	cfg *Config
}

// NewTopologyResolver 创建拓扑解析器
func NewTopologyResolver(cfg *Config) *TopologyResolver {
	return &TopologyResolver{cfg: cfg}
}

// Resolve 将声明式 Flow 配置解析为 FlowStep 树
// 核心算法：
// 1. 校验拓扑为 DAG（无环检测）
// 2. 从入口节点开始 DFS 遍历图
// 3. 为每个节点生成对应的 Server span + internal_steps，为每条边生成 Client+Server span 对
func (r *TopologyResolver) Resolve(flowCfg *DeclarativeFlowConfig) ([]*FlowStep, error) {
	// 1. 环检测
	if err := r.detectCycle(flowCfg); err != nil {
		return nil, err
	}

	// 2. 构建入口 FlowStep
	entryApp := flowCfg.EntryPoint.App
	namespace := r.cfg.GetAppNamespace(entryApp)
	appCfg := r.cfg.GetAppConfig(entryApp)
	version := "1.0.0"
	if appCfg != nil && appCfg.Version != "" {
		version = appCfg.Version
	}

	// 创建入口 span
	var rootStep *FlowStep
	switch flowCfg.EntryPoint.Protocol {
	case "http":
		method := flowCfg.EntryPoint.Method
		if method == "" {
			method = "POST"
		}
		route := flowCfg.EntryPoint.Route
		if route == "" {
			route = "/api/v1/request"
		}
		rootStep = HTTPServerCase(entryApp, method, route, 8080)
	case "grpc":
		service := flowCfg.EntryPoint.Method
		if service == "" {
			service = "EntryService/Handle"
		}
		parts := splitServiceMethod(service)
		rootStep = GRPCServerCase(entryApp, parts[0], parts[1])
	default:
		// 默认 HTTP
		rootStep = HTTPServerCase(entryApp, "POST", "/api/v1/request", 8080)
	}

	// 设置入口节点的 namespace 和 version
	if rootStep.ResourceAttributes == nil {
		rootStep.ResourceAttributes = make(map[string]string)
	}
	rootStep.ResourceAttributes["service.namespace"] = namespace
	rootStep.ResourceAttributes["service.version"] = version

	// 3. 构建入口节点的内部步骤和子树
	visited := make(map[string]bool)
	r.buildNodeContent(rootStep, entryApp, flowCfg.Topology, visited)

	return []*FlowStep{rootStep}, nil
}

// buildNodeContent 构建节点的完整内容（internal_steps + calls）
func (r *TopologyResolver) buildNodeContent(parentStep *FlowStep, currentApp string, topology map[string]*TopologyNode, visited map[string]bool) {
	node, exists := topology[currentApp]
	if !exists || node == nil {
		return
	}

	// 标记当前节点已访问（防止在同一路径上重复展开）
	visited[currentApp] = true
	defer func() { visited[currentApp] = false }()

	// 1. 生成 internal_steps
	internalSteps := r.buildInternalSteps(currentApp, node.InternalSteps)

	// 2. 生成 calls（下游调用）
	callSteps := r.buildCalls(currentApp, node.Calls, topology, visited)

	// 3. 组装：internal_steps 在前，calls 在后
	parentStep.Children = append(parentStep.Children, internalSteps...)
	parentStep.Children = append(parentStep.Children, callSteps...)
}

// buildInternalSteps 根据配置生成应用内部行为的 FlowStep 列表
func (r *TopologyResolver) buildInternalSteps(serviceName string, steps []InternalStepConfig) []*FlowStep {
	if len(steps) == 0 {
		return nil
	}

	rng := rand.New(rand.NewSource(time.Now().UnixNano()))
	result := make([]*FlowStep, 0, len(steps))

	for _, step := range steps {
		var flowStep *FlowStep

		switch step.Type {
		case "middleware":
			name := step.Name
			if name == "" {
				name = "unknown"
			}
			flowStep = MiddlewareCase(serviceName, name, step.Attributes)

		case "service_method":
			method := step.Method
			if method == "" {
				method = "Service.handle"
			}
			flowStep = ServiceMethodCase(serviceName, method, step.Attributes)

		case "repository":
			method := step.Method
			if method == "" {
				method = "Repository.execute"
			}
			flowStep = RepositoryCase(serviceName, method, step.Attributes)

		case "redis":
			operation := step.Operation
			if operation == "" {
				operation = "GET"
			}
			keyPattern := step.KeyPattern
			if keyPattern == "" {
				keyPattern = fmt.Sprintf("key:%s", randomHexStr(rng, 8))
			}
			statement := fmt.Sprintf("%s %s", operation, keyPattern)
			flowStep = RedisCase(serviceName, operation, statement)

		case "mysql":
			operation := step.Operation
			if operation == "" {
				operation = "SELECT"
			}
			db := step.Database
			if db == "" {
				db = "default_db"
			}
			table := step.Table
			if table == "" {
				table = "unknown_table"
			}
			statement := r.generateSQLStatement(operation, table, rng)
			flowStep = MySQLCase(serviceName, db, operation, table, statement)

		case "mongodb":
			operation := step.Operation
			if operation == "" {
				operation = "find"
			}
			db := step.Database
			if db == "" {
				db = "default_db"
			}
			collection := step.Collection
			if collection == "" {
				collection = "unknown_collection"
			}
			statement := fmt.Sprintf(`{"%s": "%s"}`, operation, collection)
			flowStep = MongoDBCase(serviceName, db, collection, operation, statement)

		case "kafka_send":
			topic := step.Topic
			if topic == "" {
				topic = "default-topic"
			}
			clientID := fmt.Sprintf("%s-producer-1", serviceName)
			flowStep = KafkaSendCase(serviceName, topic, clientID)

		case "internal":
			name := step.Name
			if name == "" {
				name = "internal.process"
			}
			flowStep = InternalCase(serviceName, name, step.Attributes)

		default:
			// 未知类型，创建一个通用 internal span
			name := step.Name
			if name == "" {
				name = fmt.Sprintf("unknown.%s", step.Type)
			}
			flowStep = InternalCase(serviceName, name, step.Attributes)
		}

		if flowStep != nil {
			result = append(result, flowStep)
		}
	}

	return result
}

// buildCalls 构建下游调用的 FlowStep 列表
func (r *TopologyResolver) buildCalls(currentApp string, edges []TopologyEdge, topology map[string]*TopologyNode, visited map[string]bool) []*FlowStep {
	if len(edges) == 0 {
		return nil
	}

	result := make([]*FlowStep, 0, len(edges))

	for _, edge := range edges {
		if visited[edge.Target] {
			// 跳过已访问的节点（防止环，虽然前面已做环检测）
			continue
		}

		targetNamespace := r.cfg.GetAppNamespace(edge.Target)
		targetAppCfg := r.cfg.GetAppConfig(edge.Target)
		targetVersion := "1.0.0"
		if targetAppCfg != nil && targetAppCfg.Version != "" {
			targetVersion = targetAppCfg.Version
		}

		var callStep *FlowStep

		switch edge.Protocol {
		case "grpc":
			callStep = r.buildGRPCCall(currentApp, edge, targetNamespace, targetVersion, topology, visited)

		case "http":
			callStep = r.buildHTTPCall(currentApp, edge, targetNamespace, targetVersion, topology, visited)

		case "kafka":
			callStep = r.buildKafkaCall(currentApp, edge, targetNamespace, targetVersion, topology, visited)

		case "rocketmq":
			callStep = r.buildRocketMQCall(currentApp, edge, targetNamespace, targetVersion, topology, visited)

		case "rabbitmq":
			callStep = r.buildRabbitMQCall(currentApp, edge, targetNamespace, targetVersion, topology, visited)

		default:
			// 默认按 gRPC 处理
			callStep = r.buildGRPCCall(currentApp, edge, targetNamespace, targetVersion, topology, visited)
		}

		if callStep != nil {
			result = append(result, callStep)
		}
	}

	return result
}

// buildGRPCCall 构建 gRPC 调用（Client + Server span 对）
func (r *TopologyResolver) buildGRPCCall(currentApp string, edge TopologyEdge, targetNamespace, targetVersion string, topology map[string]*TopologyNode, visited map[string]bool) *FlowStep {
	rpcService := edge.Service
	rpcMethod := edge.Method
	if rpcService == "" {
		rpcService = fmt.Sprintf("%sService", capitalize(edge.Target))
	}
	if rpcMethod == "" {
		rpcMethod = "Handle"
	}

	// GRPCCallCase 返回 Client span，其 Children[0] 是 Server span
	callStep := GRPCCallCase(currentApp, edge.Target, rpcService, rpcMethod, 50051)

	// 为 Server span 设置 namespace 和 version
	serverStep := callStep.Children[0]
	if serverStep.ResourceAttributes == nil {
		serverStep.ResourceAttributes = make(map[string]string)
	}
	serverStep.ResourceAttributes["service.namespace"] = targetNamespace
	serverStep.ResourceAttributes["service.version"] = targetVersion

	// 递归构建 Server span 的内容（internal_steps + calls）
	r.buildNodeContent(serverStep, edge.Target, topology, visited)

	return callStep
}

// buildHTTPCall 构建 HTTP 调用（Client + Server span 对）
func (r *TopologyResolver) buildHTTPCall(currentApp string, edge TopologyEdge, targetNamespace, targetVersion string, topology map[string]*TopologyNode, visited map[string]bool) *FlowStep {
	method := edge.Method
	if method == "" {
		method = "GET"
	}
	route := fmt.Sprintf("/api/v1/%s", strings.ToLower(edge.Method))
	url := fmt.Sprintf("http://%s.default.svc.cluster.local:8080%s", edge.Target, route)
	targetHost := fmt.Sprintf("%s.default.svc.cluster.local", edge.Target)

	// 创建 HTTP Client span
	clientStep := HTTPClientCase(currentApp, method, url, targetHost, 8080)

	// 创建目标的 HTTP Server span
	serverStep := HTTPServerCase(edge.Target, method, route, 8080)
	if serverStep.ResourceAttributes == nil {
		serverStep.ResourceAttributes = make(map[string]string)
	}
	serverStep.ResourceAttributes["service.namespace"] = targetNamespace
	serverStep.ResourceAttributes["service.version"] = targetVersion

	clientStep.Children = []*FlowStep{serverStep}

	// 递归构建 Server span 的内容
	r.buildNodeContent(serverStep, edge.Target, topology, visited)

	return clientStep
}

// buildKafkaCall 构建 Kafka 异步调用（Producer + Consumer span 对）
func (r *TopologyResolver) buildKafkaCall(currentApp string, edge TopologyEdge, targetNamespace, targetVersion string, topology map[string]*TopologyNode, visited map[string]bool) *FlowStep {
	topic := edge.Topic
	if topic == "" {
		topic = fmt.Sprintf("%s-events", currentApp)
	}
	consumerGroup := edge.ConsumerGroup
	if consumerGroup == "" {
		consumerGroup = fmt.Sprintf("%s-consumer-group", edge.Target)
	}

	// 创建 Producer span
	producerStep := KafkaSendCase(currentApp, topic, fmt.Sprintf("%s-producer-1", currentApp))

	// 创建 Consumer span
	consumerStep := KafkaReceiveCase(edge.Target, topic, consumerGroup, fmt.Sprintf("%s-consumer-1", edge.Target))
	if consumerStep.ResourceAttributes == nil {
		consumerStep.ResourceAttributes = make(map[string]string)
	}
	consumerStep.ResourceAttributes["service.namespace"] = targetNamespace
	consumerStep.ResourceAttributes["service.version"] = targetVersion

	// Consumer 是 Producer 的子节点（异步关联）
	producerStep.Children = []*FlowStep{consumerStep}

	// 递归构建 Consumer 的内容
	r.buildNodeContent(consumerStep, edge.Target, topology, visited)

	return producerStep
}

// buildRocketMQCall 构建 RocketMQ 异步调用（Producer + Consumer span 对）
func (r *TopologyResolver) buildRocketMQCall(currentApp string, edge TopologyEdge, targetNamespace, targetVersion string, topology map[string]*TopologyNode, visited map[string]bool) *FlowStep {
	topic := edge.Topic
	if topic == "" {
		topic = fmt.Sprintf("%s-events", currentApp)
	}
	clientGroup := fmt.Sprintf("%s-producer-group", currentApp)

	// 创建 Producer span
	producerStep := RocketMQSendCase(currentApp, topic, clientGroup, "Normal", "default")

	// 创建 Consumer span
	consumerGroup := edge.ConsumerGroup
	if consumerGroup == "" {
		consumerGroup = fmt.Sprintf("%s-consumer-group", edge.Target)
	}
	consumerStep := RocketMQReceiveCase(edge.Target, topic, consumerGroup)
	if consumerStep.ResourceAttributes == nil {
		consumerStep.ResourceAttributes = make(map[string]string)
	}
	consumerStep.ResourceAttributes["service.namespace"] = targetNamespace
	consumerStep.ResourceAttributes["service.version"] = targetVersion

	producerStep.Children = []*FlowStep{consumerStep}

	// 递归构建 Consumer 的内容
	r.buildNodeContent(consumerStep, edge.Target, topology, visited)

	return producerStep
}

// buildRabbitMQCall 构建 RabbitMQ 异步调用（Producer + Consumer span 对）
func (r *TopologyResolver) buildRabbitMQCall(currentApp string, edge TopologyEdge, targetNamespace, targetVersion string, topology map[string]*TopologyNode, visited map[string]bool) *FlowStep {
	exchange := edge.Exchange
	if exchange == "" {
		exchange = fmt.Sprintf("%s-exchange", currentApp)
	}
	routingKey := edge.RoutingKey
	if routingKey == "" {
		routingKey = fmt.Sprintf("%s.events", currentApp)
	}

	// 创建 Producer span
	producerStep := RabbitMQSendCase(currentApp, exchange, routingKey)

	// 创建 Consumer span
	queue := fmt.Sprintf("%s-queue", edge.Target)
	consumerStep := RabbitMQReceiveCase(edge.Target, queue)
	if consumerStep.ResourceAttributes == nil {
		consumerStep.ResourceAttributes = make(map[string]string)
	}
	consumerStep.ResourceAttributes["service.namespace"] = targetNamespace
	consumerStep.ResourceAttributes["service.version"] = targetVersion

	producerStep.Children = []*FlowStep{consumerStep}

	// 递归构建 Consumer 的内容
	r.buildNodeContent(consumerStep, edge.Target, topology, visited)

	return producerStep
}

// detectCycle 检测拓扑中是否存在环
// 使用 DFS 三色标记法：白色（未访问）、灰色（正在访问）、黑色（已完成）
func (r *TopologyResolver) detectCycle(flowCfg *DeclarativeFlowConfig) error {
	const (
		white = 0 // 未访问
		gray  = 1 // 正在访问（在当前 DFS 路径上）
		black = 2 // 已完成
	)

	color := make(map[string]int)
	topology := flowCfg.Topology

	// 初始化所有节点为白色
	for node := range topology {
		color[node] = white
	}

	var dfs func(node string) error
	dfs = func(node string) error {
		color[node] = gray

		topoNode, exists := topology[node]
		if exists && topoNode != nil {
			for _, edge := range topoNode.Calls {
				switch color[edge.Target] {
				case gray:
					return fmt.Errorf("cycle detected: %s → %s forms a cycle", node, edge.Target)
				case white:
					if err := dfs(edge.Target); err != nil {
						return err
					}
				}
			}
		}

		color[node] = black
		return nil
	}

	// 从所有白色节点开始 DFS
	for node := range topology {
		if color[node] == white {
			if err := dfs(node); err != nil {
				return err
			}
		}
	}

	return nil
}

// generateSQLStatement 根据操作类型生成模拟 SQL 语句
func (r *TopologyResolver) generateSQLStatement(operation, table string, rng *rand.Rand) string {
	id := fmt.Sprintf("%d", 10000+rng.Intn(90000))
	switch strings.ToUpper(operation) {
	case "SELECT":
		return fmt.Sprintf("SELECT * FROM %s WHERE id = '%s'", table, id)
	case "INSERT":
		return fmt.Sprintf("INSERT INTO %s (id, data, created_at) VALUES ('%s', ?, NOW())", table, id)
	case "UPDATE":
		return fmt.Sprintf("UPDATE %s SET updated_at = NOW() WHERE id = '%s'", table, id)
	case "DELETE":
		return fmt.Sprintf("DELETE FROM %s WHERE id = '%s'", table, id)
	default:
		return fmt.Sprintf("%s %s", operation, table)
	}
}

// splitServiceMethod 将 "Service/Method" 格式拆分为 [service, method]
func splitServiceMethod(s string) [2]string {
	for i, c := range s {
		if c == '/' {
			return [2]string{s[:i], s[i+1:]}
		}
	}
	return [2]string{s, "Handle"}
}

// capitalize 首字母大写
func capitalize(s string) string {
	if len(s) == 0 {
		return s
	}
	if s[0] >= 'a' && s[0] <= 'z' {
		return string(s[0]-32) + s[1:]
	}
	return s
}
