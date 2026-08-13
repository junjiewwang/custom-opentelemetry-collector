// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package elasticsearch

import (
	"crypto/md5"
	"encoding/hex"
)

// MetaDoc is the document stored in the singleton metadata index ({prefix}-meta).
// One document per (appID, metric name), upserted on every write. It lets
// ListMetricNames/ListMetricTypes/ListLabelNames read a small table instead of
// running a terms aggregation over the full data index — the trigger for the
// ES 429 circuit_breaker on large deployments.
//
// Naming asymmetry (important): name/appId keep the dotted OTel form
// ("jvm.memory.used"); labelKeys are underscore-normalized via
// SanitizeMetricKey ("server_address"). Readers must preserve this split.
type MetaDoc struct {
	Name         string   `json:"name"`
	AppID        string   `json:"appId"`
	Type         string   `json:"type"`
	Unit         string   `json:"unit"`
	LabelKeys    []string `json:"labelKeys"`
	ServiceNames []string `json:"serviceNames"`
	LastSeenAt   int64    `json:"lastSeenAt"`
	DocCount     int64    `json:"docCount"`
}

// metaIndexName returns the singleton metadata index name for the configured
// metric prefix, e.g. "otel-metrics-meta".
func metaIndexName(prefix string) string {
	return prefix + "-meta"
}

// metaDocID builds the deterministic document _id: "{appID}:{name}".
//
// ES caps _id at 512 bytes. Long appID/name combinations are defensively
// truncated to "{appID}:{name}#{md5[:8]}" so the id stays unique and bounded.
func metaDocID(appID, name string) string {
	id := appID + ":" + name
	if len(id) <= 400 {
		return id
	}
	sum := md5.Sum([]byte(id))
	return id[:400] + "#" + hex.EncodeToString(sum[:8])
}

// metaServiceNameCap bounds the number of service names retained per meta doc.
// serviceName is a per-resource value, so a shared infrastructure metric can
// accumulate many distinct services; the cap keeps the doc bounded. The
// Painless script enforces the same cap.
const metaServiceNameCap = 20

// metaUpsertScript is the Painless script for the scripted upsert. It unions
// labelKeys and serviceNames (capped), keeps the max lastSeenAt (multi-node
// clock-skew safe), last-writer-wins type/unit, and accumulates docCount.
//
// The script must null-guard every mutable field: an upsert that follows a
// document created by the raw metric template (or an empty doc) may have null
// labelKeys/serviceNames, and ctx._source.docCount is absent on the very first
// script run against an auto-created doc.
//
// Note: "type" is a Painless reserved word, so it can NOT be written via
// ctx._source.type / ctx._source['type'] = params['type'] — both fail to
// compile ("unexpected token ['ctx']") because the reserved word poisons the
// parser. Use ctx._source.put('type', ...) instead. put() is an expression
// statement, so it MUST be terminated with a semicolon or the next line is
// swallowed into it.
//
// Note: lastSeenAt is a date field. Painless's Math.max on it fails at runtime
// (date_time_parse_exception) when the field is null on a first-seen doc, so
// use an explicit null-guarded comparison instead — which also preserves the
// "keep max lastSeenAt" clock-skew semantics.
const metaUpsertScript = `
if (ctx._source.labelKeys == null) { ctx._source.labelKeys = [] }
for (int i = 0; i < params.labelKeys.length; i++) {
  def k = params.labelKeys[i];
  if (!ctx._source.labelKeys.contains(k)) { ctx._source.labelKeys.add(k) }
}
if (ctx._source.serviceNames == null) { ctx._source.serviceNames = [] }
for (int i = 0; i < params.serviceNames.length; i++) {
  def s = params.serviceNames[i];
  if (!ctx._source.serviceNames.contains(s) && ctx._source.serviceNames.length < params.serviceCap) {
    ctx._source.serviceNames.add(s)
  }
}
if (ctx._source.lastSeenAt == null || params.lastSeenAt > ctx._source.lastSeenAt) { ctx._source.lastSeenAt = params.lastSeenAt }
ctx._source.put('type', params['type']);
ctx._source.unit = params.unit;
if (ctx._source.docCount == null) { ctx._source.docCount = 0 }
ctx._source.docCount = ctx._source.docCount + params.increment;
`

// metaUpsertParams carries the per-upsert script parameters.
type metaUpsertParams struct {
	LabelKeys    []string `json:"labelKeys"`
	ServiceNames []string `json:"serviceNames"`
	LastSeenAt   int64    `json:"lastSeenAt"`
	Type         string   `json:"type"`
	Unit         string   `json:"unit"`
	Increment    int64    `json:"increment"`
	ServiceCap   int      `json:"serviceCap"`
}

// metaScriptedUpsert builds the body of a bulk "update" action for a scripted
// upsert: the Painless script, its params, and the doc to insert on first-seen.
// The _id/_index go on the action line (see AddScriptedUpsert, Phase 2); this
// returns only the NDJSON payload following it.
func metaScriptedUpsert(doc MetaDoc) map[string]any {
	return map[string]any{
		"script": map[string]any{
			"source": metaUpsertScript,
			"lang":   "painless",
			"params": metaUpsertParams{
				LabelKeys:    doc.LabelKeys,
				ServiceNames: doc.ServiceNames,
				LastSeenAt:   doc.LastSeenAt,
				Type:         doc.Type,
				Unit:         doc.Unit,
				Increment:    1,
				ServiceCap:   metaServiceNameCap,
			},
		},
		"scripted_upsert": true,
		"upsert": map[string]any{
			FieldName:            doc.Name,
			FieldAppID:           doc.AppID,
			FieldMetricType:      doc.Type,
			FieldMetricUnit:      doc.Unit,
			FieldMetaLabelKeys:   doc.LabelKeys,
			FieldMetaServiceNames: doc.ServiceNames,
			FieldMetaLastSeenAt:  doc.LastSeenAt,
			FieldMetaDocCount:    int64(1),
		},
	}
}
