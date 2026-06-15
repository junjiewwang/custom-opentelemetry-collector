/**
 * TraceDetail 组件 - Trace 调用链时间轴可视化
 *
 * 功能：
 * - 展示完整的 Span 树形结构（虚拟滚动）
 * - 时间轴 bar 可视化每个 Span 的相对起始时间和持续时长
 * - 展开/收起 Span 详情（Accordion 面板）
 * - 颜色按 Service 区分
 * - Span 搜索过滤（name、serviceName、attributes）
 * - 视图 Tab 切换预留（Timeline / Statistics / Table / Flamegraph / Graph）
 */

import { useState, useMemo, useRef, useCallback, type ReactNode } from 'react';
import { useNavigate } from 'react-router-dom';
import { useVirtualizer } from '@tanstack/react-virtual';
import {
  buildSpanTree,
  formatDuration,
  formatTimestamp,
  formatSpanKind,
  getServiceColor,
  anyValueToDisplay,
} from '@/utils/trace';
import type { OTelTrace, OTelSpan, SpanTreeNode, KeyValue } from '@/types/trace';
import TraceStatisticsView from './TraceStatisticsView';
import TraceSpanTableView from './TraceSpanTableView';
import TraceFlamegraphView from './TraceFlamegraphView';
import TraceGraphView from './TraceGraphView';

// ============================================================================
// 类型定义
// ============================================================================

interface TraceDetailProps {
  trace: OTelTrace;
  onClose?: () => void;
}

type ViewTab = 'timeline' | 'statistics' | 'table' | 'flamegraph' | 'graph' | 'json';

const VIEW_TABS: { key: ViewTab; label: string; icon: string }[] = [
  { key: 'timeline', label: 'Timeline', icon: 'fa-stream' },
  { key: 'statistics', label: 'Statistics', icon: 'fa-chart-bar' },
  { key: 'table', label: 'Table', icon: 'fa-table' },
  { key: 'flamegraph', label: 'Flamegraph', icon: 'fa-fire' },
  { key: 'graph', label: 'Graph', icon: 'fa-project-diagram' },
  { key: 'json', label: 'JSON', icon: 'fa-code' },
];

// ============================================================================
// 工具函数
// ============================================================================

/** 将树形 SpanTree 扁平化为一维数组（根据折叠状态过滤子节点） */
function flattenTree(nodes: SpanTreeNode[], collapsedIds: Set<string>): SpanTreeNode[] {
  const result: SpanTreeNode[] = [];
  function walk(nodeList: SpanTreeNode[]) {
    for (const node of nodeList) {
      result.push(node);
      if (!collapsedIds.has(node.span.spanId) && node.children.length > 0) {
        walk(node.children);
      }
    }
  }
  walk(nodes);
  return result;
}

/** 搜索匹配：检查 span 是否匹配搜索关键词 */
function spanMatchesSearch(node: SpanTreeNode, query: string): boolean {
  if (!query) return false;
  const q = query.toLowerCase();
  // 匹配 operation name
  if (node.span.name.toLowerCase().includes(q)) return true;
  // 匹配 serviceName
  if (node.span.serviceName.toLowerCase().includes(q)) return true;
  // 匹配 attributes 的 key 和 value
  if (node.span.attributes) {
    for (const attr of node.span.attributes) {
      if (attr.key.toLowerCase().includes(q)) return true;
      const val = String(anyValueToDisplay(attr.value));
      if (val.toLowerCase().includes(q)) return true;
    }
  }
  return false;
}

/** 判断 span 是否有错误 */
function isSpanError(span: OTelSpan): boolean {
  return span.status?.code === 'STATUS_CODE_ERROR';
}

/** 通用剪贴板复制（兼容 HTTP 非安全上下文） */
function copyText(text: string): boolean {
  if (navigator.clipboard && window.isSecureContext) {
    navigator.clipboard.writeText(text);
    return true;
  }
  // Fallback: execCommand (works in HTTP contexts)
  const ta = document.createElement('textarea');
  ta.value = text;
  ta.style.position = 'fixed';
  ta.style.opacity = '0';
  document.body.appendChild(ta);
  ta.select();
  try {
    document.execCommand('copy');
    return true;
  } catch {
    return false;
  } finally {
    document.body.removeChild(ta);
  }
}

// ============================================================================
// TraceDetail 主组件
// ============================================================================

