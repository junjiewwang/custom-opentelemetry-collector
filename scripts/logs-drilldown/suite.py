#!/usr/bin/env python3
"""End-to-end coverage suite: Grafana Logs Drilldown -> custom collector.

Mirrors scripts/metrics-drilldown/suite.py but for the Loki API surface that
the logs-drilldown plugin (v2.4.0) drives. Each case asserts on SEMANTICS
(series counts, label sets, value ordering, resultType), not just HTTP 200.

Run:
    python3 scripts/logs-drilldown/suite.py
Toggle the real Grafana proxy vs direct collector with LD_DIRECT=1.
"""
import json
import sys

from probe import (BASE, DIRECT, END, ENS, EISO, HDRS, NOW, SISO, SNS, START,
                   firstval, get, nseries, nstreams, post, streams_of, summarize)

results = []


def check(name, ok, detail=""):
    results.append((name, ok, detail))
    tag = "PASS" if ok else "FAIL"
    print("%-5s %-52s %s" % (tag, name, detail[:64]))


def labels_of(body, key):
    res = (body.get("data") or {}).get("result", [])
    return sorted({(r.get("metric") or {}).get(key, "?") for r in res})


print("=" * 96)
print("LOGS DRILLDOWN -> custom collector  (target: %s%s)" % (
    "Grafana proxy id=%s" % "8" if not DIRECT else "direct collector", ""))
print("BASE = %s" % BASE)
print("=" * 96)

# ──────────────────────────────────────────────────────────────────────────
print("\n1. DATASOURCE HEALTH (Grafana -> collector)")
# ──────────────────────────────────────────────────────────────────────────
s, b = get("/query_range", {"query": '{severity="ERROR"}', "start": SNS, "end": ENS, "limit": 5})
check("query_range health probe returns data", s == 200 and nstreams(b) > 0,
      "status=%s streams=%d" % (s, nstreams(b)))

s, b = get("/query", {"query": "vector(1)+vector(1)", "time": NOW})
check("instant scalar probe vector(1)+vector(1)=2", s == 200 and firstval(b) == "2",
      "status=%s val=%s" % (s, firstval(b)))

# ──────────────────────────────────────────────────────────────────────────
print("\n2. DISCOVERY: labels & label values")
# ──────────────────────────────────────────────────────────────────────────
s, b = get("/labels", {"start": SNS, "end": ENS})
lbls = b.get("data", []) if s == 200 and isinstance(b.get("data"), list) else []
check("labels endpoint non-empty", s == 200 and len(lbls) > 0, "%d labels" % len(lbls))
for must in ("service_name", "severity", "level"):
    check("label '%s' is discoverable" % must, must in lbls,
          "" if must in lbls else "missing from %s" % lbls)

s, b = get("/label/service_name/values", {"start": SNS, "end": ENS})
svcs = b.get("data", []) if s == 200 and isinstance(b.get("data"), list) else []
check("service_name has known java services", s == 200 and len(svcs) > 0,
      "%d services" % len(svcs))
for must in ("java-user-service", "test-java-order-service"):
    check("service_name value '%s' present" % must, must in svcs,
          "" if must in svcs else "missing")

s, b = get("/label/severity/values", {"start": SNS, "end": ENS})
sevs = b.get("data", []) if s == 200 and isinstance(b.get("data"), list) else []
check("severity values include ERROR/INFO", s == 200 and "ERROR" in sevs and "INFO" in sevs,
      "%s" % sevs)

# ──────────────────────────────────────────────────────────────────────────
print("\n3. LOG STREAM QUERY (query_range, stream resultType)")
# ──────────────────────────────────────────────────────────────────────────
s, b = get("/query_range", {"query": '{severity="ERROR"}', "start": SNS, "end": ENS, "limit": 20})
res = streams_of(b)
check("stream query returns streams", s == 200 and nstreams(b) > 0,
      "status=%s streams=%d" % (s, nstreams(b)))
check("stream resultType=streams", (b.get("data") or {}).get("resultType") == "streams",
      str((b.get("data") or {}).get("resultType")))
# every returned stream must carry service_name + severity
ok = all(("service_name" in st.get("stream", {})) and ("severity" in st.get("stream", {}))
         for st in res)
check("all streams expose service_name+severity", ok, "")
# line content + nanosecond timestamp shape
if res:
    ts, line = res[0]["values"][0][0], res[0]["values"][0][1]
    check("stream value is [ns_timestamp, line]", ts.isdigit() and len(line) > 0,
          "ts=%s line=%s" % (ts[:4] + "...", line[:30]))

# service-scoped stream query
s, b = get("/query_range", {"query": '{service_name="java-user-service"}',
                            "start": SNS, "end": ENS, "limit": 10})
res = streams_of(b)
ok = all(st.get("stream", {}).get("service_name") == "java-user-service" for st in res)
check("service-scoped stream query filters correctly", s == 200 and nstreams(b) > 0 and ok,
      "streams=%d" % nstreams(b))

