// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package victoriametrics

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// max64 returns the larger of two int64 values.
func max64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}

// VMClient is the HTTP surface to VictoriaMetrics. The writer and reader depend
// on this interface, not on the concrete HTTP client, so tests inject fakes.
type VMClient interface {
	// ImportText posts Prometheus text exposition format (sample rows plus
	// optional # TYPE / # HELP metadata rows) to /api/v1/import/prometheus.
	// VM stores the metadata natively, which /api/v1/metadata reads back.
	ImportText(ctx context.Context, body []byte) error

	// MetricMetadata reads VM's native metric metadata (type/help per family)
	// via /api/v1/metadata. metricName scopes to one family; empty = all.
	MetricMetadata(ctx context.Context, metricName string) (map[string]VMMeta, error)

	// QueryInstant executes an instant query (/api/v1/query).
	QueryInstant(ctx context.Context, query string, ts time.Time) (*VMSeriesList, error)

	// QueryRange executes a range query (/api/v1/query_range).
	QueryRange(ctx context.Context, query string, start, end time.Time, step time.Duration) (*VMSeriesList, error)

	// Export fetches raw series via /api/v1/export (JSON line stream, one
	// series per line with values/timestamps arrays). Not subject to VM's
	// query latencyOffset, so data is visible immediately after write.
	Export(ctx context.Context, match []string, start, end time.Time) ([]VMExportSeries, error)

	// LabelValues lists values of a label via /api/v1/label/{name}/values,
	// optionally constrained by match[] selectors.
	LabelValues(ctx context.Context, label string, match []string, start, end time.Time) ([]string, error)

	// LabelNames lists label names via /api/v1/labels.
	LabelNames(ctx context.Context, match []string, start, end time.Time) ([]string, error)

	// Series lists matching label sets via /api/v1/series.
	Series(ctx context.Context, match []string, start, end time.Time) ([]map[string]string, error)

	// Health probes /health.
	Health(ctx context.Context) error

	// ForAccount returns a client scoped to a VictoriaMetrics account (native
	// multitenancy). The base client (account 0) is used when account scoping is
	// disabled.
	ForAccount(accountID uint32) VMClient
}

// VMSeriesList is a Prometheus-format query result.
type VMSeriesList struct {
	ResultType string
	Series     []VMSeries
}

// VMSeries is one series in a query result.
type VMSeries struct {
	Metric map[string]string `json:"metric"`
	// Values is [ts, "value"] pairs for matrix results.
	Values [][2]any `json:"values,omitempty"`
	// Value is a single [ts, "value"] pair for vector results.
	Value *[2]any `json:"value,omitempty"`
}

// VMExportSeries is one line of the /api/v1/export JSON line stream.
type VMExportSeries struct {
	Metric     map[string]string `json:"metric"`
	Values     []float64         `json:"values"`
	Timestamps []int64           `json:"timestamps"` // milliseconds
}

// httpVMClient is the real VMClient over net/http.
type httpVMClient struct {
	writeBase    string
	readBase     string
	httpClient   *http.Client
	readClient   *http.Client
	maxRetries   int
	accountScope bool   // native multitenancy: /select/<acct>/ + /insert/<acct>/
	accountID    uint32 // set via ForAccount
}

// newHTTPVMClient builds the real client. Write and read use separate
// http.Clients so their timeouts stay independent.
func newHTTPVMClient(cfg *Config) *httpVMClient {
	return &httpVMClient{
		writeBase:    strings.TrimRight(cfg.writeBase(), "/"),
		readBase:     strings.TrimRight(cfg.readBase(), "/"),
		httpClient:   &http.Client{Timeout: cfg.WriteTimeout},
		readClient:   &http.Client{Timeout: cfg.ReadTimeout},
		maxRetries:   cfg.MaxRetries,
		accountScope: cfg.AccountScope,
	}
}

// ForAccount returns a shallow copy scoped to the given VM account. The
// underlying http.Clients are shared (safe for concurrent use).
func (c *httpVMClient) ForAccount(accountID uint32) VMClient {
	cc := *c
	cc.accountID = accountID
	return &cc
}