export default function TraceDetail({ trace, onClose }: TraceDetailProps) {
  const navigate = useNavigate();
  const spanTree = useMemo(() => buildSpanTree(trace), [trace]);

  // 视图 Tab 状态
  const [activeTab, setActiveTab] = useState<ViewTab>('timeline');

  // 树折叠状态：collapsedSpanIDs 里的 span 的子节点不显示
  const [collapsedSpanIDs, setCollapsedSpanIDs] = useState<Set<string>>(new Set());

  // Span 详情展开状态：expandedSpanIDs 里的 span 显示详情面板
  const [expandedSpanIDs, setExpandedSpanIDs] = useState<Set<string>>(new Set());

  // 搜索状态
  const [searchQuery, setSearchQuery] = useState('');
  const [currentMatchIndex, setCurrentMatchIndex] = useState(0);

  // 虚拟滚动容器 ref
  const scrollContainerRef = useRef<HTMLDivElement>(null);

  // 计算 trace 总时长（纳秒）
  const traceDurationNano = useMemo(() => {
    let minStart = Infinity;
    let maxEnd = 0;
    for (const span of trace.spans) {
      const start = Number(span.startTimeUnixNano);
      const end = Number(span.endTimeUnixNano);
      minStart = Math.min(minStart, start);
      maxEnd = Math.max(maxEnd, end);
    }
    return maxEnd - minStart;
  }, [trace]);

  // trace 起始时间（纳秒）
  const traceStartNano = useMemo(
    () => trace.spans.reduce((min, s) => Math.min(min, Number(s.startTimeUnixNano)), Infinity),
    [trace],
  );

  // 统计 services
  const serviceStats = useMemo(() => {
    const map = new Map<string, number>();
    for (const span of trace.spans) {
      const svc = span.serviceName || 'unknown';
      map.set(svc, (map.get(svc) ?? 0) + 1);
    }
    return Array.from(map.entries()).map(([name, count]) => ({ name, count }));
  }, [trace]);

  const rootNode = spanTree[0] ?? null;
  const rootServiceName = trace.rootServiceName ?? rootNode?.span.serviceName ?? serviceStats[0]?.name ?? 'unknown';
  const rootOperationName = trace.rootSpanName ?? rootNode?.span.name ?? 'unknown';

  const errorSpanCount = useMemo(
    () => trace.spans.filter(span => isSpanError(span)).length,
    [trace],
  );

  // 扁平化的 span 列表（考虑折叠状态）
  const flattenedSpans = useMemo(
    () => flattenTree(spanTree, collapsedSpanIDs),
    [spanTree, collapsedSpanIDs],
  );

  // 搜索匹配的 span 索引列表
  const matchedIndices = useMemo(() => {
    if (!searchQuery.trim()) return [];
    return flattenedSpans
      .map((node, index) => (spanMatchesSearch(node, searchQuery.trim()) ? index : -1))
      .filter(i => i >= 0);
  }, [flattenedSpans, searchQuery]);

  // 虚拟滚动器
  const virtualizer = useVirtualizer({
    count: flattenedSpans.length,
    getScrollElement: () => scrollContainerRef.current,
    estimateSize: (index) => {
      const node = flattenedSpans[index];
      if (node && expandedSpanIDs.has(node.span.spanId)) {
        return 336; // SpanRow(36) + SpanDetail(~300)
      }
      return 36;
    },
    overscan: 10,
  });

  // 切换树节点折叠/展开
  const toggleCollapse = useCallback((spanId: string) => {
    setCollapsedSpanIDs(prev => {
      const next = new Set(prev);
      if (next.has(spanId)) {
        next.delete(spanId);
      } else {
        next.add(spanId);
      }
      return next;
    });
  }, []);

  // 切换 span 详情展开
  const toggleDetail = useCallback((spanId: string) => {
    setExpandedSpanIDs(prev => {
      const next = new Set(prev);
      if (next.has(spanId)) {
        next.delete(spanId);
      } else {
        next.add(spanId);
      }
      return next;
    });
  }, []);

  // 搜索导航：跳到上/下一个匹配项
  const navigateMatch = useCallback((direction: 'prev' | 'next') => {
    if (matchedIndices.length === 0) return;
    setCurrentMatchIndex(prev => {
      let next: number;
      if (direction === 'next') {
        next = (prev + 1) % matchedIndices.length;
      } else {
        next = (prev - 1 + matchedIndices.length) % matchedIndices.length;
      }
      const targetFlatIndex = matchedIndices[next];
      if (targetFlatIndex !== undefined) {
        virtualizer.scrollToIndex(targetFlatIndex, { align: 'center' });
      }
      return next;
    });
  }, [matchedIndices, virtualizer]);

  // 搜索变更时重置当前匹配索引，并跳到第一个匹配
  const handleSearchChange = useCallback((value: string) => {
    setSearchQuery(value);
    setCurrentMatchIndex(0);
  }, []);

  // 匹配的 span IDs 集合（用于高亮）
  const matchedSpanIDs = useMemo(() => {
    const set = new Set<string>();
    for (const idx of matchedIndices) {
      const node = flattenedSpans[idx];
      if (node) set.add(node.span.spanId);
    }
    return set;
  }, [matchedIndices, flattenedSpans]);

  // 当前聚焦的匹配 span ID
  const currentFocusedSpanID = useMemo(() => {
    if (matchedIndices.length === 0) return null;
    const idx = matchedIndices[currentMatchIndex];
    return idx !== undefined ? flattenedSpans[idx]?.span.spanId ?? null : null;
  }, [matchedIndices, currentMatchIndex, flattenedSpans]);

  const searchSummary = searchQuery.trim()
    ? `${matchedIndices.length > 0 ? currentMatchIndex + 1 : 0}/${matchedIndices.length}`
    : `${flattenedSpans.length} spans`;

  return (
    <div className="h-full flex flex-col bg-slate-50 text-slate-900">
      {/* 顶部摘要 + 操作 */}
      <div className="flex-shrink-0 border-b border-slate-200 bg-white/95 backdrop-blur-sm shadow-sm">
        <div className="px-5 lg:px-6 pt-5 pb-4 flex items-start justify-between gap-4">
          <div className="min-w-0 flex-1">
            <div className="flex items-center gap-2 text-[11px] font-semibold uppercase tracking-[0.18em] text-slate-400">
              <i className="fas fa-wave-square text-primary-500" />
              Trace Detail
            </div>
            <div className="mt-2 flex items-center gap-2 flex-wrap min-w-0">
              <h3 className="text-lg lg:text-xl font-semibold text-slate-900 truncate max-w-full">
                {rootServiceName}
                <span className="mx-2 text-slate-300">/</span>
                <span className="text-slate-600">{rootOperationName}</span>
              </h3>
              <span className="inline-flex items-center rounded-full bg-primary-50 px-2.5 py-1 text-[11px] font-semibold text-primary-700">
                {formatDuration(traceDurationNano)}
              </span>
              {errorSpanCount > 0 && (
                <span className="inline-flex items-center gap-1 rounded-full bg-red-50 px-2.5 py-1 text-[11px] font-semibold text-red-600">
                  <i className="fas fa-exclamation-circle text-[10px]" />
                  {errorSpanCount} error spans
                </span>
              )}
            </div>
            <div className="mt-2 flex items-center gap-2 flex-wrap text-xs text-slate-500">
              <span className="inline-flex items-center rounded-lg border border-slate-200 bg-slate-50 px-2.5 py-1 font-mono text-[11px] text-slate-600 max-w-full truncate">
                {trace.traceId}
              </span>
              <span className="inline-flex items-center gap-1 rounded-lg bg-slate-100 px-2 py-1">
                <i className="fas fa-bolt text-[10px] text-slate-400" />
                {trace.spans.length} spans
              </span>
              <span className="inline-flex items-center gap-1 rounded-lg bg-slate-100 px-2 py-1">
                <i className="fas fa-cubes text-[10px] text-slate-400" />
                {serviceStats.length} services
              </span>
              <span className="inline-flex items-center gap-1 rounded-lg bg-slate-100 px-2 py-1">
                <i className="fas fa-clock text-[10px] text-slate-400" />
                {formatTimestamp(traceStartNano)}
              </span>
            </div>
            {serviceStats.length > 0 && (
              <div className="mt-3 flex items-center gap-2 flex-wrap">
                {serviceStats.slice(0, 6).map(svc => (
                  <span
                    key={svc.name}
                    className="inline-flex items-center gap-2 rounded-full border border-slate-200 bg-white px-2.5 py-1 text-[11px] text-slate-600 shadow-sm"
                  >
                    <span
                      className="w-2 h-2 rounded-full flex-shrink-0"
                      style={{ backgroundColor: getServiceColor(svc.name) }}
                    />
                    <span className="truncate max-w-[180px]">{svc.name}</span>
                    <span className="text-slate-400">{svc.count}</span>
                  </span>
                ))}
                {serviceStats.length > 6 && (
                  <span className="inline-flex items-center rounded-full bg-slate-100 px-2.5 py-1 text-[11px] text-slate-500">
                    +{serviceStats.length - 6} more services
                  </span>
                )}
              </div>
            )}
          </div>

          <div className="flex items-center gap-2 flex-shrink-0 ml-4">
            {rootServiceName && (
              <button
                onClick={() => navigate(`/metrics?tab=red&service=${encodeURIComponent(rootServiceName)}`)}
                className="px-3 py-2 text-xs bg-primary-50 text-primary-700 hover:bg-primary-100 rounded-xl transition flex items-center gap-1.5 shadow-sm"
                title="View RED metrics for this Service"
              >
                <i className="fas fa-chart-line" />
                View Metrics
              </button>
            )}
            <button
              onClick={() => navigate(`/traces?service=${encodeURIComponent(rootServiceName)}&lookback=1h`)}
              className="px-3 py-2 text-xs bg-slate-100 text-slate-600 hover:bg-slate-200 rounded-xl transition flex items-center gap-1.5"
              title="Search more Traces for this Service"
            >
              <i className="fas fa-search" />
              More Traces
            </button>
            <button
              onClick={onClose}
              className="w-10 h-10 inline-flex items-center justify-center rounded-xl text-slate-400 hover:text-slate-700 hover:bg-slate-100 transition"
              title="Close trace detail"
              aria-label="Close trace detail"
            >
              <i className="fas fa-times" />
            </button>
          </div>
        </div>

        {/* 工具栏：Tabs + Timeline 搜索 */}
        <div className="px-5 lg:px-6 pb-4 flex items-center justify-between gap-3 flex-wrap">
          <div className="inline-flex items-center rounded-2xl border border-slate-200 bg-slate-100/80 p-1 shadow-sm overflow-x-auto">
            {VIEW_TABS.map(tab => (
              <button
                key={tab.key}
                onClick={() => setActiveTab(tab.key)}
                className={`inline-flex items-center gap-1.5 rounded-xl px-3 py-2 text-xs font-medium whitespace-nowrap transition-all ${
                  activeTab === tab.key
                    ? 'bg-white text-primary-700 shadow-sm ring-1 ring-primary-100'
                    : 'text-slate-500 hover:text-slate-700'
                }`}
              >
                <i className={`fas ${tab.icon} text-[11px]`} />
                {tab.label}
              </button>
            ))}
          </div>

          {activeTab === 'timeline' ? (
            <div className="flex items-center gap-2 flex-1 min-w-[300px] max-w-[640px] ml-auto">
              <div className="relative flex-1">
                <i className="fas fa-search absolute left-3 top-1/2 -translate-y-1/2 text-slate-300 text-xs" />
                <input
                  type="text"
                  value={searchQuery}
                  onChange={e => handleSearchChange(e.target.value)}
                  placeholder="Search service, operation, attributes..."
                  className="w-full pl-8 pr-3 py-2 text-sm border border-slate-200 rounded-xl focus:outline-none focus:ring-2 focus:ring-primary-200 focus:border-primary-400 transition bg-white"
                />
              </div>
              <span className="text-xs text-slate-500 tabular-nums whitespace-nowrap min-w-[64px] text-right">
                {searchSummary}
              </span>
              <button
                onClick={() => navigateMatch('prev')}
                disabled={matchedIndices.length === 0}
                className="w-8 h-8 inline-flex items-center justify-center rounded-lg border border-slate-200 bg-white text-slate-500 hover:text-slate-700 hover:bg-slate-50 disabled:opacity-35 transition"
                title="Previous match"
                aria-label="Previous match"
              >
                <i className="fas fa-chevron-up text-[11px]" />
              </button>
              <button
                onClick={() => navigateMatch('next')}
                disabled={matchedIndices.length === 0}
                className="w-8 h-8 inline-flex items-center justify-center rounded-lg border border-slate-200 bg-white text-slate-500 hover:text-slate-700 hover:bg-slate-50 disabled:opacity-35 transition"
                title="Next match"
                aria-label="Next match"
              >
                <i className="fas fa-chevron-down text-[11px]" />
              </button>
              {searchQuery.trim() && (
                <button
                  onClick={() => { setSearchQuery(''); setCurrentMatchIndex(0); }}
                  className="w-8 h-8 inline-flex items-center justify-center rounded-lg border border-slate-200 bg-white text-slate-400 hover:text-slate-700 hover:bg-slate-50 transition"
                  title="Clear search"
                  aria-label="Clear search"
                >
                  <i className="fas fa-times text-[11px]" />
                </button>
              )}
            </div>
          ) : (
            <div className="text-xs text-slate-500 ml-auto">
              Explore the selected trace using a different analytical view.
            </div>
          )}
        </div>
      </div>

      {/* Tab 内容区 */}
      {activeTab === 'timeline' ? (
        <div className="flex-1 min-h-0 p-4 lg:p-5">
          <div className="h-full min-h-0 rounded-2xl border border-slate-200 bg-white shadow-sm overflow-hidden flex flex-col">
            {/* Timeline Header */}
            <div className="flex-shrink-0 border-b border-slate-200 bg-slate-50/85 px-4 lg:px-5 py-3 flex items-center text-[11px] font-medium text-slate-500">
              <div className="w-[340px] xl:w-[380px] flex-shrink-0 pr-4">Service / Operation</div>
              <div className="flex-1 grid grid-cols-5 items-center text-right">
                <span className="text-left">0</span>
                <span>{formatDuration(traceDurationNano / 4)}</span>
                <span>{formatDuration(traceDurationNano / 2)}</span>
                <span>{formatDuration(traceDurationNano * 3 / 4)}</span>
                <span>{formatDuration(traceDurationNano)}</span>
              </div>
            </div>

            {/* Virtual Scroll Span Rows */}
            <div ref={scrollContainerRef} className="flex-1 min-h-0 overflow-y-auto overflow-x-hidden">
              <div
                style={{
                  height: `${virtualizer.getTotalSize()}px`,
                  width: '100%',
                  position: 'relative',
                }}
              >
                {virtualizer.getVirtualItems().map(virtualRow => {
                  const node = flattenedSpans[virtualRow.index];
                  if (!node) return null;
                  const isDetailExpanded = expandedSpanIDs.has(node.span.spanId);
                  const isCollapsed = collapsedSpanIDs.has(node.span.spanId);
                  const isMatched = matchedSpanIDs.has(node.span.spanId);
                  const isFocused = currentFocusedSpanID === node.span.spanId;

                  return (
                    <div
                      key={node.span.spanId}
                      data-index={virtualRow.index}
                      ref={virtualizer.measureElement}
                      style={{
                        position: 'absolute',
                        top: 0,
                        left: 0,
                        width: '100%',
                        transform: `translateY(${virtualRow.start}px)`,
                      }}
                    >
                      <VirtualSpanRow
                        node={node}
                        traceDurationNano={traceDurationNano}
                        traceStartNano={traceStartNano}
                        isDetailExpanded={isDetailExpanded}
                        isCollapsed={isCollapsed}
                        hasChildren={node.children.length > 0}
                        isMatched={isMatched}
                        isFocused={isFocused}
                        onToggleCollapse={toggleCollapse}
                        onToggleDetail={toggleDetail}
                      />
                    </div>
                  );
                })}
              </div>
            </div>
          </div>
        </div>
      ) : activeTab === 'statistics' ? (
        <div className="flex-1 min-h-0 overflow-y-auto p-4 lg:p-5">
          <div className="rounded-2xl border border-slate-200 bg-white shadow-sm p-4">
            <TraceStatisticsView trace={trace} />
          </div>
        </div>
      ) : activeTab === 'table' ? (
        <div className="flex-1 min-h-0 overflow-y-auto p-4 lg:p-5">
          <div className="rounded-2xl border border-slate-200 bg-white shadow-sm p-4">
            <TraceSpanTableView trace={trace} />
          </div>
        </div>
      ) : activeTab === 'flamegraph' ? (
        <div className="flex-1 min-h-0 overflow-y-auto p-4 lg:p-5">
          <div className="rounded-2xl border border-slate-200 bg-white shadow-sm p-4">
            <TraceFlamegraphView trace={trace} />
          </div>
        </div>
      ) : activeTab === 'graph' ? (
        <div className="flex-1 min-h-0 overflow-y-auto p-4 lg:p-5">
          <div className="rounded-2xl border border-slate-200 bg-white shadow-sm p-4">
            <TraceGraphView trace={trace} />
          </div>
        </div>
      ) : activeTab === 'json' ? (
        <TraceJsonView trace={trace} />
      ) : null}
    </div>
  );
}

