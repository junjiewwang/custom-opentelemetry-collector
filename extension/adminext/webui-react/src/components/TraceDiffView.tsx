/**
 * TraceDiffView — Trace 比较视图组件
 *
 * 功能：
 * - 双列对比布局：左右各占 50%，分别显示 Trace A / B 的 span 瀑布图
 * - Diff Header：展示两侧 Trace 概要 + 差异摘要
 * - Service 差异高亮：新增(绿)/删除(红)/不变(普通)
 * - Span 匹配算法：按 operationName + serviceName 匹配，显示 duration diff
 * - 统计面板：Duration Diff、Span Count Diff、Per-Service duration comparison
 */

import { useMemo } from 'react';
import type { JaegerTrace, JaegerSpan } from '@/types/trace';
import { buildSpanTree, formatDuration, getServiceColor } from '@/utils/trace';
import type { SpanTreeNode } from '@/types/trace';

// ============================================================================
// Types
// ============================================================================

interface TraceDiffViewProps {
  traceA: JaegerTrace;
  traceB: JaegerTrace;
  onClose: () => void;
}

/** 匹配后的 span 对 */
interface MatchedSpanPair {
  key: string; // serviceName::operationName
  spanA: JaegerSpan | null;
  spanB: JaegerSpan | null;
  serviceNameA: string;
  serviceNameB: string;
  durationDiff: number; // B - A (微秒)
  status: 'matched' | 'added' | 'removed';
}

/** 每个 service 的聚合统计 */
interface ServiceDiffStat {
  serviceName: string;
  durationA: number;
  durationB: number;
  spanCountA: number;
  spanCountB: number;
  status: 'both' | 'only-a' | 'only-b';
}

// ============================================================================
// Helpers
// ============================================================================

/** 计算 trace 总时长 */
function calcTraceDuration(trace: JaegerTrace): number {
  let minStart = Infinity;
  let maxEnd = 0;
  for (const span of trace.spans) {
    minStart = Math.min(minStart, span.startTime);
    maxEnd = Math.max(maxEnd, span.startTime + span.duration);
  }
  return maxEnd - minStart;
}

/** 获取 span 的 service name */
function getSpanService(span: JaegerSpan, trace: JaegerTrace): string {
  return trace.processes[span.processID]?.serviceName ?? 'unknown';
}

/** 生成 span 匹配 key: serviceName::operationName */
function spanMatchKey(span: JaegerSpan, trace: JaegerTrace): string {
  return `${getSpanService(span, trace)}::${span.operationName}`;
}

/** 匹配两个 trace 的 span 列表 */
function matchSpans(traceA: JaegerTrace, traceB: JaegerTrace): MatchedSpanPair[] {
  // 按 key 分组 A 侧 spans（取第一个匹配）
  const mapA = new Map<string, JaegerSpan[]>();
  for (const span of traceA.spans) {
    const key = spanMatchKey(span, traceA);
    const arr = mapA.get(key) ?? [];
    arr.push(span);
    mapA.set(key, arr);
  }

  const mapB = new Map<string, JaegerSpan[]>();
  for (const span of traceB.spans) {
    const key = spanMatchKey(span, traceB);
    const arr = mapB.get(key) ?? [];
    arr.push(span);
    mapB.set(key, arr);
  }

  const results: MatchedSpanPair[] = [];
  const allKeys = new Set([...mapA.keys(), ...mapB.keys()]);

  for (const key of allKeys) {
    const spansA = mapA.get(key) ?? [];
    const spansB = mapB.get(key) ?? [];
    const maxLen = Math.max(spansA.length, spansB.length);

    for (let i = 0; i < maxLen; i++) {
      const sA = spansA[i] ?? null;
      const sB = spansB[i] ?? null;

      const serviceA = sA ? getSpanService(sA, traceA) : '';
      const serviceB = sB ? getSpanService(sB, traceB) : '';

      let status: MatchedSpanPair['status'] = 'matched';
      if (!sA) status = 'added';
      else if (!sB) status = 'removed';

      results.push({
        key: `${key}#${i}`,
        spanA: sA,
        spanB: sB,
        serviceNameA: serviceA,
        serviceNameB: serviceB,
        durationDiff: (sB?.duration ?? 0) - (sA?.duration ?? 0),
        status,
      });
    }
  }

  // 排序：matched → removed → added，同类按 key 排
  const order = { matched: 0, removed: 1, added: 2 };
  results.sort((a, b) => order[a.status] - order[b.status] || a.key.localeCompare(b.key));

  return results;
}

