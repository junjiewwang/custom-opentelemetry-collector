/**
 * TraceDetail 组件 - Trace 调用链时间轴可视化
 *
 * 功能：
 * - 展示完整的 Span 树形结构（虚拟滚动）
 * - 时间轴 bar 可视化每个 Span 的相对起始时间和持续时长
 * - 展开/收起 Span 详情（Accordion：Attributes / Events / Process / References / Warnings）
 * - 颜色按 Service 区分
 * - Span 搜索过滤（operationName、serviceName、tags）
 * - 视图 Tab 切换预留（Timeline / Statistics / Table / Flamegraph / Graph）
 */

import { useState, useMemo, useRef, useCallback } from 'react';
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

      {/* Expanded Detail (Accordion) */}
      {isDetailExpanded && (
        <div className="border-b border-gray-100 bg-gray-50 px-6 py-3">
          <SpanDetail span={span} process={process} />
        </div>
      )}
    </>
  );
}

// ============================================================================
// AccordionSection 组件 - 可折叠的内容区域
// ============================================================================

interface AccordionSectionProps {
  title: string;
  icon: string;
  count?: number;
  defaultOpen?: boolean;
  variant?: 'default' | 'danger';
  children: React.ReactNode;
}

function AccordionSection({
  title,
  icon,
  count,
  defaultOpen = false,
  variant = 'default',
  children,
}: AccordionSectionProps) {
  const [isOpen, setIsOpen] = useState(defaultOpen);

  const headerColor = variant === 'danger'
    ? 'text-red-600 hover:bg-red-50'
    : 'text-gray-700 hover:bg-gray-100';

  return (
    <div className={`border rounded-lg overflow-hidden ${
      variant === 'danger' ? 'border-red-200' : 'border-gray-200'
    }`}>
      {/* Header */}
      <button
        onClick={() => setIsOpen(!isOpen)}
        className={`w-full flex items-center justify-between px-3 py-2 text-xs font-medium transition-colors ${headerColor} bg-white`}
      >
        <div className="flex items-center gap-2">
          <i className={`fas ${icon} text-[10px] ${
            variant === 'danger' ? 'text-red-500' : 'text-gray-400'
          }`} />
          <span>{title}</span>
          {count !== undefined && count > 0 && (
            <span className={`px-1.5 py-0.5 rounded-full text-[10px] font-semibold ${
              variant === 'danger'
                ? 'bg-red-100 text-red-600'
                : 'bg-gray-100 text-gray-500'
            }`}>
              {count}
            </span>
          )}
        </div>
        <i className={`fas fa-chevron-${isOpen ? 'up' : 'down'} text-[10px] text-gray-400 transition-transform`} />
      </button>

      {/* Content with transition */}
      <div
        className={`transition-all duration-200 ease-in-out overflow-hidden ${
          isOpen ? 'max-h-[2000px] opacity-100' : 'max-h-0 opacity-0'
        }`}
      >
        <div className="px-3 py-2 border-t border-gray-100 bg-white">
          {children}
        </div>
      </div>
    </div>
  );
}

// ============================================================================
// SpanDetail 组件 - Accordion 结构的 Span 详情面板
// ============================================================================

interface SpanDetailProps {
  span: SpanTreeNode['span'];
  process: SpanTreeNode['process'];
}

function SpanDetail({ span, process }: SpanDetailProps) {
  const hasReferences = span.references.length > 0;
  const hasWarnings = span.warnings && span.warnings.length > 0;

  return (
    <div className="space-y-2 text-sm">
      {/* Basic Info (always visible) */}
      <div className="grid grid-cols-2 gap-2 text-xs mb-2">
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

      {/* Accordion: Attributes (span.tags) — 默认展开 */}
      {span.tags.length > 0 && (
        <AccordionSection
          title="Attributes"
          icon="fa-tags"
          count={span.tags.length}
          defaultOpen={true}
        >
          <KeyValueTable items={span.tags} />
        </AccordionSection>
      )}

      {/* Accordion: Events (span.logs) — 默认折叠 */}
      {span.logs.length > 0 && (
        <AccordionSection
          title="Events"
          icon="fa-list-ol"
          count={span.logs.length}
          defaultOpen={false}
        >
          <div className="space-y-1.5">
            {span.logs.map((log, i) => (
              <div key={i} className="rounded border border-gray-200 px-3 py-2 bg-gray-50">
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
        </AccordionSection>
      )}

      {/* Accordion: Process / Resource (process.tags) — 默认折叠 */}
      {process.tags.length > 0 && (
        <AccordionSection
          title="Process / Resource"
          icon="fa-server"
          count={process.tags.length}
          defaultOpen={false}
        >
          <KeyValueTable items={process.tags} />
        </AccordionSection>
      )}

      {/* Accordion: References / Links — 默认折叠（如果有） */}
      {hasReferences && (
        <AccordionSection
          title="References / Links"
          icon="fa-link"
          count={span.references.length}
          defaultOpen={false}
        >
          <div className="space-y-1">
            {span.references.map((ref, i) => (
              <div key={i} className="flex items-center gap-2 text-xs">
                <span className={`px-1.5 py-0.5 rounded text-[10px] font-medium ${
                  ref.refType === 'CHILD_OF'
                    ? 'bg-blue-100 text-blue-700'
                    : 'bg-purple-100 text-purple-700'
                }`}>
                  {ref.refType}
                </span>
                <span className="font-mono text-gray-500" title={`Trace: ${ref.traceID}`}>
                  Span: {ref.spanID}
                </span>
              </div>
            ))}
          </div>
        </AccordionSection>
      )}

      {/* Accordion: Warnings — 默认折叠（如果有，红色高亮） */}
      {hasWarnings && (
        <AccordionSection
          title="Warnings"
          icon="fa-exclamation-triangle"
          count={span.warnings!.length}
          defaultOpen={false}
          variant="danger"
        >
          <div className="space-y-1">
            {span.warnings!.map((warning, i) => (
              <div key={i} className="text-xs text-red-600 flex items-start gap-1.5">
                <i className="fas fa-exclamation-circle text-red-400 mt-0.5 flex-shrink-0" />
                <span>{warning}</span>
              </div>
            ))}
          </div>
        </AccordionSection>
      )}
    </div>
  );
}

// ============================================================================
// KeyValueTable 组件 - 通用 key-value 表格
// ============================================================================

function KeyValueTable({ items }: { items: JaegerKeyValue[] }) {
  return (
    <div className="rounded border border-gray-200 overflow-hidden">
      <table className="w-full text-xs">
        <tbody>
          {items.map((tag, i) => (
            <tr key={i} className={i % 2 === 0 ? 'bg-gray-50' : 'bg-white'}>
              <td className="px-3 py-1 font-mono text-gray-600 w-1/3">{tag.key}</td>
              <td className="px-3 py-1 text-gray-800">
                <TagValue tag={tag} />
              </td>
            </tr>
          ))}
        </tbody>
      </table>
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
