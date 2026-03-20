/**
 * TraceStatisticsView — Trace 统计视图
 *
 * 功能：
 * - 按 Service 维度统计 Span Count / Avg / Min / Max Duration / Error Count / Error Rate
 * - 按 Operation 维度统计 Count / Avg / Min / Max Duration / Error Count
 * - Tab 切换两种维度
 * - 表头 sticky，内容区可滚动
 * - Error Rate > 0 的行高亮红色背景
 */

import { useState, useMemo } from 'react';
import type { JaegerTrace, JaegerSpan } from '@/types/trace';
import { formatDuration, getServiceColor } from '@/utils/trace';

interface TraceViewProps {
  trace: JaegerTrace;
}

// ============================================================================
// 统计数据类型
// ============================================================================

interface ServiceStats {
  service: string;
  spanCount: number;
  totalDuration: number;
  avgDuration: number;
  minDuration: number;
  maxDuration: number;
  errorCount: number;
  errorRate: number;
}

interface OperationStats {
  service: string;
  operation: string;
  count: number;
  totalDuration: number;
  avgDuration: number;
  minDuration: number;
  maxDuration: number;
  errorCount: number;
}

type TabType = 'service' | 'operation';

// ============================================================================
// 工具函数
// ============================================================================

/** 判断 span 是否有错误 */
function isSpanError(span: JaegerSpan): boolean {
  return span.tags.some(
    (t) =>
      (t.key === 'error' && t.value === true) ||
      (t.key === 'otel.status_code' && t.value === 'ERROR'),
  );
}

// ============================================================================
// 主组件
// ============================================================================

export default function TraceStatisticsView({ trace }: TraceViewProps) {
  const [activeTab, setActiveTab] = useState<TabType>('service');

  // 按 Service 维度统计
  const serviceStats = useMemo<ServiceStats[]>(() => {
    const map = new Map<
      string,
      { count: number; total: number; min: number; max: number; errors: number }
    >();

    for (const span of trace.spans) {
      const proc = trace.processes[span.processID];
      if (!proc) continue;
      const svc = proc.serviceName;

      const entry = map.get(svc) ?? {
        count: 0,
        total: 0,
        min: Infinity,
        max: 0,
        errors: 0,
      };

      entry.count++;
      entry.total += span.duration;
      entry.min = Math.min(entry.min, span.duration);
      entry.max = Math.max(entry.max, span.duration);
      if (isSpanError(span)) entry.errors++;

      map.set(svc, entry);
    }

    return Array.from(map.entries())
      .map(([service, s]) => ({
        service,
        spanCount: s.count,
        totalDuration: s.total,
        avgDuration: s.count > 0 ? s.total / s.count : 0,
        minDuration: s.min === Infinity ? 0 : s.min,
        maxDuration: s.max,
        errorCount: s.errors,
        errorRate: s.count > 0 ? s.errors / s.count : 0,
      }))
      .sort((a, b) => b.avgDuration - a.avgDuration);
  }, [trace]);

  // 按 Operation 维度统计
  const operationStats = useMemo<OperationStats[]>(() => {
    const map = new Map<
      string,
      { service: string; operation: string; count: number; total: number; min: number; max: number; errors: number }
    >();

    for (const span of trace.spans) {
      const proc = trace.processes[span.processID];
      if (!proc) continue;
      const key = `${proc.serviceName}::${span.operationName}`;

      const entry = map.get(key) ?? {
        service: proc.serviceName,
        operation: span.operationName,
        count: 0,
        total: 0,
        min: Infinity,
        max: 0,
        errors: 0,
      };

      entry.count++;
      entry.total += span.duration;
      entry.min = Math.min(entry.min, span.duration);
      entry.max = Math.max(entry.max, span.duration);
      if (isSpanError(span)) entry.errors++;

      map.set(key, entry);
    }

    return Array.from(map.values())
      .map((s) => ({
        service: s.service,
        operation: s.operation,
        count: s.count,
        totalDuration: s.total,
        avgDuration: s.count > 0 ? s.total / s.count : 0,
        minDuration: s.min === Infinity ? 0 : s.min,
        maxDuration: s.max,
        errorCount: s.errors,
      }))
      .sort((a, b) => b.count - a.count);
  }, [trace]);

  // 空状态
  if (!trace.spans || trace.spans.length === 0) {
    return (
      <div className="bg-white rounded-xl shadow-sm border border-gray-200 p-4">
        <div className="flex flex-col items-center justify-center py-16 text-gray-400">
          <i className="fas fa-chart-bar text-3xl mb-3" />
          <p className="text-sm">No data</p>
        </div>
      </div>
    );
  }

  return (
    <div className="bg-white rounded-xl shadow-sm border border-gray-200 p-4">
      {/* Tab 切换 */}
      <div className="flex items-center gap-1 mb-4 border-b border-gray-100 pb-3">
        <TabButton
          active={activeTab === 'service'}
          label="By Service"
          icon="fas fa-server"
          onClick={() => setActiveTab('service')}
        />
        <TabButton
          active={activeTab === 'operation'}
          label="By Operation"
          icon="fas fa-cogs"
          onClick={() => setActiveTab('operation')}
        />
      </div>

      {/* 表格内容 */}
      {activeTab === 'service' ? (
        <ServiceTable data={serviceStats} />
      ) : (
        <OperationTable data={operationStats} />
      )}
    </div>
  );
}

// ============================================================================
// TabButton
// ============================================================================

