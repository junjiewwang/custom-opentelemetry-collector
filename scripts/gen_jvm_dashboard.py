#!/usr/bin/env python3
"""Grafana 12 dashboard generator for JVM Runtime metrics — leveraging
the full PromQL engine (delta, rate, arithmetic, sum, max, avg).
Schema version 41 for Grafana 12.0.1.
"""

import json

dashboard = {
    "title": "JVM Runtime Dashboard",
    "uid": "jvm-runtime",
    "tags": ["jvm", "otel"],
    "timezone": "browser",
    "schemaVersion": 41,
    "refresh": "30s",
    "time": {"from": "now-1h", "to": "now"},
    "templating": {
        "list": [{
            "name": "service",
            "label": "Service",
            "type": "query",
            "current": {"text": "All", "value": "$__all"},
            "datasource": {"type": "prometheus", "uid": "bfroe09bnha0wd"},
            "query": {"query": "label_values(jvm.memory.used, service_name)", "refId": "service"},
            "multi": True,
            "includeAll": True
        }]
    },
    "panels": []
}

DS = {"type": "prometheus", "uid": "bfroe09bnha0wd"}
SVC_FILTER = 'service_name=~"$service"'
LINE = {
    "drawStyle": "line", "lineInterpolation": "linear", "lineWidth": 3,
    "fillOpacity": 25, "spanNulls": 300000, "showPoints": "auto", "pointSize": 5,
    "gradientMode": "none",
}


def target(ref_id, expr, legend):
    return {"datasource": DS, "expr": expr, "legendFormat": legend, "refId": ref_id}


def timeseries_panel(id_, title, targets, unit, overrides=None, w=12, y=1, custom_extra=None, line_style=None):
    c = {"unit": unit, **LINE, **(custom_extra or {})}
    if line_style:
        c = {**c, **line_style}
    return {
        "type": "timeseries", "id": id_, "title": title, "datasource": DS,
        "gridPos": {"h": 10, "w": w, "x": 0, "y": y},
        "targets": targets,
        "fieldConfig": {
            "defaults": {"unit": unit, "custom": c},
            "overrides": overrides or []
        },
        "options": {"tooltip": {"mode": "multi", "sort": "desc"}, "legend": {"displayMode": "table", "placement": "bottom"}}
    }


# ═══════════════════ Memory ═══════════════════
dashboard["panels"].append({"type": "row", "title": "Memory", "gridPos": {"h": 1, "w": 24, "x": 0, "y": 0}, "id": 1})

# Panel 2: Heap Used vs Committed (unchanged — gauges)
dashboard["panels"].append(timeseries_panel(
    2, "Heap - Used vs Committed",
    [
        target("A", f'jvm.memory.used{{{SVC_FILTER}, jvm_memory_type="heap"}}', "{{service_name}} used - {{jvm_memory_pool_name}}"),
        target("B", f'jvm.memory.committed{{{SVC_FILTER}, jvm_memory_type="heap"}}', "{{service_name}} committed - {{jvm_memory_pool_name}}"),
    ],
    "bytes",
    overrides=[{
        "matcher": {"id": "byRegexp", "options": "/committed/"},
        "properties": [{"id": "custom.lineStyle", "value": {"fill": "dash", "dash": [10, 5]}}]
    }],
    y=1
))

# Panel 3: Non-Heap (unchanged)
dashboard["panels"].append(timeseries_panel(
    3, "Non-Heap - Used vs Committed",
    [
        target("A", f'jvm.memory.used{{{SVC_FILTER}, jvm_memory_type="non_heap"}}', "{{service_name}} used - {{jvm_memory_pool_name}}"),
        target("B", f'jvm.memory.committed{{{SVC_FILTER}, jvm_memory_type="non_heap"}}', "{{service_name}} committed - {{jvm_memory_pool_name}}"),
    ],
    "bytes",
    overrides=[{
        "matcher": {"id": "byRegexp", "options": "/committed/"},
        "properties": [{"id": "custom.lineStyle", "value": {"fill": "dash", "dash": [10, 5]}}]
    }],
    y=1, w=12,
))
dashboard["panels"][-1]["gridPos"]["x"] = 12

# Panel 4: Heap Used % — simple division (same labels, same metric, same pool)
dashboard["panels"].append(timeseries_panel(
    4, "Heap Used %",
    [
        target("A",
            f'jvm.memory.used{{{SVC_FILTER}, jvm_memory_type="heap", jvm_memory_pool_name="Tenured Gen"}} / '
            f'jvm.memory.limit{{{SVC_FILTER}, jvm_memory_type="heap", jvm_memory_pool_name="Tenured Gen"}} * 100',
            "{{service_name}} Tenured Gen %"),
    ],
    "percent", y=11, w=8,
    custom_extra={"min": 0, "max": 100}
))
dashboard["panels"][-1]["gridPos"]["x"] = 0
dashboard["panels"][-1]["fieldConfig"]["defaults"]["thresholds"] = {
    "steps": [{"color": "green"}, {"color": "yellow", "value": 60}, {"color": "red", "value": 85}]
}