// ============================================================================
// VirtualSpanRow 组件 - 虚拟滚动中的单个 Span 行
// ============================================================================

interface VirtualSpanRowProps {
  node: SpanTreeNode;
  traceDurationNano: number;
  traceStartNano: number;
  isDetailExpanded: boolean;
  isCollapsed: boolean;
  hasChildren: boolean;
  isMatched: boolean;
  isFocused: boolean;
  onToggleCollapse: (spanId: string) => void;
  onToggleDetail: (spanId: string) => void;
}

function VirtualSpanRow({
  node,
  traceDurationNano,
  traceStartNano,
  isDetailExpanded,
  isCollapsed,
  hasChildren,
  isMatched,
  isFocused,
  onToggleCollapse,
  onToggleDetail,
}: VirtualSpanRowProps) {
  const { span, depth } = node;

  const serviceColor = getServiceColor(span.serviceName);
  const hasError = isSpanError(span);

  // 计算时间轴位置（纳秒）
  const spanStartNano = Number(span.startTimeUnixNano);
  const spanDurationNano = Number(span.durationNano) || (Number(span.endTimeUnixNano) - spanStartNano);

  const offsetPercent = traceDurationNano > 0
    ? ((spanStartNano - traceStartNano) / traceDurationNano) * 100
    : 0;
  const widthPercent = traceDurationNano > 0
    ? Math.max((spanDurationNano / traceDurationNano) * 100, 0.3)
    : 0;
  const showDurationLabel = widthPercent >= 8;

  // 构建行背景色
  let rowBgClass = 'bg-white';
  if (isFocused) {
    rowBgClass = 'bg-amber-50';
  } else if (isMatched) {
    rowBgClass = 'bg-yellow-50/70';
  } else if (hasError) {
    rowBgClass = 'bg-red-50/30';
  }

  return (
    <>
      {/* Span Bar Row */}
      <div
        className={`flex items-center border-b border-slate-100 hover:bg-slate-50/80 cursor-pointer transition-colors ${rowBgClass}`}
        onClick={() => onToggleDetail(span.spanId)}
      >
        {/* Left: Service / Operation */}
        <div
          className="w-[340px] xl:w-[380px] flex-shrink-0 px-4 py-2.5 flex items-center gap-2 text-sm truncate"
          style={{ paddingLeft: `${16 + depth * 16}px` }}
        >
          {hasChildren ? (
            <button
              onClick={(e) => {
                e.stopPropagation();
                onToggleCollapse(span.spanId);
              }}
              className="w-5 h-5 rounded-md inline-flex items-center justify-center text-slate-400 hover:text-slate-600 hover:bg-slate-100 flex-shrink-0 transition"
              aria-label={isCollapsed ? 'Expand span children' : 'Collapse span children'}
            >
              <i className={`fas fa-chevron-${isCollapsed ? 'right' : 'down'} text-[10px]`} />
            </button>
          ) : (
            <span className="w-5 h-5 flex-shrink-0" />
          )}
          <span
            className="w-2.5 h-2.5 rounded-full flex-shrink-0 shadow-[0_0_0_2px_rgba(255,255,255,0.9)]"
            style={{ backgroundColor: serviceColor }}
          />
          <div className="min-w-0 flex items-center gap-1.5 truncate">
            <span className="font-semibold text-slate-700 truncate max-w-[140px]" title={span.serviceName}>
              {span.serviceName}
            </span>
            <span className="text-slate-300">/</span>
            <span className="text-slate-500 truncate" title={span.name}>
              {span.name}
            </span>
          </div>
          {hasError && (
            <i className="fas fa-exclamation-circle text-red-500 text-xs ml-1 flex-shrink-0" />
          )}
        </div>

        {/* Right: Timeline Bar */}
        <div className="flex-1 px-4 py-2.5 h-10">
          <div className="relative h-full">
            <div className="absolute inset-y-1 left-0 right-0 rounded-md bg-slate-100" />
            <div className="absolute inset-y-1 left-0 right-0 bg-[linear-gradient(to_right,transparent_0%,transparent_calc(25%-1px),rgba(148,163,184,0.18)_calc(25%-1px),rgba(148,163,184,0.18)_25%,transparent_25%,transparent_calc(50%-1px),rgba(148,163,184,0.18)_calc(50%-1px),rgba(148,163,184,0.18)_50%,transparent_50%,transparent_calc(75%-1px),rgba(148,163,184,0.18)_calc(75%-1px),rgba(148,163,184,0.18)_75%,transparent_75%)] rounded-md pointer-events-none" />
            <div
              className="absolute top-1/2 -translate-y-1/2 rounded-md h-5 min-w-[4px] flex items-center justify-end px-1.5 shadow-sm ring-1 ring-black/5"
              style={{
                left: `${offsetPercent}%`,
                width: `${widthPercent}%`,
                backgroundColor: serviceColor,
                opacity: hasError ? 0.95 : 0.82,
              }}
            >
              {showDurationLabel ? (
                <span className="text-white text-[10px] font-medium whitespace-nowrap drop-shadow-sm">
                  {formatDuration(spanDurationNano)}
                </span>
              ) : (
                <span className="sr-only">{formatDuration(spanDurationNano)}</span>
              )}
            </div>
          </div>
        </div>
      </div>

      {/* Expanded Detail */}
      {isDetailExpanded && (
        <div className="border-b border-slate-200 bg-slate-50/70 px-4 lg:px-6 py-4">
          <SpanDetail span={span} traceStartNano={traceStartNano} />
        </div>
      )}
    </>
  );
}

