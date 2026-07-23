// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package logql

import (
	"encoding/json"
	"fmt"
	"strings"
)

// PipelineResult holds pipeline-processed log entries.
type PipelineResult struct {
	Logs []PipelineLog
}

// PipelineLog is a single log entry after pipeline processing.
type PipelineLog struct {
	Timestamp string            // nanosecond timestamp
	Line      string            // the log line (after pipeline processing)
	Labels    map[string]string // extracted labels from pipeline stages
}

// ApplyPipeline processes log entries through the pipeline stages.
func ApplyPipeline(rawLogs [][]string, rawLabels map[string]string, stages []PipelineStage) []PipelineLog {
	logs := make([]PipelineLog, len(rawLogs))
	for i, entry := range rawLogs {
		ts := ""
		body := ""
		if len(entry) > 0 {
			ts = entry[0]
		}
		if len(entry) > 1 {
			body = entry[1]
		}

		labels := make(map[string]string)
		for k, v := range rawLabels {
			labels[k] = v
		}

		line := body
		for _, stage := range stages {
			switch stage.Type {
			case PipelineParser:
				line, labels = applyParserStage(stage.Parser, body, labels)
			case PipelineLabelFilter:
				if stage.LabelFilter != nil && !matchLabelFilter(stage.LabelFilter, labels) {
					line = "" // filter out this log entry
				}
			case PipelineLineFormat:
				if stage.LineFormat != "" {
					line = formatLine(stage.LineFormat, labels, body)
				}
			}
		}

		logs[i] = PipelineLog{
			Timestamp: ts,
			Line:      line,
			Labels:    labels,
		}
	}

	// Filter out entries that were filtered out by label filters.
	result := make([]PipelineLog, 0, len(logs))
	for _, l := range logs {
		if l.Line != "" {
			result = append(result, l)
		}
	}
	return result
}

func applyParserStage(parser string, body string, labels map[string]string) (string, map[string]string) {
	switch parser {
	case "json":
		var parsed map[string]interface{}
		if err := json.Unmarshal([]byte(body), &parsed); err == nil {
			for k, v := range parsed {
				labels[k] = stringOrEmpty(v)
			}
		}
		return body, labels
	case "logfmt":
		pairs := parseLogfmt(body)
		for k, v := range pairs {
			labels[k] = v
		}
		return body, labels
	case "unpack":
		var parsed map[string]interface{}
		if err := json.Unmarshal([]byte(body), &parsed); err == nil {
			for k, v := range parsed {
				labels[k] = stringOrEmpty(v)
			}
		} else {
			pairs := parseLogfmt(body)
			for k, v := range pairs {
				labels[k] = v
			}
		}
		return body, labels
	default:
		return body, labels
	}
}

// MatchPipelineLabelFilter checks whether a label matcher passes for the given labels.
// Exported so the Loki handler can apply pipeline label filters to log query results.
func MatchPipelineLabelFilter(m *LabelMatcher, labels map[string]string) bool {
	return matchLabelFilter(m, labels)
}

func matchLabelFilter(m *LabelMatcher, labels map[string]string) bool {
	val, ok := labels[m.Name]
	switch m.Type {
	case MatchEqual:
		return ok && val == m.Value
	case MatchNotEqual:
		return !ok || val != m.Value
	case MatchRegex:
		return ok && containsLit(val, m.Value)
	case MatchNotRegex:
		return !ok || !containsLit(val, m.Value)
	default:
		return true
	}
}

func containsLit(val, pattern string) bool {
	p := strings.Trim(pattern, ".*^$")
	return strings.Contains(val, p)
}

func formatLine(tmpl string, labels map[string]string, body string) string {
	s := tmpl
	for k, v := range labels {
		s = strings.ReplaceAll(s, "{{."+k+"}}", v)
	}
	s = strings.ReplaceAll(s, "{{.body}}", body)
	return s
}

// parseLogfmt parses key=value pairs, supporting quoted values.
func parseLogfmt(s string) map[string]string {
	result := make(map[string]string)
	for len(s) > 0 {
		s = strings.TrimSpace(s)
		if s == "" {
			break
		}
		eq := strings.IndexByte(s, '=')
		if eq < 0 {
			break
		}
		key := s[:eq]
		s = s[eq+1:]
		var val string
		if len(s) > 0 && s[0] == '"' {
			s = s[1:]
			end := strings.IndexByte(s, '"')
			if end >= 0 {
				val = s[:end]
				s = s[end+1:]
			} else {
				val = s
				s = ""
			}
		} else {
			end := strings.IndexByte(s, ' ')
			if end >= 0 {
				val = s[:end]
				s = s[end:]
			} else {
				val = s
				s = ""
			}
		}
		result[key] = val
	}
	return result
}

func stringOrEmpty(v interface{}) string {
	if v == nil {
		return ""
	}
	switch val := v.(type) {
	case string:
		return val
	case float64:
		return fmt.Sprintf("%v", val)
	default:
		b, _ := json.Marshal(v)
		return string(b)
	}
}