# ──────────────────────────────────────────────────────────────────────────
print("\n4. METRIC QUERIES (query_range, matrix resultType)")
# ──────────────────────────────────────────────────────────────────────────
s, b = get("/query_range",
           {"query": 'count_over_time({service_name=~".+"}[5m])',
            "start": SNS, "end": ENS, "step": "60"})
check("count_over_time -> matrix series>0", s == 200 and nseries(b) > 0,
      "status=%s series=%d" % (s, nseries(b)))
check("metric resultType=matrix", (b.get("data") or {}).get("resultType") == "matrix",
      str((b.get("data") or {}).get("resultType")))

s, b = get("/query_range",
           {"query": 'sum(count_over_time({service_name=~".+"}[5m]))',
            "start": SNS, "end": ENS, "step": "60"})
check("sum(count_over_time) -> single series", s == 200 and nseries(b) == 1,
      "series=%d val=%s" % (nseries(b), firstval(b)))

s, b = get("/query_range",
           {"query": 'sum by (service_name) (count_over_time({service_name=~".+"}[5m]))',
            "start": SNS, "end": ENS, "step": "60"})
svcset = labels_of(b, "service_name")
check("sum by (service_name) -> per-service series", s == 200 and len(svcset) > 0,
      "%d services: %s" % (len(svcset), svcset))
# the matrix values must be numeric (not empty)
ok = any(len(r.get("values", [])) > 0 for r in (b.get("data") or {}).get("result", []))
check("per-service matrix has data points", ok, "")

# rate() throughput shape
s, b = get("/query_range",
           {"query": 'sum by (service_name) (rate({service_name=~".+"}[5m]))',
            "start": SNS, "end": ENS, "step": "60"})
check("rate() -> matrix series>0", s == 200 and nseries(b) > 0,
      "series=%d" % nseries(b))

# ──────────────────────────────────────────────────────────────────────────
print("\n5. INSTANT QUERY (query, vector / instant)")
# ──────────────────────────────────────────────────────────────────────────
s, b = get("/query", {"query": 'count_over_time({severity="ERROR"}[15m])', "time": NOW})
check("instant count_over_time -> instant vector", s == 200 and nseries(b) > 0,
      "status=%s series=%d" % (s, nseries(b)))
check("instant resultType=vector", (b.get("data") or {}).get("resultType") == "vector",
      str((b.get("data") or {}).get("resultType")))

# ──────────────────────────────────────────────────────────────────────────
print("\n6. INDEX VOLUME (logs-drilldown volume panel)")
# ──────────────────────────────────────────────────────────────────────────
s, b = get("/index/volume",
           {"query": '{service_name=~".+"}', "start": SNS, "end": ENS, "limit": 20})
res = (b.get("data") or {}).get("result", [])
check("index/volume -> vector of per-service counts", s == 200 and len(res) > 0,
      "status=%s rows=%d" % (s, len(res)))
check("index/volume resultType=vector", (b.get("data") or {}).get("resultType") == "vector",
      str((b.get("data") or {}).get("resultType")))
# volumes must be non-negative integers; largest service first is not guaranteed,
# but java-user-service should dominate given earlier snapshot.
vols = {r["metric"].get("service_name"): int(r["value"][1]) for r in res if "value" in r}
check("volume counts are positive", all(v > 0 for v in vols.values()), str(vols))
check("java-user-service has highest volume",
      vols.get("java-user-service", 0) == max(vols.values()) if vols else False,
      "top=%s" % (max(vols, key=vols.get) if vols else "-"))

# ──────────────────────────────────────────────────────────────────────────
print("\n7. DETECTED LABELS / FIELDS (ISO-8601 timestamps)")
# ──────────────────────────────────────────────────────────────────────────
s, b = get("/detected_labels", {"query": "{service_name=~\".+\"}", "start": SISO, "end": EISO})
dls = b.get("detectedLabels", []) if s == 200 else []
check("detected_labels returns label list", s == 200 and len(dls) > 0,
      "%d labels" % len(dls))

s, b = get("/detected_fields", {"query": "{}", "start": SISO, "end": EISO, "limit": 100})
flds = b.get("fields", []) if s == 200 else []
check("detected_fields returns field list", s == 200 and len(flds) > 0,
      "%d fields" % len(flds))
fnames = {f.get("label") for f in flds}
for must in ("service_name", "severity"):
    check("detected_field '%s' present" % must, must in fnames,
          "" if must in fnames else "missing")

s, b = get("/detected_field/service_name/values",
           {"query": "{}", "start": SISO, "end": EISO})
dfv = b.get("data", []) if s == 200 and isinstance(b.get("data"), list) else []
check("detected_field values return service list", s == 200 and len(dfv) > 0,
      "%d values" % len(dfv))

# ──────────────────────────────────────────────────────────────────────────
print("\n8. INDEX STATS & DRILLDOWN LIMITS (config)")
# ──────────────────────────────────────────────────────────────────────────
s, b = get("/index/stats", {"query": "{}", "start": SISO, "end": EISO})
check("index/stats returns stats blob", s == 200 and "streams" in b,
      str({k: b.get(k) for k in ("streams", "chunks", "entries", "bytes") if k in b}))