// ============================================================================
// SpanDetail 组件 - Span 详情面板（内联展开）
// ============================================================================

interface SpanDetailProps {
  span: OTelSpan;
  traceStartNano: number;
}

function SpanDetail({ span, traceStartNano }: SpanDetailProps) {
  const serviceColor = getServiceColor(span.serviceName);
  const hasLinks = (span.links?.length ?? 0) > 0;
  const hasEvents = (span.events?.length ?? 0) > 0;
  const hasResource = (span.resource?.length ?? 0) > 0;
  const hasAttributes = (span.attributes?.length ?? 0) > 0;

  const spanStartNano = Number(span.startTimeUnixNano);
  const spanDurationNano = Number(span.durationNano) || (Number(span.endTimeUnixNano) - spanStartNano);
  const relativeStartNano = spanStartNano - traceStartNano;

  // 复制到剪贴板（兼容 HTTP）
  const copyToClipboard = (text: string) => {
    copyText(text);
  };

  return (
    <div className="space-y-3">
      {/* 顶部颜色条 + Overview */}
      <div
        className="rounded-lg border border-gray-200 overflow-hidden"
        style={{ borderTopWidth: '3px', borderTopColor: serviceColor }}
      >
        <div className="px-4 py-3 bg-white">
          <div className="grid grid-cols-2 md:grid-cols-5 gap-x-6 gap-y-2">
            <OverviewItem label="Service" value={span.serviceName} />
            <OverviewItem label="Duration" value={formatDuration(spanDurationNano)} bold />
            <OverviewItem label="Start Time" value={formatDuration(relativeStartNano)} />
            <OverviewItem label="Kind" value={formatSpanKind(span.kind)} />
            <OverviewItem label="Absolute Time" value={formatTimestamp(spanStartNano)} small />
          </div>
        </div>
      </div>

      {/* Status */}
      {span.status && (
        <div className="flex items-center gap-2 text-xs">
          <span className="text-gray-500 font-medium">Status:</span>
          <span className={`px-2 py-0.5 rounded-full font-semibold text-[10px] ${
            span.status.code === 'STATUS_CODE_ERROR'
              ? 'bg-red-100 text-red-700'
              : span.status.code === 'STATUS_CODE_OK'
                ? 'bg-green-100 text-green-700'
                : 'bg-gray-100 text-gray-600'
          }`}>
            {span.status.code.replace('STATUS_CODE_', '')}
          </span>
          {span.status.message && (
            <span className="text-gray-500 truncate">{span.status.message}</span>
          )}
        </div>
      )}

      {/* Accordion 区域 */}
      <div className="space-y-2">
        {/* Attributes */}
        {hasAttributes && (
          <AccordionSection
            title="Attributes"
            icon="fa-tags"
            count={span.attributes!.length}
            defaultOpen={true}
            summary={<AttributesSummary attributes={span.attributes!} />}
          >
            <KeyValueTable items={span.attributes!} />
          </AccordionSection>
        )}

        {/* Events */}
        {hasEvents && (
          <AccordionSection
            title="Events"
            icon="fa-list-ol"
            count={span.events!.length}
            defaultOpen={false}
          >
            <EventsList events={span.events!} traceStartNano={traceStartNano} />
          </AccordionSection>
        )}

        {/* Resource */}
        {hasResource && (
          <AccordionSection
            title="Resource"
            icon="fa-server"
            count={span.resource!.length}
            defaultOpen={false}
            summary={<AttributesSummary attributes={span.resource!} maxItems={3} />}
          >
            <KeyValueTable items={span.resource!} />
          </AccordionSection>
        )}

        {/* Links */}
        {hasLinks && (
          <AccordionSection
            title="Links"
            icon="fa-link"
            count={span.links!.length}
            defaultOpen={false}
          >
            <div className="space-y-1.5">
              {span.links!.map((link, i) => (
                <div key={i} className="flex items-center gap-2 text-xs py-1">
                  <span className="px-1.5 py-0.5 rounded text-[10px] font-semibold bg-blue-50 text-blue-600 border border-blue-200">
                    LINK
                  </span>
                  <span className="font-mono text-gray-500 truncate" title={`trace:${link.traceId} span:${link.spanId}`}>
                    {link.traceId.substring(0, 8)}...:{link.spanId.substring(0, 8)}...
                  </span>
                  {link.attributes && link.attributes.length > 0 && (
                    <span className="text-gray-400 text-[10px]">
                      ({link.attributes.length} attrs)
                    </span>
                  )}
                </div>
              ))}
            </div>
          </AccordionSection>
        )}
      </div>

      {/* Footer - Debug Info */}
      <div className="flex items-center justify-between px-1 pt-1">
        <div className="text-xs text-gray-400 flex items-center gap-1.5 min-w-0">
          <span className="text-gray-500 font-medium flex-shrink-0">SpanID:</span>
          <span className="font-mono truncate" title={span.spanId}>{span.spanId}</span>
          {span.parentSpanId && (
            <>
              <span className="text-gray-300 mx-1">|</span>
              <span className="text-gray-500 font-medium flex-shrink-0">Parent:</span>
              <span className="font-mono truncate" title={span.parentSpanId}>{span.parentSpanId.substring(0, 8)}...</span>
            </>
          )}
        </div>
        <div className="flex items-center gap-1.5 flex-shrink-0 ml-3">
          <button
            onClick={(e) => { e.stopPropagation(); copyToClipboard(span.spanId); }}
            className="px-2.5 py-1 text-[11px] text-gray-500 hover:text-gray-700 bg-white border border-gray-200 hover:border-gray-300 rounded-md transition flex items-center gap-1.5"
            title="Copy Span ID"
          >
            <i className="fas fa-copy text-[10px]" />
            Copy ID
          </button>
          <button
            onClick={(e) => {
              e.stopPropagation();
              const deepLink = `${window.location.origin}${window.location.pathname}?uiFind=${span.spanId}`;
              copyToClipboard(deepLink);
            }}
            className="px-2.5 py-1 text-[11px] text-gray-500 hover:text-gray-700 bg-white border border-gray-200 hover:border-gray-300 rounded-md transition flex items-center gap-1.5"
            title="Copy deep link to this span"
          >
            <i className="fas fa-link text-[10px]" />
            Deep Link
          </button>
        </div>
      </div>
    </div>
  );
}

