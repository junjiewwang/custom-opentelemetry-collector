// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package probeconv

import (
	"maps"

	"go.opentelemetry.io/collector/custom/controlplane/model"
	controlplanev1 "go.opentelemetry.io/collector/custom/proto/controlplane/v1"
)

func AgentConfigFromProto(cfg *controlplanev1.AgentConfig) *model.AgentConfig {
	if cfg == nil {
		return nil
	}

	out := &model.AgentConfig{
		DynamicResourceAttributes: cloneStringMap(cfg.GetDynamicResourceAttributes()),
		ExtensionConfigJSON:       cfg.GetExtensionConfigJson(),
	}

	if v := cfg.GetVersion(); v != nil {
		out.Version = v.GetVersion()
		out.UpdatedAt = v.GetTimestampMillis()
		out.Etag = v.GetEtag()
	}

	if s := cfg.GetSampler(); s != nil {
		rules := make([]model.SamplerRule, 0, len(s.GetRules()))
		for _, r := range s.GetRules() {
			if r == nil {
				continue
			}
			rules = append(rules, model.SamplerRule{
				Name:            r.GetName(),
				SpanNamePattern: r.GetSpanNamePattern(),
				AttributeMatch:  cloneStringMap(r.GetAttributeMatch()),
				Ratio:           r.GetRatio(),
				Priority:        r.GetPriority(),
			})
		}

		out.Sampler = &model.SamplerConfig{
			Type:  model.SamplerType(s.GetType()),
			Ratio: s.GetRatio(),
			Rules: rules,
		}
	}

	if b := cfg.GetBatch(); b != nil {
		out.Batch = &model.BatchConfig{
			MaxExportBatchSize:  b.GetMaxExportBatchSize(),
			MaxQueueSize:        b.GetMaxQueueSize(),
			ScheduleDelayMillis: b.GetScheduleDelayMillis(),
			ExportTimeoutMillis: b.GetExportTimeoutMillis(),
		}
	}

	return out
}

func AgentConfigToProto(cfg *model.AgentConfig) *controlplanev1.AgentConfig {
	if cfg == nil {
		return nil
	}

	out := &controlplanev1.AgentConfig{
		Version: &controlplanev1.ConfigVersion{
			Version:         cfg.Version,
			TimestampMillis: cfg.UpdatedAt,
			Etag:            cfg.Etag,
		},
		DynamicResourceAttributes: cloneStringMap(cfg.DynamicResourceAttributes),
		ExtensionConfigJson:       cfg.ExtensionConfigJSON,
	}

	if cfg.Sampler != nil {
		rules := make([]*controlplanev1.SamplerRule, 0, len(cfg.Sampler.Rules))
		for _, r := range cfg.Sampler.Rules {
			rc := r
			rules = append(rules, &controlplanev1.SamplerRule{
				Name:            rc.Name,
				SpanNamePattern: rc.SpanNamePattern,
				AttributeMatch:  cloneStringMap(rc.AttributeMatch),
				Ratio:           rc.Ratio,
				Priority:        rc.Priority,
			})
		}

		out.Sampler = &controlplanev1.SamplerConfig{
			Type:  controlplanev1.SamplerConfig_SamplerType(cfg.Sampler.Type),
			Ratio: cfg.Sampler.Ratio,
			Rules: rules,
		}
		// NOTE: cfg.Sampler.RulesJSON is legacy-only; probe expects structured rules.
	}

	if cfg.Batch != nil {
		out.Batch = &controlplanev1.BatchConfig{
			MaxExportBatchSize:  cfg.Batch.MaxExportBatchSize,
			MaxQueueSize:        cfg.Batch.MaxQueueSize,
			ScheduleDelayMillis: cfg.Batch.ScheduleDelayMillis,
			ExportTimeoutMillis: cfg.Batch.ExportTimeoutMillis,
		}
	}

	return out
}

func cloneStringMap(in map[string]string) map[string]string {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]string, len(in))
	maps.Copy(out, in)
	return out
}
