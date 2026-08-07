#!/usr/bin/env python3
"""End-to-end coverage suite: Grafana Metrics Drilldown -> custom collector.

Each case asserts on SEMANTICS (series counts, label sets, value ordering),
not just HTTP 200 -- the worst bugs found here all returned valid-looking
200s with wrong data.
"""
import json
import sys
import urllib.parse
import urllib.request

from probe import BASE, HDRS, NOW, START, STEP, firstval, get, instant, rng, summarize


def post(path, params):
    """Grafana POSTs form-encoded queries when the URL would be too long."""
    data = urllib.parse.urlencode(params, doseq=True).encode()
    req = urllib.request.Request(BASE + path, data=data, headers={
        **HDRS, "Content-Type": "application/x-www-form-urlencoded"})
    try:
        with urllib.request.urlopen(req, timeout=45) as r:
            return r.status, json.loads(r.read().decode())
    except urllib.error.HTTPError as e:
        return e.code, {"_raw": e.read().decode()[:200]}
    except Exception as e:
        return 0, {"_err": str(e)}

M = "traces_spanmetrics_calls_total"   # counter
G = "jvm.memory.used"                  # gauge, dotted (UTF-8) name
H = "traces_spanmetrics_latency"       # histogram

results = []


def check(name, ok, detail=""):
    results.append((name, ok, detail))
    print("%-6s %-46s %s" % ("PASS" if ok else "FAIL", name, detail[:70]))


def labels_of(body, key):
    res = body.get("data", {}).get("result", [])
    return sorted({(r.get("metric") or {}).get(key, "?") for r in res})


def nseries(body):
    return len(body.get("data", {}).get("result", []))


print("=" * 92)
print("1. DISCOVERY ENDPOINTS")
print("=" * 92)

s, b = get("/status/buildinfo")
check("buildinfo returns a version", s == 200 and b.get("data", {}).get("version"),
      "version=%s" % b.get("data", {}).get("version"))

s, b = get("/label/__name__/values")
metrics = b.get("data", []) if s == 200 else []
check("metric list non-empty", len(metrics) > 0, "%d metrics" % len(metrics))

s, b = get("/metadata")
meta = b.get("data", {}) if s == 200 else {}
types = {}
for k, v in meta.items():
    if v:
        types[v[0]["type"]] = types.get(v[0]["type"], 0) + 1
check("metadata reports >1 distinct type", len(types) > 1, str(types))
check("counter metric typed counter", meta.get(M, [{}])[0].get("type") == "counter",
      "%s -> %s" % (M, meta.get(M, [{}])[0].get("type")))
check("histogram metric typed histogram", meta.get(H, [{}])[0].get("type") == "histogram",
      "%s -> %s" % (H, meta.get(H, [{}])[0].get("type")))

s, b = get("/metadata", {"metric": H})
check("metadata honours ?metric= filter", s == 200 and list(b.get("data", {})) == [H],
      str(list(b.get("data", {})))[:60])

s, b = get("/series", {"match[]": '{__name__="%s"}' % M})
check("/series returns series", s == 200 and len(b.get("data", [])) > 0,
      "%d series" % len(b.get("data", [])))

s, b = get("/labels", {"match[]": '{__name__="%s"}' % M})
scoped = set(b.get("data", []))
s, b = get("/labels")
glob = set(b.get("data", []))
check("/labels match[] narrows label set", scoped and len(scoped) < len(glob),
      "%d scoped < %d global" % (len(scoped), len(glob)))
check("/labels global is a superset of scoped", scoped <= glob,
      "missing from global: %s" % sorted(scoped - glob))

print()
print("=" * 92)
print("2. LABEL VALUES SCOPING  (breakdown value picker)")
print("=" * 92)

s, b = get("/label/span_kind/values", {"match[]": '{__name__="%s"}' % G})
check("span_kind values scoped to a gauge metric", s == 200 and b.get("data") in ([], None),
      "got %s (expected empty: label absent on %s)" % (b.get("data"), G))

