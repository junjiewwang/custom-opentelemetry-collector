// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package instrumentationmanager

import (
	"context"
	"strings"
	"unicode"
)

const (
	// ActorTypeMCP marks calls originating from the MCP tools.
	ActorTypeMCP = "mcp"
	// ActorTypeAdmin marks calls originating from the admin HTTP API.
	ActorTypeAdmin = "admin"

	// SystemActorID is used for audit entries produced by background workers.
	SystemActorID = "system"

	maxActorIDLength = 128
)

// Actor identifies who initiated a rule change. ID must be a non-sensitive
// identifier derived by the caller (never a raw API key). TenantID is kept
// separate from ID: a tenant is not an operator.
type Actor struct {
	ID       string
	Type     string
	TenantID string
}

type actorContextKey struct{}

// WithActor attaches the authoritative caller identity to a context.
func WithActor(ctx context.Context, actor Actor) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, actorContextKey{}, Actor{
		ID:       normalizeActorID(actor.ID),
		Type:     strings.TrimSpace(actor.Type),
		TenantID: normalizeActorID(actor.TenantID),
	})
}

// ActorFromContext returns false when no usable identity is present.
func ActorFromContext(ctx context.Context) (Actor, bool) {
	if ctx == nil {
		return Actor{}, false
	}
	actor, ok := ctx.Value(actorContextKey{}).(Actor)
	if !ok || actor.ID == "" {
		return Actor{}, false
	}
	return actor, true
}

// normalizeActorID prevents oversized values and log-injection payloads from
// being persisted through request bodies.
func normalizeActorID(id string) string {
	cleaned := strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, strings.TrimSpace(id))
	runes := []rune(cleaned)
	if len(runes) > maxActorIDLength {
		return string(runes[:maxActorIDLength])
	}
	return cleaned
}

// ruleActorID returns the authoritative identity for rule fields. The context
// always wins; the request-supplied value is only a compatibility fallback for
// internal callers that do not inject an actor.
func ruleActorID(ctx context.Context, fallback string) string {
	if actor, ok := ActorFromContext(ctx); ok {
		return actor.ID
	}
	return normalizeActorID(fallback)
}

// auditActorID attributes background work to the system.
func auditActorID(ctx context.Context) string {
	if actor, ok := ActorFromContext(ctx); ok {
		return actor.ID
	}
	return SystemActorID
}

// withAuditOperator stamps audit entries at the aggregation points so the rule
// detail and the audit trail always report the same operator.
func withAuditOperator(ctx context.Context, audits []*RuleAuditEntry) []*RuleAuditEntry {
	operator := auditActorID(ctx)
	for _, audit := range audits {
		if audit != nil && audit.Operator == "" {
			audit.Operator = operator
		}
	}
	return audits
}
