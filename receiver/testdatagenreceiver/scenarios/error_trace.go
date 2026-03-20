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

const errorTraceName = "error_trace"

func init() {
	tdg.Register(errorTraceName, func() tdg.Scenario {
		return &ErrorTraceScenario{}
	})
}

// ErrorTraceScenario 异常链路场景
// 模拟一个支付服务异常请求：
//
//	HTTP POST /api/v1/payments → gRPC PaymentGateway/Charge → MySQL INSERT(超时) → 返回 500
//
// 高错误率展示异常链路的完整 Trace
type ErrorTraceScenario struct {
	tdg.BaseScenario

	serviceName string
	errorRate   float64
}

func (s *ErrorTraceScenario) Name() string        { return errorTraceName }
func (s *ErrorTraceScenario) Type() tdg.DataType   { return tdg.DataTypeTraces }

func (s *ErrorTraceScenario) Init(cfg map[string]interface{}) error {
	s.serviceName = tdg.ParseString(cfg, "service_name", "payment-service")
	s.errorRate = tdg.ParseFloat64(cfg, "error_rate", 0.5)
	return nil
}

func (s *ErrorTraceScenario) GenerateTraces() (ptrace.Traces, error) {
	r := rand.New(rand.NewSource(time.Now().UnixNano()))
	paymentID := fmt.Sprintf("PAY-%d", 100000+r.Intn(900000))
	orderID := fmt.Sprintf("ORD-%d", 100000+r.Intn(900000))

	// 构建调用树：HTTP 入口 → gRPC 调用支付网关 → MySQL 记录 → 风控校验
	root := tdg.WithAttributes(
		tdg.HTTPServerCase(s.serviceName, "POST", "/api/v1/payments", 8080),
		map[string]string{
			"payment.id": paymentID,
			"order.id":   orderID,
		},
	)

	// gRPC 调用：payment-service → payment-gateway（外部支付网关）
	grpcCall := tdg.GRPCCallCase(s.serviceName, "payment-gateway", "PaymentGateway", "Charge", 50055)
	// 在 gateway server span 上加异常信息
	gatewayServer := grpcCall.Children[0]
	tdg.WithErrorRate(gatewayServer, s.errorRate)
	tdg.WithErrorMessage(gatewayServer, "TimeoutException: upstream payment gateway timeout after 30s")
	tdg.WithDuration(gatewayServer, 100, 500)
	gatewayServer.Children = []*tdg.FlowStep{
		tdg.WithErrorRate(
			tdg.WithErrorMessage(
				tdg.InternalCase("payment-gateway", "chargeCard", map[string]string{
					"payment.method":   "credit_card",
					"payment.provider": "stripe",
				}),
				"ConnectionRefused: payment provider unreachable",
			),
			s.errorRate,
		),
	}

	// MySQL 记录支付尝试
	mysqlInsert := tdg.WithErrorRate(
		tdg.WithErrorMessage(
			tdg.MySQLCase(s.serviceName, "payment_db", "INSERT", "payment_attempts",
				fmt.Sprintf("INSERT INTO payment_attempts (payment_id, order_id, status, error_msg) VALUES ('%s', '%s', 'FAILED', ?)", paymentID, orderID)),
			"DeadlockException: lock wait timeout exceeded",
		),
		s.errorRate*0.3, // 数据库错误概率较低
	)

	// 风控校验
	riskCheck := tdg.WithErrorRate(
		tdg.WithErrorMessage(
			tdg.InternalCase(s.serviceName, "riskCheck", map[string]string{
				"risk.level":  "high",
				"risk.reason": "abnormal_amount",
			}),
			"RiskRejectException: transaction blocked by risk engine",
		),
		s.errorRate*0.2,
	)

	root.Children = []*tdg.FlowStep{
		tdg.InternalCase(s.serviceName, "validatePaymentRequest", map[string]string{
			"validation.type": "payment_params",
		}),
		riskCheck,
		grpcCall,
		mysqlInsert,
	}

	// 根 span 也有错误概率
	tdg.WithErrorRate(root, s.errorRate)
	tdg.WithErrorMessage(root, "500 Internal Server Error: payment processing failed")

	td := tdg.ExecuteFlow([]*tdg.FlowStep{root}, s.errorRate)
	return td, nil
}
