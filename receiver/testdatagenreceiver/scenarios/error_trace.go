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

	// L1: HTTP 入口
	root := tdg.WithAttributes(
		tdg.HTTPServerCase(s.serviceName, "POST", "/api/v1/payments", 8080),
		map[string]string{
			"payment.id": paymentID,
			"order.id":   orderID,
		},
	)

	// L2: 中间件层 - 鉴权 + 限流
	authMiddleware := tdg.MiddlewareCase(s.serviceName, "auth", map[string]string{
		"auth.type": "api_key",
	})
	rateLimitMiddleware := tdg.MiddlewareCase(s.serviceName, "rateLimit", map[string]string{
		"rate_limit.bucket": "payment_api",
	})

	// L2: Controller 层 - handleCreatePayment
	controller := tdg.ControllerCase(s.serviceName, "handleCreatePayment", map[string]string{
		"payment.id": paymentID,
		"order.id":   orderID,
	})

	// L3: 参数校验
	validateStep := tdg.InternalCase(s.serviceName, "validatePaymentRequest", map[string]string{
		"validation.type": "payment_params",
	})

	// L3: Service 层 - PaymentService.processPayment
	paymentService := tdg.ServiceMethodCase(s.serviceName, "PaymentService.processPayment", map[string]string{
		"payment.id": paymentID,
		"order.id":   orderID,
	})
	tdg.WithDuration(paymentService, 10, 80)

	// L4: 风控校验
	riskCheck := tdg.WithErrorRate(
		tdg.WithErrorMessage(
			tdg.ServiceMethodCase(s.serviceName, "RiskService.evaluate", map[string]string{
				"risk.level":  "high",
				"risk.reason": "abnormal_amount",
			}),
			"RiskRejectException: transaction blocked by risk engine",
		),
		s.errorRate*0.2,
	)

	// L4: gRPC 调用 payment-gateway（外部支付网关）
	grpcCall := tdg.GRPCCallCase(s.serviceName, "payment-gateway", "PaymentGateway", "Charge", 50055)
	gatewayServer := grpcCall.Children[0]
	tdg.WithErrorRate(gatewayServer, s.errorRate)
	tdg.WithErrorMessage(gatewayServer, "TimeoutException: upstream payment gateway timeout after 30s")
	tdg.WithDuration(gatewayServer, 100, 500)

	// L5 (gateway 内部): chargeCard
	chargeCard := tdg.WithErrorRate(
		tdg.WithErrorMessage(
			tdg.ServiceMethodCase("payment-gateway", "ChargeService.chargeCard", map[string]string{
				"payment.method":   "credit_card",
				"payment.provider": "stripe",
			}),
			"ConnectionRefused: payment provider unreachable",
		),
		s.errorRate,
	)
	// L6 (gateway 内部): HTTP 调用外部支付接口
	chargeCard.Children = []*tdg.FlowStep{
		tdg.WithErrorRate(
			tdg.WithErrorMessage(
				tdg.HTTPClientCase("payment-gateway", "POST", "https://api.stripe.com/v1/charges", "api.stripe.com", 443),
				"HTTP 502: Bad Gateway - upstream timeout",
			),
			s.errorRate,
		),
	}
	gatewayServer.Children = []*tdg.FlowStep{chargeCard}

	// L4: Repository 层 - PaymentRepository.saveAttempt → MySQL
	repoSave := tdg.WithErrorRate(
		tdg.WithErrorMessage(
			tdg.RepositoryCase(s.serviceName, "PaymentRepository.saveAttempt", nil),
			"DeadlockException: lock wait timeout exceeded",
		),
		s.errorRate*0.3,
	)
	repoSave.Children = []*tdg.FlowStep{
		tdg.WithErrorRate(
			tdg.WithErrorMessage(
				tdg.MySQLCase(s.serviceName, "payment_db", "INSERT", "payment_attempts",
					fmt.Sprintf("INSERT INTO payment_attempts (payment_id, order_id, status, error_msg) VALUES ('%s', '%s', 'FAILED', ?)", paymentID, orderID)),
				"DeadlockException: lock wait timeout exceeded",
			),
			s.errorRate*0.3,
		),
	}

	// 组装 Service 层子步骤
	paymentService.Children = []*tdg.FlowStep{
		riskCheck,
		grpcCall,
		repoSave,
	}

	// 组装 Controller 层子步骤
	controller.Children = []*tdg.FlowStep{
		validateStep,
		paymentService,
	}

	// 组装根节点
	root.Children = []*tdg.FlowStep{
		authMiddleware,
		rateLimitMiddleware,
		controller,
	}

	// 根 span 也有错误概率
	tdg.WithErrorRate(root, s.errorRate)
	tdg.WithErrorMessage(root, "500 Internal Server Error: payment processing failed")

	td := tdg.ExecuteFlow([]*tdg.FlowStep{root}, s.errorRate)
	return td, nil
}
