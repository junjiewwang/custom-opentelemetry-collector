/**
 * TraceSpanTableView — Span 表格视图
 *
 * 功能：
 * - 所有 spans 以平铺表格展示
 * - 支持按 Duration / Start Time 排序（点击表头切换 asc/desc）
 * - 搜索过滤框：过滤 service, operation, tag keys/values
 * - Status badge（OK / ERROR）
 * - Tags 列：前 3 个 tag，hover tooltip 显示全部
 * - 行点击高亮
 * - 使用 @tanstack/react-virtual 虚拟滚动
 */

import { useState, useMemo, useRef, useCallback } from 'react';
import { useVirtualizer } from '@tanstack/react-virtual';
import type { JaegerTrace, JaegerSpan } from '@/types/trace';
import { formatDuration, formatTimestamp, getServiceColor } from '@/utils/trace';

interface TraceViewProps {
  trace: JaegerTrace;
}

// ============================================================================
// 类型
// ============================================================================

type SortField = 'duration' | 'startTime';
type SortOrder = 'asc' | 'desc';

interface FlatSpan {
  index: number;
  span: JaegerSpan;
  serviceName: string;
  hasError: boolean;
}

// ============================================================================
// 工具函数
// ============================================================================

function isSpanError(span: JaegerSpan): boolean {
  return span.tags.some(
    (t) =>
      (t.key === 'error' && t.value === true) ||
      (t.key === 'otel.status_code' && t.value === 'ERROR'),
  );
}

/** 检查 span 是否匹配搜索词 */
function matchesSearch(item: FlatSpan, query: string): boolean {
  const q = query.toLowerCase();
  if (item.serviceName.toLowerCase().includes(q)) return true;
  if (item.span.operationName.toLowerCase().includes(q)) return true;
  for (const tag of item.span.tags) {
    if (tag.key.toLowerCase().includes(q)) return true;
    if (String(tag.value).toLowerCase().includes(q)) return true;
  }
  return false;
}

// ============================================================================
// 主组件
// ============================================================================

const ROW_HEIGHT = 44;

