/**
 * TraceDetail 组件 - Trace 调用链时间轴可视化
 *
 * 功能：
 * - 展示完整的 Span 树形结构
 * - 时间轴 bar 可视化每个 Span 的相对起始时间和持续时长
 * - 展开/收起 Span 详情（Tags、Logs、Process）
 * - 颜色按 Service 区分
 */

import { useState, useMemo } from 'react';
import { useNavigate } from 'react-router-dom';
import { buildSpanTree, formatDuration, formatTimestamp, getServiceColor } from '@/utils/trace';
import type { JaegerTrace, JaegerKeyValue, SpanTreeNode } from '@/types/trace';

interface TraceDetailProps {
  trace: JaegerTrace;
  onClose: () => void;
}

export default function TraceDetail({ trace, onClose }: TraceDetailProps) {
  const navigate = useNavigate();
  const spanTree = useMemo(() => buildSpanTree(trace), [trace]);

  // 计算 trace 总时长（微秒）
  const traceDuration = useMemo(() => {
    let minStart = Infinity;
    let maxEnd = 0;
    for (const span of trace.spans) {
      minStart = Math.min(minStart, span.startTime);
      maxEnd = Math.max(maxEnd, span.startTime + span.duration);
    }
    return maxEnd - minStart;
  }, [trace]);

  // 统计 services
  const serviceStats = useMemo(() => {
    const map = new Map<string, number>();
    for (const span of trace.spans) {
      const proc = trace.processes[span.processID];
      if (proc) {
        map.set(proc.serviceName, (map.get(proc.serviceName) ?? 0) + 1);
      }
    }
    return Array.from(map.entries()).map(([name, count]) => ({ name, count }));
  }, [trace]);

  return (
    <div className="bg-white rounded-xl shadow-sm border border-gray-200 overflow-hidden">
      {/* Header */}
      <div className="px-6 py-4 border-b border-gray-200 bg-gray-50">
        <div className="flex items-center justify-between">
          <div>
            <h3 className="text-lg font-bold text-gray-800 flex items-center gap-2">
              <i className="fas fa-project-diagram text-primary-600" />
              Trace Detail
            </h3>
            <div className="flex items-center gap-4 mt-1 text-sm text-gray-500">
              <span className="font-mono">{trace.traceID}</span>
              <span>{trace.spans.length} Spans</span>
              <span>{serviceStats.length} Services</span>
              <span className="font-semibold text-gray-700">{formatDuration(traceDuration)}</span>
            </div>
          </div>
          <div className="flex items-center gap-2">
            {/* Trace → Metric 联动: 跳转到对应 Service 的 RED Dashboard */}
            {serviceStats.length > 0 && (
              <button
                onClick={() => {
                  const rootService = serviceStats[0]?.name;
                  if (rootService) {
                    navigate(`/metrics?tab=red&service=${encodeURIComponent(rootService)}`);
                  }
                }}
                className="px-3 py-1.5 text-xs bg-primary-50 text-primary-700 hover:bg-primary-100 rounded-lg transition flex items-center gap-1.5"
                title="查看该 Service 的 RED 指标"
              >
                <i className="fas fa-chart-line" />
                View Metrics
              </button>
            )}
            {/* Trace → Trace 搜索: 用 root service 在同一时段搜索更多 Trace */}
            <button
              onClick={() => {
                const rootService = serviceStats[0]?.name;
                if (rootService) {
                  navigate(`/traces?service=${encodeURIComponent(rootService)}&lookback=1h`);
                }
              }}
              className="px-3 py-1.5 text-xs bg-gray-100 text-gray-600 hover:bg-gray-200 rounded-lg transition flex items-center gap-1.5"
              title="搜索该 Service 的更多 Trace"
            >
              <i className="fas fa-search" />
              More Traces
            </button>
            <button
              onClick={onClose}
              className="p-2 text-gray-400 hover:text-gray-600 hover:bg-gray-200 rounded-lg transition"
            >
              <i className="fas fa-times" />
            </button>
          </div>
        </div>

        {/* Service Legend */}
        <div className="flex items-center gap-3 mt-3 flex-wrap">
          {serviceStats.map(svc => (
            <span key={svc.name} className="flex items-center gap-1 text-xs">
              <span
                className="w-3 h-3 rounded-sm flex-shrink-0"
                style={{ backgroundColor: getServiceColor(svc.name) }}
              />
              <span className="text-gray-600">{svc.name} ({svc.count})</span>
            </span>
          ))}
        </div>
      </div>

      {/* Timeline Header */}
      <div className="px-6 py-2 border-b border-gray-100 flex items-center text-xs text-gray-400">
        <div className="w-1/3 flex-shrink-0">Service :: Operation</div>
        <div className="flex-1 flex items-center justify-between">
          <span>0ms</span>
          <span>{formatDuration(traceDuration / 4)}</span>
          <span>{formatDuration(traceDuration / 2)}</span>
          <span>{formatDuration(traceDuration * 3 / 4)}</span>
          <span>{formatDuration(traceDuration)}</span>
        </div>
      </div>

      {/* Span Rows */}
      <div className="max-h-[600px] overflow-y-auto">
        {spanTree.map(node => (
          <SpanRow
            key={node.span.spanID}
            node={node}
            traceDuration={traceDuration}
            traceStartTime={trace.spans.reduce((min, s) => Math.min(min, s.startTime), Infinity)}
          />
        ))}
      </div>
    </div>
  );
}

