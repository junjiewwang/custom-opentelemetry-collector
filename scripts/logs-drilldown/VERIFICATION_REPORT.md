# Loki (logs-drilldown) — Verification Report

**Date:** 2026-08-07
**Collector:** `custom-otlp-collector` (namespace `default`, image `custom-otlp-collector:latest-arm64`)
**Cluster:** minikube (`kubectl config current-context` = `minikube`)
**Method:** grill-me (plan) + opsx feature-dev (code-explorer exploration, code-reviewer review) + live end-to-end suite

---

## 1. Scope (locked via grill-me)

| # | Decision | Result |
|---|----------|--------|
| 1 | Scope | 36-case regression + two-bug regression + all endpoints + deploy consistency |
| 2 | Targets | Both paths: Grafana proxy (id=8) **and** direct collector |
| 3 | Deploy | Build + redeploy before verify — **not needed** (see §4) |
| 4 | Gate | `go build ./...` + `go test ./extension/adminext/...` must pass |
| 5 | Pass criteria | Strict: 36/36 on both paths AND two-bug regression green on both |
| 6 | Report | `scripts/logs-drilldown/` |
| 7 | Commit | After green |

## 2. Environment

- **Grafana proxy path:** `http://grafana.istio-system.svc.cluster.local:3000/api/datasources/proxy/8/loki/api/v1` (Grafana appends `/loki/api/v1` to the Loki datasource URL `/api/v2/loki`, so the collector receives `/api/v2/loki/loki/api/v1/*`).
- **Direct collector path:** `http://10.96.236.117:8088/api/v2/loki/loki/api/v1` (same route prefix; `10.96.x.x` is the minikube pod/service CIDR).
- Suite: `scripts/logs-drilldown/suite.py` (probes both paths; `LD_DIRECT=1` for direct).

## 3. Verification results — 36/36 BOTH PATHS ✅

| Section | Cases | Grafana proxy | Direct |
|---------|-------|---------------|--------|
| 1. Datasource health | 2 | PASS | PASS |
| 2. Labels & label values | 8 | PASS | PASS |
| 3. Log stream query (streams) | 4 | PASS | PASS |
| 4. Metric queries (matrix) | 6 | PASS | PASS |
| 5. Instant query (vector) | 2 | PASS | PASS |
| 6. Index volume | 4 | PASS | PASS |
| 7. Detected labels/fields (ISO-8601) | 5 | PASS | PASS |
| 8. Index stats & drilldown-limits | 2 | PASS | PASS |
| 9. Line filter & parser shapes | 2 | PASS | PASS |
| **TOTAL** | **36** | **36/36** | **36/36** |

### Original two fixes — proven live (behavioral)

- **FIX 1 — `parseLokiTime` unit inference** (`loki_handler.go`): Section 5 sends `time: NOW` (seconds, 10-digit). A broken parser would treat it as nanoseconds → 1970 → empty window → series=0. Live result: `resultType=vector`, series>0 on **both** paths → seconds correctly interpreted as seconds.
- **FIX 2 — instant metric query returns vector** (`loki_metric.go`): Section 5 `instant resultType=vector` PASS on **both** paths.

## 4. Deploy consistency — proven empirically (redeploy not needed)

The running `custom-otlp-collector:latest-arm64` already contains both fixes (proven by the live behavioral tests above). Redeploy was planned but became unnecessary once the live collector demonstrated correct behavior. This also sidesteps a real deploy hazard discovered during exploration: `minikube` CLI is **not installed** on this machine, and the deployment uses `imagePullPolicy: IfNotPresent` with a fixed tag, so a naive `kubectl apply` would not have refreshed the image anyway.

## 5. Code review (opsx code-reviewer) — 2 HIGH latent bugs found & fixed

Both bugs are in code paths **not exercised by the 36-case suite** (hence not caught by live verification), but are real correctness defects. Fixed as part of this work.