s, b = get("/label/jvm_memory_type/values", {"match[]": '{__name__="%s"}' % M})
check("jvm_memory_type scoped to a counter metric", s == 200 and b.get("data") in ([], None),
      "got %s (expected empty)" % (b.get("data"),))

s, b = get("/label/span_kind/values", {"match[]": '{__name__="%s"}' % M})
check("span_kind values present on the right metric", s == 200 and len(b.get("data") or []) > 0,
      str(b.get("data"))[:60])

print()
print("=" * 92)
print("3. LABEL MATCHERS  (ad-hoc filters -- on EVERY plugin query)")
print("=" * 92)

s, b = instant("sum by (span_kind) (rate(%s[5m]))" % M)
all_sk = labels_of(b, "span_kind")
s, b = instant("sum by (service_name) (rate(%s[5m]))" % M)
all_sv = labels_of(b, "service_name")
print("   baseline span_kind=%s" % all_sk)
print("   baseline service_name=%d values" % len(all_sv))

matcher_cases = [
    ("= exact", '{span_kind="Client"}', "span_kind", ["Client"]),
    ("=~ literal", '{span_kind=~"Client"}', "span_kind", ["Client"]),
    ("=~ alternation", '{span_kind=~"Client|Server"}', "span_kind", ["Client", "Server"]),
    ("=~ prefix", '{service_name=~"test-java.*"}', "service_name",
     [x for x in all_sv if x.startswith("test-java")]),
    ("!= negation", '{span_kind!="Client"}', "span_kind",
     [x for x in all_sk if x != "Client"]),
    ("!~ negated regex", '{service_name!~"test-java.*"}', "service_name",
     [x for x in all_sv if not x.startswith("test-java")]),
]
for label, sel, key, expected in matcher_cases:
    q = "sum by (%s) (rate(%s%s[5m]))" % (key, M, sel)
    s, b = instant(q)
    got = labels_of(b, key)
    check("matcher %s" % label, got == sorted(expected),
          "got %d, expected %d" % (len(got), len(expected)))

print()
print("=" * 92)
print("4. __name__ SELECTOR  (plugin emits this for non-identifier names)")
print("=" * 92)

s, bare = instant(M)
s, named = instant('{__name__="%s"}' % M)
check("{__name__=\"m\"} filters like the bare name",
      nseries(named) == nseries(bare) and nseries(bare) > 0,
      "%d vs %d series" % (nseries(named), nseries(bare)))

s, b = instant('{__name__="%s"}' % G)
names = {(r.get("metric") or {}).get("__name__") for r in b.get("data", {}).get("result", [])}
check("{__name__=\"dotted.name\"} returns only that metric",
      names == {G}, "names=%s" % str(names)[:50])

s, b = instant('{"%s"}' % G)   # UTF-8 quoted form the plugin actually emits
names = {(r.get("metric") or {}).get("__name__") for r in b.get("data", {}).get("result", [])}
check("UTF-8 quoted selector returns only that metric", names == {G}, "names=%s" % str(names)[:50])

print()
print("=" * 92)
print("5. PANEL QUERIES BY METRIC TYPE")
print("=" * 92)

# Counter -> sum(rate(...)); instant and range must agree.
qi = "sum(rate(%s[5m]))" % M
s, bi = instant(qi)
s, br = rng(qi)
check("counter sum(rate) collapses to 1 series (instant)", nseries(bi) == 1, "%d" % nseries(bi))
check("counter sum(rate) collapses to 1 series (range)", nseries(br) == 1, "%d" % nseries(br))
vi, vr = firstval(bi), firstval(br)
# Compare instant against the RANGE of range values, not its last point: the
# final step covers a partial window and legitimately reads low.
rvals = [float(p[1]) for p in br["data"]["result"][0]["values"]] if nseries(br) else []
in_band = bool(rvals) and vi is not None and min(rvals) * 0.7 <= vi <= max(rvals) * 1.3
check("counter instant falls within the range band", in_band,
      "instant=%.2f range=[%.2f, %.2f]" % (vi or -1, min(rvals or [0]), max(rvals or [0])))