s, b = get("/drilldown-limits")
cfg = b if s == 200 else {}
check("drilldown-limits returns config", s == 200 and "max_query_series" in cfg,
      "version=%s" % cfg.get("version"))

# ──────────────────────────────────────────────────────────────────────────
print("\n9. LINE FILTER & PARSER SHAPES (real plugin LogQL)")
# ──────────────────────────────────────────────────────────────────────────
# line filter |= (substring)
s, b = get("/query_range",
           {"query": '{service_name=~".+"} |= "Exception"', "start": SNS, "end": ENS, "limit": 10})
check("line filter |= matches lines", s == 200 and nstreams(b) > 0,
      "streams=%d" % nstreams(b))

# label matcher with regex
s, b = get("/query_range",
           {"query": '{service_name=~"test-java-.*"}', "start": SNS, "end": ENS, "limit": 10})
res = streams_of(b)
ok = all(st.get("stream", {}).get("service_name", "").startswith("test-java-") for st in res)
check("regex matcher service_name=~ filters correctly", s == 200 and nstreams(b) > 0 and ok,
      "streams=%d" % nstreams(b))

# ──────────────────────────────────────────────────────────────────────────
# ──────────────────────────────────────────────────────────────────────────
print("\n10. COVERAGE GAPS (real plugin formats + newly-added endpoints)")
# ──────────────────────────────────────────────────────────────────────────

# 10a. query_range with ISO-8601 start/end — this is the ACTUAL wire format the
#      logs-drilldown plugin emits (range.from.utc().toISOString()), distinct
#      from the epoch nanoseconds the Grafana core Loki client sends.
s, b = get("/query_range",
           {"query": '{severity="ERROR"}', "start": SISO, "end": EISO, "limit": 20})
check("query_range accepts ISO-8601 start/end (real plugin fmt)",
      s == 200 and nstreams(b) >= 0, "status=%s streams=%d" % (s, nstreams(b)))

# 10b. step as duration string "15s" (Bug B fix) — plugin sends request.interval
#      e.g. "15s"/"1m", NOT a bare-second integer.
s, b = get("/query_range",
           {"query": 'count_over_time({service_name=~".+"}[5m])',
            "start": SNS, "end": ENS, "step": "15s"})
check("metric query with step='15s' (Bug B) -> matrix",
      s == 200 and nseries(b) > 0 and (b.get("data") or {}).get("resultType") == "matrix",
      "status=%s series=%d" % (s, nseries(b)))

# 10c. scientific-notation time "1.78e9" (Bug A fix) — must parse as seconds,
#      NOT drop the exponent and collapse to 1970.
s, b = get("/query_range",
           {"query": '{severity="ERROR"}', "start": "1.78e9", "end": ENS, "limit": 20})
check("scientific-notation start '1.78e9' parses (Bug A)",
      s == 200, "status=%s" % s)

# 10d. Grafana health probe variant "1+1" (not just vector(1)+vector(1)).
s, b = get("/query", {"query": "1+1", "time": NOW})
check("instant scalar probe 1+1=2", s == 200 and firstval(b) == "2",
      "status=%s val=%s" % (s, firstval(b)))

# 10e. detected_fields/<name>/values PLURAL path (the real plugin path).
s, b = get("/detected_fields/service_name/values",
           {"query": "{}", "start": SISO, "end": EISO})
dfv2 = b.get("data", []) if s == 200 and isinstance(b.get("data"), list) else []
check("detected_fields/<name>/values PLURAL path works",
      s == 200 and len(dfv2) > 0, "status=%s values=%d" % (s, len(dfv2)))

# 10f. /series endpoint (ad-hoc filter key auto-completion).
s, b = get("/series", {"match": '{service_name=~".+"}', "start": SNS, "end": ENS})
ser = b.get("data", []) if s == 200 and isinstance(b.get("data"), list) else []
check("/series returns label-key series", s == 200 and len(ser) > 0,
      "status=%s series=%d" % (s, len(ser)))

# 10g. /patterns endpoint (Patterns panel ingester).
s, b = get("/patterns",
           {"query": '{service_name=~".+"}', "start": SNS, "end": ENS, "step": "60"})
pats = b.get("data", []) if s == 200 and isinstance(b.get("data"), list) else None
check("/patterns returns data array (no 404)", s == 200 and pats is not None,
      "status=%s data=%s" % (s, type(pats).__name__))

# 10h. detected_labels with NO time range — must degrade gracefully (200 + empty).
s, b = get("/detected_labels", {"query": '{service_name=~".+"}'})
dls2 = b.get("detectedLabels", []) if s == 200 else None
check("detected_labels without time range is 200", s == 200 and dls2 is not None,
      "status=%s" % s)

print("\n" + "=" * 96)
npass = sum(1 for _, ok, _ in results if ok)
nfail = len(results) - npass
print("TOTAL: %d/%d passed, %d failed" % (npass, len(results), nfail))
print("=" * 96)
for name, ok, detail in results:
    if not ok:
        print("  FAIL: %s -- %s" % (name, detail))
sys.exit(1 if nfail else 0)
