/**
 * Logs 页面 - 日志查询与可视化
 *
 * 功能：
 * - 搜索面板（全文搜索、Service、Severity、时间范围、TraceID）
 * - 日志列表（Timestamp、Severity、Service、Body）
 * - 日志详情展开（Attributes、Resource、Context）
 * - 统计面板（Severity 分布、时间直方图）
 */

import { useState, useEffect, useCallback, useRef } from 'react';
import { useSearchParams } from 'react-router-dom';
import { apiClient } from '@/api/client';
import EmptyState from '@/components/EmptyState';
import type {
  LogRecord,
  LogSearchResult,
  LogSearchParams,
  LogStats,
} from '@/types/log';
import { SEVERITY_COLORS } from '@/types/log';
import { formatTimestamp, anyValueToDisplay } from '@/utils/trace';

// ============================================================================
// 常量
// ============================================================================

const SEVERITY_OPTIONS = ['FATAL', 'ERROR', 'WARN', 'INFO', 'DEBUG', 'TRACE'];

const TIME_RANGE_PRESETS = [
  { label: '最近 15 分钟', value: '15m', ms: 15 * 60 * 1000 },
  { label: '最近 1 小时', value: '1h', ms: 60 * 60 * 1000 },
  { label: '最近 6 小时', value: '6h', ms: 6 * 60 * 60 * 1000 },
  { label: '最近 24 小时', value: '24h', ms: 24 * 60 * 60 * 1000 },
  { label: '最近 7 天', value: '7d', ms: 7 * 24 * 60 * 60 * 1000 },
];

const PAGE_SIZE = 50;

// ============================================================================
// Component
// ============================================================================

