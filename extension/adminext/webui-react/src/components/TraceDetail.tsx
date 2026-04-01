/**
 * TraceDetail 组件 - Trace 调用链时间轴可视化
 *
 * 功能：
 * - 展示完整的 Span 树形结构（虚拟滚动）
 * - 时间轴 bar 可视化每个 Span 的相对起始时间和持续时长
 * - 展开/收起 Span 详情（参考 Jaeger UI 设计的 Accordion 面板）
 * - 颜色按 Service 区分
 * - Span 搜索过滤（operationName、serviceName、tags）
 * - 视图 Tab 切换预留（Timeline / Statistics / Table / Flamegraph / Graph）
 */

import { useState, useMemo, useRef, useCallback, type ReactNode } from 'react';
import { useNavigate } from 'react-router-dom';
import { useVirtualizer } from '@tanstack/react-virtual';
import { buildSpanTree, formatDuration, formatTimestamp, getServiceColor } from '@/utils/trace';
import type { JaegerTrace, JaegerKeyValue, SpanTreeNode } from '@/types/trace';
import TraceStatisticsView from './TraceStatisticsView';
import TraceSpanTableView from './TraceSpanTableView';
import TraceFlamegraphView from './TraceFlamegraphView';
import TraceGraphView from './TraceGraphView';

// ============================================================================
// 类型定义
// ============================================================================

interface TraceDetailProps {
  trace: JaegerTrace;
  onClose: () => void;
}

type ViewTab = 'timeline' | 'statistics' | 'table' | 'flamegraph' | 'graph';

