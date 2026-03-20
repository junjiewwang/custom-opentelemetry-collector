// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package testdatagenreceiver

import (
	"fmt"
	"math/rand"
	"time"

	"go.opentelemetry.io/collector/pdata/pcommon"
	"go.opentelemetry.io/collector/pdata/ptrace"
)

// BusinessFlow 定义一条完整的业务调用流编排
// 一个 Flow 生成一条跨多服务的完整 Trace，模拟真实的微服务调用链
type BusinessFlow interface {
	// Name 业务流唯一标识
	Name() string

	// Description 业务流描述
	Description() string

	// Init 根据用户配置初始化业务流
	Init(cfg map[string]interface{}) error

	// GenerateTraces 生成一条完整的业务调用链 Trace
	GenerateTraces() (ptrace.Traces, error)
}

// SpanKindType span 的类型
type SpanKindType int

const (
	SpanKindEntry    SpanKindType = iota // 入口 span（Server）
	SpanKindCall                         // 调用下游 span（Client）
	SpanKindInternal                     // 内部处理 span
	SpanKindProduce                      // 消息发送 span（Producer）
	SpanKindConsume                      // 消息消费 span（Consumer）
)

// FlowStep 描述业务流中的一个调用步骤
type FlowStep struct {
	// ServiceName 当前步骤所属的服务名
	ServiceName string

	// SpanName span 名称，如 "HTTP GET /api/orders"
	SpanName string

	// Kind span 类型
	Kind SpanKindType

	// Duration 模拟耗时范围 [MinMs, MaxMs]
	MinDurationMs int
	MaxDurationMs int

	// ErrorRate 该步骤的错误概率 (0.0 ~ 1.0)
	ErrorRate float64

	// ErrorMessage 错误消息模板
	ErrorMessage string

	// Attributes 该步骤的自定义属性
	Attributes map[string]string

	// IntAttributes 整数类型属性
	IntAttributes map[string]int64

	// ResourceAttributes 服务级别的额外 Resource 属性
	ResourceAttributes map[string]string

	// ScopeName instrumentation scope 名称，模拟真实 SDK
	ScopeName string

	// Children 该步骤的子步骤（并行或顺序调用的下游）
	Children []*FlowStep

	// IsAsync 是否为异步调用（消息队列场景：producer→consumer 通过 link 关联而非 parent-child）
	IsAsync bool
}

// FlowContext 执行流程时的共享上下文
type FlowContext struct {
	TraceID   pcommon.TraceID
	StartTime time.Time
	Offset    time.Duration // 当前时间偏移量（相对于 trace 起始时间）
	Rand      *rand.Rand
}

// ExecuteFlow 编排引擎：将 FlowStep 树转换为 ptrace.Traces
// 核心逻辑：
// 1. 同一 TraceID 贯穿所有步骤
// 2. 树形结构自动产生 parent-child 关系
// 3. 时间线严格因果递增（child 在 parent 内部）
// 4. 异步步骤（消息队列）通过 SpanLink 关联
func ExecuteFlow(rootSteps []*FlowStep, errorRate float64) ptrace.Traces {
	td := ptrace.NewTraces()
	r := rand.New(rand.NewSource(time.Now().UnixNano()))

	ctx := &FlowContext{
		TraceID:   NewTraceID(),
		StartTime: time.Now(),
		Offset:    0,
		Rand:      r,
	}

	// 按服务名分组收集 span，确保同一服务的 span 在同一 ResourceSpans 下
	serviceSpans := make(map[string]*serviceSpanCollector)

	for _, step := range rootSteps {
		buildSpans(ctx, step, pcommon.SpanID{}, serviceSpans, nil, errorRate)
	}

	// 将收集到的 span 写入 Traces
	for _, collector := range serviceSpans {
		rs := td.ResourceSpans().AppendEmpty()
		resAttrs := rs.Resource().Attributes()
		resAttrs.PutStr("service.name", collector.serviceName)
		resAttrs.PutStr("telemetry.sdk.language", "go")
		resAttrs.PutStr("telemetry.sdk.name", "opentelemetry")
		resAttrs.PutStr("telemetry.sdk.version", "1.28.0")
		resAttrs.PutStr("host.name", fmt.Sprintf("%s-pod-%s", collector.serviceName, randomHexStr(r, 4)))
		resAttrs.PutStr("os.type", "linux")
		resAttrs.PutStr("deployment.environment", "production")
		resAttrs.PutStr("service.namespace", "e-commerce")
		resAttrs.PutStr("service.version", collector.serviceVersion)
		resAttrs.PutStr("k8s.pod.name", fmt.Sprintf("%s-%s", collector.serviceName, randomHexStr(r, 8)))
		resAttrs.PutStr("k8s.namespace.name", "default")

		// 追加自定义 Resource 属性
		for k, v := range collector.resourceAttrs {
			resAttrs.PutStr(k, v)
		}

		// 按 scope 分组
		for scopeName, spans := range collector.scopeSpans {
			ss := rs.ScopeSpans().AppendEmpty()
			ss.Scope().SetName(scopeName)
			ss.Scope().SetVersion("1.28.0")

			for _, spanData := range spans {
				span := ss.Spans().AppendEmpty()
				spanData.CopyTo(span)
			}
		}
	}

	return td
}

