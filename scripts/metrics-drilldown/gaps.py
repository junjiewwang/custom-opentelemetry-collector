#!/usr/bin/env python3
"""Probe the KNOWN-UNVERIFIED surface: things suite.py does not cover.

Purpose is to turn "possible gap" into "confirmed bug + severity", so the
remaining-work list is evidence-based rather than guessed.
"""
import json
import urllib.parse
import urllib.request

from probe import BASE, HDRS, NOW, START, STEP, firstval, get, instant, rng, summarize

M = "traces_spanmetrics_calls_total"
G = "jvm.memory.used"
H = "traces_spanmetrics_latency"


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


def nseries(b):
    return len(b.get("data", {}).get("result", []))


def show(tag, q, body_i, body_r):
    print("  %-34s instant=%-26s range=%s" % (
        tag, "%s/%s" % summarize(200, body_i)[:2][::-1][::-1] if False else
        "%s %s" % summarize(200, body_i), "%s %s" % summarize(200, body_r)))


print("=" * 94)
print("A. HEATMAP  — sum by (le) must PRESERVE one series per bucket")
print("=" * 94)
q = 'sum by (le) (rate({"%s"}[5m]))' % H
si, bi = instant(q)
sr, br = rng(q)
print("  query: %s" % q)
print("  instant: %s   range: %s" % (summarize(si, bi), summarize(sr, br)))
les = [(r.get("metric") or {}).get("le") for r in bi.get("data", {}).get("result", [])]
print("  le labels on instant result: %s" % les[:12])
print("  VERDICT: %s" % ("OK — buckets preserved" if len([x for x in les if x]) > 1
                         else "BUG — le dimension collapsed, heatmap panel cannot render"))

print()
print("=" * 94)
print("B. RECENT-METRICS DISCOVERY  — group by (__name__) (...) unless (... offset)")
print("=" * 94)
for tag, q in [
    ("group by (__name__)", 'group by (__name__) ({__name__!=""})'),
    ("full unless form",
     'group by (__name__) ({__name__!=""}) unless (group by (__name__) ({__name__!=""} offset 1h))'),
]:
    si, bi = instant(q)
    sr, br = rng(q)
    print("  %-22s instant=%-30s range=%s" % (tag, summarize(si, bi), summarize(sr, br)))
print("  VERDICT: drives the 'recently added metrics' filter in the plugin")

print()
print("=" * 94)
print("C. BINARY RATIO  — Knowledge Graph error-rate panels")
print("=" * 94)
for tag, q in [
    ("bare ratio", "sum(rate(%s[5m])) / sum(rate(%s[5m]))" % (M, M)),
    ("ratio by label", "sum by (service_name) (rate(%s[5m])) / sum by (service_name) (rate(%s[5m]))" % (M, M)),
]:
    si, bi = instant(q)
    sr, br = rng(q)
    print("  %-16s instant=%-28s range=%s" % (tag, summarize(si, bi), summarize(sr, br)))
    if nseries(bi):
        r0 = bi["data"]["result"][0]
        print("      instant sample = %s" % json.dumps(r0, ensure_ascii=False)[:150])

print()
print("=" * 94)
print("D. PARSE FAILURES  — must they 400, or do they 200+empty?")
print("=" * 94)
for tag, q in [
    ("sum without()", "sum without (span_kind) (rate(%s[5m]))" % M),
    ("nonsense text", "this is not promql at all"),
    ("unclosed brace", "%s{service_name=" % M),
    ("unknown function", "bogus_func(%s)" % M),
]:
    si, bi = instant(q)
    v = summarize(si, bi)
    names = [(r.get("metric") or {}).get("__name__", "") for r in bi.get("data", {}).get("result", [])]
    print("  %-18s HTTP %-4s %-30s __name__=%s" % (tag, si, v, str(names[:1])[:44]))
print("  VERDICT: 200+empty is indistinguishable from 'no data' in the Grafana UI")

print()
print("=" * 94)
print("E. POST METHOD  — Grafana POSTs when the query string is long")
print("=" * 94)
s, b = post("/query", {"query": "sum(rate(%s[5m]))" % M, "time": NOW})
print("  POST /query        HTTP %s  %s" % (s, summarize(s, b)))
s, b = post("/query_range", {"query": "sum(rate(%s[5m]))" % M,
                             "start": START, "end": NOW, "step": STEP})
print("  POST /query_range  HTTP %s  %s" % (s, summarize(s, b)))
s, b = post("/series", {"match[]": '{__name__="%s"}' % M})
print("  POST /series       HTTP %s  %d series" % (s, len(b.get("data", []))))
s, b = post("/labels", {"match[]": '{__name__="%s"}' % M})
print("  POST /labels       HTTP %s  %d labels" % (s, len(b.get("data", []))))

print()
print("=" * 94)
print("F. RATE-INTERVAL DURATION FORMATS  — $__rate_interval interpolates to these")
print("=" * 94)
for dur in ["5m", "1m0s", "4m30s", "30s", "1h", "2h15m0s", "1m30s"]:
    q = "sum(rate(%s[%s]))" % (M, dur)
    si, bi = instant(q)
    print("  [%-8s] %-28s val=%s" % (dur, summarize(si, bi), firstval(bi)))
print("  VERDICT: Grafana emits Go-style compound durations like 1m0s / 4m30s")

print()
print("=" * 94)
print("G. limit PARAM on discovery endpoints")
print("=" * 94)
s, b = get("/label/__name__/values")
full = len(b.get("data", []))
s, b = get("/label/__name__/values", {"limit": 5})
lim = len(b.get("data", []))
print("  /label/__name__/values  no-limit=%d  limit=5 -> %d  %s" % (
    full, lim, "OK" if lim <= 5 else "IGNORED"))
s, b = get("/series", {"match[]": '{__name__="%s"}' % M, "limit": 3})
print("  /series limit=3 -> %d series  %s" % (
    len(b.get("data", [])), "OK" if len(b.get("data", [])) <= 3 else "IGNORED"))

print()
print("=" * 94)
print("H. HISTOGRAM SUB-SERIES  — plugin's isCounterMetric matches _sum/_count/_total")
print("=" * 94)
for suffix in ["_sum", "_count", "_bucket"]:
    q = "sum(rate(%s%s[5m]))" % (H, suffix)
    si, bi = instant(q)
    print("  %-28s %s" % (H + suffix, summarize(si, bi)))
s, b = get("/label/__name__/values")
names = b.get("data", [])
print("  metrics exposing _bucket: %d   _sum: %d   _count: %d" % (
    len([n for n in names if n.endswith("_bucket")]),
    len([n for n in names if n.endswith("_sum")]),
    len([n for n in names if n.endswith("_count")])))
print("  NOTE: plugin classifies by NAME SUFFIX first; metadata only overrides gauge/counter")

print()
print("=" * 94)
print("I. RULER API  — 'firing alerts' badge in the metric list")
print("=" * 94)
for p in ["/rules", "/alerts"]:
    s, b = get(p)
    print("  GET %-10s HTTP %s %s" % (p, s, str(b)[:70]))