const VIEW_TABS: { key: ViewTab; label: string; icon: string }[] = [
  { key: 'timeline', label: 'Timeline', icon: 'fa-stream' },
  { key: 'statistics', label: 'Statistics', icon: 'fa-chart-bar' },
  { key: 'table', label: 'Table', icon: 'fa-table' },
  { key: 'flamegraph', label: 'Flamegraph', icon: 'fa-fire' },
  { key: 'graph', label: 'Graph', icon: 'fa-project-diagram' },
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
      if (!collapsedIds.has(node.span.spanID) && node.children.length > 0) {
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
  // 匹配 operationName
  if (node.span.operationName.toLowerCase().includes(q)) return true;
  // 匹配 serviceName
  if (node.process.serviceName.toLowerCase().includes(q)) return true;
  // 匹配 tags 的 key 和 value
  for (const tag of node.span.tags) {
    if (tag.key.toLowerCase().includes(q)) return true;
    if (String(tag.value).toLowerCase().includes(q)) return true;
  }
  return false;
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

  // trace 起始时间
  const traceStartTime = useMemo(
    () => trace.spans.reduce((min, s) => Math.min(min, s.startTime), Infinity),
    [trace],
  );

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
      if (node && expandedSpanIDs.has(node.span.spanID)) {
        return 336; // SpanRow(36) + SpanDetail(~300)
      }
      return 36;
    },
    overscan: 10,
  });

  // 切换树节点折叠/展开
  const toggleCollapse = useCallback((spanID: string) => {
    setCollapsedSpanIDs(prev => {
      const next = new Set(prev);
      if (next.has(spanID)) {
        next.delete(spanID);
      } else {
        next.add(spanID);
      }
      return next;
    });
  }, []);

  // 切换 span 详情展开
  const toggleDetail = useCallback((spanID: string) => {
    setExpandedSpanIDs(prev => {
      const next = new Set(prev);
      if (next.has(spanID)) {
        next.delete(spanID);
      } else {
        next.add(spanID);
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
      if (node) set.add(node.span.spanID);
    }
    return set;
  }, [matchedIndices, flattenedSpans]);

  // 当前聚焦的匹配 span ID
  const currentFocusedSpanID = useMemo(() => {
    if (matchedIndices.length === 0) return null;
    const idx = matchedIndices[currentMatchIndex];
    return idx !== undefined ? flattenedSpans[idx]?.span.spanID ?? null : null;
  }, [matchedIndices, currentMatchIndex, flattenedSpans]);

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
            {/* Trace → Metric 联动 */}
            {serviceStats.length > 0 && (
              <button
                onClick={() => {
                  const rootService = serviceStats[0]?.name;
                  if (rootService) {
                    navigate(`/metrics?tab=red&service=${encodeURIComponent(rootService)}`);
                  }
                }}
                className="px-3 py-1.5 text-xs bg-primary-50 text-primary-700 hover:bg-primary-100 rounded-lg transition flex items-center gap-1.5"
                title="View RED metrics for this Service"
              >
                <i className="fas fa-chart-line" />
                View Metrics
              </button>
            )}
            {/* Trace → Trace 搜索 */}
            <button
              onClick={() => {
                const rootService = serviceStats[0]?.name;
                if (rootService) {
                  navigate(`/traces?service=${encodeURIComponent(rootService)}&lookback=1h`);
                }
              }}
              className="px-3 py-1.5 text-xs bg-gray-100 text-gray-600 hover:bg-gray-200 rounded-lg transition flex items-center gap-1.5"
              title="Search more Traces for this Service"
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

      {/* View Tab 切换 */}
      <div className="px-6 border-b border-gray-200 bg-white flex items-center gap-0">
        {VIEW_TABS.map(tab => (
          <button
            key={tab.key}
            onClick={() => setActiveTab(tab.key)}
            className={`px-4 py-2.5 text-sm font-medium border-b-2 transition-colors ${
              activeTab === tab.key
                ? 'border-primary-500 text-primary-600'
                : 'border-transparent text-gray-400 hover:text-gray-600 hover:border-gray-300'
            }`}
          >
            <i className={`fas ${tab.icon} mr-1.5`} />
            {tab.label}
          </button>
        ))}
      </div>

      {/* Tab 内容区 */}
      {activeTab === 'timeline' ? (
        <>
          {/* Span 搜索栏 */}
          <div className="px-6 py-2 border-b border-gray-100 flex items-center gap-3 bg-white">
            <div className="flex-1 relative">
              <i className="fas fa-search absolute left-3 top-1/2 -translate-y-1/2 text-gray-300 text-xs" />
              <input
                type="text"
                value={searchQuery}
                onChange={e => handleSearchChange(e.target.value)}
                placeholder="Search spans..."
                className="w-full pl-8 pr-3 py-1.5 text-sm border border-gray-200 rounded-lg focus:outline-none focus:ring-2 focus:ring-primary-200 focus:border-primary-400 transition bg-gray-50"
              />
            </div>
            {searchQuery.trim() && (
              <div className="flex items-center gap-2 text-xs text-gray-500 flex-shrink-0">
                <span>
                  {matchedIndices.length > 0
                    ? `${currentMatchIndex + 1} of ${matchedIndices.length}`
                    : '0 of 0'}{' '}
                  spans
                </span>
                <button
                  onClick={() => navigateMatch('prev')}
                  disabled={matchedIndices.length === 0}
                  className="p-1 hover:bg-gray-100 rounded disabled:opacity-30 transition"
                  title="Previous match"
                >
                  <i className="fas fa-chevron-up" />
                </button>
                <button
                  onClick={() => navigateMatch('next')}
                  disabled={matchedIndices.length === 0}
                  className="p-1 hover:bg-gray-100 rounded disabled:opacity-30 transition"
                  title="Next match"
                >
                  <i className="fas fa-chevron-down" />
                </button>
                <button
                  onClick={() => { setSearchQuery(''); setCurrentMatchIndex(0); }}
                  className="p-1 hover:bg-gray-100 rounded transition"
                  title="Clear search"
                >
                  <i className="fas fa-times text-gray-400" />
                </button>
              </div>
            )}
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

          {/* Virtual Scroll Span Rows */}
          <div
            ref={scrollContainerRef}
            className="overflow-y-auto"
            style={{ maxHeight: '600px' }}
          >
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
                const isDetailExpanded = expandedSpanIDs.has(node.span.spanID);
                const isCollapsed = collapsedSpanIDs.has(node.span.spanID);
                const isMatched = matchedSpanIDs.has(node.span.spanID);
                const isFocused = currentFocusedSpanID === node.span.spanID;

                return (
                  <div
                    key={node.span.spanID}
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
                      traceDuration={traceDuration}
                      traceStartTime={traceStartTime}
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
        </>
      ) : activeTab === 'statistics' ? (
        <div className="p-4">
          <TraceStatisticsView trace={trace} />
        </div>
      ) : activeTab === 'table' ? (
        <div className="p-4">
          <TraceSpanTableView trace={trace} />
        </div>
      ) : activeTab === 'flamegraph' ? (
        <div className="p-4">
          <TraceFlamegraphView trace={trace} />
        </div>
      ) : activeTab === 'graph' ? (
        <div className="p-4">
          <TraceGraphView trace={trace} />
        </div>
      ) : null}
    </div>
  );
}

// ============================================================================
// VirtualSpanRow 组件 - 虚拟滚动中的单个 Span 行（不再递归）
// ============================================================================

interface VirtualSpanRowProps {
  node: SpanTreeNode;
  traceDuration: number;
  traceStartTime: number;
  isDetailExpanded: boolean;
  isCollapsed: boolean;
  hasChildren: boolean;
  isMatched: boolean;
  isFocused: boolean;
  onToggleCollapse: (spanID: string) => void;
  onToggleDetail: (spanID: string) => void;
}

function VirtualSpanRow({
  node,
  traceDuration,
  traceStartTime,
  isDetailExpanded,
  isCollapsed,
  hasChildren,
  isMatched,
  isFocused,
  onToggleCollapse,
  onToggleDetail,
}: VirtualSpanRowProps) {
  const { span, process, depth } = node;

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
    ? Math.max((span.duration / traceDuration) * 100, 0.3)
    : 0;

  // 构建行背景色
  let rowBgClass = '';
  if (isFocused) {
    rowBgClass = 'bg-yellow-100';
  } else if (isMatched) {
    rowBgClass = 'bg-yellow-50';
  } else if (hasError) {
    rowBgClass = 'bg-red-50/30';
  }

  return (
    <>
      {/* Span Bar Row */}
      <div
        className={`flex items-center border-b border-gray-50 hover:bg-gray-50/60 cursor-pointer transition-colors ${rowBgClass}`}
        onClick={() => onToggleDetail(span.spanID)}
      >
        {/* Left: Service :: Operation */}
        <div
          className="w-1/3 flex-shrink-0 px-4 py-2 flex items-center gap-1 text-sm truncate"
          style={{ paddingLeft: `${16 + depth * 20}px` }}
        >
          {hasChildren ? (
            <button
              onClick={(e) => {
                e.stopPropagation();
                onToggleCollapse(span.spanID);
              }}
              className="w-4 h-4 flex items-center justify-center text-gray-400 hover:text-gray-600 flex-shrink-0"
            >
              <i className={`fas fa-caret-${isCollapsed ? 'right' : 'down'} text-xs`} />
            </button>
          ) : (
            <span className="w-4" />
          )}
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
              opacity: hasError ? 1 : 0.7,
            }}
          >
            <span className="text-white text-[10px] px-1 whitespace-nowrap">
              {formatDuration(span.duration)}
            </span>
          </div>
        </div>
      </div>

      {/* Expanded Detail (Jaeger 风格内联展开) */}
      {isDetailExpanded && (
        <div className="border-b border-gray-100 bg-gray-50/50 px-6 py-3">
          <SpanDetail span={span} process={process} traceStartTime={traceStartTime} />
        </div>
      )}
    </>
  );
}

