/**
 * Traces 页面 - Trace 数据查询与可视化
 *
 * 功能：
 * - 搜索面板（Service、Operation、Tags、时间范围、Duration）
 * - 结果列表（Trace ID、Duration、Spans 数量等）
 * - 点击展开 Trace Detail 时间轴
 */

import { useState, useEffect, useCallback, useRef } from 'react';
import { useSearchParams } from 'react-router-dom';
import { apiClient } from '@/api/client';
import { traceToListItem, formatDuration, formatTimestamp, getServiceColor } from '@/utils/trace';
import type { JaegerTrace, TraceListItem, TraceSearchParams } from '@/types/trace';
import TraceDetail from '@/components/TraceDetail';
import EmptyState from '@/components/EmptyState';

export default function TracesPage() {
  // ========================================================================
  // URL 查询参数（支持从其他页面联动跳转）
  // 例如: /traces?service=my-svc&lookback=1h
  // ========================================================================

  const [searchParams, setSearchParams] = useSearchParams();

  // 是否已从 URL 参数初始化过（防止重复触发搜索）
  const initializedFromURL = useRef(false);
  // 是否需要在 services 加载完成后自动搜索
  const pendingAutoSearch = useRef(false);

  // ========================================================================
  // State
  // ========================================================================

  // 搜索面板
  const [services, setServices] = useState<string[]>([]);
  const [operations, setOperations] = useState<string[]>([]);
  const [selectedService, setSelectedService] = useState('');
  const [selectedOperation, setSelectedOperation] = useState('');
  const [tagsInput, setTagsInput] = useState('');
  const [lookback, setLookback] = useState('1h');
  const [minDuration, setMinDuration] = useState('');
  const [maxDuration, setMaxDuration] = useState('');
  const [limit, setLimit] = useState(20);

  // 结果
  const [traces, setTraces] = useState<TraceListItem[]>([]);
  const [rawTraces, setRawTraces] = useState<JaegerTrace[]>([]);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState('');

  // 详情
  const [selectedTraceID, setSelectedTraceID] = useState<string | null>(null);

  // ========================================================================
  // 加载 Service 列表
  // ========================================================================

  useEffect(() => {
    loadServices();
  }, []);

  const loadServices = async () => {
    try {
      const resp = await apiClient.getTraceServices();
      const svcList = resp.data?.sort() ?? [];
      setServices(svcList);

      // 如果有待执行的自动搜索（URL 参数触发），在 services 加载完后执行
      if (pendingAutoSearch.current && svcList.length > 0) {
        pendingAutoSearch.current = false;
      }
    } catch {
      // Jaeger 未配置或不可用，静默处理
      setServices([]);
    }
  };

  // ========================================================================
  // 从 URL 查询参数初始化搜索条件
  // 支持参数: service, operation, lookback, tags, minDuration, maxDuration, limit
  // ========================================================================

  useEffect(() => {
    if (initializedFromURL.current) return;
    initializedFromURL.current = true;

    const urlService = searchParams.get('service');
    const urlOperation = searchParams.get('operation');
    const urlLookback = searchParams.get('lookback');
    const urlTags = searchParams.get('tags');
    const urlMinDuration = searchParams.get('minDuration');
    const urlMaxDuration = searchParams.get('maxDuration');
    const urlLimit = searchParams.get('limit');

    // 如果 URL 中没有 service 参数，不做任何初始化
    if (!urlService) return;

    setSelectedService(urlService);
    if (urlOperation) setSelectedOperation(urlOperation);
    if (urlLookback) setLookback(urlLookback);
    if (urlTags) setTagsInput(urlTags);
    if (urlMinDuration) setMinDuration(urlMinDuration);
    if (urlMaxDuration) setMaxDuration(urlMaxDuration);
    if (urlLimit) setLimit(Number(urlLimit));

    // 标记需要在 services 加载完成后自动执行搜索
    pendingAutoSearch.current = true;

    // 清除 URL 参数，避免刷新页面重复触发
    setSearchParams({}, { replace: true });
  }, [searchParams, setSearchParams]);

  // ========================================================================
  // 加载 Operations（当 Service 变更时）
  // ========================================================================

  useEffect(() => {
    if (!selectedService) {
      setOperations([]);
      return;
    }
    loadOperations(selectedService);
  }, [selectedService]);

  // ========================================================================
  // 自动搜索（从 URL 参数初始化后，等 service 设置完毕自动触发）
  // ========================================================================

  useEffect(() => {
    if (pendingAutoSearch.current && selectedService) {
      pendingAutoSearch.current = false;
      // 使用 setTimeout 确保所有 state 更新完毕后再触发搜索
      const timer = setTimeout(() => {
        searchTraces();
      }, 100);
      return () => clearTimeout(timer);
    }
  }, [selectedService]);

  const loadOperations = async (service: string) => {
    try {
      const resp = await apiClient.getTraceOperations(service);
      const ops = resp.data?.map(op => (typeof op === 'string' ? op : op.name)) ?? [];
      setOperations(ops.sort());
    } catch {
      setOperations([]);
    }
  };

  // ========================================================================
  // 搜索 Traces
  // ========================================================================

  const searchTraces = useCallback(async () => {
    if (!selectedService) {
      setError('Please select a service');
      return;
    }

    setLoading(true);
    setError('');
    setSelectedTraceID(null);

    try {
      // 计算时间范围
      const now = Date.now() * 1000; // 转微秒
      const lookbackMap: Record<string, number> = {
        '15m': 15 * 60 * 1_000_000,
        '30m': 30 * 60 * 1_000_000,
        '1h': 60 * 60 * 1_000_000,
        '3h': 3 * 60 * 60 * 1_000_000,
        '6h': 6 * 60 * 60 * 1_000_000,
        '12h': 12 * 60 * 60 * 1_000_000,
        '24h': 24 * 60 * 60 * 1_000_000,
        '2d': 2 * 24 * 60 * 60 * 1_000_000,
        '7d': 7 * 24 * 60 * 60 * 1_000_000,
      };
      const lookbackDuration = lookbackMap[lookback] ?? 60 * 60 * 1_000_000;

      const params: TraceSearchParams = {
        service: selectedService,
        limit,
        end: now,
        start: now - lookbackDuration,
      };
      if (selectedOperation) params.operation = selectedOperation;
      if (tagsInput.trim()) params.tags = tagsInput.trim();
      if (minDuration.trim()) params.minDuration = minDuration.trim();
      if (maxDuration.trim()) params.maxDuration = maxDuration.trim();

      const resp = await apiClient.searchTraces(params);
      const data = resp.data ?? [];

      setRawTraces(data);
      setTraces(data.map(traceToListItem).sort((a, b) => b.startTime - a.startTime));
    } catch (err: unknown) {
      const apiErr = err as { message?: string };
      setError(apiErr.message ?? 'Search failed');
      setTraces([]);
      setRawTraces([]);
    } finally {
      setLoading(false);
    }
  }, [selectedService, selectedOperation, tagsInput, lookback, minDuration, maxDuration, limit]);

  // ========================================================================
  // 获取选中的 Trace 原始数据
  // ========================================================================

  const selectedTrace = rawTraces.find(t => t.traceID === selectedTraceID) ?? null;

  // ========================================================================
  // 渲染
  // ========================================================================

  return (
    <div className="fade-in">
      {/* Page Header */}
      <div className="mb-6">
        <h2 className="text-2xl font-bold text-gray-800 flex items-center gap-3">
          <i className="fas fa-route text-primary-600" />
          Traces
        </h2>
        <p className="text-gray-500 mt-1">Search and explore distributed tracing data</p>
      </div>

      {/* Search Panel */}
      <div className="bg-white rounded-xl shadow-sm border border-gray-200 p-6 mb-6">
        <div className="grid grid-cols-1 md:grid-cols-2 lg:grid-cols-4 gap-4">
          {/* Service */}
          <div>
            <label className="block text-sm font-medium text-gray-700 mb-1">Service</label>
            <select
              value={selectedService}
              onChange={(e) => {
                setSelectedService(e.target.value);
                setSelectedOperation('');
              }}
              className="w-full px-3 py-2 border border-gray-200 rounded-lg text-sm focus:ring-2 focus:ring-primary-500 focus:border-primary-500"
            >
              <option value="">Select a service...</option>
              {services.map(s => (
                <option key={s} value={s}>{s}</option>
              ))}
            </select>
          </div>

          {/* Operation */}
          <div>
            <label className="block text-sm font-medium text-gray-700 mb-1">Operation</label>
            <select
              value={selectedOperation}
              onChange={(e) => setSelectedOperation(e.target.value)}
              disabled={!selectedService}
              className="w-full px-3 py-2 border border-gray-200 rounded-lg text-sm focus:ring-2 focus:ring-primary-500 focus:border-primary-500 disabled:bg-gray-50"
            >
              <option value="">All operations</option>
              {operations.map(op => (
                <option key={op} value={op}>{op}</option>
              ))}
            </select>
          </div>

          {/* Lookback */}
          <div>
            <label className="block text-sm font-medium text-gray-700 mb-1">Lookback</label>
            <select
              value={lookback}
              onChange={(e) => setLookback(e.target.value)}
              className="w-full px-3 py-2 border border-gray-200 rounded-lg text-sm focus:ring-2 focus:ring-primary-500 focus:border-primary-500"
            >
              <option value="15m">Last 15 min</option>
              <option value="30m">Last 30 min</option>
              <option value="1h">Last 1 hour</option>
              <option value="3h">Last 3 hours</option>
              <option value="6h">Last 6 hours</option>
              <option value="12h">Last 12 hours</option>
              <option value="24h">Last 24 hours</option>
              <option value="2d">Last 2 days</option>
              <option value="7d">Last 7 days</option>
            </select>
          </div>

          {/* Limit */}
          <div>
            <label className="block text-sm font-medium text-gray-700 mb-1">Limit</label>
            <select
              value={limit}
              onChange={(e) => setLimit(Number(e.target.value))}
              className="w-full px-3 py-2 border border-gray-200 rounded-lg text-sm focus:ring-2 focus:ring-primary-500 focus:border-primary-500"
            >
              <option value={10}>10</option>
              <option value={20}>20</option>
              <option value={50}>50</option>
              <option value={100}>100</option>
            </select>
          </div>
        </div>

        {/* Advanced: Tags, Duration — 与第一行保持 4 列对齐，Tags 占 2 列 */}
        <div className="grid grid-cols-1 md:grid-cols-2 lg:grid-cols-4 gap-4 mt-4">
          {/* Tags (占 2 列) */}
          <div className="md:col-span-2">
            <label className="block text-sm font-medium text-gray-700 mb-1">
              Tags <span className="text-gray-400 font-normal">(JSON)</span>
            </label>
            <input
              type="text"
              value={tagsInput}
              onChange={(e) => setTagsInput(e.target.value)}
              placeholder='{"http.status_code":"500"}'
              className="w-full px-3 py-2 border border-gray-200 rounded-lg text-sm focus:ring-2 focus:ring-primary-500 focus:border-primary-500"
            />
          </div>

          {/* Min Duration */}
          <div>
            <label className="block text-sm font-medium text-gray-700 mb-1">Min Duration</label>
            <input
              type="text"
              value={minDuration}
              onChange={(e) => setMinDuration(e.target.value)}
              placeholder="e.g. 100ms, 1.5s"
              className="w-full px-3 py-2 border border-gray-200 rounded-lg text-sm focus:ring-2 focus:ring-primary-500 focus:border-primary-500"
            />
          </div>

          {/* Max Duration */}
          <div>
            <label className="block text-sm font-medium text-gray-700 mb-1">Max Duration</label>
            <input
              type="text"
              value={maxDuration}
              onChange={(e) => setMaxDuration(e.target.value)}
              placeholder="e.g. 5s, 10s"
              className="w-full px-3 py-2 border border-gray-200 rounded-lg text-sm focus:ring-2 focus:ring-primary-500 focus:border-primary-500"
            />
          </div>
        </div>

        {/* Search Button */}
        <div className="mt-4 flex items-center gap-4">
          <button
            onClick={searchTraces}
            disabled={loading || !selectedService}
            className="px-6 py-2 bg-primary-600 text-white rounded-lg hover:bg-primary-700 transition flex items-center gap-2 disabled:opacity-50"
          >
            {loading ? <i className="fas fa-spinner fa-spin" /> : <i className="fas fa-search" />}
            <span>{loading ? 'Searching...' : 'Find Traces'}</span>
          </button>

          {traces.length > 0 && (
            <span className="text-sm text-gray-500">
              Found {traces.length} trace{traces.length !== 1 ? 's' : ''}
            </span>
          )}
        </div>

        {/* Error */}
        {error && (
          <div className="mt-4 px-4 py-2 bg-red-50 text-red-600 rounded-lg text-sm">
            <i className="fas fa-exclamation-circle mr-2" />
            {error}
          </div>
        )}
      </div>

      {/* Trace Detail (展开时显示) */}
      {selectedTrace && (
        <div className="mb-6">
          <TraceDetail
            trace={selectedTrace}
            onClose={() => setSelectedTraceID(null)}
          />
        </div>
      )}

      {/* Results List */}
      {traces.length > 0 && (
        <div className="space-y-3">
          {traces.map((item) => (
            <TraceListRow
              key={item.traceID}
              item={item}
              isSelected={selectedTraceID === item.traceID}
              onClick={() => setSelectedTraceID(
                selectedTraceID === item.traceID ? null : item.traceID,
              )}
            />
          ))}
        </div>
      )}

      {/* Empty State */}
      {!loading && traces.length === 0 && selectedService && !error && (
        <div className="bg-white rounded-xl shadow-sm border border-gray-200">
          <EmptyState
            icon="fas fa-search"
            title="No Traces Found"
            description="Try adjusting search parameters or selecting a different time range."
            size="lg"
          />
        </div>
      )}

      {/* Initial State */}
      {!selectedService && services.length === 0 && !loading && (
        <div className="bg-white rounded-xl shadow-sm border border-gray-200">
          <EmptyState
            icon="fas fa-exclamation-triangle"
            iconColor="text-yellow-500"
            iconBg="bg-yellow-50"
            title="Jaeger Backend Not Available"
            description={
              <span>
                Please configure <code className="bg-gray-100 px-1 rounded">admin.observability.jaeger.endpoint</code> in Collector settings to enable Trace querying.
              </span>
            }
            size="lg"
          />
        </div>
      )}
    </div>
  );
}