// ============================================================================
// OverviewItem - Overview 区域的单个信息项
// ============================================================================

function OverviewItem({
  label,
  value,
  bold = false,
  small = false,
}: {
  label: string;
  value: string;
  bold?: boolean;
  small?: boolean;
}) {
  return (
    <div>
      <div className="text-[10px] text-gray-400 uppercase tracking-wider mb-0.5">{label}</div>
      <div
        className={`${small ? 'text-xs' : 'text-sm'} ${
          bold ? 'font-bold text-gray-800' : 'text-gray-600'
        } font-mono`}
      >
        {value}
      </div>
    </div>
  );
}

// ============================================================================
// AccordionSection - 可折叠的内容区域
// ============================================================================

interface AccordionSectionProps {
  title: string;
  icon: string;
  count?: number;
  defaultOpen?: boolean;
  variant?: 'default' | 'warning';
  /** 折叠时显示的摘要内容 */
  summary?: ReactNode;
  children: ReactNode;
}

function AccordionSection({
  title,
  icon,
  count,
  defaultOpen = false,
  variant = 'default',
  summary,
  children,
}: AccordionSectionProps) {
  const [isOpen, setIsOpen] = useState(defaultOpen);

  const isWarning = variant === 'warning';
  const borderColor = isWarning ? 'border-amber-200' : 'border-gray-200';
  const headerBg = isWarning
    ? 'bg-amber-50/80 hover:bg-amber-50'
    : 'bg-gray-50/50 hover:bg-gray-100/60';
  const iconColor = isWarning ? 'text-amber-500' : 'text-gray-400';
  const titleColor = isWarning ? 'text-amber-700' : 'text-gray-700';

  return (
    <div className={`border rounded-xl overflow-hidden ${borderColor}`}>
      {/* Header */}
      <button
        onClick={(e) => { e.stopPropagation(); setIsOpen(!isOpen); }}
        className={`w-full flex items-center gap-2 px-3 py-2.5 text-xs font-medium transition-colors ${headerBg}`}
      >
        <span className={`w-4 h-4 rounded-md inline-flex items-center justify-center flex-shrink-0 ${
          isWarning ? 'bg-amber-100 text-amber-600' : 'bg-slate-100 text-slate-500'
        }`}>
          <i className={`fas ${isOpen ? 'fa-minus' : 'fa-plus'} text-[9px]`} />
        </span>
        <i className={`fas ${icon} text-[10px] ${iconColor}`} />
        <span className={titleColor}>{title}</span>
        {count !== undefined && count > 0 && (
          <span
            className={`px-1.5 py-0.5 rounded-full text-[10px] font-semibold ${
              isWarning
                ? 'bg-amber-100 text-amber-600'
                : 'bg-gray-100 text-gray-500'
            }`}
          >
            {count}
          </span>
        )}
      </button>

      {/* 折叠时的摘要 */}
      {!isOpen && summary && (
        <div className="px-3 py-1.5 border-t border-gray-100 bg-white">
          {summary}
        </div>
      )}

      {/* 展开内容 */}
      {isOpen && (
        <div className="px-3 py-2.5 border-t border-gray-100 bg-white">
          {children}
        </div>
      )}
    </div>
  );
}

