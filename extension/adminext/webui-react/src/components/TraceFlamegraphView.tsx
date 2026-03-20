/**
 * TraceFlamegraphView — 火焰图视图
 *
 * 功能：
 * - 使用 div 绝对定位实现横向堆叠火焰图
 * - 每层一行，span 按 startTime 排列
 * - 宽度 = duration / traceDuration * 100%
 * - 左偏移 = (startTime - traceStartTime) / traceDuration * 100%
 * - 颜色按 service 着色（getServiceColor）
 * - 错误 span 红色边框标记
 * - Tooltip: Service::Operation, Duration, Status
 * - 支持缩放和水平滚动
 */

import { useMemo, useState, useRef, useCallback } from 'react';
import type { JaegerTrace, JaegerSpan } from '@/types/trace';
import { buildSpanTree, formatDuration, getServiceColor } from '@/utils/trace';
import type { SpanTreeNode } from '@/types/trace';

interface TraceViewProps {
  trace: JaegerTrace;
}

// ============================================================================
// 类型
// ============================================================================

interface FlatRow {
  span: JaegerSpan;
  serviceName: string;
  depth: number;
  offsetPercent: number;
  widthPercent: number;
  hasError: boolean;
}

interface TooltipData {
  x: number;
  y: number;
  serviceName: string;
  operationName: string;
  duration: number;
  hasError: boolean;
}

// ============================================================================
// 工具函数
// ============================================================================

const ROW_HEIGHT = 24;
const ROW_GAP = 2;

function isSpanError(span: JaegerSpan): boolean {
  return span.tags.some(
    (t) =>
      (t.key === 'error' && t.value === true) ||
      (t.key === 'otel.status_code' && t.value === 'ERROR'),
  );
}

/** 将 span 树扁平化为行数据 */
function flattenTree(
  nodes: SpanTreeNode[],
  traceStartTime: number,
  traceDuration: number,
  processes: JaegerTrace['processes'],
): FlatRow[] {
  const rows: FlatRow[] = [];

  function walk(node: SpanTreeNode) {
    const proc = processes[node.span.processID];
    rows.push({
      span: node.span,
      serviceName: proc?.serviceName ?? 'unknown',
      depth: node.depth,
      offsetPercent:
        traceDuration > 0
          ? ((node.span.startTime - traceStartTime) / traceDuration) * 100
          : 0,
      widthPercent:
        traceDuration > 0
          ? Math.max((node.span.duration / traceDuration) * 100, 0.2) // 最小 0.2% 保证可见
          : 0,
      hasError: isSpanError(node.span),
    });
    for (const child of node.children) {
      walk(child);
    }
  }

  for (const root of nodes) {
    walk(root);
  }
  return rows;
}

// ============================================================================
// 主组件
// ============================================================================

