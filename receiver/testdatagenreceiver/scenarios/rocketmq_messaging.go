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

const rocketmqMessagingName = "rocketmq_messaging"

func init() {
	tdg.Register(rocketmqMessagingName, func() tdg.Scenario {
		return &RocketMQMessagingScenario{}
	})
}

// RocketMQMessagingScenario RocketMQ 消息场景
// 模拟支付服务通过 RocketMQ 发送事务消息，账户服务消费并处理：
//
//	payment-service: gRPC接收请求 → 处理支付 → RocketMQ send(payment-topic)
//	account-service: RocketMQ receive(payment-topic) → 更新账户余额(MySQL)
type RocketMQMessagingScenario struct {
	tdg.BaseScenario

	topic       string
	clientGroup string
	messageType string
	tag         string
	errorRate   float64
}

func (s *RocketMQMessagingScenario) Name() string      { return rocketmqMessagingName }
func (s *RocketMQMessagingScenario) Type() tdg.DataType { return tdg.DataTypeTraces }

func (s *RocketMQMessagingScenario) Init(cfg map[string]interface{}) error {
	s.topic = tdg.ParseString(cfg, "topic", "payment-topic")
	s.clientGroup = tdg.ParseString(cfg, "client_group", "account-consumer-group")
	s.messageType = tdg.ParseString(cfg, "message_type", "transaction")
	s.tag = tdg.ParseString(cfg, "tag", "pay-success")
	s.errorRate = tdg.ParseFloat64(cfg, "error_rate", 0.05)
	return nil
}

func (s *RocketMQMessagingScenario) GenerateTraces() (ptrace.Traces, error) {
	r := rand.New(rand.NewSource(time.Now().UnixNano()))
	paymentID := fmt.Sprintf("PAY-%d", 200000+r.Intn(800000))
	accountID := fmt.Sprintf("ACC-%d", 10000+r.Intn(90000))

	// 构建调用树：payment-service 处理支付 → RocketMQ → account-service 更新余额
	root := tdg.GRPCServerCase("payment-service", "PaymentService", "ProcessPayment")
	tdg.WithAttributes(root, map[string]string{
		"payment.id": paymentID,
	})

	// RocketMQ 发送事务消息
	rocketSend := tdg.RocketMQSendCase("payment-service", s.topic, s.clientGroup, s.messageType, s.tag)
	tdg.WithAttributes(rocketSend, map[string]string{
		"messaging.message.id": fmt.Sprintf("RMQID_%016X", r.Int63()),
	})

	// RocketMQ consumer 侧
	rocketReceive := tdg.RocketMQReceiveCase("account-service", s.topic, s.clientGroup)
	rocketReceive.Children = []*tdg.FlowStep{
		tdg.InternalCase("account-service", "updateBalance", map[string]string{
			"account.id": accountID,
			"payment.id": paymentID,
		}),
		tdg.MySQLCase("account-service", "account_db", "UPDATE", "accounts",
			fmt.Sprintf("UPDATE accounts SET balance = balance - ? WHERE account_id = '%s'", accountID)),
		tdg.MySQLCase("account-service", "account_db", "INSERT", "transactions",
			fmt.Sprintf("INSERT INTO transactions (account_id, payment_id, type, amount) VALUES ('%s', '%s', 'DEBIT', ?)", accountID, paymentID)),
	}

	rocketSend.Children = []*tdg.FlowStep{rocketReceive}

	root.Children = []*tdg.FlowStep{
		tdg.InternalCase("payment-service", "validatePayment", map[string]string{
			"payment.id": paymentID,
		}),
		tdg.MySQLCase("payment-service", "payment_db", "INSERT", "payments",
			fmt.Sprintf("INSERT INTO payments (payment_id, status) VALUES ('%s', 'SUCCESS')", paymentID)),
		rocketSend,
	}

	td := tdg.ExecuteFlow([]*tdg.FlowStep{root}, s.errorRate)
	return td, nil
}