// ============================================================================
// AttributesSummary - 折叠时的 Attributes 摘要
// ============================================================================

function AttributesSummary({
  attributes,
  maxItems = 5,
}: {
  attributes: KeyValue[];
  maxItems?: number;
}) {
  const displayAttrs = attributes.slice(0, maxItems);
  const remaining = attributes.length - maxItems;

  return (
    <div className="flex flex-wrap gap-1">
      {displayAttrs.map((attr, i) => {
        const displayVal = String(anyValueToDisplay(attr.value));
        return (
          <span
            key={i}
            className="inline-flex items-center px-1.5 py-0.5 rounded text-[10px] bg-gray-100 text-gray-600 font-mono max-w-[200px] truncate"
            title={`${attr.key}=${displayVal}`}
          >
            <span className="text-gray-400">{attr.key}</span>
            <span className="text-gray-300 mx-0.5">=</span>
            <span className="truncate">{displayVal}</span>
          </span>
        );
      })}
      {remaining > 0 && (
        <span className="text-[10px] text-gray-400 self-center">
          +{remaining} more
        </span>
      )}
    </div>
  );
}

// ============================================================================
// KeyValueTable - 通用 key-value 表格（带 hover 复制按钮）
// ============================================================================

function KeyValueTable({ items }: { items: KeyValue[] }) {
  const [copiedIndex, setCopiedIndex] = useState<number | null>(null);

  const handleCopy = useCallback((value: string, index: number) => {
    copyText(value);
    setCopiedIndex(index);
    setTimeout(() => setCopiedIndex(null), 1500);
  }, []);

  return (
    <div className="rounded border border-gray-200 overflow-hidden">
      <table className="w-full text-xs">
        <tbody>
          {items.map((attr, i) => {
            const displayVal = String(anyValueToDisplay(attr.value));
            return (
              <tr
                key={i}
                className={`group ${i % 2 === 0 ? 'bg-gray-50/50' : 'bg-white'} hover:bg-blue-50/30 transition-colors`}
              >
                <td className="px-2.5 py-1.5 font-mono text-gray-500 w-2/5 align-top">
                  {attr.key}
                </td>
                <td className="px-2.5 py-1.5 text-gray-800 align-top">
                  <div className="flex items-start justify-between gap-1">
                    <span className="break-all">
                      <AttributeValue attr={attr} />
                    </span>
                    <button
                      onClick={(e) => { e.stopPropagation(); handleCopy(displayVal, i); }}
                      className="opacity-0 group-hover:opacity-100 p-0.5 text-gray-300 hover:text-gray-500 transition flex-shrink-0"
                      title="Copy value"
                    >
                      <i className={`fas ${copiedIndex === i ? 'fa-check text-green-500' : 'fa-copy'} text-[9px]`} />
                    </button>
                  </div>
                </td>
              </tr>
            );
          })}
        </tbody>
      </table>
    </div>
  );
}

