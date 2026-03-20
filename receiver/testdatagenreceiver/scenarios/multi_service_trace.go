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

const multiServiceTraceName = "multi_service_trace"

func init() {
	tdg.Register(multiServiceTraceName, func() tdg.Scenario {
		return &MultiServiceTraceScenario{}
	})
}

// MultiServiceTraceScenario 多服务调用链场景
// 模拟 API 网关到多个微服务的扇出调用：
//
//	api-gateway → order-service → (inventory-service + payment-service + notification-service)
//
// 展示完整的分布式调用拓扑
type MultiServiceTraceScenario struct {
	tdg.BaseScenario

	errorRate float64
}

func (s *MultiServiceTraceScenario) Name() string      { return multiServiceTraceName }
func (s *MultiServiceTraceScenario) Type() tdg.DataType { return tdg.DataTypeTraces }

func (s *MultiServiceTraceScenario) Init(cfg map[string]interface{}) error {
	s.errorRate = tdg.ParseFloat64(cfg, "error_rate", 0.0)
	return nil
}

func (s *MultiServiceTraceScenario) GenerateTraces() (ptrace.Traces, error) {
	r := rand.New(rand.NewSource(time.Now().UnixNano()))
	orderID := fmt.Sprintf("ORD-%d", 100000+r.Intn(900000))
	productID := fmt.Sprintf("PROD-%d", 1000+r.Intn(9000))

	// L1: api-gateway HTTP 入口
	root := tdg.WithDuration(
		tdg.HTTPServerCase("api-gateway", "POST", "/api/v1/orders", 443),
		50, 200,
	)

	// L2: gateway 中间件 - 鉴权 + 限流
	gatewayAuth := tdg.MiddlewareCase("api-gateway", "auth", map[string]string{
		"auth.type": "jwt",
	})
	gatewayRateLimit := tdg.MiddlewareCase("api-gateway", "rateLimit", map[string]string{
		"rate_limit.bucket": "order_api",
	})

	// L2: gRPC: gateway → order-service
	gatewayToOrder := tdg.GRPCCallCase("api-gateway", "order-service", "OrderService", "CreateOrder", 50051)
	orderServer := gatewayToOrder.Children[0]
	tdg.WithAttributes(orderServer, map[string]string{"order.id": orderID})

	// === order-service 内部分层 ===

	// L4: Service 层 - OrderService.createOrder
	orderService := tdg.ServiceMethodCase("order-service", "OrderService.createOrder", map[string]string{
		"order.id": orderID,
	})
	tdg.WithDuration(orderService, 20, 80)

	// L5: 校验
	validateOrder := tdg.InternalCase("order-service", "validateOrder", map[string]string{
		"order.id": orderID,
	})

	// L5: gRPC: order-service → inventory-service（查库存）
	orderToInventory := tdg.GRPCCallCase("order-service", "inventory-service", "InventoryService", "CheckStock", 50052)
	inventoryServer := orderToInventory.Children[0]

	// inventory-service 内部分层
	inventoryService := tdg.ServiceMethodCase("inventory-service", "InventoryService.checkStock", map[string]string{
		"product.id": productID,
	})
	inventoryRepo := tdg.RepositoryCase("inventory-service", "ProductRepository.findStock", nil)
	inventoryRepo.Children = []*tdg.FlowStep{
		tdg.MySQLCase("inventory-service", "inventory_db", "SELECT", "products",
			fmt.Sprintf("SELECT stock FROM products WHERE product_id = '%s' FOR UPDATE", productID)),
	}
	inventoryService.Children = []*tdg.FlowStep{inventoryRepo}
	inventoryServer.Children = []*tdg.FlowStep{inventoryService}

	// L5: Repository 层 - OrderRepository.save → MySQL 创建订单
	orderRepo := tdg.RepositoryCase("order-service", "OrderRepository.save", nil)
	orderRepo.Children = []*tdg.FlowStep{
		tdg.MySQLCase("order-service", "order_db", "INSERT", "orders",
			fmt.Sprintf("INSERT INTO orders (order_id, status) VALUES ('%s', 'CREATED')", orderID)),
	}

	// L5: gRPC: order-service → payment-service（发起支付）
	orderToPayment := tdg.GRPCCallCase("order-service", "payment-service", "PaymentService", "Charge", 50053)
	paymentServer := orderToPayment.Children[0]

	// payment-service 内部分层
	paymentService := tdg.ServiceMethodCase("payment-service", "PaymentService.charge", map[string]string{
		"payment.method": "credit_card",
		"order.id":       orderID,
	})
	paymentRepo := tdg.RepositoryCase("payment-service", "PaymentRepository.save", nil)
	paymentRepo.Children = []*tdg.FlowStep{
		tdg.MySQLCase("payment-service", "payment_db", "INSERT", "transactions",
			fmt.Sprintf("INSERT INTO transactions (order_id, amount, status) VALUES ('%s', ?, 'SUCCESS')", orderID)),
	}
	paymentService.Children = []*tdg.FlowStep{
		tdg.InternalCase("payment-service", "processPayment", map[string]string{
			"payment.method": "credit_card",
			"order.id":       orderID,
		}),
		paymentRepo,
	}
	paymentServer.Children = []*tdg.FlowStep{paymentService}

	// L5: gRPC: order-service → notification-service（发通知）
	orderToNotification := tdg.GRPCCallCase("order-service", "notification-service", "NotificationService", "Send", 50054)
	notificationServer := orderToNotification.Children[0]

	// notification-service 内部分层
	notifyService := tdg.ServiceMethodCase("notification-service", "NotificationService.sendConfirmation", map[string]string{
		"order.id": orderID,
	})
	templateRepo := tdg.RepositoryCase("notification-service", "TemplateRepository.getTemplate", nil)
	templateRepo.Children = []*tdg.FlowStep{
		tdg.RedisCase("notification-service", "GET", "GET template:order_confirmation"),
	}
	notifyService.Children = []*tdg.FlowStep{
		templateRepo,
		tdg.InternalCase("notification-service", "sendEmail", map[string]string{
			"email.subject": fmt.Sprintf("Order %s Confirmed", orderID),
		}),
	}
	notificationServer.Children = []*tdg.FlowStep{notifyService}

	// 组装 order-service 的 Service 层子步骤
	orderService.Children = []*tdg.FlowStep{
		validateOrder,
		orderToInventory,
		orderRepo,
		orderToPayment,
		orderToNotification,
	}

	orderServer.Children = []*tdg.FlowStep{orderService}

	root.Children = []*tdg.FlowStep{
		gatewayAuth,
		gatewayRateLimit,
		gatewayToOrder,
	}

	td := tdg.ExecuteFlow([]*tdg.FlowStep{root}, s.errorRate)
	return td, nil
}