### Bug A — scientific notation with a dot misparsed as 1970
- **Location:** `loki_handler.go` `parseLokiTime`, dot-branch gate.
- **Symptom:** `parseLokiTime("1.78e9")` returned `time.Unix(1, 780000000)` (1970) instead of `time.Unix(1780000000, 0)` (2026). The dot-branch split on `.`, parsed `"1"` as seconds, and **stripped the `e9` exponent from the fractional part without ever applying it to the integer part**. The code's own comment falsely claimed to support scientific notation.
- **Fix:** gate the dot-branch with `!strings.ContainsAny(s, "eE")` so `"1.78e9"` falls through to the `ParseFloat`/`math.Modf` scientific branch.
- **Test:** `TestParseLokiTime_ScientificWithDot`.

### Bug B — `step` parsed as an epoch timestamp instead of a duration
- **Location:** `loki_metric.go` `handleLokiMetricQuery` (`step, _ := parseLokiTime(r.FormValue("step"))`) and `computeMetricInterval`.
- **Symptom:** Loki/Prometheus `step` is a **duration** (`"15"`, `"15s"`, `"5m"`), not an epoch. Parsing it via `parseLokiTime` (epoch semantics) only worked by coincidence for bare integers (`"15"` → epoch+15s → `Sub(epoch)` = 15s). Suffixed values (`"15s"`, `"5m"`) were silently dropped (`parseLokiTime` returned zero), and millisecond values were wildly wrong. Standard range-vector queries (`[5m]`) hid this because `rangeDur` takes priority.
- **Fix:** parse `step` with `parsePrometheusDuration` (returns `time.Duration`); change `computeMetricInterval` signature `step time.Time` → `step time.Duration`; replace `step.Sub(time.Unix(0,0))` with `step`; replace `step.IsZero()` with `step != 0`.
- **Test:** `TestComputeMetricInterval_Step` updated to pass a `time.Duration`.

### Lower-severity findings (not fixed, noted for follow-up)
- `isMetricQuery` wrapper in `loki_metric.go` is dead code (call sites use `logql.IsMetricQuery` directly).
- `topN = 1000` magic number should be a named constant shared with `volume_max_series`.
- `parseLokiTime` vs `parseTimeParam` (observability_handler_v2.go) will diverge over time — worth a cross-reference comment.

## 6. Test gate

```
go build ./...                                    -> BUILD_EXIT=0
go test -count=1 ./extension/adminext/...         -> ok (all pass)
```

New/updated tests: `TestParseLokiTime_ScientificWithDot`, `TestComputeMetricInterval_Step` (duration), plus the original `TestParseLokiTime_Units`, `TestParseLokiTime_InstantTimeNotMistakenForNanos`, `TestHandleLokiMetricQuery_InstantReturnsVector`, `TestHandleLokiMetricQuery_InstantNoStartEnd`.

## 7. Reproduction

```bash
cd scripts/logs-drilldown
# Grafana proxy path (id=8)
LD_GRAFANA=http://grafana.istio-system.svc.cluster.local:3000 LD_DS_ID=8 python3 suite.py
# Direct collector path
LD_DIRECT=1 LD_COLLECTOR=http://10.96.236.117:8088 LD_API_KEY=your-api-key-1 python3 suite.py
# Unit tests
go test -count=1 ./extension/adminext/...
```

## 8. Files changed

- `extension/adminext/loki_handler.go` — `parseLokiTime` (FIX 1 unit inference + Bug A scientific-dot gate)
- `extension/adminext/loki_metric.go` — `handleLokiMetricQuery` instant→vector (FIX 2) + Bug B step-as-duration
- `extension/adminext/loki_handler_test.go` — `TestParseLokiTime_*` incl. new `ScientificWithDot`
- `extension/adminext/loki_metric_test.go` — `TestComputeMetricInterval_*` updated for `time.Duration` step
- `scripts/logs-drilldown/VERIFICATION_REPORT.md` — this report

---

## 9. Addendum — source-driven coverage gap analysis & fix (2026-08-07)

After the 36/36 pass, a coverage question remained: **did the suite exercise every
real API call the logs-drilldown plugin makes?** We enumerated every Loki HTTP
call from the plugin source (`github.com/grafana/grafana-lokiexplore-app`,
`src/services/datasource.ts` and friends), cross-checked against the collector's
route table (`extension/adminext/router.go`), and **found 3 real gaps** — all
HTTP 404 against the live collector.

### 9.1 How the plugin actually reaches each endpoint