// writePath builds the write URL. Under account scoping the account is injected
// as /insert/<accountID>/prometheus before the suffix; otherwise the single-
// tenant vmsingle path is used verbatim.
func (c *httpVMClient) writePath(suffix string) string {
	if c.accountScope {
		return fmt.Sprintf("%s/insert/%d/prometheus%s", c.writeBase, c.accountID, suffix)
	}
	return c.writeBase + suffix
}

// readPath builds the read URL. Under account scoping the account is injected as
// /select/<accountID>/prometheus before the suffix; otherwise the single-tenant
// vmsingle path is used verbatim.
func (c *httpVMClient) readPath(suffix string) string {
	if c.accountScope {
		return fmt.Sprintf("%s/select/%d/prometheus%s", c.readBase, c.accountID, suffix)
	}
	return c.readBase + suffix
}

// VMMeta is one metric family's native metadata from VM.
type VMMeta struct {
	Type string `json:"type"`
	Help string `json:"help"`
}

func (c *httpVMClient) ImportText(ctx context.Context, lines []byte) error {
	if len(lines) == 0 {
		return nil
	}
	var lastErr error
	for attempt := 0; attempt <= c.maxRetries; attempt++ {
		if attempt > 0 {
			// Exponential backoff: 250ms, 500ms, 1s...
			select {
			case <-time.After(time.Duration(1<<(attempt-1)) * 250 * time.Millisecond):
			case <-ctx.Done():
				return ctx.Err()
			}
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodPost,
			c.writePath("/api/v1/import/prometheus"), bytes.NewReader(lines))
		if err != nil {
			return err
		}
		resp, err := c.httpClient.Do(req)
		if err != nil {
			lastErr = fmt.Errorf("vm import request failed: %w", err)
			continue // network error → retry
		}
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		resp.Body.Close()
		switch {
		case resp.StatusCode >= 200 && resp.StatusCode < 300:
			return nil
		case resp.StatusCode >= 400 && resp.StatusCode < 500:
			// 4xx = data format bug; retrying the same payload cannot succeed.
			return fmt.Errorf("vm import rejected (status %d, no retry): %s", resp.StatusCode, strings.TrimSpace(string(body)))
		default:
			lastErr = fmt.Errorf("vm import failed (status %d): %s", resp.StatusCode, strings.TrimSpace(string(body)))
		}
	}
	return lastErr
}

// promResponse is the shared Prometheus JSON envelope.
type promResponse struct {
	Status string          `json:"status"`
	Error  string          `json:"error,omitempty"`
	Data   json.RawMessage `json:"data"`
}

func (c *httpVMClient) QueryInstant(ctx context.Context, query string, ts time.Time) (*VMSeriesList, error) {
	q := url.Values{}
	q.Set("query", query)
	if !ts.IsZero() {
		q.Set("time", strconv.FormatInt(ts.Unix(), 10))
	}
	var out struct {
		ResultType string      `json:"resultType"`
		Result     []VMSeries  `json:"result"`
	}
	if err := c.promGet(ctx, "/api/v1/query", q, &out); err != nil {
		return nil, err
	}
	return &VMSeriesList{ResultType: out.ResultType, Series: out.Result}, nil
}

func (c *httpVMClient) QueryRange(ctx context.Context, query string, start, end time.Time, step time.Duration) (*VMSeriesList, error) {
	q := url.Values{}
	q.Set("query", query)
	q.Set("start", strconv.FormatInt(start.Unix(), 10))
	q.Set("end", strconv.FormatInt(end.Unix(), 10))
	q.Set("step", strconv.FormatInt(max64(step.Milliseconds()/1000, 1), 10)+"s")
	var out struct {
		ResultType string     `json:"resultType"`
		Result     []VMSeries `json:"result"`
	}
	if err := c.promGet(ctx, "/api/v1/query_range", q, &out); err != nil {
		return nil, err
	}
	return &VMSeriesList{ResultType: out.ResultType, Series: out.Result}, nil
}