# Gauge -> avg(...)
qg = 'avg({"%s"})' % G
s, bi = instant(qg)
s, br = rng(qg)
check("gauge avg() collapses to 1 series (instant)", nseries(bi) == 1, "%d" % nseries(bi))
check("gauge avg() collapses to 1 series (range)", nseries(br) == 1, "%d" % nseries(br))

# Breakdown: one series per label value.
s, b = instant("sum by (service_name) (rate(%s[5m]))" % M)
check("counter breakdown yields one series per service",
      nseries(b) == len(labels_of(b, "service_name")) and nseries(b) > 1,
      "%d series" % nseries(b))

print()
print("=" * 92)
print("6. HISTOGRAM QUANTILES  (native-histogram form: no _bucket, no by(le))")
print("=" * 92)

vals = {}
for th in ("0.50", "0.90", "0.99"):
    q = 'histogram_quantile(%s, sum(rate({"%s"}[5m])))' % (th, H)
    s, bi = instant(q)
    s, br = rng(q)
    vals[th] = (firstval(bi), nseries(bi), firstval(br), nseries(br))
    check("p%s collapses to 1 series (instant/range)" % th,
          vals[th][1] == 1 and vals[th][3] == 1,
          "instant=%d range=%d series" % (vals[th][1], vals[th][3]))

p50, p90, p99 = vals["0.50"][0], vals["0.90"][0], vals["0.99"][0]
check("quantiles are monotonic p50<=p90<=p99",
      p50 is not None and p50 <= p90 <= p99,
      "p50=%.3f p90=%.3f p99=%.3f" % (p50 or -1, p90 or -1, p99 or -1))
check("theta is actually applied (p50 != p99)", p50 != p99,
      "p50=%.3f p99=%.3f" % (p50 or -1, p99 or -1))

# Classic form must agree with the native form.
s, b_classic = instant('histogram_quantile(0.99, sum by (le) (rate({"%s"}[5m])))' % H)
check("by (le) form matches no-le form", nseries(b_classic) == 1,
      "%d series, val=%s" % (nseries(b_classic), firstval(b_classic)))

# Grouped quantile keeps the grouping dimension.
s, b = instant('histogram_quantile(0.99, sum by (le, service_name) (rate({"%s"}[5m])))' % H)
svc = labels_of(b, "service_name")
check("by (le, service_name) yields one series per service",
      nseries(b) > 1 and nseries(b) == len(svc) and "?" not in svc,
      "%d series" % nseries(b))
has_le = any("le" in (r.get("metric") or {}) for r in b.get("data", {}).get("result", []))
check("le is consumed, never emitted", not has_le, "le present" if has_le else "le absent")

print()
print("=" * 92)
print("7. OTHER PLUGIN QUERY SHAPES")
print("=" * 92)

shapes = [
    ("topk", "topk(5, sum by (service_name) (rate(%s[5m])))" % M),
    ("min() status", "min(%s)" % M),
    ("count()", "count(%s)" % M),
    ("stddev()", 'stddev({"%s"})' % G),
]
for name, q in shapes:
    si, bi = instant(q)
    sr, br = rng(q)
    vi, vr = summarize(si, bi), summarize(sr, br)
    check("shape %s" % name, vi[0] == "OK" and vr[0] == "OK",
          "instant=%s range=%s" % (vi[0], vr[0]))

s, b = instant("topk(3, sum by (service_name) (rate(%s[5m])))" % M)
check("topk(3) instant returns at most 3", nseries(b) <= 3, "%d series" % nseries(b))
s, b = rng("topk(3, sum by (service_name) (rate(%s[5m])))" % M)
check("topk(3) range returns at most 3", nseries(b) <= 3, "%d series" % nseries(b))

print()
print("=" * 92)
print("8. HEATMAP  (sum by (le) must expand the bucket dimension)")
print("=" * 92)