// ============================================================================
// SpanRow 组件 - 单个 Span 行（递归渲染子 Span）
// ============================================================================

interface SpanRowProps {
  node: SpanTreeNode;
  traceDuration: number;
  traceStartTime: number;
}

function SpanRow({ node, traceDuration, traceStartTime }: SpanRowProps) {
  const [expanded, setExpanded] = useState(false);
  const { span, process, children, depth } = node;

  const serviceColor = getServiceColor(process.serviceName);
  const hasError = span.tags.some(t =>
    (t.key === 'error' && t.value === true) ||
    (t.key === 'otel.status_code' && t.value === 'ERROR'),
  );

  // 计算时间轴位置
  const offsetPercent = traceDuration > 0
    ? ((span.startTime - traceStartTime) / traceDuration) * 100
    : 0;
  const widthPercent = traceDuration > 0
    ? Math.max((span.duration / traceDuration) * 100, 0.3) // 最小 0.3% 可见
    : 0;

  return (
    <>
      {/* Span Bar Row */}
      <div
        className={`flex items-center border-b border-gray-50 hover:bg-gray-50 cursor-pointer transition-colors ${
          hasError ? 'bg-red-50/50' : ''
        }`}
        onClick={() => setExpanded(!expanded)}
      >
        {/* Left: Service :: Operation */}
        <div
          className="w-1/3 flex-shrink-0 px-4 py-2 flex items-center gap-1 text-sm truncate"
          style={{ paddingLeft: `${16 + depth * 20}px` }}
        >
          {children.length > 0 && (
            <i className={`fas fa-caret-${expanded ? 'down' : 'right'} text-gray-400 w-3 text-xs`} />
          )}
          {children.length === 0 && <span className="w-3" />}
          <span
            className="w-2 h-2 rounded-full flex-shrink-0"
            style={{ backgroundColor: serviceColor }}
          />
          <span className="text-gray-600 truncate" title={`${process.serviceName}::${span.operationName}`}>
            <span className="font-medium">{process.serviceName}</span>
            <span className="text-gray-400">::</span>
            {span.operationName}
          </span>
          {hasError && (
            <i className="fas fa-exclamation-circle text-red-500 text-xs ml-1" />
          )}
        </div>

        {/* Right: Timeline Bar */}
        <div className="flex-1 px-4 py-2 relative h-8">
          <div
            className="absolute top-1/2 -translate-y-1/2 rounded-sm h-5 min-w-[3px] flex items-center justify-end"
            style={{
              left: `${offsetPercent}%`,
              width: `${widthPercent}%`,
              backgroundColor: serviceColor,
              opacity: hasError ? 1 : 0.8,
            }}
          >
            <span className="text-white text-[10px] px-1 whitespace-nowrap">
              {formatDuration(span.duration)}
            </span>
          </div>
        </div>
      </div>

      {/* Expanded Detail */}
      {expanded && (
        <div className="border-b border-gray-100 bg-gray-50 px-6 py-3">
          <SpanDetail span={span} process={process} />
        </div>
      )}

      {/* Children */}
      {children.map(child => (
        <SpanRow
          key={child.span.spanID}
          node={child}
          traceDuration={traceDuration}
          traceStartTime={traceStartTime}
        />
      ))}
    </>
  );
}