/** 计算 per-service 差异统计 */
function calcServiceDiffStats(traceA: JaegerTrace, traceB: JaegerTrace): ServiceDiffStat[] {
  const statsA = new Map<string, { duration: number; count: number }>();
  for (const span of traceA.spans) {
    const svc = getSpanService(span, traceA);
    const prev = statsA.get(svc) ?? { duration: 0, count: 0 };
    prev.duration += span.duration;
    prev.count += 1;
    statsA.set(svc, prev);
  }

  const statsB = new Map<string, { duration: number; count: number }>();
  for (const span of traceB.spans) {
    const svc = getSpanService(span, traceB);
    const prev = statsB.get(svc) ?? { duration: 0, count: 0 };
    prev.duration += span.duration;
    prev.count += 1;
    statsB.set(svc, prev);
  }

  const allServices = new Set([...statsA.keys(), ...statsB.keys()]);
  const results: ServiceDiffStat[] = [];

  for (const svc of allServices) {
    const a = statsA.get(svc);
    const b = statsB.get(svc);
    results.push({
      serviceName: svc,
      durationA: a?.duration ?? 0,
      durationB: b?.duration ?? 0,
      spanCountA: a?.count ?? 0,
      spanCountB: b?.count ?? 0,
      status: a && b ? 'both' : a ? 'only-a' : 'only-b',
    });
  }

  // 按总 duration 降序
  results.sort((a, b) => Math.max(b.durationA, b.durationB) - Math.max(a.durationA, a.durationB));
  return results;
}

// ============================================================================
// Main Component
// ============================================================================

