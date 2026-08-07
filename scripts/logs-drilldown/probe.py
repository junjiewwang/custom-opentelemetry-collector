#!/usr/bin/env python3
"""Logs Drilldown -> custom collector Loki API coverage probe.

Targets the EXACT endpoints + LogQL shapes the Grafana logs-drilldown plugin
(v2.4.0) emits, derived from source inspection of
github.com/grafana/grafana-lokiexplore-app.

By default this hits the REAL Grafana proxy path
(http://<grafana>/api/datasources/proxy/<id>/loki/api/v1/...), exercising the
full Grafana -> collector chain including X-API-Key header forwarding. Set
LD_DIRECT=1 to bypass Grafana and hit the collector's Loki API directly
(/api/v2/loki/loki/api/v1).

Configure via environment:
    LD_GRAFANA      Grafana base, default http://localhost:3000
    LD_DS_ID        numeric Grafana datasource id, default 8
    LD_DIRECT       1 to hit collector directly instead of via Grafana proxy
    LD_COLLECTOR    collector base (direct mode), default http://localhost:8088
    LD_API_KEY      value for the X-API-Key header (direct mode)
"""
import json
import os
import time
import urllib.parse
import urllib.request

GRAFANA = os.environ.get("LD_GRAFANA", "http://localhost:3000").rstrip("/")
DS_ID = os.environ.get("LD_DS_ID", "8")
DIRECT = os.environ.get("LD_DIRECT", "") == "1"
COLLECTOR = os.environ.get("LD_COLLECTOR", "http://localhost:8088").rstrip("/")
API_KEY = os.environ.get("LD_API_KEY", "")

if DIRECT:
    BASE = COLLECTOR + "/api/v2/loki/loki/api/v1"
    HDRS = {"X-API-Key": API_KEY}
else:
    BASE = GRAFANA + "/api/datasources/proxy/" + DS_ID + "/loki/api/v1"
    HDRS = {}  # Grafana proxy forwards auth + X-API-Key from datasource config

NOW = int(time.time())
START = NOW - 1800
END = NOW
# nanosecond epoch (what the Loki datasource sends for query_range/query)
SNS = START * 1_000_000_000
ENS = END * 1_000_000_000
# ISO-8601 (what logs-drilldown sends for detected_*/index endpoints)
SISO = time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime(START))
EISO = time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime(END))


def get(path, params=None):
    url = BASE + path
    if params:
        url += "?" + urllib.parse.urlencode(params, doseq=True)
    req = urllib.request.Request(url, headers=HDRS)
    try:
        with urllib.request.urlopen(req, timeout=60) as r:
            body = r.read().decode()
            try:
                return r.status, json.loads(body)
            except json.JSONDecodeError:
                return r.status, {"_raw": body[:400]}
    except urllib.error.HTTPError as e:
        body = e.read().decode()[:400]
        try:
            return e.code, json.loads(body)
        except json.JSONDecodeError:
            return e.code, {"_raw": body}
    except Exception as e:
        return 0, {"_err": str(e)}


def post(path, params):
    data = urllib.parse.urlencode(params, doseq=True).encode()
    req = urllib.request.Request(BASE + path, data=data, headers={
        **HDRS, "Content-Type": "application/x-www-form-urlencoded"})
    try:
        with urllib.request.urlopen(req, timeout=60) as r:
            body = r.read().decode()
            try:
                return r.status, json.loads(body)
            except json.JSONDecodeError:
                return r.status, {"_raw": body[:400]}
    except urllib.error.HTTPError as e:
        body = e.read().decode()[:400]
        try:
            return e.code, json.loads(body)
        except json.JSONDecodeError:
            return e.code, {"_raw": body}
    except Exception as e:
        return 0, {"_err": str(e)}


def streams_of(body):
    d = body.get("data") or {}
    if isinstance(d, dict):
        return d.get("result", [])
    return []


def nstreams(body):
    return len(streams_of(body))


def nseries(body):
    d = body.get("data") or {}
    if isinstance(d, dict):
        return len(d.get("result", []))
    return 0


def firstval(body):
    res = (body.get("data") or {}).get("result", [])
    if not res:
        return None
    r = res[0]
    if "values" in r:          # matrix / streams
        return r["values"][0][1] if r.get("values") else None
    if "value" in r:           # vector / instant
        return r["value"][1]
    return None


def summarize(status, body):
    if status != 200:
        err = body.get("error", body.get("_raw", body.get("_err", "")))
        return "HTTP%s" % status, str(err)[:160]
    return "OK", ""