// ============================================================================
// SpanDetail 组件 - Jaeger 风格的 Span 详情面板（内联展开）
// ============================================================================

interface SpanDetailProps {
  span: SpanTreeNode['span'];
  process: SpanTreeNode['process'];
  traceStartTime: number;
}

function SpanDetail({ span, process, traceStartTime }: SpanDetailProps) {
  const serviceColor = getServiceColor(process.serviceName);
  const hasReferences = span.references.length > 0;
  const hasWarnings = span.warnings && span.warnings.length > 0;
  const hasEvents = span.logs.length > 0;
  const hasProcessTags = process.tags.length > 0;
  const relativeStartTime = span.startTime - traceStartTime;

  // 复制到剪贴板
  const copyToClipboard = (text: string) => {
    navigator.clipboard.writeText(text);
  };

  return (
    <div className="space-y-3">
      {/* 顶部颜色条 + Overview */}
      <div
        className="rounded-lg border border-gray-200 overflow-hidden"
        style={{ borderTopWidth: '3px', borderTopColor: serviceColor }}
      >
        <div className="px-4 py-3 bg-white">
          <div className="grid grid-cols-2 md:grid-cols-4 gap-x-6 gap-y-2">
            <OverviewItem label="Service" value={process.serviceName} />
            <OverviewItem label="Duration" value={formatDuration(span.duration)} bold />
            <OverviewItem label="Start Time" value={formatDuration(relativeStartTime)} />
            <OverviewItem label="Absolute Time" value={formatTimestamp(span.startTime)} small />
          </div>
        </div>
      </div>

      {/* Accordion 区域 */}
      <div className="space-y-2">
        {/* Attributes (Tags) — 默认展开 */}
        {span.tags.length > 0 && (
          <AccordionSection
            title="Attributes"
            icon="fa-tags"
            count={span.tags.length}
            defaultOpen={true}
            summary={<TagsSummary tags={span.tags} />}
          >
            <KeyValueTable items={span.tags} />
          </AccordionSection>
        )}

        {/* Events (Logs) */}
        {hasEvents && (
          <AccordionSection
            title="Events"
            icon="fa-list-ol"
            count={span.logs.length}
            defaultOpen={false}
          >
            <EventsList events={span.logs} traceStartTime={traceStartTime} />
          </AccordionSection>
        )}

        {/* Process / Resource */}
        {hasProcessTags && (
          <AccordionSection
            title="Resource"
            icon="fa-server"
            count={process.tags.length}
            defaultOpen={false}
            summary={<TagsSummary tags={process.tags} maxItems={3} />}
          >
            <KeyValueTable items={process.tags} />
          </AccordionSection>
        )}

        {/* References / Links */}
        {hasReferences && (
          <AccordionSection
            title="References"
            icon="fa-link"
            count={span.references.length}
            defaultOpen={false}
          >
            <div className="space-y-1.5">
              {span.references.map((ref, i) => (
                <div key={i} className="flex items-center gap-2 text-xs py-1">
                  <span
                    className={`px-1.5 py-0.5 rounded text-[10px] font-semibold ${
                      ref.refType === 'CHILD_OF'
                        ? 'bg-blue-50 text-blue-600 border border-blue-200'
                        : 'bg-purple-50 text-purple-600 border border-purple-200'
                    }`}
                  >
                    {ref.refType}
                  </span>
                  <span className="font-mono text-gray-500 truncate" title={ref.spanID}>
                    {ref.spanID}
                  </span>
                </div>
              ))}
            </div>
          </AccordionSection>
        )}

        {/* Warnings */}
        {hasWarnings && (
          <AccordionSection
            title="Warnings"
            icon="fa-exclamation-triangle"
            count={span.warnings!.length}
            defaultOpen={true}
            variant="warning"
          >
            <div className="space-y-1.5">
              {span.warnings!.map((warning, i) => (
                <div key={i} className="text-xs text-amber-700 flex items-start gap-2 py-1">
                  <i className="fas fa-exclamation-circle text-amber-500 mt-0.5 flex-shrink-0 text-[10px]" />
                  <span>{warning}</span>
                </div>
              ))}
            </div>
          </AccordionSection>
        )}
      </div>

      {/* Footer - Debug Info（参考 Jaeger） */}
      <div className="flex items-center justify-between px-1 pt-1">
        <div className="text-xs text-gray-400 flex items-center gap-1.5 min-w-0">
          <span className="text-gray-500 font-medium flex-shrink-0">SpanID:</span>
          <span className="font-mono truncate" title={span.spanID}>{span.spanID}</span>
        </div>
        <div className="flex items-center gap-1.5 flex-shrink-0 ml-3">
          <button
            onClick={(e) => { e.stopPropagation(); copyToClipboard(span.spanID); }}
            className="px-2.5 py-1 text-[11px] text-gray-500 hover:text-gray-700 bg-white border border-gray-200 hover:border-gray-300 rounded-md transition flex items-center gap-1.5"
            title="Copy Span ID"
          >
            <i className="fas fa-copy text-[10px]" />
            Copy ID
          </button>
          <button
            onClick={(e) => {
              e.stopPropagation();
              const deepLink = `${window.location.origin}${window.location.pathname}?uiFind=${span.spanID}`;
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
// AccordionSection - 可折叠的内容区域（Jaeger 风格）
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
    <div className={`border rounded-lg overflow-hidden ${borderColor}`}>
      {/* Header */}
      <button
        onClick={(e) => { e.stopPropagation(); setIsOpen(!isOpen); }}
        className={`w-full flex items-center gap-2 px-3 py-2 text-xs font-medium transition-colors ${headerBg}`}
      >
        <i
          className={`fas fa-chevron-right text-[9px] text-gray-400 transition-transform duration-150 ${
            isOpen ? 'rotate-90' : ''
          }`}
        />
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
// TagsSummary - 折叠时的 Tags 摘要（参考 Jaeger 的 AttributesSummary）
// ============================================================================

function TagsSummary({
  tags,
  maxItems = 5,
}: {
  tags: JaegerKeyValue[];
  maxItems?: number;
}) {
  const displayTags = tags.slice(0, maxItems);
  const remaining = tags.length - maxItems;

  return (
    <div className="flex flex-wrap gap-1">
      {displayTags.map((tag, i) => (
        <span
          key={i}
          className="inline-flex items-center px-1.5 py-0.5 rounded text-[10px] bg-gray-100 text-gray-600 font-mono max-w-[200px] truncate"
          title={`${tag.key}=${String(tag.value)}`}
        >
          <span className="text-gray-400">{tag.key}</span>
          <span className="text-gray-300 mx-0.5">=</span>
          <span className="truncate">{String(tag.value)}</span>
        </span>
      ))}
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

function KeyValueTable({ items }: { items: JaegerKeyValue[] }) {
  const [copiedIndex, setCopiedIndex] = useState<number | null>(null);

  const handleCopy = useCallback((value: string, index: number) => {
    navigator.clipboard.writeText(value);
    setCopiedIndex(index);
    setTimeout(() => setCopiedIndex(null), 1500);
  }, []);

  return (
    <div className="rounded border border-gray-200 overflow-hidden">
      <table className="w-full text-xs">
        <tbody>
          {items.map((tag, i) => (
            <tr
              key={i}
              className={`group ${i % 2 === 0 ? 'bg-gray-50/50' : 'bg-white'} hover:bg-blue-50/30 transition-colors`}
            >
              <td className="px-2.5 py-1.5 font-mono text-gray-500 w-2/5 align-top">
                {tag.key}
              </td>
              <td className="px-2.5 py-1.5 text-gray-800 align-top">
                <div className="flex items-start justify-between gap-1">
                  <span className="break-all">
                    <TagValue tag={tag} />
                  </span>
                  <button
                    onClick={(e) => { e.stopPropagation(); handleCopy(String(tag.value), i); }}
                    className="opacity-0 group-hover:opacity-100 p-0.5 text-gray-300 hover:text-gray-500 transition flex-shrink-0"
                    title="Copy value"
                  >
                    <i className={`fas ${copiedIndex === i ? 'fa-check text-green-500' : 'fa-copy'} text-[9px]`} />
                  </button>
                </div>
              </td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
}

// ============================================================================
// TagValue - 格式化 Tag 值显示
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

  // 长文本（如 JSON）尝试格式化
  if (typeof tag.value === 'string' && tag.value.length > 100) {
    return <LongValueDisplay value={tag.value} />;
  }

  return <span className="break-all">{val}</span>;
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
// EventsList - Events/Logs 列表
// ============================================================================

function EventsList({
  events,
  traceStartTime,
}: {
  events: SpanTreeNode['span']['logs'];
  traceStartTime: number;
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
      {events.map((log, i) => {
        const isExpanded = expandedEvents.has(i);
        const relativeTime = log.timestamp - traceStartTime;

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
                {formatDuration(relativeTime)}
              </span>
              {/* 显示第一个 field 作为摘要 */}
              {log.fields.length > 0 && log.fields[0] && (
                <span className="text-gray-400 truncate">
                  {log.fields[0].key}: {String(log.fields[0].value).substring(0, 50)}
                  {String(log.fields[0].value).length > 50 ? '...' : ''}
                </span>
              )}
              <span className="ml-auto text-[10px] text-gray-300 flex-shrink-0">
                {log.fields.length} field{log.fields.length !== 1 ? 's' : ''}
              </span>
            </button>

            {/* Event Fields */}
            {isExpanded && (
              <div className="border-t border-gray-100 bg-white">
                <table className="w-full text-xs">
                  <tbody>
                    {log.fields.map((field, j) => (
                      <tr key={j} className={j % 2 === 0 ? 'bg-gray-50/30' : ''}>
                        <td className="px-2.5 py-1 font-mono text-gray-500 w-1/3 align-top">
                          {field.key}
                        </td>
                        <td className="px-2.5 py-1 text-gray-700 break-all align-top">
                          {String(field.value)}
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
