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

	// === 发送侧：order-service 完成订单后发送通知消息 ===

	// L1: HTTP 入口
	root := tdg.HTTPServerCase("order-service", "POST", "/api/v1/orders/confirm", 8080)

	// L2: Controller 层 - handleConfirmOrder
	controller := tdg.ControllerCase("order-service", "handleConfirmOrder", map[string]string{
		"user.id":         userID,
		"notification.id": notificationID,
	})

	// L3: Service 层 - NotificationService.sendAsync
	notifyService := tdg.ServiceMethodCase("order-service", "NotificationService.sendAsync", map[string]string{
		"notification.id":   notificationID,
		"notification.type": "email",
	})

	// L4: 准备通知内容
	prepareStep := tdg.InternalCase("order-service", "prepareNotification", map[string]string{
		"user.id":         userID,
		"notification.id": notificationID,
	})

	// L4: RabbitMQ 发送通知消息
	rabbitSend := tdg.RabbitMQSendCase("order-service", s.exchange, s.routingKey)
	tdg.WithAttributes(rabbitSend, map[string]string{
		"messaging.message.id": fmt.Sprintf("amq-%016X", r.Int63()),
		"notification.id":      notificationID,
		"notification.type":    "email",
	})

	// === 消费侧：notification-service 消费并发送邮件 ===

	// RabbitMQ consumer 入口
	rabbitReceive := tdg.RabbitMQReceiveCase("notification-service", s.queue)

	// 消费侧 L1: MessageHandler - EmailMessageHandler.handle
	msgHandler := tdg.MessageHandlerCase("notification-service", "EmailMessageHandler.handle", map[string]string{
		"notification.id": notificationID,
	})

	// 消费侧 L2: Service 层 - EmailService.sendConfirmation
	emailService := tdg.ServiceMethodCase("notification-service", "EmailService.sendConfirmation", map[string]string{
		"notification.id": notificationID,
		"email.to":        fmt.Sprintf("user_%s@example.com", userID),
	})

	// 消费侧 L3: 查模板 - TemplateRepository.getTemplate → Redis
	templateRepo := tdg.RepositoryCase("notification-service", "TemplateRepository.getTemplate", nil)
	templateRepo.Children = []*tdg.FlowStep{
		tdg.RedisCase("notification-service", "GET", "GET template:order_confirmation"),
	}

	// 消费侧 L3: 渲染模板
	renderStep := tdg.InternalCase("notification-service", "renderTemplate", map[string]string{
		"template.name":   "order_confirmation",
		"notification.id": notificationID,
	})

	// 消费侧 L3: 发送邮件
	sendStep := tdg.InternalCase("notification-service", "sendEmail", map[string]string{
		"email.to":      fmt.Sprintf("user_%s@example.com", userID),
		"email.subject": "Your order has been confirmed",
	})

	// 消费侧 L3: 记录发送状态 → Redis
	statusRepo := tdg.RepositoryCase("notification-service", "NotificationRepository.updateStatus", nil)
	statusRepo.Children = []*tdg.FlowStep{
		tdg.RedisCase("notification-service", "SET",
			fmt.Sprintf("SET notification:status:%s sent EX 86400", notificationID)),
	}

	// 组装消费侧
	emailService.Children = []*tdg.FlowStep{templateRepo, renderStep, sendStep, statusRepo}
	msgHandler.Children = []*tdg.FlowStep{emailService}
	rabbitReceive.Children = []*tdg.FlowStep{msgHandler}
	rabbitSend.Children = []*tdg.FlowStep{rabbitReceive}

	// 组装发送侧
	notifyService.Children = []*tdg.FlowStep{prepareStep, rabbitSend}
	controller.Children = []*tdg.FlowStep{notifyService}
	root.Children = []*tdg.FlowStep{controller}

	td := tdg.ExecuteFlow([]*tdg.FlowStep{root}, s.errorRate)
	return td, nil
}
