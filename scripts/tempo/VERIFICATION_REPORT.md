# Tempo (Grafana Explore) — Verification Report

**Date:** 2026-08-07
**Collector:** `custom-otlp-collector` (namespace `default`, image `custom-otlp-collector:latest-arm64`)
**Cluster:** minikube
**Method:** source analysis (design doc `docs/2026-07-10/tempo-api-design.md` + `extension/adminext/tempo_handler.go`) crossed against **live probes** of the running collector, then fixes + re-verification.

Endpoint prefix under test: `http://<collector>:8088/api/v2/tempo` (Grafana Tempo datasource URL). Auth: `X-API-Key: your-api-key-1` (admin API `api_key` type).

## 1. Results matrix (before → after)

| # | Explore Tempo scenario | Endpoint | Before | After |
|---|------------------------|----------|--------|-------|
| 1 | Health check | `GET /api/echo` | 200 ✅ | 200 ✅ |
| 2 | Version probe | `GET /api/status/buildinfo` | 200 ✅ | 200 ✅ |
| 3 | Trace search (V1) | `GET /api/search?q=...` | 200 ✅ | 200 ✅ |
| 4 | Trace search (V2) | `GET /api/v2/search?q=...` | **binary protobuf ❌** | **JSON ✅ (BUG 1)** |
| 5 | Browse tags (V1) | `GET /api/search/tags` | 200 ✅ | 200 ✅ |
| 6 | Browse tags (V2) | `GET /api/v2/search/tags` | 200 ✅ | 200 ✅ |
| 7 | Tag values (V1) | `GET /api/search/tag/service.name/values` | **empty `[]` ❌** | **9 values ✅ (BUG 2)** |
| 8 | Tag values (V2) | `GET /api/v2/search/tag/resource.service.name/values` | 200 ✅ | 200 ✅ |
| 9 | View trace (V1, JSON) | `GET /api/traces/{id}` | 200 (attr values corrupted ⚠️) | 200 (unchanged — see BUG 5) |
| 10 | View trace (V2, protobuf) | `GET /api/v2/traces/{id}` | 200 ✅ (correct) | 200 ✅ |
| 11 | TraceQL metrics (standard) | `GET /api/metrics/query_range?q=rate({...}[5m])` | **400 ❌** | **series ✅ (BUG 3)** |
| 12 | TraceQL metrics (pipeline) | `...q={...}\|rate()` | 200 ✅ | 200 ✅ |
| 13 | Time range format RFC3339 | any search `start=`/`end=` | **silently ignored ❌** | **parsed ✅ (BUG 4)** |

## 2. Gaps found & fixed

### BUG 1 — `/api/v2/search` returned protobuf, not JSON
`handleTempoV2Search` fetched full traces and serialized them as OTLP **protobuf**
(`application/protobuf`). Tempo's V2 *search* endpoint returns the **same JSON** as
V1 search; only `/api/v2/traces/{id}` returns protobuf. Grafana 12+ defaults to the
V2 search path, so it received a binary body and could not render results.
**Fix:** `handleTempoV2Search` now builds the standard `tempoSearchResponse` JSON
(reusing `convertTraceSummaryToTempoSearchTrace`), matching V1.

### BUG 2 — V1 tag values `service.name` returned empty
`resolveTagValues` used `parseTimeRange` (observability_handler_v2.go), whose default
window is **1 hour**. The V2 tag-values handler uses `parseTempoTagValuesTimeRange`
(default **7 days**), which is why V2 returned 9 services while V1 returned `[]`.
**Fix:** `resolveTagValues` now uses `parseTempoTagValuesTimeRange` (7-day default),
consistent with V2.