// serviceSpanCollector 按服务名收集 span
type serviceSpanCollector struct {
	serviceName    string
	serviceVersion string
	resourceAttrs  map[string]string
	scopeSpans     map[string][]ptrace.Span // scopeName -> spans
}

func getOrCreateCollector(collectors map[string]*serviceSpanCollector, step *FlowStep) *serviceSpanCollector {
	if c, ok := collectors[step.ServiceName]; ok {
		return c
	}
	c := &serviceSpanCollector{
		serviceName:    step.ServiceName,
		serviceVersion: "1.0.0",
		resourceAttrs:  make(map[string]string),
		scopeSpans:     make(map[string][]ptrace.Span),
	}
	if step.ResourceAttributes != nil {
		for k, v := range step.ResourceAttributes {
			c.resourceAttrs[k] = v
		}
		if ver, ok := step.ResourceAttributes["service.version"]; ok {
			c.serviceVersion = ver
		}
	}
	collectors[step.ServiceName] = c
	return c
}

// buildSpans 递归构建 span 树
func buildSpans(
	ctx *FlowContext,
	step *FlowStep,
	parentSpanID pcommon.SpanID,
	collectors map[string]*serviceSpanCollector,
	producerSpanID *pcommon.SpanID, // 消息队列消费者的 link 来源
	globalErrorRate float64,
) pcommon.SpanID {
	collector := getOrCreateCollector(collectors, step)

	// 创建临时 Traces 来构建 span（因为 pdata 不允许直接创建独立 span）
	tmpTraces := ptrace.NewTraces()
	tmpSpan := tmpTraces.ResourceSpans().AppendEmpty().ScopeSpans().AppendEmpty().Spans().AppendEmpty()

	spanID := NewSpanID()
	tmpSpan.SetTraceID(ctx.TraceID)
	tmpSpan.SetSpanID(spanID)
	tmpSpan.SetName(step.SpanName)

	// 设置 parent
	emptySpanID := pcommon.SpanID{}
	if parentSpanID != emptySpanID {
		tmpSpan.SetParentSpanID(parentSpanID)
	}

	// 设置 kind
	switch step.Kind {
	case SpanKindEntry:
		tmpSpan.SetKind(ptrace.SpanKindServer)
	case SpanKindCall:
		tmpSpan.SetKind(ptrace.SpanKindClient)
	case SpanKindInternal:
		tmpSpan.SetKind(ptrace.SpanKindInternal)
	case SpanKindProduce:
		tmpSpan.SetKind(ptrace.SpanKindProducer)
	case SpanKindConsume:
		tmpSpan.SetKind(ptrace.SpanKindConsumer)
	}

	// 设置时间（因果递增）
	minMs := step.MinDurationMs
	maxMs := step.MaxDurationMs
	if minMs <= 0 {
		minMs = 2
	}
	if maxMs <= minMs {
		maxMs = minMs + 10
	}
	duration := time.Duration(minMs+ctx.Rand.Intn(maxMs-minMs+1)) * time.Millisecond
	startTime := ctx.StartTime.Add(ctx.Offset)
	tmpSpan.SetStartTimestamp(pcommon.NewTimestampFromTime(startTime))

	// 异步消息消费场景：通过 SpanLink 关联 producer
	if step.Kind == SpanKindConsume && producerSpanID != nil {
		link := tmpSpan.Links().AppendEmpty()
		link.SetTraceID(ctx.TraceID)
		link.SetSpanID(*producerSpanID)
		link.Attributes().PutStr("opentelemetry.link.type", "messaging")
	}

	// 设置属性
	attrs := tmpSpan.Attributes()
	for k, v := range step.Attributes {
		attrs.PutStr(k, v)
	}
	for k, v := range step.IntAttributes {
		attrs.PutInt(k, v)
	}

	// 递归处理子步骤
	childOffset := ctx.Offset + time.Duration(1+ctx.Rand.Intn(3))*time.Millisecond
	for _, child := range step.Children {
		childCtx := &FlowContext{
			TraceID:   ctx.TraceID,
			StartTime: ctx.StartTime,
			Offset:    childOffset,
			Rand:      ctx.Rand,
		}

		var producerID *pcommon.SpanID
		if child.IsAsync {
			// 异步场景：当前 span 是 producer，child 是 consumer
			producerID = &spanID
		}

		childSpanID := buildSpans(childCtx, child, spanID, collectors, producerID, globalErrorRate)
		_ = childSpanID

		// 更新 offset，模拟顺序调用的时间推进
		childDuration := time.Duration(child.MinDurationMs+ctx.Rand.Intn(child.MaxDurationMs-child.MinDurationMs+1)) * time.Millisecond
		childOffset += childDuration + time.Duration(1+ctx.Rand.Intn(2))*time.Millisecond
	}

	// 结束时间：要晚于所有子 span 的结束
	totalChildDuration := childOffset - ctx.Offset
	if totalChildDuration > duration {
		duration = totalChildDuration + time.Duration(1+ctx.Rand.Intn(3))*time.Millisecond
	}
	tmpSpan.SetEndTimestamp(pcommon.NewTimestampFromTime(startTime.Add(duration)))

	// 更新 context offset
	ctx.Offset = ctx.Offset + duration

	// 模拟错误
	effectiveErrorRate := step.ErrorRate
	if effectiveErrorRate <= 0 {
		effectiveErrorRate = globalErrorRate
	}
	if effectiveErrorRate > 0 && ctx.Rand.Float64() < effectiveErrorRate {
		tmpSpan.Status().SetCode(ptrace.StatusCodeError)
		errMsg := step.ErrorMessage
		if errMsg == "" {
			errMsg = fmt.Sprintf("error in %s", step.SpanName)
		}
		tmpSpan.Status().SetMessage(errMsg)

		// 添加 exception 事件
		event := tmpSpan.Events().AppendEmpty()
		event.SetName("exception")
		event.SetTimestamp(pcommon.NewTimestampFromTime(startTime.Add(duration / 2)))
		event.Attributes().PutStr("exception.type", "RuntimeException")
		event.Attributes().PutStr("exception.message", errMsg)
	} else {
		tmpSpan.Status().SetCode(ptrace.StatusCodeOk)
	}

	// 收集到对应 service 的 collector
	scopeName := step.ScopeName
	if scopeName == "" {
		scopeName = "io.opentelemetry.auto"
	}
	collector.scopeSpans[scopeName] = append(collector.scopeSpans[scopeName], tmpSpan)

	return spanID
}

// randomHexStr 生成指定长度的随机十六进制字符串
func randomHexStr(r *rand.Rand, length int) string {
	const chars = "0123456789abcdef"
	result := make([]byte, length)
	for i := range result {
		result[i] = chars[r.Intn(len(chars))]
	}
	return string(result)
}
