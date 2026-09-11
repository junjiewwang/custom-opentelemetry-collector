// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package victoriametrics

import (
	"context"
	"testing"

	"go.opentelemetry.io/collector/custom/extension/observabilitystorageext/tenantctx"
)

func TestWriteReadPathAccountScope(t *testing.T) {
	// account_scope off → vmsingle single-tenant path (no /insert|select/<acct>).
	off := &httpVMClient{writeBase: "http://vm:8428", readBase: "http://vm:8428"}
	if got, want := off.writePath("/api/v1/import/prometheus"), "http://vm:8428/api/v1/import/prometheus"; got != want {
		t.Fatalf("writePath(off) = %q, want %q", got, want)
	}
	if got, want := off.readPath("/api/v1/query"), "http://vm:8428/api/v1/query"; got != want {
		t.Fatalf("readPath(off) = %q, want %q", got, want)
	}

	// account_scope on → vm-cluster account-scoped path.
	on := &httpVMClient{writeBase: "http://vminsert:8480", readBase: "http://vmselect:8481", accountScope: true, accountID: 3}
	if got, want := on.writePath("/api/v1/import/prometheus"), "http://vminsert:8480/insert/3/prometheus/api/v1/import/prometheus"; got != want {
		t.Fatalf("writePath(on) = %q, want %q", got, want)
	}
	if got, want := on.readPath("/api/v1/query"), "http://vmselect:8481/select/3/prometheus/api/v1/query"; got != want {
		t.Fatalf("readPath(on) = %q, want %q", got, want)
	}
}

func TestForAccount(t *testing.T) {
	c := &httpVMClient{accountScope: true, accountID: 0}
	scoped, ok := c.ForAccount(5).(*httpVMClient)
	if !ok {
		t.Fatalf("ForAccount returned %T, want *httpVMClient", c.ForAccount(5))
	}
	if scoped.accountID != 5 {
		t.Fatalf("ForAccount accountID = %d, want 5", scoped.accountID)
	}
	if c.accountID != 0 {
		t.Fatalf("ForAccount mutated the original client: accountID = %d", c.accountID)
	}
}

func TestResolveAccountID(t *testing.T) {
	// Nil resolver → 0.
	if got, err := resolveAccountID(nil, context.Background(), "tn"); err != nil || got != 0 {
		t.Fatalf("nil resolver = (%d, %v), want (0, nil)", got, err)
	}
	// Empty tenant → 0 (never hits the resolver).
	called := false
	r := tenantctx.AccountResolver(func(_ context.Context, tenantID string) (uint32, error) {
		called = true
		return 7, nil
	})
	if got, _ := resolveAccountID(r, context.Background(), ""); got != 0 || called {
		t.Fatalf("empty tenant = %d, called=%v; want 0, false", got, called)
	}
	// Real tenant → resolver value.
	if got, _ := resolveAccountID(r, context.Background(), "tn"); got != 7 {
		t.Fatalf("resolver = %d, want 7", got)
	}
}
