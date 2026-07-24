// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package elasticsearch

import (
	"strings"

	"go.opentelemetry.io/collector/custom/extension/observabilitystorageext/storedmodel"
)

// ═══════════════════════════════════════════════════
// PromQL → ES Metric Label Translator
// ═══════════════════════════════════════════════════
//
// OpenTelemetry standard attributes use dot-separated keys (e.g. "span.kind",
// "service.name"). The Prometheus exporter replaces dots with underscores
// (e.g. "span_kind", "service_name"). ES stores the original OTel dot-format
// keys under the "labels" field.
//
// Custom attributes that never had dots in OTel (e.g. "client", "server",
// "connection_type") are stored in ES as-is and must NOT be translated.
//
// This translator maps known OTel standard attribute keys from Prometheus
// underscore format back to dot format, and normalizes enum values
// (e.g. "SPAN_KIND_SERVER" → "Server").

// prometheusToOtelLabelKeys maps Prometheus-style label keys (underscores)
// to OTel-style dot-separated label keys as stored in ES.
//
// Only OTel standard semantic convention attributes are included here.
// Custom/user-defined labels pass through unchanged.
var prometheusToOtelLabelKeys = map[string]string{
	// ── Span attributes ──
	"span_kind": "span.kind",
	"span_name": "span.name",

	// ── Resource attributes ──
	"service_name":        "service.name",
	"service_instance_id": "service.instance.id",
	"service_version":     "service.version",
	"service_namespace":   "service.namespace",

	// ── Process ──
	"process_pid":                  "process.pid",
	"process_command_line":         "process.command_line",
	"process_executable_path":      "process.executable_path",
	"process_runtime_name":         "process.runtime_name",
	"process_runtime_version":      "process.runtime_version",
	"process_runtime_description":  "process.runtime_description",

	// ── Telemetry ──
	"telemetry_sdk_name":      "telemetry.sdk_name",
	"telemetry_sdk_language":  "telemetry.sdk_language",
	"telemetry_sdk_version":   "telemetry.sdk_version",
	"telemetry_distro_name":   "telemetry.distro_name",
	"telemetry_distro_version": "telemetry.distro_version",

	// ── Host / OS ──
	"host_arch":        "host.arch",
	"host_name":        "host.name",
	"os_type":          "os.type",
	"os_description":   "os.description",

	// ── Container ──
	"container_id": "container.id",

	// ── App ──
	"app_id": "app_id",
	"custom": "custom",

	// ── Status ──
	"status_code":    "status.code",
	"status_message": "status.message",

	// ── Peer / Network ──
	"peer_service":  "peer.service",
	"net_peer_name": "net.peer.name",
	"net_peer_port": "net.peer.port",
	"net_transport": "net.transport",
	"net_host_name": "net.host.name",
	"net_host_port": "net.host.port",

	// ── HTTP ──
	"http_method":       "http.method",
	"http_status_code":  "http.status_code",
	"http_route":        "http.route",
	"http_scheme":       "http.scheme",
	"http_host":         "http.host",
	"http_url":          "http.url",
	"http_target":       "http.target",
	"http_client_ip":    "http.client_ip",
	"http_request_size": "http.request_size",

	// ── RPC ──
	"rpc_method":           "rpc.method",
	"rpc_service":          "rpc.service",
	"rpc_system":           "rpc.system",
	"rpc_grpc_status_code": "rpc.grpc.status_code",

	// ── Database ──
	"db_system":    "db.system",
	"db_name":      "db.name",
	"db_operation": "db.operation",
	"db_statement": "db.statement",
	"db_user":      "db.user",

	// ── Messaging ──
	"messaging_system":      "messaging.system",
	"messaging_destination": "messaging.destination",
	"messaging_message_id":  "messaging.message_id",

	// ── Exception ──
	"exception_type":    "exception.type",
	"exception_message": "exception.message",

	// ── Code / Thread ──
	"thread_name":   "thread.name",
	"thread_id":     "thread.id",
	"code_function": "code.function",
	"code_namespace": "code.namespace",
	"code_filepath":  "code.filepath",

	// ── URL ──
	"url_scheme": "url.scheme",
	"url_full":   "url.full",
	"url_path":   "url.path",
	"url_query":  "url.query",
}

// translateLabelKey returns the ES-side label key for a Prometheus-style key.
// Known OTel standard attributes get dot conversion. All keys are sanitized
// via SanitizeMetricKey to match the metric storage format (dots → underscores).
func translateLabelKey(promKey string) string {
	var otelKey string
	if known, ok := prometheusToOtelLabelKeys[promKey]; ok {
		otelKey = known
	} else {
		otelKey = promKey
	}
	return storedmodel.SanitizeMetricKey(otelKey)
}

// translateLabelValue normalizes known OTel enum values for the given ES key.
// Prometheus queries may send full enum strings (e.g. "SPAN_KIND_SERVER") or
// short forms (e.g. "Server"). ES stores short forms. This function normalizes
// any input to the short-form value stored in ES.
func translateLabelValue(esKey, value string) string {
	switch esKey {
	case "span.kind", "span_kind":
		return normalizeSpanKindForStorage(value)
	case "status.code", "status_code":
		return normalizeStatusCodeForStorage(value)
	default:
		return value
	}
}

// normalizeSpanKindForStorage converts any span kind representation to the
// canonical Storage form used in ES labels.span.kind field.
//   Server  ← SPAN_KIND_SERVER, server, Server, 2
//   Client  ← SPAN_KIND_CLIENT, client, Client, 3
func normalizeSpanKindForStorage(value string) string {
	switch strings.ToUpper(strings.TrimSpace(value)) {
	case "INTERNAL", "SPAN_KIND_INTERNAL", "1":
		return "Internal"
	case "SERVER", "SPAN_KIND_SERVER", "2":
		return "Server"
	case "CLIENT", "SPAN_KIND_CLIENT", "3":
		return "Client"
	case "PRODUCER", "SPAN_KIND_PRODUCER", "4":
		return "Producer"
	case "CONSUMER", "SPAN_KIND_CONSUMER", "5":
		return "Consumer"
	default:
		return "Unspecified"
	}
}

// normalizeStatusCodeForStorage converts any status code representation to the
// canonical Storage form used in ES.
//   Ok    ← STATUS_CODE_OK, ok, Ok, 1
//   Error ← STATUS_CODE_ERROR, error, Error, 2
func normalizeStatusCodeForStorage(value string) string {
	switch strings.ToUpper(strings.TrimSpace(value)) {
	case "OK", "STATUS_CODE_OK", "1":
		return "Ok"
	case "ERROR", "STATUS_CODE_ERROR", "2":
		return "Error"
	default:
		return "Unset"
	}
}

// normalizeMetricQueryLabels translates a set of Prometheus-style labels to
// ES-compatible labels. Both exact-match labels and regex-match labels are
// translated (keys only for regex, keys+values for exact match).
func normalizeMetricQueryLabels(labels, labelMatch map[string]string) (map[string]string, map[string]string) {
	// Translate exact-match labels: both key and value.
	outLabels := make(map[string]string, len(labels))
	for k, v := range labels {
		esKey := translateLabelKey(k)
		esValue := translateLabelValue(esKey, v)
		outLabels[esKey] = esValue
	}

	// Translate regex-match labels: key only (patterns are user-provided).
	outMatch := make(map[string]string, len(labelMatch))
	for k, v := range labelMatch {
		esKey := translateLabelKey(k)
		outMatch[esKey] = v
	}

	return outLabels, outMatch
}