export default function TraceSpanTableView({ trace }: TraceViewProps) {
  const [search, setSearch] = useState('');
  const [sortField, setSortField] = useState<SortField>('startTime');
  const [sortOrder, setSortOrder] = useState<SortOrder>('asc');
  const [selectedSpanId, setSelectedSpanId] = useState<string | null>(null);
  const parentRef = useRef<HTMLDivElement>(null);

  // 扁平化 spans 列表（含 service 信息）
  const flatSpans = useMemo<FlatSpan[]>(() => {
    return trace.spans.map((span, index) => {
      const proc = trace.processes[span.processID];
      return {
        index: index + 1,
        span,
        serviceName: proc?.serviceName ?? 'unknown',
        hasError: isSpanError(span),
      };
    });
  }, [trace]);

  // 过滤 + 排序
  const filteredSpans = useMemo(() => {
    let result = flatSpans;

    // 搜索过滤
    if (search.trim()) {
      result = result.filter((item) => matchesSearch(item, search.trim()));
    }

    // 排序
    result = [...result].sort((a, b) => {
      const valA = sortField === 'duration' ? a.span.duration : a.span.startTime;
      const valB = sortField === 'duration' ? b.span.duration : b.span.startTime;
      return sortOrder === 'asc' ? valA - valB : valB - valA;
    });

    return result;
  }, [flatSpans, search, sortField, sortOrder]);

  // 虚拟滚动
  const virtualizer = useVirtualizer({
    count: filteredSpans.length,
    getScrollElement: () => parentRef.current,
    estimateSize: () => ROW_HEIGHT,
    overscan: 10,
  });

  const handleSort = useCallback(
    (field: SortField) => {
      if (sortField === field) {
        setSortOrder((prev) => (prev === 'asc' ? 'desc' : 'asc'));
      } else {
        setSortField(field);
        setSortOrder('desc');
      }
    },
    [sortField],
  );

  // 空状态
  if (!trace.spans || trace.spans.length === 0) {
    return (
      <div className="bg-white rounded-xl shadow-sm border border-gray-200 p-4">
        <div className="flex flex-col items-center justify-center py-16 text-gray-400">
          <i className="fas fa-table text-3xl mb-3" />
          <p className="text-sm">No data</p>
        </div>
      </div>
    );
  }

  return (
    <div className="bg-white rounded-xl shadow-sm border border-gray-200 p-4">
      {/* 搜索框 + 结果计数 */}
      <div className="flex items-center gap-3 mb-4">
        <div className="relative flex-1 max-w-md">
          <i className="fas fa-search absolute left-3 top-1/2 -translate-y-1/2 text-gray-400 text-xs" />
          <input
            type="text"
            placeholder="Filter by service, operation, or tag..."
            value={search}
            onChange={(e) => setSearch(e.target.value)}
            className="w-full pl-9 pr-4 py-2 text-sm border border-gray-200 rounded-lg focus:outline-none focus:ring-2 focus:ring-primary-500/20 focus:border-primary-400 transition"
          />
          {search && (
            <button
              onClick={() => setSearch('')}
              className="absolute right-3 top-1/2 -translate-y-1/2 text-gray-400 hover:text-gray-600"
            >
              <i className="fas fa-times text-xs" />
            </button>
          )}
        </div>
        <span className="text-xs text-gray-400">
          {filteredSpans.length} / {flatSpans.length} spans
        </span>
      </div>

      {/* 表头 */}
      <div className="border border-gray-200 rounded-lg overflow-hidden">
        <div className="flex items-center bg-gray-50 border-b border-gray-200 text-xs font-semibold text-gray-600">
          <div className="w-12 px-3 py-2.5 text-center">#</div>
          <div className="w-[160px] px-3 py-2.5">Service</div>
          <div className="flex-1 px-3 py-2.5">Operation</div>
          <SortHeader
            label="Duration"
            field="duration"
            activeField={sortField}
            order={sortOrder}
            onClick={handleSort}
            width="w-[110px]"
          />
          <SortHeader
            label="Start Time"
            field="startTime"
            activeField={sortField}
            order={sortOrder}
            onClick={handleSort}
            width="w-[170px]"
          />
          <div className="w-[80px] px-3 py-2.5 text-center">Status</div>
          <div className="w-[220px] px-3 py-2.5">Tags</div>
        </div>

        {/* 虚拟滚动容器 */}
        <div ref={parentRef} className="overflow-auto max-h-[500px]">
          <div
            style={{ height: `${virtualizer.getTotalSize()}px`, position: 'relative', width: '100%' }}
          >
            {virtualizer.getVirtualItems().map((virtualRow) => {
              const item = filteredSpans[virtualRow.index]!;
              const isSelected = selectedSpanId === item.span.spanID;

              return (
                <div
                  key={item.span.spanID}
                  data-index={virtualRow.index}
                  ref={virtualizer.measureElement}
                  className={`flex items-center border-b border-gray-50 text-sm cursor-pointer transition-colors ${
                    isSelected
                      ? 'bg-primary-50 ring-1 ring-inset ring-primary-200'
                      : item.hasError
                        ? 'bg-red-50/40 hover:bg-red-50'
                        : 'hover:bg-gray-50'
                  }`}
                  style={{
                    position: 'absolute',
                    top: 0,
                    left: 0,
                    width: '100%',
                    height: `${ROW_HEIGHT}px`,
                    transform: `translateY(${virtualRow.start}px)`,
                  }}
                  onClick={() =>
                    setSelectedSpanId((prev) =>
                      prev === item.span.spanID ? null : item.span.spanID,
                    )
                  }
                >
                  {/* # */}
                  <div className="w-12 px-3 py-2 text-center text-xs text-gray-400 tabular-nums">
                    {item.index}
                  </div>

                  {/* Service */}
                  <div className="w-[160px] px-3 py-2 truncate">
                    <span className="flex items-center gap-1.5">
                      <span
                        className="w-2 h-2 rounded-full flex-shrink-0"
                        style={{ backgroundColor: getServiceColor(item.serviceName) }}
                      />
                      <span className="text-gray-700 font-medium truncate">{item.serviceName}</span>
                    </span>
                  </div>

                  {/* Operation */}
                  <div className="flex-1 px-3 py-2 truncate text-gray-600 font-mono text-xs">
                    {item.span.operationName}
                  </div>

                  {/* Duration */}
                  <div className="w-[110px] px-3 py-2 text-right tabular-nums text-gray-700 font-medium">
                    {formatDuration(item.span.duration)}
                  </div>

                  {/* Start Time */}
                  <div className="w-[170px] px-3 py-2 text-right text-xs text-gray-500 tabular-nums">
                    {formatTimestamp(item.span.startTime)}
                  </div>

                  {/* Status */}
                  <div className="w-[80px] px-3 py-2 text-center">
                    {item.hasError ? (
                      <span className="inline-block px-2 py-0.5 text-[10px] font-semibold rounded-full bg-red-100 text-red-700">
                        ERROR
                      </span>
                    ) : (
                      <span className="inline-block px-2 py-0.5 text-[10px] font-semibold rounded-full bg-green-100 text-green-700">
                        OK
                      </span>
                    )}
                  </div>

                  {/* Tags */}
                  <div className="w-[220px] px-3 py-2 group relative">
                    <div className="flex flex-wrap gap-1">
                      {item.span.tags.slice(0, 3).map((tag, i) => (
                        <span
                          key={i}
                          className="inline-block px-1.5 py-0.5 text-[10px] bg-gray-100 text-gray-600 rounded truncate max-w-[100px]"
                          title={`${tag.key}=${tag.value}`}
                        >
                          {tag.key}={String(tag.value)}
                        </span>
                      ))}
                      {item.span.tags.length > 3 && (
                        <span className="text-[10px] text-gray-400">
                          +{item.span.tags.length - 3}
                        </span>
                      )}
                    </div>
                    {/* Tooltip on hover */}
                    {item.span.tags.length > 3 && (
                      <div className="hidden group-hover:block absolute right-0 top-full z-20 bg-gray-900 text-white text-[10px] rounded-lg p-2 shadow-lg max-w-xs whitespace-pre-wrap">
                        {item.span.tags.map((tag, i) => (
                          <div key={i}>
                            {tag.key}={String(tag.value)}
                          </div>
                        ))}
                      </div>
                    )}
                  </div>
                </div>
              );
            })}
          </div>
        </div>
      </div>
    </div>
  );
}

// ============================================================================
// SortHeader
// ============================================================================

function SortHeader({
  label,
  field,
  activeField,
  order,
  onClick,
  width,
}: {
  label: string;
  field: SortField;
  activeField: SortField;
  order: SortOrder;
  onClick: (field: SortField) => void;
  width: string;
}) {
  const isActive = activeField === field;
  return (
    <div
      className={`${width} px-3 py-2.5 text-right cursor-pointer select-none hover:bg-gray-100 transition-colors flex items-center justify-end gap-1`}
      onClick={() => onClick(field)}
    >
      <span>{label}</span>
      <span className={`text-[10px] ${isActive ? 'text-primary-600' : 'text-gray-300'}`}>
        {isActive ? (order === 'asc' ? '▲' : '▼') : '⇅'}
      </span>
    </div>
  );
}
