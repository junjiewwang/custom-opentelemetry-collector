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

const kafkaMessagingName = "kafka_messaging"

func init() {
	tdg.Register(kafkaMessagingName, func() tdg.Scenario {
		return &KafkaMessagingScenario{}
	})
}

// KafkaMessagingScenario Kafka 消息场景
// 模拟订单服务通过 Kafka 发送事件，支付服务消费并处理：
//
//	order-service: HTTP接收请求 → MySQL创建订单 → Kafka send(order-events)
//	payment-service: Kafka receive(order-events) → processPayment → MySQL记录支付
//
// 展示消息队列的 SpanLink 关联
type KafkaMessagingScenario struct {
	tdg.BaseScenario

	topic         string
	consumerGroup string
	errorRate     float64
}

func (s *KafkaMessagingScenario) Name() string      { return kafkaMessagingName }
func (s *KafkaMessagingScenario) Type() tdg.DataType { return tdg.DataTypeTraces }

func (s *KafkaMessagingScenario) Init(cfg map[string]interface{}) error {
	s.topic = tdg.ParseString(cfg, "topic", "order-events")
	s.consumerGroup = tdg.ParseString(cfg, "consumer_group", "payment-processor-group")
	s.errorRate = tdg.ParseFloat64(cfg, "error_rate", 0.05)
	return nil
}

func (s *KafkaMessagingScenario) GenerateTraces() (ptrace.Traces, error) {
	r := rand.New(rand.NewSource(time.Now().UnixNano()))
	orderID := fmt.Sprintf("ORD-%d", 100000+r.Intn(900000))
	paymentID := fmt.Sprintf("PAY-%d", 200000+r.Intn(800000))

	// === 发送侧：order-service 处理请求并发送 Kafka 消息 ===

	// L1: HTTP 入口
	root := tdg.HTTPServerCase("order-service", "POST", "/api/v1/orders", 8080)
	tdg.WithAttributes(root, map[string]string{"order.id": orderID})

	// L2: Controller 层 - handleCreateOrder
	controller := tdg.ControllerCase("order-service", "handleCreateOrder", map[string]string{
		"order.id": orderID,
	})

	// L3: Service 层 - OrderService.createOrder
	orderService := tdg.ServiceMethodCase("order-service", "OrderService.createOrder", map[string]string{
		"order.id": orderID,
	})

	// L4: Repository 层 - OrderRepository.save → MySQL
	repoSave := tdg.RepositoryCase("order-service", "OrderRepository.save", nil)
	repoSave.Children = []*tdg.FlowStep{
		tdg.MySQLCase("order-service", "order_db", "INSERT", "orders",
			fmt.Sprintf("INSERT INTO orders (order_id, status) VALUES ('%s', 'PENDING')", orderID)),
	}

	// L4: Kafka producer → consumer（异步，通过 SpanLink 关联）
	kafkaSend := tdg.KafkaSendCase("order-service", s.topic, "order-service-producer-1")
	tdg.WithAttributes(kafkaSend, map[string]string{
		"messaging.message.id":                 fmt.Sprintf("msg-%s", tdg.RandomPick([]string{"a1b2c3", "d4e5f6", "g7h8i9"})),
		"messaging.kafka.destination.partition": fmt.Sprintf("%d", r.Intn(6)),
		"messaging.kafka.message.offset":        fmt.Sprintf("%d", 10000+r.Intn(90000)),
	})

	// === 消费侧：payment-service 消费并处理 ===

	// Kafka consumer 入口
	kafkaReceive := tdg.KafkaReceiveCase("payment-service", s.topic, s.consumerGroup, "payment-service-consumer-1")

	// 消费侧 L1: MessageHandler - PaymentMessageHandler.handle
	msgHandler := tdg.MessageHandlerCase("payment-service", "PaymentMessageHandler.handle", map[string]string{
		"order.id":   orderID,
		"payment.id": paymentID,
	})

	// 消费侧 L2: Service 层 - PaymentService.processPayment
	paymentService := tdg.ServiceMethodCase("payment-service", "PaymentService.processPayment", map[string]string{
		"payment.id":     paymentID,
		"payment.method": "credit_card",
		"order.id":       orderID,
	})

	// 消费侧 L3: Repository 层 - PaymentRepository.save → MySQL
	paymentRepo := tdg.RepositoryCase("payment-service", "PaymentRepository.save", nil)
	paymentRepo.Children = []*tdg.FlowStep{
		tdg.MySQLCase("payment-service", "payment_db", "INSERT", "payments",
			fmt.Sprintf("INSERT INTO payments (payment_id, order_id, amount, status) VALUES ('%s', '%s', ?, 'SUCCESS')", paymentID, orderID)),
	}

	// 消费侧 L3: Repository 层 - OrderRepository.updateStatus → MySQL
	orderRepo := tdg.RepositoryCase("payment-service", "OrderRepository.updateStatus", nil)
	orderRepo.Children = []*tdg.FlowStep{
		tdg.MySQLCase("payment-service", "order_db", "UPDATE", "orders",
			fmt.Sprintf("UPDATE orders SET status = 'PAID', payment_id = '%s' WHERE order_id = '%s'", paymentID, orderID)),
	}

	// 组装消费侧
	paymentService.Children = []*tdg.FlowStep{paymentRepo, orderRepo}
	msgHandler.Children = []*tdg.FlowStep{paymentService}
	kafkaReceive.Children = []*tdg.FlowStep{msgHandler}
	kafkaSend.Children = []*tdg.FlowStep{kafkaReceive}

	// 组装发送侧
	orderService.Children = []*tdg.FlowStep{repoSave, kafkaSend}
	controller.Children = []*tdg.FlowStep{orderService}
	root.Children = []*tdg.FlowStep{controller}

	td := tdg.ExecuteFlow([]*tdg.FlowStep{root}, s.errorRate)
	return td, nil
}
