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

	// 构建调用树：gateway → order-service → 并发调用 3 个下游服务
	root := tdg.WithDuration(
		tdg.HTTPServerCase("api-gateway", "POST", "/api/v1/orders", 443),
		50, 200,
	)

	// order-service 内部逻辑：校验 → 查库存 → 扣库存 → 创建订单 → 发起支付 → 发通知
	// gRPC: gateway → order-service
	gatewayToOrder := tdg.GRPCCallCase("api-gateway", "order-service", "OrderService", "CreateOrder", 50051)
	orderServer := gatewayToOrder.Children[0]
	tdg.WithAttributes(orderServer, map[string]string{"order.id": orderID})

	// gRPC: order-service → inventory-service（查库存）
	orderToInventory := tdg.GRPCCallCase("order-service", "inventory-service", "InventoryService", "CheckStock", 50052)
	inventoryServer := orderToInventory.Children[0]
	inventoryServer.Children = []*tdg.FlowStep{
		tdg.MySQLCase("inventory-service", "inventory_db", "SELECT", "products",
			fmt.Sprintf("SELECT stock FROM products WHERE product_id = 'PROD-%d' FOR UPDATE", 1000+r.Intn(9000))),
	}

	// gRPC: order-service → payment-service（发起支付）
	orderToPayment := tdg.GRPCCallCase("order-service", "payment-service", "PaymentService", "Charge", 50053)
	paymentServer := orderToPayment.Children[0]
	paymentServer.Children = []*tdg.FlowStep{
		tdg.InternalCase("payment-service", "processPayment", map[string]string{
			"payment.method": "credit_card",
			"order.id":       orderID,
		}),
		tdg.MySQLCase("payment-service", "payment_db", "INSERT", "transactions",
			fmt.Sprintf("INSERT INTO transactions (order_id, amount, status) VALUES ('%s', ?, 'SUCCESS')", orderID)),
	}

	// gRPC: order-service → notification-service（发通知）
	orderToNotification := tdg.GRPCCallCase("order-service", "notification-service", "NotificationService", "Send", 50054)
	notificationServer := orderToNotification.Children[0]
	notificationServer.Children = []*tdg.FlowStep{
		tdg.RedisCase("notification-service", "GET", "GET template:order_confirmation"),
		tdg.InternalCase("notification-service", "sendEmail", map[string]string{
			"email.subject": fmt.Sprintf("Order %s Confirmed", orderID),
		}),
	}

	// 组装 order-service 的子步骤
	orderServer.Children = []*tdg.FlowStep{
		tdg.InternalCase("order-service", "validateOrder", map[string]string{
			"order.id": orderID,
		}),
		orderToInventory,
		tdg.MySQLCase("order-service", "order_db", "INSERT", "orders",
			fmt.Sprintf("INSERT INTO orders (order_id, status) VALUES ('%s', 'CREATED')", orderID)),
		orderToPayment,
		orderToNotification,
	}

	root.Children = []*tdg.FlowStep{gatewayToOrder}

	td := tdg.ExecuteFlow([]*tdg.FlowStep{root}, s.errorRate)
	return td, nil
}