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

	// ================================================================
	// L1: api-gateway HTTP 入口
	// ================================================================
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

	// L2: gateway 中间件
	gatewayAuth := MiddlewareCase("api-gateway", "cors", map[string]string{
		"cors.origin": "*",
	})
	gatewayRateLimit := MiddlewareCase("api-gateway", "rateLimit", map[string]string{
		"rate_limit.bucket": "auth_api",
	})

	// L2: gRPC: gateway → auth-service
	gatewayToAuth := GRPCCallCase("api-gateway", "auth-service", "AuthService", "Login", 50052)
	authServer := gatewayToAuth.Children[0]
	WithAttributes(authServer, map[string]string{"user.id": userID})
	WithResourceAttributes(authServer, map[string]string{"service.version": "1.8.0"})
	WithDuration(authServer, 15, 70)

	// ================================================================
	// auth-service 内部分层
	// ================================================================

	// L4: Service 层 - AuthService.authenticate
	authService := ServiceMethodCase("auth-service", "AuthService.authenticate", map[string]string{
		"user.id": userID,
	})
	WithDuration(authService, 10, 50)

	// L5: SessionRepository.findExisting → Redis（防重复登录）
	sessionRepo := RepositoryCase("auth-service", "SessionRepository.findExisting", map[string]string{
		"user.id": userID,
	})
	sessionRepo.Children = []*FlowStep{
		RedisCase("auth-service", "GET", fmt.Sprintf("GET user:session:%s", userID)),
	}

	// L5: UserRepository.findById → MySQL
	userRepo := RepositoryCase("auth-service", "UserRepository.findById", nil)
	userRepo.Children = []*FlowStep{
		MySQLCase("auth-service", "auth_db", "SELECT", "users",
			fmt.Sprintf("SELECT id, username, password_hash, status FROM users WHERE id = '%s' AND status = 'active'", userID)),
	}

	// L5: 密码验证
	verifyPassword := InternalCase("auth-service", "verifyPassword", map[string]string{
		"auth.method": "bcrypt",
	})

	// L5: TokenService.generateToken
	tokenService := ServiceMethodCase("auth-service", "TokenService.generateToken", map[string]string{
		"auth.token_type": "JWT",
		"auth.expires_in": "3600",
	})

	// L5: SessionRepository.save → Redis
	sessionSave := RepositoryCase("auth-service", "SessionRepository.save", nil)
	sessionSave.Children = []*FlowStep{
		RedisCase("auth-service", "SET",
			fmt.Sprintf("SET user:session:%s %s EX 3600", userID, sessionID)),
	}

	// ================================================================
	// L5: auth-service → audit-service（gRPC 调用对）
	// ================================================================
	authToAudit := GRPCCallCase("auth-service", "audit-service", "AuditService", "LogEvent", 50054)
	auditServer := authToAudit.Children[0]
	WithAttributes(auditServer, map[string]string{
		"audit.event":   "user_login",
		"audit.user_id": userID,
		"audit.ip":      loginIP,
	})
	WithResourceAttributes(auditServer, map[string]string{"service.version": "1.1.0"})

	// audit-service 内部分层
	auditService := ServiceMethodCase("audit-service", "AuditService.logLoginEvent", map[string]string{
		"audit.event":   "user_login",
		"audit.user_id": userID,
	})

	auditRepo := RepositoryCase("audit-service", "AuditRepository.save", nil)
	auditRepo.Children = []*FlowStep{
		MongoDBCase("audit-service", "audit_db", "login_events", "insert",
			fmt.Sprintf(`{"insert": "login_events", "documents": [{"user_id": "%s", "ip": "%s", "action": "login"}]}`, userID, loginIP)),
	}
	auditService.Children = []*FlowStep{auditRepo}
	auditServer.Children = []*FlowStep{auditService}

	// 组装 auth-service Service 层
	authService.Children = []*FlowStep{
		sessionRepo,
		userRepo,
		verifyPassword,
		tokenService,
		sessionSave,
		authToAudit,
	}

	authServer.Children = []*FlowStep{authService}

	root.Children = []*FlowStep{
		gatewayAuth,
		gatewayRateLimit,
		gatewayToAuth,
	}

	td := ExecuteFlow([]*FlowStep{root}, f.errorRate)
	return td, nil
}