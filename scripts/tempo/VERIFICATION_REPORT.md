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

## 3. BUG 5 — fixed (V1 trace detail resource attributes were Go struct dumps)

**Symptom (visible in Grafana Explore):** opening a trace in Explore showed every
**resource** attribute (`service.name`, `os.type`, `container.id`, `process.*`,
`host.*`, `app_id`, ...) as a Go dump like
`"stringValue":"{0x400060dc10 <nil> <nil> <nil> <nil> <nil> <nil>}"` instead of the
real value. Span attributes were fine; only resource attributes were corrupted.

**Root cause:** `convertToTempoTrace` built a `map[string]any` of resource attrs by
copying `span.Resource` entries (`resAttrs[kv.Key] = kv.Value`) — but `kv.Value` is
already an `observabilitystorageext.AnyValue` STRUCT (converted from the stored
`map[string]any` by `StoredSpanToPublic`). It then passed that map through
`mapToTempoKeyValues` → `anyToTempoValue`, whose `default` case does
`fmt.Sprintf("%v", val)` on the `AnyValue` struct → the `{0x... <nil> ...}` dump.
Span attributes avoided this because they go through `publicKeyValuesToTempo`
(which calls `publicAnyValueToTempo`, the correct `AnyValue`-aware converter).

**Fix:** `convertToTempoTrace` now keeps `span.Resource` (`[]KeyValue`) as-is and
serializes it with `publicKeyValuesToTempo`, the same correct path span attributes use.
**Test:** `TestConvertToTempoTrace_ResourceAttrsNotDumped`.

## 4. Verification — ALL through the Grafana proxy (the real Explore path)

The Grafana Tempo datasource (id=7, type=tempo, url `...:8088/api/v2/tempo`) proxies
to the collector and forwards `X-API-Key` (configured via `httpHeaderName1` +
`secureJsonData.httpHeaderValue1`). All fixes verified live through
`http://grafana.istio-system.svc.cluster.local:3000/api/datasources/proxy/7/...`:

| # | Scenario | Before | After (via Grafana proxy) |
|---|----------|--------|---------------------------|
| 1 | `/api/echo` | 200 | 200 ✅ |
| 2 | `/api/status/buildinfo` | 200 | 200 ✅ |
| 3 | V1 `/api/search` | 200 | 200 ✅ |
| 4 | V2 `/api/v2/search` (BUG 1) | binary protobuf | **JSON `{"traces":[...]}`** ✅ |
| 5 | V1 tag values (BUG 2) | empty `[]` | **9 values** ✅ |
| 6 | metrics `rate({...}[5m])` (BUG 3) | 400 | **series** ✅ |
| 7 | RFC3339 time (BUG 4) | silently ignored | **parsed** ✅ |
| 8 | V2 trace detail (protobuf) | 200 | 200 ✅ |
| 9 | V1 trace detail resource attrs (BUG 5) | `{0x... <nil>...}` dumps | **`service.name`=test-java-gateway-service, `os.type`=linux** ✅ |

(Initial verification was direct-to-collector only; the Grafana-proxy pass above
was added after the user correctly pointed out the real production path wasn't tested.)

## 5. Deployment & re-verification
```
export DOCKER_CONTEXT=minikube && make docker-build
kubectl rollout restart deployment/custom-otlp-collector -n default
```
All five fixes (BUG 1–5) confirmed **live through the Grafana proxy** post-redeploy.

## 6. Files changed
- `extension/adminext/tempo_handler.go`
  - `handleTempoV2Search` → JSON response (BUG 1)
  - `resolveTagValues` → 7-day default window (BUG 2)
  - `parseTempoTimeRange` + new `parseTempoTimeParam` → RFC3339 support (BUG 4)
  - `normalizeTraceQLMetricsQuery` + `metricFnPrefixRe` → standard metrics syntax (BUG 3)
  - `convertToTempoTrace` → route resource attrs through `publicKeyValuesToTempo` (BUG 5)
  - applied metrics normalizer at all `traceql.Parse` sites
- `extension/adminext/tempo_handler_test.go` — `TestNormalizeTraceQLMetricsQuery`, `TestParseTempoTimeParam`, `TestConvertToTempoTrace_ResourceAttrsNotDumped`

## 7. Reproduction (via Grafana proxy — the real Explore path)
```bash
G=http://grafana.istio-system.svc.cluster.local:3000/api/datasources/proxy/7
# NOTE: X-API-Key is forwarded by Grafana from the datasource config; if probing
# the collector directly instead, add -H "X-API-Key: your-api-key-1".
S=$(date -d '2 hours ago' +%s); E=$(date +%s)
curl -G "$G/api/v2/search" --data-urlencode "q={}" --data-urlencode "start=$S" --data-urlencode "end=$E"          # BUG1
curl -G "$G/api/search/tag/service.name/values" --data-urlencode "start=$S" --data-urlencode "end=$E"             # BUG2
curl -G "$G/api/metrics/query_range" --data-urlencode 'q=rate({resource.service.name="java-user-service"}[5m])' \
    --data-urlencode "start=$S" --data-urlencode "end=$E" --data-urlencode "step=60"                              # BUG3
curl -G "$G/api/search" --data-urlencode "q={}" --data-urlencode "start=2026-08-06T00:00:00Z" \
    --data-urlencode "end=2026-08-06T01:00:00Z"                                                                  # BUG4
TID=$(curl -G "$G/api/search" --data-urlencode 'q={resource.service.name="java-user-service"}' --data-urlencode limit=1 | python3 -c "import sys,json;print(json.load(sys.stdin)['traces'][0]['traceID'])")
curl "$G/api/traces/$TID" | python3 -c "import sys; b=sys.stdin.read(); print('dump present:', '{0x' in b)"      # BUG5 -> dump present: False
go test -count=1 -run 'TestConvertToTempoTrace_ResourceAttrsNotDumped|TestNormalizeTraceQLMetricsQuery|TestParseTempoTimeParam' ./extension/adminext/
```
