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

const rabbitmqMessagingName = "rabbitmq_messaging"

func init() {
	tdg.Register(rabbitmqMessagingName, func() tdg.Scenario {
		return &RabbitMQMessagingScenario{}
	})
}

// RabbitMQMessagingScenario RabbitMQ 消息场景
// 模拟通知服务通过 RabbitMQ 异步发送邮件通知：
//
//	order-service: HTTP接收请求 → 发送 RabbitMQ 消息(notification-exchange)
//	notification-service: RabbitMQ receive(email-queue) → 查模板(Redis) → 发送邮件
type RabbitMQMessagingScenario struct {
	tdg.BaseScenario

	exchange   string
	routingKey string
	queue      string
	errorRate  float64
}

func (s *RabbitMQMessagingScenario) Name() string      { return rabbitmqMessagingName }
func (s *RabbitMQMessagingScenario) Type() tdg.DataType { return tdg.DataTypeTraces }

func (s *RabbitMQMessagingScenario) Init(cfg map[string]interface{}) error {
	s.exchange = tdg.ParseString(cfg, "exchange", "notification-exchange")
	s.routingKey = tdg.ParseString(cfg, "routing_key", "email.send")
	s.queue = tdg.ParseString(cfg, "queue", "email-queue")
	s.errorRate = tdg.ParseFloat64(cfg, "error_rate", 0.05)
	return nil
}

func (s *RabbitMQMessagingScenario) GenerateTraces() (ptrace.Traces, error) {
	r := rand.New(rand.NewSource(time.Now().UnixNano()))
	notificationID := fmt.Sprintf("NOTIF-%d", 100000+r.Intn(900000))
	userID := fmt.Sprintf("USR-%d", 10000+r.Intn(90000))

	// 构建调用树：order-service 完成订单后发 RabbitMQ → notification-service 处理通知
	root := tdg.HTTPServerCase("order-service", "POST", "/api/v1/orders/confirm", 8080)

	// RabbitMQ 发送通知消息
	rabbitSend := tdg.RabbitMQSendCase("order-service", s.exchange, s.routingKey)
	tdg.WithAttributes(rabbitSend, map[string]string{
		"messaging.message.id": fmt.Sprintf("amq-%016X", r.Int63()),
		"notification.id":      notificationID,
		"notification.type":    "email",
	})

	// RabbitMQ consumer 侧
	rabbitReceive := tdg.RabbitMQReceiveCase("notification-service", s.queue)
	rabbitReceive.Children = []*tdg.FlowStep{
		tdg.RedisCase("notification-service", "GET", "GET template:order_confirmation"),
		tdg.InternalCase("notification-service", "renderTemplate", map[string]string{
			"template.name":   "order_confirmation",
			"notification.id": notificationID,
		}),
		tdg.InternalCase("notification-service", "sendEmail", map[string]string{
			"email.to":      fmt.Sprintf("user_%s@example.com", userID),
			"email.subject": "Your order has been confirmed",
		}),
		tdg.RedisCase("notification-service", "SET",
			fmt.Sprintf("SET notification:status:%s sent EX 86400", notificationID)),
	}

	rabbitSend.Children = []*tdg.FlowStep{rabbitReceive}

	root.Children = []*tdg.FlowStep{
		tdg.InternalCase("order-service", "prepareNotification", map[string]string{
			"user.id":         userID,
			"notification.id": notificationID,
		}),
		rabbitSend,
	}

	td := tdg.ExecuteFlow([]*tdg.FlowStep{root}, s.errorRate)
	return td, nil
}