export default function LogsPage() {
  const [searchParams, setSearchParams] = useSearchParams();

  // ========================================================================
  // State
  // ========================================================================

  // Search form
  const [queryInput, setQueryInput] = useState(searchParams.get('query') || '');
  const [selectedService, setSelectedService] = useState(searchParams.get('service') || '');
  const [selectedSeverities, setSelectedSeverities] = useState<string[]>(
    searchParams.get('severity')?.split(',').filter(Boolean) || [],
  );
  const [traceIdInput, setTraceIdInput] = useState(searchParams.get('traceId') || '');
  const [timeRange, setTimeRange] = useState(searchParams.get('range') || '1h');

  // Results
  const [result, setResult] = useState<LogSearchResult | null>(null);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState('');
  const [currentPage, setCurrentPage] = useState(0);

  // Detail
  const [expandedLogId, setExpandedLogId] = useState<string | null>(null);

  // Stats
  const [stats, setStats] = useState<LogStats | null>(null);

  // Available services (from trace services endpoint or log fields)
  const [services, setServices] = useState<string[]>([]);

  // Availability check
  const [logsAvailable, setLogsAvailable] = useState<boolean | null>(null);

  const searchTriggeredRef = useRef(false);

  // ========================================================================
  // Load services on mount
  // ========================================================================

  useEffect(() => {
    // 尝试获取 log services（复用 trace services 接口）
    apiClient.getTraceServices()
      .then(res => {
        if (res.data) {
          setServices(res.data.map(s => s.name));
        }
        setLogsAvailable(true);
      })
      .catch(() => {
        // Fallback: try log fields
        setLogsAvailable(true);
      });
  }, []);

  // ========================================================================
  // Search
  // ========================================================================

  const doSearch = useCallback(async (page: number = 0) => {
    setLoading(true);
    setError('');

    const preset = TIME_RANGE_PRESETS.find(p => p.value === timeRange);
    const now = Date.now();
    const start = preset ? now - preset.ms : now - 3600000;

    const params: LogSearchParams = {
      query: queryInput || undefined,
      service: selectedService || undefined,
      severity: selectedSeverities.length > 0 ? selectedSeverities.join(',') : undefined,
      traceId: traceIdInput || undefined,
      start,
      end: now,
      limit: PAGE_SIZE,
      offset: page * PAGE_SIZE,
    };

    try {
      const [searchResult, statsResult] = await Promise.all([
        apiClient.searchLogs(params),
        apiClient.getLogStats({
          service: selectedService || undefined,
          start,
          end: now,
          groupBy: 'severity',
        }),
      ]);
      setResult(searchResult);
      setStats(statsResult);
      setCurrentPage(page);
      setLogsAvailable(true);

      // Update URL params
      const newParams = new URLSearchParams();
      if (queryInput) newParams.set('query', queryInput);
      if (selectedService) newParams.set('service', selectedService);
      if (selectedSeverities.length) newParams.set('severity', selectedSeverities.join(','));
      if (traceIdInput) newParams.set('traceId', traceIdInput);
      newParams.set('range', timeRange);
      setSearchParams(newParams, { replace: true });
    } catch (err: unknown) {
      const e = err as { status?: number; message?: string };
      if (e.status === 503) {
        setLogsAvailable(false);
      } else {
        setError(e.message || 'Failed to search logs');
      }
    } finally {
      setLoading(false);
    }
  }, [queryInput, selectedService, selectedSeverities, traceIdInput, timeRange, setSearchParams]);

  // Auto-search on initial load if URL has params
  useEffect(() => {
    if (!searchTriggeredRef.current && searchParams.has('service')) {
      searchTriggeredRef.current = true;
      doSearch();
    }
  }, [searchParams, doSearch]);

  // ========================================================================
  // Severity toggle
  // ========================================================================

  const toggleSeverity = (severity: string) => {
    setSelectedSeverities(prev =>
      prev.includes(severity)
        ? prev.filter(s => s !== severity)
        : [...prev, severity],
    );
  };

  // ========================================================================
  // Render: Not Available
  // ========================================================================

  if (logsAvailable === false) {
    return (
      <div className="p-6">
        <div className="bg-yellow-50 border border-yellow-200 rounded-lg p-4 text-yellow-800">
          <h3 className="font-semibold text-lg mb-2">日志查询不可用</h3>
          <p className="text-sm">
            日志查询需要配置 <code className="bg-yellow-100 px-1 rounded">observability.storage_extension</code>。
            请确保 observabilitystorageext 扩展已启用并正确配置。
          </p>
        </div>
      </div>
    );
  }

  // ========================================================================
  // Render
  // ========================================================================

  return (
    <div className="p-6 space-y-4">
      {/* Page Header */}
      <div className="flex items-center justify-between">
        <h1 className="text-2xl font-bold text-gray-900 dark:text-gray-100">日志查询</h1>
        {stats && (
          <div className="text-sm text-gray-500">
            共 {stats.totalCount.toLocaleString()} 条日志
          </div>
        )}
      </div>

      {/* Search Panel */}
      <div className="bg-white dark:bg-gray-800 rounded-lg shadow-sm border border-gray-200 dark:border-gray-700 p-4 space-y-3">
        {/* Row 1: Query + Service */}
        <div className="grid grid-cols-1 md:grid-cols-3 gap-3">
          <div className="md:col-span-2">
            <label className="block text-xs font-medium text-gray-600 dark:text-gray-400 mb-1">
              全文搜索
            </label>
            <input
              type="text"
              value={queryInput}
              onChange={e => setQueryInput(e.target.value)}
              onKeyDown={e => e.key === 'Enter' && doSearch()}
              placeholder="输入关键词搜索日志内容..."
              className="w-full px-3 py-2 border border-gray-300 dark:border-gray-600 rounded-md
                bg-white dark:bg-gray-700 text-sm focus:ring-2 focus:ring-blue-500 focus:border-blue-500"
            />
          </div>
          <div>
            <label className="block text-xs font-medium text-gray-600 dark:text-gray-400 mb-1">
              Service
            </label>
            <select
              value={selectedService}
              onChange={e => setSelectedService(e.target.value)}
              className="w-full px-3 py-2 border border-gray-300 dark:border-gray-600 rounded-md
                bg-white dark:bg-gray-700 text-sm"
            >
              <option value="">全部 Service</option>
              {services.map(s => (
                <option key={s} value={s}>{s}</option>
              ))}
            </select>
          </div>
        </div>

        {/* Row 2: Severity + TraceID + TimeRange */}
        <div className="grid grid-cols-1 md:grid-cols-3 gap-3">
          <div>
            <label className="block text-xs font-medium text-gray-600 dark:text-gray-400 mb-1">
              Severity
            </label>
            <div className="flex flex-wrap gap-1">
              {SEVERITY_OPTIONS.map(sev => (
                <button
                  key={sev}
                  onClick={() => toggleSeverity(sev)}
                  className={`px-2 py-0.5 text-xs rounded-full border transition-colors ${
                    selectedSeverities.includes(sev)
                      ? 'text-white border-transparent'
                      : 'text-gray-600 dark:text-gray-400 border-gray-300 dark:border-gray-600 hover:border-gray-400'
                  }`}
                  style={selectedSeverities.includes(sev) ? { backgroundColor: SEVERITY_COLORS[sev] || '#6b7280' } : {}}
                >
                  {sev}
                </button>
              ))}
            </div>
          </div>
          <div>
            <label className="block text-xs font-medium text-gray-600 dark:text-gray-400 mb-1">
              Trace ID
            </label>
            <input
              type="text"
              value={traceIdInput}
              onChange={e => setTraceIdInput(e.target.value)}
              placeholder="按 Trace ID 关联..."
              className="w-full px-3 py-2 border border-gray-300 dark:border-gray-600 rounded-md
                bg-white dark:bg-gray-700 text-sm"
            />
          </div>
          <div>
            <label className="block text-xs font-medium text-gray-600 dark:text-gray-400 mb-1">
              时间范围
            </label>
            <select
              value={timeRange}
              onChange={e => setTimeRange(e.target.value)}
              className="w-full px-3 py-2 border border-gray-300 dark:border-gray-600 rounded-md
                bg-white dark:bg-gray-700 text-sm"
            >
              {TIME_RANGE_PRESETS.map(p => (
                <option key={p.value} value={p.value}>{p.label}</option>
              ))}
            </select>
          </div>
        </div>

        {/* Search Button */}
        <div className="flex justify-end">
          <button
            onClick={() => doSearch(0)}
            disabled={loading}
            className="px-4 py-2 bg-blue-600 text-white text-sm font-medium rounded-md
              hover:bg-blue-700 disabled:opacity-50 disabled:cursor-not-allowed
              transition-colors"
          >
            {loading ? '搜索中...' : '搜索'}
          </button>
        </div>
      </div>

      {/* Stats Bar */}
      {stats && stats.severityCounts && Object.keys(stats.severityCounts).length > 0 && (
        <div className="bg-white dark:bg-gray-800 rounded-lg shadow-sm border border-gray-200 dark:border-gray-700 p-3">
          <div className="flex items-center gap-4 text-sm">
            <span className="text-gray-500 font-medium">Severity 分布:</span>
            {Object.entries(stats.severityCounts)
              .sort(([a], [b]) => (SEVERITY_OPTIONS.indexOf(a) - SEVERITY_OPTIONS.indexOf(b)))
              .map(([sev, count]) => (
                <span
                  key={sev}
                  className="flex items-center gap-1"
                >
                  <span
                    className="w-2 h-2 rounded-full"
                    style={{ backgroundColor: SEVERITY_COLORS[sev] || '#6b7280' }}
                  />
                  <span className="text-gray-700 dark:text-gray-300">{sev}: {count.toLocaleString()}</span>
                </span>
              ))}
          </div>
        </div>
      )}

      {/* Error */}
      {error && (
        <div className="bg-red-50 border border-red-200 rounded-lg p-3 text-red-700 text-sm">
          {error}
        </div>
      )}

      {/* Results */}
      {result && result.logs.length > 0 ? (
        <div className="bg-white dark:bg-gray-800 rounded-lg shadow-sm border border-gray-200 dark:border-gray-700 overflow-hidden">
          {/* Log list */}
          <div className="divide-y divide-gray-100 dark:divide-gray-700">
            {result.logs.map(log => (
              <LogRow
                key={log.id}
                log={log}
                expanded={expandedLogId === log.id}
                onToggle={() => setExpandedLogId(expandedLogId === log.id ? null : log.id)}
              />
            ))}
          </div>

          {/* Pagination */}
          {result.total > PAGE_SIZE && (
            <div className="px-4 py-3 border-t border-gray-200 dark:border-gray-700 flex items-center justify-between">
              <span className="text-sm text-gray-500">
                第 {currentPage * PAGE_SIZE + 1} - {Math.min((currentPage + 1) * PAGE_SIZE, result.total)} 条，
                共 {result.total.toLocaleString()} 条
              </span>
              <div className="flex gap-2">
                <button
                  onClick={() => doSearch(currentPage - 1)}
                  disabled={currentPage === 0 || loading}
                  className="px-3 py-1 text-sm border border-gray-300 dark:border-gray-600 rounded
                    disabled:opacity-50 disabled:cursor-not-allowed hover:bg-gray-50 dark:hover:bg-gray-700"
                >
                  上一页
                </button>
                <button
                  onClick={() => doSearch(currentPage + 1)}
                  disabled={(currentPage + 1) * PAGE_SIZE >= result.total || loading}
                  className="px-3 py-1 text-sm border border-gray-300 dark:border-gray-600 rounded
                    disabled:opacity-50 disabled:cursor-not-allowed hover:bg-gray-50 dark:hover:bg-gray-700"
                >
                  下一页
                </button>
              </div>
            </div>
          )}
        </div>
      ) : result && result.logs.length === 0 ? (
        <EmptyState
          icon="📋"
          title="未找到日志"
          description="调整搜索条件后重试"
        />
      ) : !loading && !error && (
        <EmptyState
          icon="🔍"
          title="搜索日志"
          description="输入查询条件后点击搜索按钮"
        />
      )}
    </div>
  );
}