// ============================================================================
// SpanDetail 组件 - 展开的 Span 详细信息
// ============================================================================

interface SpanDetailProps {
  span: SpanTreeNode['span'];
  process: SpanTreeNode['process'];
}

function SpanDetail({ span, process }: SpanDetailProps) {
  return (
    <div className="space-y-3 text-sm">
      {/* Basic Info */}
      <div className="grid grid-cols-2 gap-2 text-xs">
        <div>
          <span className="text-gray-400">Span ID:</span>{' '}
          <span className="font-mono text-gray-600">{span.spanID}</span>
        </div>
        <div>
          <span className="text-gray-400">Start Time:</span>{' '}
          <span className="text-gray-600">{formatTimestamp(span.startTime)}</span>
        </div>
        <div>
          <span className="text-gray-400">Duration:</span>{' '}
          <span className="font-semibold text-gray-700">{formatDuration(span.duration)}</span>
        </div>
        <div>
          <span className="text-gray-400">Service:</span>{' '}
          <span className="text-gray-600">{process.serviceName}</span>
        </div>
      </div>

      {/* Tags */}
      {span.tags.length > 0 && (
        <div>
          <h4 className="text-xs font-semibold text-gray-500 mb-1">Tags ({span.tags.length})</h4>
          <div className="bg-white rounded border border-gray-200 overflow-hidden">
            <table className="w-full text-xs">
              <tbody>
                {span.tags.map((tag, i) => (
                  <tr key={i} className={i % 2 === 0 ? 'bg-gray-50' : ''}>
                    <td className="px-3 py-1 font-mono text-gray-600 w-1/3">{tag.key}</td>
                    <td className="px-3 py-1 text-gray-800">
                      <TagValue tag={tag} />
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        </div>
      )}

      {/* Process Tags */}
      {process.tags.length > 0 && (
        <div>
          <h4 className="text-xs font-semibold text-gray-500 mb-1">Process Tags ({process.tags.length})</h4>
          <div className="bg-white rounded border border-gray-200 overflow-hidden">
            <table className="w-full text-xs">
              <tbody>
                {process.tags.map((tag, i) => (
                  <tr key={i} className={i % 2 === 0 ? 'bg-gray-50' : ''}>
                    <td className="px-3 py-1 font-mono text-gray-600 w-1/3">{tag.key}</td>
                    <td className="px-3 py-1 text-gray-800">
                      <TagValue tag={tag} />
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        </div>
      )}

      {/* Logs */}
      {span.logs.length > 0 && (
        <div>
          <h4 className="text-xs font-semibold text-gray-500 mb-1">Logs ({span.logs.length})</h4>
          <div className="space-y-1">
            {span.logs.map((log, i) => (
              <div key={i} className="bg-white rounded border border-gray-200 px-3 py-2">
                <div className="text-[10px] text-gray-400 mb-1">
                  {formatTimestamp(log.timestamp)}
                </div>
                {log.fields.map((field, j) => (
                  <div key={j} className="text-xs">
                    <span className="font-mono text-gray-500">{field.key}:</span>{' '}
                    <span className="text-gray-700">{String(field.value)}</span>
                  </div>
                ))}
              </div>
            ))}
          </div>
        </div>
      )}
    </div>
  );
}

// ============================================================================
// TagValue 组件 - 格式化 Tag 值显示
// ============================================================================

function TagValue({ tag }: { tag: JaegerKeyValue }) {
  const val = String(tag.value);

  // 错误标签高亮
  if (tag.key === 'error' && tag.value === true) {
    return <span className="text-red-600 font-semibold">true</span>;
  }
  if (tag.key === 'otel.status_code' && tag.value === 'ERROR') {
    return <span className="text-red-600 font-semibold">ERROR</span>;
  }
  if (tag.key === 'http.status_code') {
    const code = Number(val);
    if (code >= 400) {
      return <span className="text-red-600 font-semibold">{val}</span>;
    }
    if (code >= 300) {
      return <span className="text-yellow-600">{val}</span>;
    }
    return <span className="text-green-600">{val}</span>;
  }

  // 布尔值
  if (typeof tag.value === 'boolean') {
    return <span className={tag.value ? 'text-green-600' : 'text-gray-400'}>{val}</span>;
  }

  return <span className="break-all">{val}</span>;
}