### BUG 3 — standard TraceQL metrics syntax rejected
Grafana's TraceQL metrics panel / service map emits `rate({...}[5m])`
(function-prefix + range selector). The collector's TraceQL parser only tokenizes the
**pipeline** form `{...} | rate()`; the lexer has no `[`/`]` token, so `rate({...}[5m])`
failed with `lexer error: unexpected character '['`.
**Fix:** added `normalizeTraceQLMetricsQuery` which rewrites the standard form into the
pipeline form before parsing (the `[5m]` range is redundant — step is derived from the
query time range):
- `rate({...}[5m])` → `{...} | rate()`
- `count_over_time({...}[5m])` → `{...} | count_over_time()`
- `quantile_over_time({...}[5m], 0.95)` → `{...} | quantile_over_time(duration, 0.95)`
- `histogram_over_time({...}[5m])` → `{...} | histogram_over_time(duration)`
- `... by (label)` clauses are preserved.
Applied at all `traceql.Parse` call sites (search metrics path, `query_range`,
tag-value filter). Non-matching queries are returned unchanged.

### BUG 4 — RFC3339 time range silently ignored
`parseTempoTimeRange` only parsed unix-seconds floats. Grafana may send RFC3339
(`2026-08-07T10:00:00Z`); the parse failed and the handler fell back to the default
window, so the requested time range was silently dropped. (The observability V2 API's
`parseTimeParam` already handled this — Tempo's own parser was weaker.)
**Fix:** extracted `parseTempoTimeParam`, which tries float-seconds then RFC3339 /
RFC3339Nano, used by `parseTempoTimeRange`.

## 3. Known gap NOT fixed this round (BUG 5)

**V1 `/api/traces/{id}` attribute values are corrupted.** The response body contains
Go struct dumps like `"stringValue":"{0x4001e10360 <nil> <nil> ...}"` inside span
attribute values. `publicAnyValueToTempo` itself is correct; the corruption originates
in the **ES→OTLP `AnyValue` conversion for the full-trace (`GetTrace`) path** — the
summary path (`SearchTraceSummaries`) serializes attributes correctly, which is why
search results look fine but trace detail views do not. This is a deeper conversion
bug in the storage layer (outside `tempo_handler.go`) and needs a targeted follow-up;
it does not affect search, tags, metrics, or V2 protobuf traces.

## 4. Deployment & re-verification
```
export DOCKER_CONTEXT=minikube && make docker-build
kubectl rollout restart deployment/custom-otlp-collector -n default
```
All four fixes (BUG 1–4) confirmed **live** post-redeploy (see §1 before→after).

## 5. Files changed
- `extension/adminext/tempo_handler.go`
  - `handleTempoV2Search` → JSON response (BUG 1)
  - `resolveTagValues` → 7-day default window (BUG 2)
  - `parseTempoTimeRange` + new `parseTempoTimeParam` → RFC3339 support (BUG 4)
  - `normalizeTraceQLMetricsQuery` + `metricFnPrefixRe` → standard metrics syntax (BUG 3)
  - applied normalizer at all `traceql.Parse` sites
- `extension/adminext/tempo_handler_test.go` — `TestNormalizeTraceQLMetricsQuery`, `TestParseTempoTimeParam`

## 6. Reproduction
```bash
B=http://custom-otlp-collector.default.svc.cluster.local:8088/api/v2/tempo
H="-H X-API-Key: your-api-key-1"   # NOTE: X-API-Key MUST be quoted with a space: -H "X-API-Key: your-api-key-1"
S=$(date -d '2 hours ago' +%s); E=$(date +%s)
curl "$B/api/v2/search?q=%7B%7D&limit=2&start=$S&end=$E" $H          # BUG1: JSON now
curl "$B/api/search/tag/service.name/values?start=$S&end=$E" $H     # BUG2: 9 values
curl -G --data-urlencode 'q=rate({resource.service.name="java-user-service"}[5m])' \
    --data-urlencode "start=$S" --data-urlencode "end=$E" --data-urlencode "step=60" \
    "$B/api/metrics/query_range" $H                                   # BUG3: series
curl "$B/api/search?q=%7B%7D&start=2026-08-06T00:00:00Z&end=2026-08-06T01:00:00Z" $H  # BUG4: RFC3339
go test -count=1 -run 'TestNormalizeTraceQLMetricsQuery|TestParseTempoTimeParam' ./extension/adminext/
```