export default function TraceFlamegraphView({ trace }: TraceViewProps) {
  const [zoom, setZoom] = useState(1);
  const [tooltip, setTooltip] = useState<TooltipData | null>(null);
  const containerRef = useRef<HTMLDivElement>(null);

  const spanTree = useMemo(() => buildSpanTree(trace), [trace]);

  // 计算 trace 时间范围
  const { traceStartTime, traceDuration } = useMemo(() => {
    let start = Infinity;
    let end = 0;
    for (const span of trace.spans) {
      start = Math.min(start, span.startTime);
      end = Math.max(end, span.startTime + span.duration);
    }
    return { traceStartTime: start, traceDuration: end - start };
  }, [trace]);

  // 扁平化行数据
  const rows = useMemo(
    () => flattenTree(spanTree, traceStartTime, traceDuration, trace.processes),
    [spanTree, traceStartTime, traceDuration, trace.processes],
  );

  // 计算最大深度
  const maxDepth = useMemo(() => {
    let max = 0;
    for (const row of rows) {
      if (row.depth > max) max = row.depth;
    }
    return max;
  }, [rows]);

  const handleMouseEnter = useCallback(
    (row: FlatRow, e: React.MouseEvent) => {
      const rect = containerRef.current?.getBoundingClientRect();
      if (!rect) return;
      setTooltip({
        x: e.clientX - rect.left,
        y: e.clientY - rect.top,
        serviceName: row.serviceName,
        operationName: row.span.operationName,
        duration: row.span.duration,
        hasError: row.hasError,
      });
    },
    [],
  );

  const handleMouseLeave = useCallback(() => {
    setTooltip(null);
  }, []);

  // 空状态
  if (!trace.spans || trace.spans.length === 0) {
    return (
      <div className="bg-white rounded-xl shadow-sm border border-gray-200 p-4">
        <div className="flex flex-col items-center justify-center py-16 text-gray-400">
          <i className="fas fa-fire text-3xl mb-3" />
          <p className="text-sm">No data</p>
        </div>
      </div>
    );
  }

  const totalHeight = (maxDepth + 1) * (ROW_HEIGHT + ROW_GAP);

  return (
    <div className="bg-white rounded-xl shadow-sm border border-gray-200 p-4">
      {/* 工具栏 */}
      <div className="flex items-center justify-between mb-3">
        <div className="flex items-center gap-2">
          <h4 className="text-sm font-semibold text-gray-700 flex items-center gap-1.5">
            <i className="fas fa-fire text-orange-400" />
            Flamegraph
          </h4>
          <span className="text-xs text-gray-400">
            {trace.spans.length} spans · {formatDuration(traceDuration)}
          </span>
        </div>
        <div className="flex items-center gap-1">
          <button
            onClick={() => setZoom((z) => Math.max(1, z / 1.5))}
            className="px-2 py-1 text-xs text-gray-500 hover:bg-gray-100 rounded transition"
            title="Zoom Out"
          >
            <i className="fas fa-search-minus" />
          </button>
          <span className="text-xs text-gray-400 w-12 text-center tabular-nums">
            {zoom.toFixed(1)}x
          </span>
          <button
            onClick={() => setZoom((z) => Math.min(20, z * 1.5))}
            className="px-2 py-1 text-xs text-gray-500 hover:bg-gray-100 rounded transition"
            title="Zoom In"
          >
            <i className="fas fa-search-plus" />
          </button>
          <button
            onClick={() => setZoom(1)}
            className="px-2 py-1 text-xs text-gray-500 hover:bg-gray-100 rounded transition ml-1"
            title="Reset Zoom"
          >
            <i className="fas fa-undo" />
          </button>
        </div>
      </div>

      {/* 时间轴标尺 */}
      <div className="flex items-center justify-between text-[10px] text-gray-400 mb-1 px-1">
        <span>0</span>
        <span>{formatDuration(traceDuration / 4)}</span>
        <span>{formatDuration(traceDuration / 2)}</span>
        <span>{formatDuration((traceDuration * 3) / 4)}</span>
        <span>{formatDuration(traceDuration)}</span>
      </div>

      {/* 火焰图容器 */}
      <div
        ref={containerRef}
        className="relative overflow-auto border border-gray-100 rounded-lg bg-gray-50/50"
        style={{ maxHeight: Math.min(totalHeight + 20, 500) }}
      >
        <div
          className="relative"
          style={{
            width: `${zoom * 100}%`,
            height: totalHeight,
            minWidth: '100%',
          }}
        >
          {rows.map((row) => (
            <div
              key={row.span.spanID}
              className={`absolute rounded-sm text-[10px] text-white leading-tight truncate px-1 cursor-pointer transition-opacity hover:opacity-90 flex items-center ${
                row.hasError ? 'ring-2 ring-red-500 ring-inset' : ''
              }`}
              style={{
                left: `${row.offsetPercent}%`,
                width: `${row.widthPercent}%`,
                top: row.depth * (ROW_HEIGHT + ROW_GAP),
                height: ROW_HEIGHT,
                backgroundColor: getServiceColor(row.serviceName),
                minWidth: 3,
              }}
              onMouseEnter={(e) => handleMouseEnter(row, e)}
              onMouseLeave={handleMouseLeave}
            >
              <span className="truncate">
                {row.serviceName}::{row.span.operationName}
              </span>
            </div>
          ))}
        </div>

        {/* Tooltip */}
        {tooltip && (
          <div
            className="absolute z-30 bg-gray-900 text-white text-xs rounded-lg px-3 py-2 shadow-xl pointer-events-none"
            style={{
              left: Math.min(tooltip.x + 10, (containerRef.current?.clientWidth ?? 300) - 200),
              top: tooltip.y + 10,
            }}
          >
            <div className="font-medium">
              {tooltip.serviceName}::{tooltip.operationName}
            </div>
            <div className="flex items-center gap-3 mt-1 text-gray-300">
              <span>{formatDuration(tooltip.duration)}</span>
              {tooltip.hasError ? (
                <span className="text-red-400 font-semibold">ERROR</span>
              ) : (
                <span className="text-green-400">OK</span>
              )}
            </div>
          </div>
        )}
      </div>

      {/* 图例 */}
      <div className="flex items-center gap-3 mt-3 flex-wrap">
        {Array.from(
          new Set(rows.map((r) => r.serviceName)),
        ).map((svc) => (
          <span key={svc} className="flex items-center gap-1 text-[10px]">
            <span
              className="w-3 h-2 rounded-sm flex-shrink-0"
              style={{ backgroundColor: getServiceColor(svc) }}
            />
            <span className="text-gray-500">{svc}</span>
          </span>
        ))}
      </div>
    </div>
  );
}