// ============================================================================
// Log Row Component
// ============================================================================

interface LogRowProps {
  log: LogRecord;
  expanded: boolean;
  onToggle: () => void;
}

function LogRow({ log, expanded, onToggle }: LogRowProps) {
  const timestamp = formatTimestamp(log.timeUnixNano);

  return (
    <div className="group">
      {/* Main row */}
      <div
        className="px-4 py-2 flex items-start gap-3 cursor-pointer hover:bg-gray-50 dark:hover:bg-gray-700/50 transition-colors"
        onClick={onToggle}
      >
        {/* Severity badge */}
        <span
          className="flex-shrink-0 px-1.5 py-0.5 text-[10px] font-bold rounded text-white mt-0.5"
          style={{ backgroundColor: SEVERITY_COLORS[log.severityText] || '#6b7280' }}
        >
          {log.severityText}
        </span>

        {/* Timestamp */}
        <span className="flex-shrink-0 text-xs text-gray-500 dark:text-gray-400 font-mono mt-0.5 w-36">
          {timestamp}
        </span>

        {/* Service */}
        <span className="flex-shrink-0 text-xs text-blue-600 dark:text-blue-400 font-medium mt-0.5 w-32 truncate">
          {log.serviceName}
        </span>

        {/* Body */}
        <span className="flex-1 text-sm text-gray-800 dark:text-gray-200 truncate font-mono">
          {log.body}
        </span>

        {/* Expand indicator */}
        <span className="flex-shrink-0 text-gray-400 mt-0.5">
          {expanded ? '▾' : '▸'}
        </span>
      </div>

      {/* Expanded detail */}
      {expanded && (
        <div className="px-4 py-3 bg-gray-50 dark:bg-gray-900/50 border-t border-gray-100 dark:border-gray-700">
          <div className="grid grid-cols-1 md:grid-cols-2 gap-4 text-sm">
            {/* Metadata */}
            <div className="space-y-2">
              <h4 className="font-medium text-gray-700 dark:text-gray-300">元数据</h4>
              <dl className="space-y-1">
                <LogDetailField label="Log ID" value={log.id} mono />
                <LogDetailField label="Timestamp" value={timestamp} />
                <LogDetailField label="Service" value={log.serviceName} />
                <LogDetailField label="Severity" value={`${log.severityText} (${log.severityNumber})`} />
                {log.traceId && <LogDetailField label="Trace ID" value={log.traceId} mono link={`/traces?traceID=${log.traceId}`} />}
                {log.spanId && <LogDetailField label="Span ID" value={log.spanId} mono />}
                {log.appId && <LogDetailField label="App ID" value={log.appId} />}
              </dl>
            </div>

            {/* Body */}
            <div className="space-y-2">
              <h4 className="font-medium text-gray-700 dark:text-gray-300">日志内容</h4>
              <pre className="text-xs bg-white dark:bg-gray-800 border border-gray-200 dark:border-gray-700 rounded p-2 overflow-auto max-h-48 whitespace-pre-wrap font-mono">
                {log.body}
              </pre>
            </div>
          </div>

          {/* Attributes */}
          {log.attributes && log.attributes.length > 0 && (
            <div className="mt-3 space-y-1">
              <h4 className="font-medium text-gray-700 dark:text-gray-300 text-sm">Attributes</h4>
              <div className="flex flex-wrap gap-1">
                {log.attributes.map(attr => (
                  <span
                    key={attr.key}
                    className="inline-flex items-center px-2 py-0.5 text-xs bg-blue-50 dark:bg-blue-900/30 text-blue-700 dark:text-blue-300 rounded"
                  >
                    <span className="font-medium">{attr.key}:</span>
                    <span className="ml-1">{String(anyValueToDisplay(attr.value))}</span>
                  </span>
                ))}
              </div>
            </div>
          )}

          {/* Resource */}
          {log.resource && log.resource.length > 0 && (
            <div className="mt-3 space-y-1">
              <h4 className="font-medium text-gray-700 dark:text-gray-300 text-sm">Resource</h4>
              <div className="flex flex-wrap gap-1">
                {log.resource.map(attr => (
                  <span
                    key={attr.key}
                    className="inline-flex items-center px-2 py-0.5 text-xs bg-green-50 dark:bg-green-900/30 text-green-700 dark:text-green-300 rounded"
                  >
                    <span className="font-medium">{attr.key}:</span>
                    <span className="ml-1">{String(anyValueToDisplay(attr.value))}</span>
                  </span>
                ))}
              </div>
            </div>
          )}
        </div>
      )}
    </div>
  );
}

// ============================================================================
// Helper Components
// ============================================================================

function LogDetailField({ label, value, mono, link }: { label: string; value: string; mono?: boolean; link?: string }) {
  const valueClass = `text-gray-800 dark:text-gray-200 ${mono ? 'font-mono text-xs' : ''}`;
  return (
    <div className="flex">
      <dt className="w-24 flex-shrink-0 text-gray-500 dark:text-gray-400">{label}:</dt>
      <dd className={valueClass}>
        {link ? (
          <a href={link} className="text-blue-600 dark:text-blue-400 hover:underline">{value}</a>
        ) : (
          value
        )}
      </dd>
    </div>
  );
}
