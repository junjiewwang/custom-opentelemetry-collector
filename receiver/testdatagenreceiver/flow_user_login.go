// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package testdatagenreceiver

import (
	"fmt"
	"math/rand"
	"time"

	"go.opentelemetry.io/collector/pdata/ptrace"
)

const userLoginFlowName = "user_login"

func init() {
	RegisterFlow(userLoginFlowName, func() BusinessFlow {
		return &UserLoginFlow{}
	})
}

// UserLoginFlow 用户登录业务流
//
// 模拟一次完整的用户登录流程：
//
//	用户请求 → api-gateway → auth-service → Redis(查session) → MySQL(查用户) → Redis(写session)
//	         → audit-service → MongoDB(写审计日志)
//
// 涉及 3 个微服务：api-gateway, auth-service, audit-service
type UserLoginFlow struct {
	errorRate float64
}

func (f *UserLoginFlow) Name() string {
	return userLoginFlowName
}

func (f *UserLoginFlow) Description() string {
	return "用户登录：gateway → auth-service → (Redis + MySQL) → audit-service → MongoDB"
}

func (f *UserLoginFlow) Init(cfg map[string]interface{}) error {
	f.errorRate = ParseFloat64(cfg, "error_rate", 0.03)
	return nil
}

func (f *UserLoginFlow) GenerateTraces() (ptrace.Traces, error) {
	r := rand.New(rand.NewSource(time.Now().UnixNano()))

	userID := fmt.Sprintf("USR-%d", 10000+r.Intn(90000))
	sessionID := fmt.Sprintf("sess_%s", randomHexStr(r, 32))
	loginIP := fmt.Sprintf("192.168.%d.%d", r.Intn(255), 1+r.Intn(254))
	userAgents := []string{
		"Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36",
		"Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36",
		"Mozilla/5.0 (Linux; Android 14) AppleWebKit/537.36",
		"Mozilla/5.0 (iPhone; CPU iPhone OS 17_0 like Mac OS X) AppleWebKit/605.1.15",
	}

	// 1. api-gateway HTTP 入口
	root := WithDuration(
		WithAttributes(
			HTTPServerCase("api-gateway", "POST", "/api/v1/auth/login", 443),
			map[string]string{
				"http.user_agent": userAgents[r.Intn(len(userAgents))],
				"http.client_ip":  loginIP,
			},
		),
		30, 100,
	)
	WithResourceAttributes(root, map[string]string{"service.version": "2.3.1"})

	// 2. gateway → auth-service（gRPC 调用对）
	gatewayToAuth := GRPCCallCase("api-gateway", "auth-service", "AuthService", "Login", 50052)
	authServer := gatewayToAuth.Children[0]
	WithAttributes(authServer, map[string]string{"user.id": userID})
	WithResourceAttributes(authServer, map[string]string{"service.version": "1.8.0"})
	WithDuration(authServer, 15, 70)

	// 3. auth-service 内部操作
	authServer.Children = []*FlowStep{
		// Redis: 查已有 session（防重复登录）
		RedisCase("auth-service", "GET", fmt.Sprintf("GET user:session:%s", userID)),
		// MySQL: 查询用户信息
		MySQLCase("auth-service", "auth_db", "SELECT", "users",
			fmt.Sprintf("SELECT id, username, password_hash, status FROM users WHERE id = '%s' AND status = 'active'", userID)),
		// 密码验证
		InternalCase("auth-service", "verifyPassword", map[string]string{
			"auth.method": "bcrypt",
		}),
		// JWT Token 生成
		InternalCase("auth-service", "generateToken", map[string]string{
			"auth.token_type": "JWT",
			"auth.expires_in": "3600",
		}),
		// Redis: 写入新 session
		RedisCase("auth-service", "SET",
			fmt.Sprintf("SET user:session:%s %s EX 3600", userID, sessionID)),
	}

	// 4. auth-service → audit-service（gRPC 调用对）
	authToAudit := GRPCCallCase("auth-service", "audit-service", "AuditService", "LogEvent", 50054)
	auditServer := authToAudit.Children[0]
	WithAttributes(auditServer, map[string]string{
		"audit.event":   "user_login",
		"audit.user_id": userID,
		"audit.ip":      loginIP,
	})
	WithResourceAttributes(auditServer, map[string]string{"service.version": "1.1.0"})

	// 5. audit-service: MongoDB 写入审计日志
	auditServer.Children = []*FlowStep{
		MongoDBCase("audit-service", "audit_db", "login_events", "insert",
			fmt.Sprintf(`{"insert": "login_events", "documents": [{"user_id": "%s", "ip": "%s", "action": "login"}]}`, userID, loginIP)),
	}

	// 将 audit 调用加入 auth-service
	authServer.Children = append(authServer.Children, authToAudit)

	root.Children = []*FlowStep{gatewayToAuth}

	td := ExecuteFlow([]*FlowStep{root}, f.errorRate)
	return td, nil
}