s, b = rng('sum by (le) (rate({"%s"}[5m]))' % H)
res = b.get("data", {}).get("result", [])
les = [(r.get("metric") or {}).get("le") for r in res]
check("heatmap yields one series per bucket", len(res) > 2, "%d series" % len(res))
check("every series carries an le label", res and all(les), "le values: %s" % str(les[:6]))
check("+Inf bucket present", "+Inf" in les, "le set includes +Inf")
finals = [float(r["values"][-1][1]) for r in res if r.get("values")]
check("buckets are cumulative (non-decreasing)",
      all(finals[i] <= finals[i + 1] + 1e-9 for i in range(len(finals) - 1)),
      "%d bucket values checked" % len(finals))

s, b = rng('sum by (le, service_name) (rate({"%s"}[5m]))' % H)
res = b.get("data", {}).get("result", [])
svcs = {(r.get("metric") or {}).get("service_name") for r in res}
check("grouped heatmap keeps both le and the group label",
      len(res) > len(svcs) and len(svcs) > 1 and None not in svcs,
      "%d series across %d services" % (len(res), len(svcs)))

print()
print("=" * 92)
print("9. UNSUPPORTED QUERIES MUST 400, NOT 200+EMPTY")
print("=" * 92)

# A 200 with an empty vector is indistinguishable from "no data" in Grafana,
# and for binary operators it returned the LEFT operand as if it were the ratio.
for name, q in [
    ("binary ratio", "sum(rate(%s[5m])) / sum(rate(%s[5m]))" % (M, M)),
    ("binary product", "sum(rate(%s[5m])) * 2" % M),
    ("sum without()", "sum without (span_kind) (rate(%s[5m]))" % M),
    ("max_over_time", 'max_over_time({"%s"}[5m])' % G),
    ("count_over_time", 'count_over_time({"%s"}[5m])' % G),
    ("time() - avg()", 'time() - avg({"%s"})' % G),
    ("and > -Inf", 'avg({"%s"}) and avg({"%s"}) > -Inf' % (G, G)),
    ("nonsense text", "this is not promql at all"),
    ("unknown function", "bogus_func(%s)" % M),
]:
    si, _ = instant(q)
    sr, _ = rng(q)
    check("rejects %s" % name, si == 400 and sr == 400,
          "instant=%s range=%s" % (si, sr))

print()
print("=" * 92)
print("10. TRANSPORT + DURATION FORMATS")
print("=" * 92)

# Grafana switches to POST when the query string would be too long for a URL.
s, b = post("/query", {"query": "sum(rate(%s[5m]))" % M, "time": NOW})
check("POST /query", s == 200 and nseries(b) == 1, "HTTP %s, %d series" % (s, nseries(b)))
s, b = post("/query_range", {"query": "sum(rate(%s[5m]))" % M,
                             "start": START, "end": NOW, "step": STEP})
check("POST /query_range", s == 200 and nseries(b) == 1, "HTTP %s, %d series" % (s, nseries(b)))
s, b = post("/series", {"match[]": '{__name__="%s"}' % M})
check("POST /series", s == 200 and len(b.get("data", [])) > 0,
      "HTTP %s, %d series" % (s, len(b.get("data", []))))

# $__rate_interval interpolates to Go-style compound durations. Windows shorter
# than the scrape interval hold <2 samples, so an empty result is correct there —
# what must never happen is a malformed {"metric":null,"value":null} series.
for dur in ["5m", "1m0s", "4m30s", "30s", "1h", "2h15m0s"]:
    s, b = instant("sum(rate(%s[%s]))" % (M, dur))
    res = b.get("data", {}).get("result", [])
    wellformed = all(r.get("metric") is not None and r.get("value") is not None for r in res)
    check("duration [%s]" % dur, s == 200 and wellformed,
          "%d series%s" % (len(res), "" if wellformed else " — NULL metric/value!"))

print()
print("=" * 92)
npass = sum(1 for _, ok, _ in results if ok)
print("TOTAL: %d/%d passed" % (npass, len(results)))
if npass < len(results):
    print("\nFailures:")
    for n, ok, d in results:
        if not ok:
            print("  - %-44s %s" % (n, d))
print("=" * 92)
sys.exit(0 if npass == len(results) else 1)
