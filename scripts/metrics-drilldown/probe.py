#!/usr/bin/env python3
"""Metrics Drilldown -> custom collector Prometheus API coverage probe.

Targets the EXACT query shapes the Grafana metrics-drilldown plugin emits,
derived from source inspection of the plugin's query builders.

Configure via environment:
    MD_BASE_URL   collector Prometheus base, default http://localhost:8088
    MD_API_KEY    value for the X-API-Key header
"""
import json
import os
import sys
import time
import urllib.parse
import urllib.request

BASE = os.environ.get("MD_BASE_URL", "http://localhost:8088").rstrip("/") + "/api/v2/prometheus/api/v1"
HDRS = {"X-API-Key": os.environ.get("MD_API_KEY", "")}

NOW = int(time.time())
START = NOW - 3600
STEP = 60


def get(path, params=None):
    url = BASE + path
    if params:
        url += "?" + urllib.parse.urlencode(params, doseq=True)
    req = urllib.request.Request(url, headers=HDRS)
    try:
        with urllib.request.urlopen(req, timeout=45) as r:
            body = r.read().decode()
            try:
                return r.status, json.loads(body)
            except json.JSONDecodeError:
                return r.status, {"_raw": body[:300]}
    except urllib.error.HTTPError as e:
        body = e.read().decode()[:300]
        try:
            return e.code, json.loads(body)
        except json.JSONDecodeError:
            return e.code, {"_raw": body}
    except Exception as e:
        return 0, {"_err": str(e)}


def instant(q):
    return get("/query", {"query": q, "time": NOW})


def rng(q):
    return get("/query_range", {"query": q, "start": START, "end": NOW, "step": STEP})


def summarize(status, body):
    """Return (verdict, detail) for a query response."""
    if status != 200:
        err = body.get("error", body.get("_raw", body.get("_err", "")))
        return "HTTP%s" % status, str(err)[:160]
    if body.get("status") != "success":
        return "ERR", str(body.get("error", ""))[:160]
    data = body.get("data", {})
    rt = data.get("resultType")
    res = data.get("result", [])
    if not res:
        return "EMPTY", "resultType=%s, 0 series" % rt
    names = [(r.get("metric") or {}).get("__name__", "") for r in res]
    if rt == "matrix":
        pts = sum(len(r.get("values", [])) for r in res)
        detail = "%d series, %d pts" % (len(res), pts)
    else:
        detail = "%d series" % len(res)
    # A parser that echoes query text into __name__ is the known failure mode.
    # NOTE: an EMPTY __name__ is legitimate -- Prometheus drops __name__ on aggregation.
    suspicious = [n for n in names if any(c in n for c in "()[]/<>") or n.strip() in {"and", "or", "unless"}]
    if suspicious:
        return "GARBAGE", "%s | __name__=%r" % (detail, suspicious[0][:80])
    return "OK", detail


def firstval(body):
    """Latest scalar value of the first series, for cross-checking instant vs range."""
    res = body.get("data", {}).get("result", [])
    if not res:
        return None
    r = res[0]
    if "value" in r:
        return float(r["value"][1])
    vals = r.get("values") or []
    return float(vals[-1][1]) if vals else None