// ============================================================================
// Trace 列表行组件
// ============================================================================

interface TraceListRowProps {
  item: TraceListItem;
  isSelected: boolean;
  onClick: () => void;
}

function TraceListRow({ item, isSelected, onClick }: TraceListRowProps) {
  return (
    <div
      onClick={onClick}
      className={`bg-white rounded-xl shadow-sm border cursor-pointer transition-all hover:shadow-md ${
        isSelected ? 'border-primary-400 ring-2 ring-primary-100' : 'border-gray-200 hover:border-gray-300'
      }`}
    >
      <div className="p-4">
        <div className="flex items-center justify-between mb-3">
          {/* Left: Service + Operation */}
          <div className="flex items-center gap-3 min-w-0">
            <span
              className="w-3 h-3 rounded-full flex-shrink-0"
              style={{ backgroundColor: getServiceColor(item.rootServiceName) }}
            />
            <div className="min-w-0">
              <span className="font-semibold text-gray-800">{item.rootServiceName}</span>
              <span className="text-gray-400 mx-2">::</span>
              <span className="text-gray-600 truncate">{item.rootOperationName}</span>
            </div>
          </div>

          {/* Right: Duration + Badge */}
          <div className="flex items-center gap-3 flex-shrink-0">
            {item.hasError && (
              <span className="px-2 py-0.5 bg-red-100 text-red-600 text-xs rounded-full font-medium">
                Error
              </span>
            )}
            <span className="text-lg font-bold text-gray-800">
              {formatDuration(item.duration)}
            </span>
          </div>
        </div>

        <div className="flex items-center justify-between text-sm text-gray-500">
          <div className="flex items-center gap-4">
            {/* Trace ID */}
            <span className="font-mono text-xs text-gray-400" title={item.traceID}>
              {item.traceID.substring(0, 8)}...
            </span>
            {/* Timestamp */}
            <span>{formatTimestamp(item.startTime)}</span>
            {/* Span count */}
            <span>{item.spanCount} Spans</span>
            {/* Service count */}
            <span>{item.serviceCount} Services</span>
          </div>

          {/* Service Badges */}
          <div className="flex items-center gap-1 overflow-hidden">
            {item.services.slice(0, 5).map(svc => (
              <span
                key={svc.name}
                className="px-2 py-0.5 rounded-full text-xs text-white"
                style={{ backgroundColor: getServiceColor(svc.name) }}
                title={`${svc.name}: ${svc.count} spans`}
              >
                {svc.name} ({svc.count})
              </span>
            ))}
            {item.services.length > 5 && (
              <span className="text-xs text-gray-400">
                +{item.services.length - 5} more
              </span>
            )}
          </div>
        </div>
      </div>
    </div>
  );
}