// ============================================================================
// AttributeValue - 格式化 Attribute 值显示
// ============================================================================

function AttributeValue({ attr }: { attr: KeyValue }) {
  const val = anyValueToDisplay(attr.value);
  const strVal = String(val);

  // 错误状态高亮
  if (attr.key === 'error' && attr.value.boolValue === true) {
    return <span className="text-red-600 font-semibold">true</span>;
  }
  if (attr.key === 'otel.status_code' && attr.value.stringValue === 'ERROR') {
    return <span className="text-red-600 font-semibold">ERROR</span>;
  }
  if (attr.key === 'http.status_code' || attr.key === 'http.response.status_code') {
    const code = Number(strVal);
    if (code >= 400) {
      return <span className="text-red-600 font-semibold">{strVal}</span>;
    }
    if (code >= 300) {
      return <span className="text-yellow-600">{strVal}</span>;
    }
    return <span className="text-green-600">{strVal}</span>;
  }

  // 布尔值
  if (attr.value.boolValue !== undefined) {
    return <span className={attr.value.boolValue ? 'text-green-600' : 'text-gray-400'}>{strVal}</span>;
  }

  // 长文本尝试格式化
  if (typeof val === 'string' && val.length > 100) {
    return <LongValueDisplay value={val} />;
  }

  return <span className="break-all">{strVal}</span>;
}

// ============================================================================
// LongValueDisplay - 长文本值展示（可展开/折叠）
// ============================================================================