# Tenured Gen After GC + Metaspace Used — timeseries (not stat) so multi-service
# selections render one line per service instead of collapsing to a single value.
dashboard["panels"].append(timeseries_panel(
    5, "Tenured Gen After GC",
    [target("A", f'jvm.memory.used_after_last_gc{{{SVC_FILTER}, jvm_memory_pool_name="Tenured Gen"}}', "{{service_name}}")],
    "bytes", w=8, y=11
))
dashboard["panels"][-1]["gridPos"]["x"] = 8

dashboard["panels"].append(timeseries_panel(
    6, "Metaspace Used",
    [target("A", f'jvm.memory.used{{{SVC_FILTER}, jvm_memory_pool_name="Metaspace"}}', "{{service_name}}")],
    "bytes", w=8, y=11
))
dashboard["panels"][-1]["gridPos"]["x"] = 16

# ═══════════════════ GC ═══════════════════
dashboard["panels"].append({"type": "row", "title": "GC", "gridPos": {"h": 1, "w": 24, "x": 0, "y": 17}, "id": 7})

# Panel 8: GC Duration (cumulative — unchanged)
dashboard["panels"].append(timeseries_panel(
    8, "GC Duration (cumulative seconds)",
    [target("A", f'jvm.gc.duration{{{SVC_FILTER}}}', "{{service_name}} {{jvm_gc_name}} {{jvm_gc_action}}")],
    "s", w=12, y=18
))

# Panel 9: GC Rate (NEW — rate() via promql.Engine)
dashboard["panels"].append(timeseries_panel(
    9, "GC Rate (seconds/minute)",
    [target("A", f'rate(jvm.gc.duration{{{SVC_FILTER}}}[5m]) * 60', "{{service_name}} {{jvm_gc_name}} {{jvm_gc_action}}")],
    "s", w=12, y=18
))
dashboard["panels"][-1]["gridPos"]["x"] = 12

# ═══════════════════ Runtime ═══════════════════
dashboard["panels"].append({"type": "row", "title": "Runtime", "gridPos": {"h": 1, "w": 24, "x": 0, "y": 28}, "id": 10})

# Panel 11: Thread Count (unchanged)
thread_custom = {**LINE, "stacking": {"mode": "normal", "group": "A"}}
dashboard["panels"].append(timeseries_panel(
    11, "Thread Count (by state)",
    [target("A", f'jvm.thread.count{{{SVC_FILTER}}}', "{{service_name}} {{jvm_thread_daemon}} {{jvm_thread_state}}")],
    "short", w=8, y=29, custom_extra=thread_custom
))

# Panel 12: CPU Utilization (unchanged)
dashboard["panels"].append(timeseries_panel(
    12, "CPU Recent Utilization",
    [target("A", f'jvm.cpu.recent_utilization{{{SVC_FILTER}}}', "{{service_name}}")],
    "percentunit", w=8, y=29, custom_extra={"min": 0, "max": 1}
))
dashboard["panels"][-1]["gridPos"]["x"] = 8

# Panel 13: CPU Rate (NEW — delta via promql.Engine)
dashboard["panels"].append(timeseries_panel(
    13, "CPU Core Usage (delta 5m)",
    [target("A", f'delta(jvm.cpu.time{{{SVC_FILTER}}}[5m])', "{{service_name}}")],
    "s", w=8, y=29,
    custom_extra={"min": 0}
))
dashboard["panels"][-1]["gridPos"]["x"] = 16

# ═══════════════════ Classes ═══════════════════
dashboard["panels"].append({"type": "row", "title": "Classes", "gridPos": {"h": 1, "w": 24, "x": 0, "y": 39}, "id": 14})

# Panel 15: Class Count (unchanged)
dashboard["panels"].append(timeseries_panel(
    15, "Class Count (Loaded / Unloaded)",
    [
        target("A", f'jvm.class.loaded{{{SVC_FILTER}}}', "{{service_name}} loaded"),
        target("B", f'jvm.class.unloaded{{{SVC_FILTER}}}', "{{service_name}} unloaded"),
    ],
    "short", w=8, y=40
))

# Panel 16: Class Load Rate (NEW — rate() via promql.Engine)
dashboard["panels"].append(timeseries_panel(
    16, "Class Load Rate (per minute)",
    [target("A", f'rate(jvm.class.loaded{{{SVC_FILTER}}}[5m]) * 60', "{{service_name}}")],
    "short", w=8, y=40
))
dashboard["panels"][-1]["gridPos"]["x"] = 8

# Panel 17: CPU Cores — timeseries (multi-service renders one line per service)
dashboard["panels"].append(timeseries_panel(
    17, "CPU Cores",
    [target("A", f'jvm.cpu.count{{{SVC_FILTER}}}', "{{service_name}}")],
    "short", w=8, y=40
))
dashboard["panels"][-1]["gridPos"]["x"] = 16

import os
path = os.path.expanduser("~/GolandProjects/github/custom-opentelemetry-collector/docs/grafana-jvm-dashboard.json")
with open(path, 'w') as f:
    json.dump(dashboard, f, indent=2)
    f.write('\n')
print(f"Written {os.path.getsize(path)} bytes to {path}")
print(f"schemaVersion: {dashboard['schemaVersion']}, panels: {len(dashboard['panels'])}")