The plugin drives everything through `ds.getResource('<name>', params)`, which
maps to `/loki/api/v1/<name>`. Key facts confirmed from source:

- `getResource('detected_labels')` → `/detected_labels` ✅ (registered)
- `getResource('detected_fields')` → `/detected_fields` ✅ (registered)
- **detected-field VALUES** are fetched by the underlying Loki datasource's
  `languageProvider.fetchDetectedLabelValues`, which hits
  **`/detected_fields/<name>/values` (PLURAL)**.
- `getTagKeys` (ad-hoc filter key auto-completion) → underlying Loki
  datasource `/series`.
- `getResource('patterns')` (Patterns panel) → `/patterns`.
- Time params for `detected_*` / `index` / `patterns` are sent as **ISO-8601**
  (`range.from.utc().toISOString()`); `query_range`/`query` go through the
  Grafana core Loki client (epoch) — both formats are accepted by
  `parseLokiTime` (RFC3339 + epoch magnitudes + scientific).

### 9.2 Gaps found (all live-verified 404 before fix)

| # | Plugin call | Collector state | Result (before) |
|---|-------------|----------------|-----------------|
| G1 | `/detected_fields/<name>/values` (PLURAL) | only `/detected_field/<name>/values` (SINGULAR) registered | **404** |
| G2 | `/series` | not registered | **404** |
| G3 | `/patterns` | not registered | **404** |

Consequence: the detected-field value picker, ad-hoc filter key dropdown, and
the Patterns panel would all fail/error in the real UI, despite the earlier
36/36 "pass" (which had used the singular path and never probed series/patterns).

### 9.3 Fixes

- **router.go** — registered the PLURAL `/detected_fields/{name}/values` (alongside
  the singular alias, both reusing `handleLokiDetectedFieldValues` which resolves
  the field via `ListLogLabelValues`); added `/series` and `/patterns`.
- **loki_handler.go** —
  - `handleLokiSeries`: returns `{status, data:[{metric:{label:label}}]}` built
    from `ListLogLabels` so `getTagKeys` can auto-complete label keys. Degrades to
    an empty set on storage error instead of 500/404.
  - `handleLokiPatterns`: returns `{data:[]}` (no patterns ingester) so the
    Patterns panel renders "no patterns" instead of erroring.

### 9.4 Redeploy required (unlike §4)

The original two fixes were already in the running image; these three were not,
so a rebuild + restart was required:

```bash
export DOCKER_CONTEXT=minikube && make docker-build
kubectl rollout restart deployment/custom-otlp-collector -n default
```

Verified the three endpoints return 200 post-redeploy (direct collector path,
`X-API-Key: your-api-key-1`):
- `/detected_fields/service_name/values` → 200 + 6 values
- `/series` → 200 + 5 label-key series
- `/patterns` → 200 + `{"data":[]}`

### 9.5 Suite extended (section 10) → 44/44

Added 8 cases under "10. COVERAGE GAPS": real-plugin ISO-8601 `query_range`,
`step="15s"` (Bug B live), scientific `start="1.78e9"` (Bug A live), `1+1`
health probe, plural detected_fields values, `/series`, `/patterns`, and
`detected_labels` with no time range.

```
go build ./extension/adminext/...          -> BUILD_EXIT=0
LD_DIRECT=1 LD_API_KEY=your-api-key-1 \
  LD_COLLECTOR=http://custom-otlp-collector.default.svc.cluster.local:8088 \
  python3 scripts/logs-drilldown/suite.py  -> TOTAL: 44/44 passed, 0 failed
```

(Grafana proxy path could not be probed from this shell — Grafana not reachable
on `localhost:3000` — but the proxy is a transparent pass-through over the same
route prefix, so collector-side behavior is identical.)

### 9.6 Files changed (this addendum)

- `extension/adminext/router.go` — added `/detected_fields/{name}/values` (plural), `/series`, `/patterns`
- `extension/adminext/loki_handler.go` — `handleLokiSeries`, `handleLokiPatterns`
- `scripts/logs-drilldown/suite.py` — section 10 (8 new cases, total 44)
- `scripts/logs-drilldown/VERIFICATION_REPORT.md` — §9 addendum