export default function TraceDiffView({ traceA, traceB, onClose }: TraceDiffViewProps) {
  const durationA = useMemo(() => calcTraceDuration(traceA), [traceA]);
  const durationB = useMemo(() => calcTraceDuration(traceB), [traceB]);
  const durationDiff = durationB - durationA;

  const spanTreeA = useMemo(() => buildSpanTree(traceA), [traceA]);
  const spanTreeB = useMemo(() => buildSpanTree(traceB), [traceB]);

  const matchedPairs = useMemo(() => matchSpans(traceA, traceB), [traceA, traceB]);
  const serviceDiffStats = useMemo(() => calcServiceDiffStats(traceA, traceB), [traceA, traceB]);

  // 差异摘要
  const diffSummary = useMemo(() => {
    let added = 0;
    let removed = 0;
    let changed = 0;
    for (const pair of matchedPairs) {
      if (pair.status === 'added') added++;
      else if (pair.status === 'removed') removed++;
      else if (pair.durationDiff !== 0) changed++;
    }
    return { added, removed, changed };
  }, [matchedPairs]);

  // service 差异集合
  const servicesOnlyA = useMemo(
    () => new Set(serviceDiffStats.filter(s => s.status === 'only-a').map(s => s.serviceName)),
    [serviceDiffStats],
  );
  const servicesOnlyB = useMemo(
    () => new Set(serviceDiffStats.filter(s => s.status === 'only-b').map(s => s.serviceName)),
    [serviceDiffStats],
  );

  return (
    <div className="bg-white rounded-xl shadow-sm border border-gray-200 overflow-hidden">
      {/* ================================================================ */}
      {/* Diff Header */}
      {/* ================================================================ */}
      <div className="px-6 py-4 border-b border-gray-200 bg-gray-50">
        <div className="flex items-center justify-between mb-3">
          <h3 className="text-lg font-bold text-gray-800 flex items-center gap-2">
            <i className="fas fa-columns text-primary-600" />
            Trace Comparison
          </h3>
          <button
            onClick={onClose}
            className="p-2 text-gray-400 hover:text-gray-600 hover:bg-gray-200 rounded-lg transition"
          >
            <i className="fas fa-times" />
          </button>
        </div>

        <div className="grid grid-cols-3 gap-4 text-sm">
          {/* Trace A info */}
          <div className="bg-blue-50 rounded-lg px-4 py-3">
            <div className="text-xs text-blue-500 font-semibold mb-1">Trace A</div>
            <div className="font-mono text-blue-700 text-xs truncate" title={traceA.traceID}>
              {traceA.traceID.substring(0, 8)}…
            </div>
            <div className="flex items-center gap-3 mt-1 text-xs text-blue-600">
              <span>{formatDuration(durationA)}</span>
              <span>{traceA.spans.length} spans</span>
            </div>
          </div>

          {/* Diff Summary */}
          <div className="flex flex-col items-center justify-center text-xs">
            <div className="flex items-center gap-2 flex-wrap justify-center">
              {diffSummary.added > 0 && (
                <span className="px-2 py-0.5 bg-green-100 text-green-700 rounded-full">
                  +{diffSummary.added} span{diffSummary.added > 1 ? 's' : ''}
                </span>
              )}
              {diffSummary.removed > 0 && (
                <span className="px-2 py-0.5 bg-red-100 text-red-700 rounded-full">
                  -{diffSummary.removed} span{diffSummary.removed > 1 ? 's' : ''}
                </span>
              )}
              {diffSummary.changed > 0 && (
                <span className="px-2 py-0.5 bg-yellow-100 text-yellow-700 rounded-full">
                  {diffSummary.changed} changed
                </span>
              )}
              {diffSummary.added === 0 && diffSummary.removed === 0 && diffSummary.changed === 0 && (
                <span className="px-2 py-0.5 bg-gray-100 text-gray-500 rounded-full">
                  No differences
                </span>
              )}
            </div>
            <div className={`mt-1 font-semibold ${durationDiff < 0 ? 'text-green-600' : durationDiff > 0 ? 'text-red-600' : 'text-gray-500'}`}>
              {durationDiff < 0 ? '' : durationDiff > 0 ? '+' : '±'}{formatDuration(Math.abs(durationDiff))}
            </div>
          </div>

          {/* Trace B info */}
          <div className="bg-purple-50 rounded-lg px-4 py-3">
            <div className="text-xs text-purple-500 font-semibold mb-1">Trace B</div>
            <div className="font-mono text-purple-700 text-xs truncate" title={traceB.traceID}>
              {traceB.traceID.substring(0, 8)}…
            </div>
            <div className="flex items-center gap-3 mt-1 text-xs text-purple-600">
              <span>{formatDuration(durationB)}</span>
              <span>{traceB.spans.length} spans</span>
            </div>
          </div>
        </div>

        {/* Service Legend with diff markers */}
        <div className="flex items-center gap-3 mt-3 flex-wrap">
          {serviceDiffStats.map(svc => {
            const isOnlyA = svc.status === 'only-a';
            const isOnlyB = svc.status === 'only-b';
            return (
              <span
                key={svc.serviceName}
                className={`flex items-center gap-1 text-xs px-2 py-0.5 rounded-full ${
                  isOnlyA
                    ? 'bg-red-100 text-red-700'
                    : isOnlyB
                      ? 'bg-green-100 text-green-700'
                      : 'text-gray-600'
                }`}
              >
                <span
                  className="w-2.5 h-2.5 rounded-sm flex-shrink-0"
                  style={{ backgroundColor: getServiceColor(svc.serviceName) }}
                />
                {svc.serviceName}
                {isOnlyA && <span className="ml-1 font-semibold">(-)</span>}
                {isOnlyB && <span className="ml-1 font-semibold">(+)</span>}
              </span>
            );
          })}
        </div>
      </div>

      {/* ================================================================ */}
      {/* Dual-Column Span Waterfall */}
      {/* ================================================================ */}
      <div className="grid grid-cols-2 divide-x divide-gray-200">
        {/* Trace A Column */}
        <div>
          <div className="px-4 py-2 bg-blue-50/50 border-b border-gray-100 text-xs font-semibold text-blue-600">
            Trace A — {traceA.traceID.substring(0, 8)}
          </div>
          <div className="max-h-[400px] overflow-y-auto">
            {spanTreeA.length > 0 ? (
              spanTreeA.map(node => (
                <DiffSpanRow
                  key={node.span.spanID}
                  node={node}
                  traceDuration={durationA}
                  traceStartTime={traceA.spans.reduce((min, s) => Math.min(min, s.startTime), Infinity)}
                  side="a"
                  servicesOnlyThis={servicesOnlyA}
                  servicesOnlyOther={servicesOnlyB}
                  matchedPairs={matchedPairs}
                  trace={traceA}
                />
              ))
            ) : (
              <div className="p-8 text-center text-gray-400 text-sm">No spans</div>
            )}
          </div>
        </div>

        {/* Trace B Column */}
        <div>
          <div className="px-4 py-2 bg-purple-50/50 border-b border-gray-100 text-xs font-semibold text-purple-600">
            Trace B — {traceB.traceID.substring(0, 8)}
          </div>
          <div className="max-h-[400px] overflow-y-auto">
            {spanTreeB.length > 0 ? (
              spanTreeB.map(node => (
                <DiffSpanRow
                  key={node.span.spanID}
                  node={node}
                  traceDuration={durationB}
                  traceStartTime={traceB.spans.reduce((min, s) => Math.min(min, s.startTime), Infinity)}
                  side="b"
                  servicesOnlyThis={servicesOnlyB}
                  servicesOnlyOther={servicesOnlyA}
                  matchedPairs={matchedPairs}
                  trace={traceB}
                />
              ))
            ) : (
              <div className="p-8 text-center text-gray-400 text-sm">No spans</div>
            )}
          </div>
        </div>
      </div>

      {/* ================================================================ */}
      {/* Matched Span Pairs Table */}
      {/* ================================================================ */}
      <div className="border-t border-gray-200">
        <div className="px-6 py-3 bg-gray-50 border-b border-gray-100">
          <h4 className="text-sm font-semibold text-gray-700 flex items-center gap-2">
            <i className="fas fa-exchange-alt text-gray-400" />
            Span Matching ({matchedPairs.length} pairs)
          </h4>
        </div>
        <div className="max-h-[300px] overflow-y-auto">
          <table className="w-full text-xs">
            <thead className="bg-gray-50 sticky top-0">
              <tr>
                <th className="px-4 py-2 text-left text-gray-500 font-medium">Service :: Operation</th>
                <th className="px-4 py-2 text-right text-gray-500 font-medium">Duration A</th>
                <th className="px-4 py-2 text-right text-gray-500 font-medium">Duration B</th>
                <th className="px-4 py-2 text-right text-gray-500 font-medium">Diff</th>
                <th className="px-4 py-2 text-center text-gray-500 font-medium">Status</th>
              </tr>
            </thead>
            <tbody>
              {matchedPairs.map(pair => {
                const svc = pair.serviceNameA || pair.serviceNameB;
                const op = pair.spanA?.operationName ?? pair.spanB?.operationName ?? '';
                return (
                  <tr
                    key={pair.key}
                    className={`border-b border-gray-50 ${
                      pair.status === 'added'
                        ? 'bg-green-50/50'
                        : pair.status === 'removed'
                          ? 'bg-red-50/50'
                          : pair.durationDiff !== 0
                            ? 'bg-yellow-50/30'
                            : ''
                    }`}
                  >
                    <td className="px-4 py-2 text-gray-700">
                      <span className="font-medium">{svc}</span>
                      <span className="text-gray-400">::</span>
                      <span className="truncate">{op}</span>
                    </td>
                    <td className="px-4 py-2 text-right font-mono text-gray-600">
                      {pair.spanA ? formatDuration(pair.spanA.duration) : '—'}
                    </td>
                    <td className="px-4 py-2 text-right font-mono text-gray-600">
                      {pair.spanB ? formatDuration(pair.spanB.duration) : '—'}
                    </td>
                    <td className={`px-4 py-2 text-right font-mono font-semibold ${
                      pair.status !== 'matched'
                        ? 'text-gray-400'
                        : pair.durationDiff < 0
                          ? 'text-green-600'
                          : pair.durationDiff > 0
                            ? 'text-red-600'
                            : 'text-gray-400'
                    }`}>
                      {pair.status === 'matched'
                        ? `${pair.durationDiff >= 0 ? '+' : ''}${formatDuration(Math.abs(pair.durationDiff))}`
                        : '—'}
                    </td>
                    <td className="px-4 py-2 text-center">
                      {pair.status === 'added' && (
                        <span className="px-2 py-0.5 bg-green-100 text-green-700 rounded-full text-[10px] font-semibold">
                          NEW
                        </span>
                      )}
                      {pair.status === 'removed' && (
                        <span className="px-2 py-0.5 bg-red-100 text-red-700 rounded-full text-[10px] font-semibold">
                          DEL
                        </span>
                      )}
                      {pair.status === 'matched' && pair.durationDiff !== 0 && (
                        <span className="px-2 py-0.5 bg-yellow-100 text-yellow-700 rounded-full text-[10px] font-semibold">
                          CHG
                        </span>
                      )}
                      {pair.status === 'matched' && pair.durationDiff === 0 && (
                        <span className="px-2 py-0.5 bg-gray-100 text-gray-500 rounded-full text-[10px]">
                          OK
                        </span>
                      )}
                    </td>
                  </tr>
                );
              })}
            </tbody>
          </table>
        </div>
      </div>

      {/* ================================================================ */}
      {/* Statistics Panel */}
      {/* ================================================================ */}
      <div className="border-t border-gray-200 px-6 py-4 bg-gray-50">
        <h4 className="text-sm font-semibold text-gray-700 mb-3 flex items-center gap-2">
          <i className="fas fa-chart-bar text-gray-400" />
          Statistics
        </h4>

        <div className="grid grid-cols-1 md:grid-cols-3 gap-4 mb-4">
          {/* Duration Diff */}
          <div className="bg-white rounded-lg border border-gray-200 p-4">
            <div className="text-xs text-gray-500 mb-1">Total Duration Diff</div>
            <div className={`text-xl font-bold ${
              durationDiff < 0 ? 'text-green-600' : durationDiff > 0 ? 'text-red-600' : 'text-gray-500'
            }`}>
              {durationDiff < 0 ? '' : durationDiff > 0 ? '+' : '±'}
              {formatDuration(Math.abs(durationDiff))}
            </div>
            <div className="text-xs text-gray-400 mt-1">
              {durationDiff < 0 ? 'B is faster' : durationDiff > 0 ? 'B is slower' : 'Same duration'}
            </div>
          </div>

          {/* Span Count Diff */}
          <div className="bg-white rounded-lg border border-gray-200 p-4">
            <div className="text-xs text-gray-500 mb-1">Span Count Diff</div>
            <div className="text-xl font-bold text-gray-800">
              {traceA.spans.length} → {traceB.spans.length}
            </div>
            <div className="text-xs text-gray-400 mt-1">
              {traceB.spans.length - traceA.spans.length > 0
                ? `+${traceB.spans.length - traceA.spans.length}`
                : traceB.spans.length - traceA.spans.length < 0
                  ? `${traceB.spans.length - traceA.spans.length}`
                  : '±0'}
              {' '}spans
            </div>
          </div>

          {/* Service Count Diff */}
          <div className="bg-white rounded-lg border border-gray-200 p-4">
            <div className="text-xs text-gray-500 mb-1">Service Count Diff</div>
            <div className="text-xl font-bold text-gray-800">
              {new Set(traceA.spans.map(s => getSpanService(s, traceA))).size}
              {' → '}
              {new Set(traceB.spans.map(s => getSpanService(s, traceB))).size}
            </div>
            <div className="text-xs text-gray-400 mt-1">
              {servicesOnlyB.size > 0 && <span className="text-green-600">+{servicesOnlyB.size} new </span>}
              {servicesOnlyA.size > 0 && <span className="text-red-600">-{servicesOnlyA.size} removed</span>}
              {servicesOnlyA.size === 0 && servicesOnlyB.size === 0 && <span>Same services</span>}
            </div>
          </div>
        </div>

        {/* Per-Service Duration Comparison Bar Chart */}
        <div className="bg-white rounded-lg border border-gray-200 p-4">
          <div className="text-xs text-gray-500 font-semibold mb-3">Per-Service Duration Comparison</div>
          <div className="space-y-2">
            {serviceDiffStats.map(svc => {
              const maxDuration = Math.max(
                ...serviceDiffStats.map(s => Math.max(s.durationA, s.durationB)),
                1,
              );
              const widthA = (svc.durationA / maxDuration) * 100;
              const widthB = (svc.durationB / maxDuration) * 100;

              return (
                <div key={svc.serviceName} className="flex items-center gap-3">
                  <div className="w-28 text-xs text-gray-600 truncate flex-shrink-0 flex items-center gap-1">
                    <span
                      className="w-2 h-2 rounded-sm flex-shrink-0"
                      style={{ backgroundColor: getServiceColor(svc.serviceName) }}
                    />
                    {svc.serviceName}
                  </div>
                  <div className="flex-1 space-y-0.5">
                    {/* A bar */}
                    <div className="flex items-center gap-1">
                      <span className="text-[10px] text-blue-500 w-4 flex-shrink-0">A</span>
                      <div className="flex-1 bg-gray-100 rounded-full h-2.5 overflow-hidden">
                        <div
                          className="h-full bg-blue-400 rounded-full transition-all"
                          style={{ width: `${Math.max(widthA, 0.5)}%` }}
                        />
                      </div>
                      <span className="text-[10px] text-gray-500 w-16 text-right flex-shrink-0">
                        {formatDuration(svc.durationA)}
                      </span>
                    </div>
                    {/* B bar */}
                    <div className="flex items-center gap-1">
                      <span className="text-[10px] text-purple-500 w-4 flex-shrink-0">B</span>
                      <div className="flex-1 bg-gray-100 rounded-full h-2.5 overflow-hidden">
                        <div
                          className="h-full bg-purple-400 rounded-full transition-all"
                          style={{ width: `${Math.max(widthB, 0.5)}%` }}
                        />
                      </div>
                      <span className="text-[10px] text-gray-500 w-16 text-right flex-shrink-0">
                        {formatDuration(svc.durationB)}
                      </span>
                    </div>
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
// DiffSpanRow — 带 diff 标记的 span 行（递归）
// ============================================================================

interface DiffSpanRowProps {
  node: SpanTreeNode;
  traceDuration: number;
  traceStartTime: number;
  side: 'a' | 'b';
  servicesOnlyThis: Set<string>;
  servicesOnlyOther: Set<string>;
  matchedPairs: MatchedSpanPair[];
  trace: JaegerTrace;
}

function DiffSpanRow({
  node,
  traceDuration,
  traceStartTime,
  side,
  servicesOnlyThis,
  servicesOnlyOther,
  matchedPairs,
  trace,
}: DiffSpanRowProps) {
  const { span, process, children, depth } = node;
  const serviceColor = getServiceColor(process.serviceName);
  const isOnlyThis = servicesOnlyThis.has(process.serviceName);

  // 找到此 span 的匹配对
  const pair = matchedPairs.find(p => {
    if (side === 'a') return p.spanA?.spanID === span.spanID;
    return p.spanB?.spanID === span.spanID;
  });

  // 确定 diff 背景
  let bgClass = '';
  if (isOnlyThis) {
    bgClass = side === 'b' ? 'bg-green-50' : 'bg-red-50';
  } else if (pair?.status === 'added') {
    bgClass = 'bg-green-50';
  } else if (pair?.status === 'removed') {
    bgClass = 'bg-red-50';
  }

  // 时间轴位置
  const offsetPercent = traceDuration > 0
    ? ((span.startTime - traceStartTime) / traceDuration) * 100
    : 0;
  const widthPercent = traceDuration > 0
    ? Math.max((span.duration / traceDuration) * 100, 0.5)
    : 0;

  return (
    <>
      <div className={`flex items-center border-b border-gray-50 hover:bg-gray-50/50 text-xs ${bgClass}`}>
        {/* Service :: Operation */}
        <div
          className="w-2/5 flex-shrink-0 px-3 py-1.5 flex items-center gap-1 truncate"
          style={{ paddingLeft: `${12 + depth * 14}px` }}
        >
          <span
            className="w-2 h-2 rounded-full flex-shrink-0"
            style={{ backgroundColor: serviceColor }}
          />
          <span className="truncate text-gray-600" title={`${process.serviceName}::${span.operationName}`}>
            <span className="font-medium">{process.serviceName}</span>
            <span className="text-gray-300">::</span>
            {span.operationName}
          </span>
        </div>

        {/* Timeline Bar */}
        <div className="flex-1 px-2 py-1.5 relative h-6">
          <div
            className="absolute top-1/2 -translate-y-1/2 rounded-sm h-4 min-w-[2px]"
            style={{
              left: `${offsetPercent}%`,
              width: `${widthPercent}%`,
              backgroundColor: serviceColor,
              opacity: 0.8,
            }}
          />
        </div>

        {/* Duration + Diff */}
        <div className="w-20 flex-shrink-0 px-2 py-1.5 text-right text-gray-600">
          {formatDuration(span.duration)}
          {pair?.status === 'matched' && pair.durationDiff !== 0 && (
            <div className={`text-[9px] ${pair.durationDiff < 0 ? 'text-green-600' : 'text-red-500'}`}>
              {side === 'b'
                ? `${pair.durationDiff >= 0 ? '+' : ''}${formatDuration(Math.abs(pair.durationDiff))}`
                : ''}
            </div>
          )}
        </div>
      </div>

      {/* Children */}
      {children.map(child => (
        <DiffSpanRow
          key={child.span.spanID}
          node={child}
          traceDuration={traceDuration}
          traceStartTime={traceStartTime}
          side={side}
          servicesOnlyThis={servicesOnlyThis}
          servicesOnlyOther={servicesOnlyOther}
          matchedPairs={matchedPairs}
          trace={trace}
        />
      ))}
    </>
  );
}
