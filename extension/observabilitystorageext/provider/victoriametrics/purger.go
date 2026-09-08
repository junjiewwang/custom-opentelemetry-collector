// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package victoriametrics

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"go.opentelemetry.io/collector/custom/extension/observabilitystorageext/lifecycle"
	"go.uber.org/zap"
)

// Purger implements lifecycle.LifecyclePurger against VM's
// /api/v1/admin/tsdb/delete_series endpoint. VM retention is global
// (-retentionPeriod); this purger provides the per-app logical deletion the
// lifecycle scheduler expects, scoped by the app_id label every written
// series carries.
//
// Note: delete_series needs -deleteAuthKey unset (or matching) on the VM
// side; the minimal local vmsingle deployment has it disabled.
type Purger struct {
	client *httpVMClient
	logger *zap.Logger
}

// NewPurger builds the VM purger.
func NewPurger(client VMClient, logger *zap.Logger) *Purger {
	httpClient, ok := client.(*httpVMClient)
	if !ok {
		// Fake clients (tests) can't delete; return a purger that no-ops.
		return &Purger{logger: logger}
	}
	return &Purger{client: httpClient, logger: logger}
}

// PurgeExpired removes all metric data older than `before`.
func (p *Purger) PurgeExpired(ctx context.Context, signal lifecycle.SignalType, before time.Time) (*lifecycle.PurgeResult, error) {
	start := time.Time{}
	return p.delete(ctx, "", signal, start, before)
}

// PurgeByApp removes expired metric data scoped to one app (app_id label).
func (p *Purger) PurgeByApp(ctx context.Context, appID string, signal lifecycle.SignalType, before time.Time) (*lifecycle.PurgeResult, error) {
	return p.delete(ctx, appID, signal, time.Time{}, before)
}

// EstimatePurge is a preview; VM's delete API is fire-and-forget (no counts),
// so the estimate reports the API call that would run without executing it.
func (p *Purger) EstimatePurge(ctx context.Context, signal lifecycle.SignalType, before time.Time) (*lifecycle.PurgeEstimate, error) {
	return &lifecycle.PurgeEstimate{
		Signal:        signal,
		AffectedUnits: []string{"victoriametrics:delete_series"},
	}, nil
}

func (p *Purger) delete(ctx context.Context, appID string, signal lifecycle.SignalType, start, end time.Time) (*lifecycle.PurgeResult, error) {
	res := &lifecycle.PurgeResult{Signal: signal}
	if signal != lifecycle.SignalMetric {
		res.Message = "victoriametrics stores metrics only; nothing to purge"
		return res, nil
	}
	if p.client == nil {
		res.Message = "victoriametrics purger: client unavailable (test mode)"
		return res, nil
	}
	q := url.Values{}
	if appID != "" {
		q.Set("match[]", fmt.Sprintf(`{app_id=%q}`, appID))
	} else {
		q.Set("match[]", `{__name__!=""}`)
	}
	if !start.IsZero() {
		q.Set("start", strconv.FormatInt(start.Unix(), 10))
	}
	q.Set("end", strconv.FormatInt(end.Unix(), 10))
	u := p.client.readBase + "/api/v1/admin/tsdb/delete_series?" + q.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, nil)
	if err != nil {
		return nil, err
	}
	resp, err := p.client.readClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("vm delete_series request failed: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent && resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return nil, fmt.Errorf("vm delete_series failed (status %d): %s", resp.StatusCode, string(body))
	}
	return res, nil
}
