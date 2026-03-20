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

	// ================================================================
	// L1: api-gateway HTTP 入口
	// ================================================================
	root := WithDuration(
		WithAttributes(
			HTTPServerCase("api-gateway", "POST", "/api/v1/orders", 443),
			map[string]string{
				"http.client_ip":                   fmt.Sprintf("10.%d.%d.%d", r.Intn(255), r.Intn(255), r.Intn(255)),
				"enduser.id":                       userID,
				"http.request.header.x-request-id": fmt.Sprintf("req-%s", randomHexStr(r, 16)),
			},
		),
		80, 200,
	)
	WithResourceAttributes(root, map[string]string{"service.version": "2.3.1"})

	// L2: gateway 中间件
	gatewayAuth := MiddlewareCase("api-gateway", "auth", map[string]string{
		"auth.type": "jwt",
	})
	gatewayRateLimit := MiddlewareCase("api-gateway", "rateLimit", map[string]string{
		"rate_limit.bucket": "order_api",
	})

	// L2: gRPC: gateway → order-service
	gatewayToOrder := GRPCCallCase("api-gateway", "order-service", "OrderService", "CreateOrder", 50051)
	orderServer := gatewayToOrder.Children[0]
	WithAttributes(orderServer, map[string]string{"order.id": orderID, "user.id": userID})
	WithResourceAttributes(orderServer, map[string]string{"service.version": "1.5.2"})
	WithDuration(orderServer, 50, 130)

	// ================================================================
	// order-service 内部分层
	// ================================================================

	// L4: Service 层 - OrderService.createOrder
	orderService := ServiceMethodCase("order-service", "OrderService.createOrder", map[string]string{
		"order.id": orderID,
		"user.id":  userID,
	})
	WithDuration(orderService, 30, 100)

	// L5: SessionService.validate → Redis
	sessionCheck := ServiceMethodCase("order-service", "SessionService.validate", map[string]string{
		"user.id": userID,
	})
	sessionCheck.Children = []*FlowStep{
		RedisCase("order-service", "GET", fmt.Sprintf("GET user:session:%s", userID)),
	}

	// L5: InventoryRepository.checkStock → MySQL
	inventoryRepo := RepositoryCase("order-service", "InventoryRepository.checkStock", map[string]string{
		"product.id": productID,
	})
	inventoryRepo.Children = []*FlowStep{
		MySQLCase("order-service", "inventory_db", "SELECT", "products",
			fmt.Sprintf("SELECT stock FROM products WHERE product_id = '%s' FOR UPDATE", productID)),
	}

	// L5: OrderRepository.save → MySQL
	orderRepo := RepositoryCase("order-service", "OrderRepository.save", nil)
	orderRepo.Children = []*FlowStep{
		MySQLCase("order-service", "order_db", "INSERT", "orders",
			fmt.Sprintf("INSERT INTO orders (order_id, user_id, product_id, amount, status) VALUES ('%s', '%s', '%s', ?, 'PENDING')", orderID, userID, productID)),
	}

	// L5: CacheService.setOrderCache → Redis
	orderCacheSet := ServiceMethodCase("order-service", "CacheService.setOrderCache", nil)
	orderCacheSet.Children = []*FlowStep{
		RedisCase("order-service", "SET", fmt.Sprintf("SET order:%s {json} EX 3600", orderID)),
	}

	// ================================================================
	// L5: Kafka 发送订单事件（异步）→ payment-service 消费
	// ================================================================
	kafkaSend := WithAttributes(
		KafkaSendCase("order-service", "order-events", "order-service-producer-1"),
		map[string]string{
			"messaging.message.id":                 fmt.Sprintf("msg-%s", randomHexStr(r, 16)),
			"messaging.kafka.destination.partition": fmt.Sprintf("%d", r.Intn(6)),
			"messaging.kafka.message.offset":        fmt.Sprintf("%d", 10000+r.Intn(90000)),
		},
	)

	// payment-service: Kafka consumer 入口
	kafkaReceive := KafkaReceiveCase("payment-service", "order-events", "payment-processor-group", "payment-service-consumer-1")
	WithResourceAttributes(kafkaReceive, map[string]string{"service.version": "1.2.0"})

	// payment-service: MessageHandler
	paymentMsgHandler := MessageHandlerCase("payment-service", "PaymentMessageHandler.handle", map[string]string{
		"order.id":   orderID,
		"payment.id": paymentID,
	})

	// payment-service: Service 层 - PaymentService.processPayment
	paymentService := WithAttributes(
		ServiceMethodCase("payment-service", "PaymentService.processPayment", nil),
		map[string]string{"payment.id": paymentID, "payment.method": "credit_card", "order.id": orderID},
	)
	WithDuration(paymentService, 5, 30)

	// payment-service: Repository 层 - PaymentRepository.save → MySQL
	paymentRepo := RepositoryCase("payment-service", "PaymentRepository.save", nil)
	paymentRepo.Children = []*FlowStep{
		MySQLCase("payment-service", "payment_db", "INSERT", "payments",
			fmt.Sprintf("INSERT INTO payments (payment_id, order_id, amount, status) VALUES ('%s', '%s', ?, 'SUCCESS')", paymentID, orderID)),
	}

	// payment-service: Repository 层 - OrderRepository.updateStatus → MySQL
	paymentOrderRepo := RepositoryCase("payment-service", "OrderRepository.updateStatus", nil)
	paymentOrderRepo.Children = []*FlowStep{
		MySQLCase("payment-service", "order_db", "UPDATE", "orders",
			fmt.Sprintf("UPDATE orders SET status = 'PAID', payment_id = '%s' WHERE order_id = '%s'", paymentID, orderID)),
	}

	// ================================================================
	// payment-service → notification-service（gRPC 调用对）
	// ================================================================
	paymentToNotify := GRPCCallCase("payment-service", "notification-service", "NotificationService", "SendOrderConfirmation", 50053)
	notifyServer := paymentToNotify.Children[0]
	WithAttributes(notifyServer, map[string]string{
		"notification.type":      "email",
		"notification.recipient": userID,
	})
	WithResourceAttributes(notifyServer, map[string]string{"service.version": "1.0.3"})

	// notification-service: Service 层 - NotificationService.sendConfirmation
	notifyService := ServiceMethodCase("notification-service", "NotificationService.sendConfirmation", map[string]string{
		"order.id": orderID,
	})

	// notification-service: TemplateRepository → Redis
	templateRepo := RepositoryCase("notification-service", "TemplateRepository.getTemplate", nil)
	templateRepo.Children = []*FlowStep{
		RedisCase("notification-service", "GET", "GET template:order_confirmation"),
	}

	notifyService.Children = []*FlowStep{
		templateRepo,
		WithAttributes(
			InternalCase("notification-service", "sendEmail", nil),
			map[string]string{
				"email.to":      fmt.Sprintf("user_%s@example.com", userID),
				"email.subject": fmt.Sprintf("Order %s Confirmed", orderID),
			},
		),
	}
	notifyServer.Children = []*FlowStep{notifyService}

	// 组装 payment-service 处理链
	paymentService.Children = []*FlowStep{paymentRepo, paymentOrderRepo, paymentToNotify}
	paymentMsgHandler.Children = []*FlowStep{paymentService}
	kafkaReceive.Children = []*FlowStep{paymentMsgHandler}
	kafkaSend.Children = []*FlowStep{kafkaReceive}

	// 组装 order-service Service 层
	orderService.Children = []*FlowStep{
		sessionCheck,
		inventoryRepo,
		orderRepo,
		orderCacheSet,
		kafkaSend,
	}

	orderServer.Children = []*FlowStep{orderService}

	root.Children = []*FlowStep{
		gatewayAuth,
		gatewayRateLimit,
		gatewayToOrder,
	}

	td := ExecuteFlow([]*FlowStep{root}, f.errorRate)
	return td, nil
}