function TabButton({
  active,
  label,
  icon,
  onClick,
}: {
  active: boolean;
  label: string;
  icon: string;
  onClick: () => void;
}) {
  return (
    <button
      onClick={onClick}
      className={`flex items-center gap-1.5 px-3 py-1.5 text-sm rounded-lg transition-colors ${
        active
          ? 'bg-primary-50 text-primary-700 font-medium'
          : 'text-gray-500 hover:text-gray-700 hover:bg-gray-50'
      }`}
    >
      <i className={icon} />
      {label}
    </button>
  );
}

// ============================================================================
// ServiceTable
// ============================================================================

function ServiceTable({ data }: { data: ServiceStats[] }) {
  return (
    <div className="overflow-auto max-h-[500px] rounded-lg border border-gray-200">
      <table className="w-full text-sm">
        <thead className="sticky top-0 z-10 bg-gray-50">
          <tr className="border-b border-gray-200">
            <th className="text-left px-4 py-2.5 font-semibold text-gray-600">Service</th>
            <th className="text-right px-4 py-2.5 font-semibold text-gray-600">Span Count</th>
            <th className="text-right px-4 py-2.5 font-semibold text-gray-600">Avg Duration</th>
            <th className="text-right px-4 py-2.5 font-semibold text-gray-600">Min Duration</th>
            <th className="text-right px-4 py-2.5 font-semibold text-gray-600">Max Duration</th>
            <th className="text-right px-4 py-2.5 font-semibold text-gray-600">Error Count</th>
            <th className="text-right px-4 py-2.5 font-semibold text-gray-600">Error Rate</th>
          </tr>
        </thead>
        <tbody>
          {data.map((row) => (
            <tr
              key={row.service}
              className={`border-b border-gray-50 transition-colors hover:bg-gray-50 ${
                row.errorRate > 0 ? 'bg-red-50/60 hover:bg-red-50' : ''
              }`}
            >
              <td className="px-4 py-2.5">
                <span className="flex items-center gap-2">
                  <span
                    className="w-2.5 h-2.5 rounded-full flex-shrink-0"
                    style={{ backgroundColor: getServiceColor(row.service) }}
                  />
                  <span className="font-medium text-gray-700">{row.service}</span>
                </span>
              </td>
              <td className="text-right px-4 py-2.5 text-gray-600 tabular-nums">{row.spanCount}</td>
              <td className="text-right px-4 py-2.5 text-gray-600 tabular-nums font-medium">
                {formatDuration(Math.round(row.avgDuration))}
              </td>
              <td className="text-right px-4 py-2.5 text-gray-500 tabular-nums">
                {formatDuration(row.minDuration)}
              </td>
              <td className="text-right px-4 py-2.5 text-gray-500 tabular-nums">
                {formatDuration(row.maxDuration)}
              </td>
              <td className="text-right px-4 py-2.5 tabular-nums">
                <span className={row.errorCount > 0 ? 'text-red-600 font-semibold' : 'text-gray-400'}>
                  {row.errorCount}
                </span>
              </td>
              <td className="text-right px-4 py-2.5 tabular-nums">
                <span className={row.errorRate > 0 ? 'text-red-600 font-semibold' : 'text-gray-400'}>
                  {(row.errorRate * 100).toFixed(1)}%
                </span>
              </td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
}

// ============================================================================
// OperationTable
// ============================================================================

function OperationTable({ data }: { data: OperationStats[] }) {
  return (
    <div className="overflow-auto max-h-[500px] rounded-lg border border-gray-200">
      <table className="w-full text-sm">
        <thead className="sticky top-0 z-10 bg-gray-50">
          <tr className="border-b border-gray-200">
            <th className="text-left px-4 py-2.5 font-semibold text-gray-600">Service</th>
            <th className="text-left px-4 py-2.5 font-semibold text-gray-600">Operation</th>
            <th className="text-right px-4 py-2.5 font-semibold text-gray-600">Count</th>
            <th className="text-right px-4 py-2.5 font-semibold text-gray-600">Avg Duration</th>
            <th className="text-right px-4 py-2.5 font-semibold text-gray-600">Min Duration</th>
            <th className="text-right px-4 py-2.5 font-semibold text-gray-600">Max Duration</th>
            <th className="text-right px-4 py-2.5 font-semibold text-gray-600">Error Count</th>
          </tr>
        </thead>
        <tbody>
          {data.map((row) => (
            <tr
              key={`${row.service}::${row.operation}`}
              className={`border-b border-gray-50 transition-colors hover:bg-gray-50 ${
                row.errorCount > 0 ? 'bg-red-50/60 hover:bg-red-50' : ''
              }`}
            >
              <td className="px-4 py-2.5">
                <span className="flex items-center gap-2">
                  <span
                    className="w-2.5 h-2.5 rounded-full flex-shrink-0"
                    style={{ backgroundColor: getServiceColor(row.service) }}
                  />
                  <span className="font-medium text-gray-700">{row.service}</span>
                </span>
              </td>
              <td className="px-4 py-2.5 text-gray-600 font-mono text-xs">{row.operation}</td>
              <td className="text-right px-4 py-2.5 text-gray-600 tabular-nums font-medium">{row.count}</td>
              <td className="text-right px-4 py-2.5 text-gray-600 tabular-nums">
                {formatDuration(Math.round(row.avgDuration))}
              </td>
              <td className="text-right px-4 py-2.5 text-gray-500 tabular-nums">
                {formatDuration(row.minDuration)}
              </td>
              <td className="text-right px-4 py-2.5 text-gray-500 tabular-nums">
                {formatDuration(row.maxDuration)}
              </td>
              <td className="text-right px-4 py-2.5 tabular-nums">
                <span className={row.errorCount > 0 ? 'text-red-600 font-semibold' : 'text-gray-400'}>
                  {row.errorCount}
                </span>
              </td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
}