func (c *httpVMClient) Export(ctx context.Context, match []string, start, end time.Time) ([]VMExportSeries, error) {
	q := url.Values{}
	for _, m := range match {
		q.Add("match[]", m)
	}
	q.Set("start", strconv.FormatInt(start.Unix(), 10))
	q.Set("end", strconv.FormatInt(end.Unix(), 10))
	q.Set("format", "json")

	u := c.readPath("/api/v1/export?") + q.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.readClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("vm export request failed: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return nil, fmt.Errorf("vm export failed (status %d): %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	var series []VMExportSeries
	dec := json.NewDecoder(resp.Body)
	for {
		var s VMExportSeries
		if err := dec.Decode(&s); err != nil {
			if err == io.EOF {
				break
			}
			return nil, fmt.Errorf("vm export decode failed: %w", err)
		}
		series = append(series, s)
	}
	return series, nil
}

func (c *httpVMClient) LabelValues(ctx context.Context, label string, match []string, start, end time.Time) ([]string, error) {
	q := url.Values{}
	for _, m := range match {
		q.Add("match[]", m)
	}
	q.Set("start", strconv.FormatInt(start.Unix(), 10))
	q.Set("end", strconv.FormatInt(end.Unix(), 10))
	var out []string
	if err := c.promGet(ctx, "/api/v1/label/"+url.PathEscape(label)+"/values", q, &out); err != nil {
		return nil, err
	}
	return out, nil
}

func (c *httpVMClient) LabelNames(ctx context.Context, match []string, start, end time.Time) ([]string, error) {
	q := url.Values{}
	for _, m := range match {
		q.Add("match[]", m)
	}
	q.Set("start", strconv.FormatInt(start.Unix(), 10))
	q.Set("end", strconv.FormatInt(end.Unix(), 10))
	var out []string
	if err := c.promGet(ctx, "/api/v1/labels", q, &out); err != nil {
		return nil, err
	}
	return out, nil
}

func (c *httpVMClient) Series(ctx context.Context, match []string, start, end time.Time) ([]map[string]string, error) {
	q := url.Values{}
	for _, m := range match {
		q.Add("match[]", m)
	}
	q.Set("start", strconv.FormatInt(start.Unix(), 10))
	q.Set("end", strconv.FormatInt(end.Unix(), 10))
	var out []map[string]string
	if err := c.promGet(ctx, "/api/v1/series", q, &out); err != nil {
		return nil, err
	}
	return out, nil
}

func (c *httpVMClient) Health(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.readBase+"/health", nil)
	if err != nil {
		return err
	}
	resp, err := c.readClient.Do(req)
	if err != nil {
		return fmt.Errorf("vm health check failed: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("vm health endpoint returned status %d", resp.StatusCode)
	}
	return nil
}

// promGet issues a GET against the read endpoint and decodes the Prometheus
// envelope {"status","data"} into out.
func (c *httpVMClient) promGet(ctx context.Context, path string, q url.Values, out any) error {
	u := c.readPath(path) + "?" + q.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return err
	}
	resp, err := c.readClient.Do(req)
	if err != nil {
		return fmt.Errorf("vm %s request failed: %w", path, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("vm %s failed (status %d): %s", path, resp.StatusCode, strings.TrimSpace(string(body)))
	}
	var env promResponse
	if err := json.NewDecoder(resp.Body).Decode(&env); err != nil {
		return fmt.Errorf("vm %s decode failed: %w", path, err)
	}
	if env.Status != "success" {
		return fmt.Errorf("vm %s returned status %q: %s", path, env.Status, env.Error)
	}
	if err := json.Unmarshal(env.Data, out); err != nil {
		return fmt.Errorf("vm %s data decode failed: %w", path, err)
	}
	return nil
}


// MetricMetadata reads VM's native metric metadata via /api/v1/metadata.
// The response shape is {"status","data":{family:[{type,help}]}}.
func (c *httpVMClient) MetricMetadata(ctx context.Context, metricName string) (map[string]VMMeta, error) {
	q := url.Values{}
	if metricName != "" {
		q.Set("metric", metricName)
	}
	var out map[string][]VMMeta
	if err := c.promGet(ctx, "/api/v1/metadata", q, &out); err != nil {
		return nil, err
	}
	res := make(map[string]VMMeta, len(out))
	for family, items := range out {
		if len(items) > 0 {
			res[family] = items[0]
		}
	}
	return res, nil
}
