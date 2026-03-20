/**
 * TraceGraphView — DAG 服务调用关系图
 *
 * 功能：
 * - 使用 ECharts graph（force 布局）展示 Trace 中服务间的调用关系
 * - 节点 = 每个 service（去重），大小与 span count 成比例
 * - 边 = service A 的 span 调用了 service B 的 span，权重 = 调用次数
 * - 节点颜色 = getServiceColor
 * - 支持拖拽和缩放
 * - Tooltip: service name, span count, error count
 */

import { useMemo } from 'react';
import ReactECharts from 'echarts-for-react';
import type { JaegerTrace, JaegerSpan } from '@/types/trace';
import { getServiceColor } from '@/utils/trace';

interface TraceViewProps {
  trace: JaegerTrace;
}

// ============================================================================
// 数据提取
// ============================================================================

interface ServiceNode {
  name: string;
  spanCount: number;
  errorCount: number;
}

interface ServiceEdge {
  source: string;
  target: string;
  callCount: number;
}

function isSpanError(span: JaegerSpan): boolean {
  return span.tags.some(
    (t) =>
      (t.key === 'error' && t.value === true) ||
      (t.key === 'otel.status_code' && t.value === 'ERROR'),
  );
}

function extractGraphData(trace: JaegerTrace): {
  nodes: ServiceNode[];
  edges: ServiceEdge[];
} {
  const { spans, processes } = trace;

  // 统计每个 service 的 span 信息
  const serviceMap = new Map<string, { spanCount: number; errorCount: number }>();

  // spanID → serviceName 映射
  const spanServiceMap = new Map<string, string>();

  for (const span of spans) {
    const proc = processes[span.processID];
    const svc = proc?.serviceName ?? 'unknown';
    spanServiceMap.set(span.spanID, svc);

    const entry = serviceMap.get(svc) ?? { spanCount: 0, errorCount: 0 };
    entry.spanCount++;
    if (isSpanError(span)) entry.errorCount++;
    serviceMap.set(svc, entry);
  }

  // 提取边: parent-child 关系推断服务间调用
  const edgeMap = new Map<string, number>();

  for (const span of spans) {
    const childService = spanServiceMap.get(span.spanID);
    if (!childService) continue;

    for (const ref of span.references) {
      if (ref.refType !== 'CHILD_OF') continue;
      const parentService = spanServiceMap.get(ref.spanID);
      if (!parentService || parentService === childService) continue;

      const edgeKey = `${parentService}→${childService}`;
      edgeMap.set(edgeKey, (edgeMap.get(edgeKey) ?? 0) + 1);
    }
  }

  const nodes: ServiceNode[] = Array.from(serviceMap.entries()).map(([name, stats]) => ({
    name,
    spanCount: stats.spanCount,
    errorCount: stats.errorCount,
  }));

  const edges: ServiceEdge[] = Array.from(edgeMap.entries()).map(([key, count]) => {
    const [source, target] = key.split('→');
    return { source: source!, target: target!, callCount: count };
  });

  return { nodes, edges };
}

// ============================================================================
// 主组件
// ============================================================================

export default function TraceGraphView({ trace }: TraceViewProps) {
  const { nodes, edges } = useMemo(() => extractGraphData(trace), [trace]);

  const option = useMemo(() => {
    if (nodes.length === 0) return null;

    return {
      tooltip: {
        trigger: 'item',
        formatter: (params: any) => {
          if (params.dataType === 'node') {
            const data = params.data;
            return `
              <div style="font-weight:600; margin-bottom:4px;">${data.name}</div>
              <div>Spans: ${data.spanCount}</div>
              ${data.errorCount > 0 ? `<div style="color:#ef4444;">Errors: ${data.errorCount}</div>` : ''}
            `;
          }
          if (params.dataType === 'edge') {
            return `${params.data.source} → ${params.data.target}<br/>Calls: ${params.data.callCount}`;
          }
          return '';
        },
      },
      series: [
        {
          type: 'graph',
          layout: 'force',
          roam: true,
          draggable: true,
          force: {
            repulsion: 300,
            edgeLength: [100, 200],
            gravity: 0.1,
          },
          label: {
            show: true,
            position: 'bottom',
            fontSize: 11,
            fontWeight: 500,
            color: '#374151',
          },
          edgeLabel: {
            show: true,
            formatter: (params: any) => `${params.data.callCount}`,
            fontSize: 10,
            color: '#9ca3af',
          },
          lineStyle: {
            curveness: 0.2,
            color: '#d1d5db',
          },
          emphasis: {
            focus: 'adjacency',
            lineStyle: {
              width: 4,
              color: '#6366f1',
            },
          },
          data: nodes.map((node) => ({
            name: node.name,
            spanCount: node.spanCount,
            errorCount: node.errorCount,
            symbolSize: Math.max(Math.log2(node.spanCount + 1) * 15, 20),
            itemStyle: {
              color: getServiceColor(node.name),
              borderColor: node.errorCount > 0 ? '#ef4444' : '#fff',
              borderWidth: node.errorCount > 0 ? 3 : 2,
              shadowBlur: 6,
              shadowColor: 'rgba(0,0,0,0.15)',
            },
          })),
          edges: edges.map((edge) => ({
            source: edge.source,
            target: edge.target,
            callCount: edge.callCount,
            lineStyle: {
              width: Math.max(Math.log2(edge.callCount + 1) * 2, 1),
            },
          })),
        },
      ],
    };
  }, [nodes, edges]);

  // 空状态
  if (!trace.spans || trace.spans.length === 0 || !option) {
    return (
      <div className="bg-white rounded-xl shadow-sm border border-gray-200 p-4">
        <div className="flex flex-col items-center justify-center py-16 text-gray-400">
          <i className="fas fa-project-diagram text-3xl mb-3" />
          <p className="text-sm">No data</p>
        </div>
      </div>
    );
  }

  return (
    <div className="bg-white rounded-xl shadow-sm border border-gray-200 p-4">
      {/* 标题 */}
      <div className="flex items-center justify-between mb-3">
        <h4 className="text-sm font-semibold text-gray-700 flex items-center gap-1.5">
          <i className="fas fa-project-diagram text-indigo-400" />
          Service Dependency Graph
        </h4>
        <span className="text-xs text-gray-400">
          {nodes.length} services · {edges.length} dependencies
        </span>
      </div>

      {/* ECharts 图 */}
      <div className="border border-gray-100 rounded-lg overflow-hidden">
        <ReactECharts
          option={option}
          style={{ height: 420, width: '100%' }}
          opts={{ renderer: 'canvas' }}
        />
      </div>

      {/* 图例 */}
      <div className="flex items-center gap-3 mt-3 flex-wrap">
        {nodes.map((node) => (
          <span key={node.name} className="flex items-center gap-1.5 text-[10px]">
            <span
              className="w-3 h-3 rounded-full flex-shrink-0"
              style={{ backgroundColor: getServiceColor(node.name) }}
            />
            <span className="text-gray-500">
              {node.name}
              <span className="text-gray-400 ml-1">({node.spanCount})</span>
            </span>
          </span>
        ))}
      </div>
    </div>
  );
}
