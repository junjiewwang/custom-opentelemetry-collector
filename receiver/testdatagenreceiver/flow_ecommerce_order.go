// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package testdatagenreceiver

import (
	"fmt"
	"math/rand"
	"time"

	"go.opentelemetry.io/collector/pdata/ptrace"
)

const ecommerceOrderFlowName = "ecommerce_order"

func init() {
	RegisterFlow(ecommerceOrderFlowName, func() BusinessFlow {
		return &EcommerceOrderFlow{}
	})
}

// EcommerceOrderFlow 电商下单业务流
//
// 模拟一次完整的电商下单流程，涉及多个微服务：
//
//	用户请求 → api-gateway → order-service → (Redis缓存 + MySQL查库存 + MySQL下单)
//             → Kafka发消息 → payment-service → MySQL记录支付
//             → notification-service → gRPC发通知
type EcommerceOrderFlow struct {
	errorRate float64
}

func (f *EcommerceOrderFlow) Name() string {
	return ecommerceOrderFlowName
}

func (f *EcommerceOrderFlow) Description() string {
	return "电商下单：gateway → order-service → (Redis + MySQL + Kafka) → payment-service → notification-service"
}

func (f *EcommerceOrderFlow) Init(cfg map[string]interface{}) error {
	f.errorRate = ParseFloat64(cfg, "error_rate", 0.05)
	return nil
}

func (f *EcommerceOrderFlow) GenerateTraces() (ptrace.Traces, error) {
	r := rand.New(rand.NewSource(time.Now().UnixNano()))

	// 动态生成本次请求的业务数据
	orderID := fmt.Sprintf("ORD-%d", 100000+r.Intn(900000))
	userID := fmt.Sprintf("USR-%d", 10000+r.Intn(90000))
	productID := fmt.Sprintf("PROD-%d", 1000+r.Intn(9000))
	paymentID := fmt.Sprintf("PAY-%d", 200000+r.Intn(800000))

	// 1. api-gateway 入口
	root := WithDuration(
		WithAttributes(
			HTTPServerCase("api-gateway", "POST", "/api/v1/orders", 443),
			map[string]string{
				"http.client_ip":                    fmt.Sprintf("10.%d.%d.%d", r.Intn(255), r.Intn(255), r.Intn(255)),
				"enduser.id":                        userID,
				"http.request.header.x-request-id":  fmt.Sprintf("req-%s", randomHexStr(r, 16)),
			},
		),
		80, 200,
	)
	WithResourceAttributes(root, map[string]string{"service.version": "2.3.1"})

	// 2. api-gateway → order-service（gRPC 调用对）
	gatewayToOrder := GRPCCallCase("api-gateway", "order-service", "OrderService", "CreateOrder", 50051)
	orderServer := gatewayToOrder.Children[0]
	WithAttributes(orderServer, map[string]string{"order.id": orderID, "user.id": userID})
	WithResourceAttributes(orderServer, map[string]string{"service.version": "1.5.2"})
	WithDuration(orderServer, 50, 130)

	// 3. order-service 内部操作
	orderServer.Children = []*FlowStep{
		// Redis 校验 session
		RedisCase("order-service", "GET", fmt.Sprintf("GET user:session:%s", userID)),
		// MySQL 查库存
		MySQLCase("order-service", "inventory_db", "SELECT", "products",
			fmt.Sprintf("SELECT stock FROM products WHERE product_id = '%s' FOR UPDATE", productID)),
		// MySQL 创建订单
		MySQLCase("order-service", "order_db", "INSERT", "orders",
			fmt.Sprintf("INSERT INTO orders (order_id, user_id, product_id, amount, status) VALUES ('%s', '%s', '%s', ?, 'PENDING')", orderID, userID, productID)),
		// Redis 设置订单缓存
		RedisCase("order-service", "SET", fmt.Sprintf("SET order:%s {json} EX 3600", orderID)),
	}

	// 4. Kafka 发送订单事件（异步）→ payment-service 消费
	kafkaSend := WithAttributes(
		KafkaSendCase("order-service", "order-events", "order-service-producer-1"),
		map[string]string{
			"messaging.message.id":                      fmt.Sprintf("msg-%s", randomHexStr(r, 16)),
			"messaging.kafka.destination.partition":      fmt.Sprintf("%d", r.Intn(6)),
			"messaging.kafka.message.offset":             fmt.Sprintf("%d", 10000+r.Intn(90000)),
		},
	)

	// 5. payment-service 消费 Kafka 消息
	kafkaReceive := KafkaReceiveCase("payment-service", "order-events", "payment-processor-group", "payment-service-consumer-1")
	WithResourceAttributes(kafkaReceive, map[string]string{"service.version": "1.2.0"})

	// 6. payment-service 处理支付
	paymentProcess := WithAttributes(
		InternalCase("payment-service", "processPayment", nil),
		map[string]string{"payment.id": paymentID, "payment.method": "credit_card", "order.id": orderID},
	)
	WithDuration(paymentProcess, 5, 30)

	// 7. payment-service → notification-service（gRPC 调用对）
	paymentToNotify := GRPCCallCase("payment-service", "notification-service", "NotificationService", "SendOrderConfirmation", 50053)
	notifyServer := paymentToNotify.Children[0]
	WithAttributes(notifyServer, map[string]string{
		"notification.type":      "email",
		"notification.recipient": userID,
	})
	WithResourceAttributes(notifyServer, map[string]string{"service.version": "1.0.3"})

	// 8. notification-service 内部操作
	notifyServer.Children = []*FlowStep{
		RedisCase("notification-service", "GET", "GET template:order_confirmation"),
		WithAttributes(
			InternalCase("notification-service", "sendEmail", nil),
			map[string]string{
				"email.to":      fmt.Sprintf("user_%s@example.com", userID),
				"email.subject": fmt.Sprintf("Order %s Confirmed", orderID),
			},
		),
	}

	// 组装 payment-service 处理链
	paymentProcess.Children = []*FlowStep{
		MySQLCase("payment-service", "payment_db", "INSERT", "payments",
			fmt.Sprintf("INSERT INTO payments (payment_id, order_id, amount, status) VALUES ('%s', '%s', ?, 'SUCCESS')", paymentID, orderID)),
		MySQLCase("payment-service", "order_db", "UPDATE", "orders",
			fmt.Sprintf("UPDATE orders SET status = 'PAID', payment_id = '%s' WHERE order_id = '%s'", paymentID, orderID)),
		paymentToNotify,
	}

	kafkaReceive.Children = []*FlowStep{paymentProcess}
	kafkaSend.Children = []*FlowStep{kafkaReceive}

	// 将 Kafka 发送加入 order-service 的子步骤
	orderServer.Children = append(orderServer.Children, kafkaSend)

	root.Children = []*FlowStep{gatewayToOrder}

	td := ExecuteFlow([]*FlowStep{root}, f.errorRate)
	return td, nil
}