function LongValueDisplay({ value }: { value: string }) {
  const [expanded, setExpanded] = useState(false);
  const displayValue = expanded ? value : value.substring(0, 100) + '...';

  return (
    <div>
      <span className="break-all">{displayValue}</span>
      <button
        onClick={(e) => {
          e.stopPropagation();
          setExpanded(!expanded);
        }}
        className="ml-1 text-primary-500 hover:text-primary-700 text-[10px] font-medium"
      >
        {expanded ? 'Show less' : 'Show more'}
      </button>
    </div>
  );
}

// ============================================================================
// TraceJsonView - 原始 JSON 数据查看（方便 agent 分析和优化）
// ============================================================================

function TraceJsonView({ trace }: TraceDetailProps) {
  const [copied, setCopied] = useState(false);

  const jsonText = useMemo(() => {
    try {
      return JSON.stringify(trace, null, 2);
    } catch {
      return '{}';
    }
  }, [trace]);

  const lineCount = useMemo(() => jsonText.split('\n').length, [jsonText]);

  const handleCopy = useCallback(() => {
    copyText(jsonText);
    setCopied(true);
    setTimeout(() => setCopied(false), 2000);
  }, [jsonText]);

  const handleDownload = useCallback(() => {
    const blob = new Blob([jsonText], { type: 'application/json' });
    const url = URL.createObjectURL(blob);
    const a = document.createElement('a');
    a.href = url;
    a.download = `trace-${trace.traceId.substring(0, 16)}.json`;
    a.click();
    URL.revokeObjectURL(url);
  }, [jsonText, trace.traceId]);

  return (
    <div className="flex-1 min-h-0 flex flex-col p-4 lg:p-5">
      {/* 工具栏 */}
      <div className="flex-shrink-0 flex items-center justify-between mb-3 px-2">
        <div className="flex items-center gap-3 text-xs text-slate-500">
          <span className="inline-flex items-center gap-1.5">
            <i className="fas fa-file-code text-slate-400" />
            <span className="font-mono text-slate-600">{trace.traceId}</span>
          </span>
          <span className="text-slate-300">|</span>
          <span>{trace.spans.length} spans</span>
          <span className="text-slate-300">|</span>
          <span>{lineCount} lines</span>
        </div>
        <div className="flex items-center gap-2">
          <button
            onClick={handleCopy}
            className="inline-flex items-center gap-1.5 px-3 py-1.5 text-xs font-medium rounded-xl border border-slate-200 bg-white text-slate-600 hover:bg-slate-50 hover:border-slate-300 transition shadow-sm"
          >
            <i className={`fas ${copied ? 'fa-check text-green-500' : 'fa-copy'} text-[11px]`} />
            {copied ? 'Copied!' : 'Copy'}
          </button>
          <button
            onClick={handleDownload}
            className="inline-flex items-center gap-1.5 px-3 py-1.5 text-xs font-medium rounded-xl bg-primary-50 text-primary-700 hover:bg-primary-100 transition shadow-sm"
          >
            <i className="fas fa-download text-[11px]" />
            Download .json
          </button>
        </div>
      </div>

      {/* JSON 内容区 — 代码风格展示 */}
      <div className="flex-1 min-h-0 rounded-2xl border border-slate-200 bg-[#1e293b] shadow-sm overflow-hidden">
        <pre className="h-full overflow-auto p-5 text-[13px] leading-relaxed font-mono text-slate-300 whitespace-pre">
          <code>{jsonText}</code>
        </pre>
      </div>
    </div>
  );
}

// ============================================================================
// EventsList - Events 列表
// ============================================================================

function EventsList({
  events,
  traceStartNano,
}: {
  events: OTelSpan['events'] & {};
  traceStartNano: number;
}) {
  const [expandedEvents, setExpandedEvents] = useState<Set<number>>(new Set());

  const toggleEvent = useCallback((index: number) => {
    setExpandedEvents(prev => {
      const next = new Set(prev);
      if (next.has(index)) {
        next.delete(index);
      } else {
        next.add(index);
      }
      return next;
    });
  }, []);

  return (
    <div className="space-y-1.5">
      {events.map((event, i) => {
        const isExpanded = expandedEvents.has(i);
        const eventTimeNano = Number(event.timeUnixNano);
        const relativeNano = eventTimeNano - traceStartNano;

        return (
          <div
            key={i}
            className="rounded border border-gray-200 overflow-hidden"
          >
            {/* Event Header */}
            <button
              onClick={(e) => { e.stopPropagation(); toggleEvent(i); }}
              className="w-full flex items-center gap-2 px-2.5 py-1.5 text-xs bg-gray-50/50 hover:bg-gray-100/60 transition-colors"
            >
              <i
                className={`fas fa-chevron-right text-[8px] text-gray-400 transition-transform duration-150 ${
                  isExpanded ? 'rotate-90' : ''
                }`}
              />
              <span className="font-mono text-gray-500">
                {formatDuration(relativeNano)}
              </span>
              <span className="text-gray-600 font-medium truncate">
                {event.name}
              </span>
              {event.attributes && event.attributes.length > 0 && (
                <span className="ml-auto text-[10px] text-gray-300 flex-shrink-0">
                  {event.attributes.length} attr{event.attributes.length !== 1 ? 's' : ''}
                </span>
              )}
            </button>

            {/* Event Attributes */}
            {isExpanded && event.attributes && event.attributes.length > 0 && (
              <div className="border-t border-gray-100 bg-white">
                <table className="w-full text-xs">
                  <tbody>
                    {event.attributes.map((attr, j) => (
                      <tr key={j} className={j % 2 === 0 ? 'bg-gray-50/30' : ''}>
                        <td className="px-2.5 py-1 font-mono text-gray-500 w-1/3 align-top">
                          {attr.key}
                        </td>
                        <td className="px-2.5 py-1 text-gray-700 break-all align-top">
                          {String(anyValueToDisplay(attr.value))}
                        </td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              </div>
            )}
          </div>
        );
      })}
    </div>
  );
}
