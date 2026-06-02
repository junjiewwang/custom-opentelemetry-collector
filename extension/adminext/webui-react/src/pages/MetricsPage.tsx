/**
 * Metrics 页面 - 指标数据查询与可视化
 *
 * 功能：
 * - 指标名称查询（支持自定义 metric + 预设面板）
 * - 时间范围选择器
 * - ECharts 时间序列图表（折线/面积）
 * - 预设 RED Dashboard 面板（Rate / Error / Duration）
 * - 自动计算 step
 */

import { useState, useEffect, useCallback } from 'react';
import { useNavigate, useSearchParams } from 'react-router-dom';
import { apiClient } from '@/api/client';
import TimeSeriesChart from '@/components/TimeSeriesChart';
import type { ChartSeries } from '@/types/metric';
import {
  seriesToChartSeries,
  calculateStep,
  TIME_RANGE_PRESETS,
  RED_PANELS,
} from '@/utils/metric';

/** 查询面板 Tab 类型 */
type TabType = 'query' | 'red';

export default function MetricsPage() {
  // ========================================================================
  // State
  // ========================================================================

  // Tab 切换
  const [activeTab, setActiveTab] = useState<TabType>('query');

  // Metric 查询面板
  const [metricInput, setMetricInput] = useState('');
  const [serviceFilter, setServiceFilter] = useState('');
  const [timeRange, setTimeRange] = useState('1h');
  const [chartSeries, setChartSeries] = useState<ChartSeries[]>([]);
  const [queryLoading, setQueryLoading] = useState(false);
  const [queryError, setQueryError] = useState('');

  // RED Dashboard 面板
  const [redService, setRedService] = useState('');
  const [services, setServices] = useState<string[]>([]);
  const [redPanelData, setRedPanelData] = useState<Record<string, { series: ChartSeries[]; loading: boolean; error: string }>>({});

  // Metric backend 可用性
  const [metricsAvailable, setMetricsAvailable] = useState<boolean | null>(null);

  // Metric names (autocomplete)
  const [metricNames, setMetricNames] = useState<string[]>([]);

  // 路由
  const navigate = useNavigate();
  const [searchParams] = useSearchParams();

  // ========================================================================
  // URL 查询参数支持（从 Trace 页面联动跳转）
  // 例如: /metrics?service=my-svc&tab=red
  // ========================================================================

  useEffect(() => {
    const urlService = searchParams.get('service');
    const urlTab = searchParams.get('tab');
    if (urlTab === 'red') setActiveTab('red');
    if (urlService) setRedService(urlService);
  }, [searchParams]);

  // ========================================================================
  // 检查 Metric backend 可用性
  // ========================================================================

  useEffect(() => {
    checkMetricsAvailability();
  }, []);

  const checkMetricsAvailability = async () => {
    try {
      await apiClient.getMetricLabels();
      setMetricsAvailable(true);
    } catch {
      setMetricsAvailable(false);
    }
  };

  // ========================================================================
  // 加载 Service 列表 + Metric Names
  // ========================================================================

  useEffect(() => {
    if (metricsAvailable) {
      loadServices();
      loadMetricNames();
    }
  }, [metricsAvailable]);

  const loadServices = async () => {
    try {
      const resp = await apiClient.getMetricLabelValues('service_name');
      if (resp.data) {
        setServices(resp.data.sort());
        return;
      }
    } catch {
      // 降级尝试从 Trace API 获取
      try {
        const resp = await apiClient.getTraceServices();
        setServices(resp.data.map(s => s.name).sort());
      } catch {
        setServices([]);
      }
    }
  };

  const loadMetricNames = async () => {
    try {
      const resp = await apiClient.getMetricNames();
      setMetricNames(resp.data ?? []);
    } catch {
      setMetricNames([]);
    }
  };

  // ========================================================================
  // 获取当前时间范围参数
  // ========================================================================

  const getTimeParams = useCallback(() => {
    const preset = TIME_RANGE_PRESETS.find(p => p.value === timeRange);
    const seconds = preset?.seconds ?? 3600;
    const end = Date.now();  // Unix ms
    const start = end - seconds * 1000;
    const step = calculateStep(seconds);
    return { start, end, step, seconds };
  }, [timeRange]);

  // ========================================================================
  // 执行 Metric 查询
  // ========================================================================

  const executeQuery = useCallback(async () => {
    if (!metricInput.trim()) {
      setQueryError('Please enter a metric name');
      return;
    }

    setQueryLoading(true);
    setQueryError('');

    try {
      const { start, end, step } = getTimeParams();
      const resp = await apiClient.metricQueryRange({
        metric: metricInput.trim(),
        service: serviceFilter || undefined,
        start,
        end,
        step,
      });

      const series = seriesToChartSeries(resp);
      setChartSeries(series);

      if (series.length === 0) {
        setQueryError('Query returned no data');
      }
    } catch (err: unknown) {
      const apiErr = err as { message?: string };
      setQueryError(apiErr.message ?? 'Query failed');
      setChartSeries([]);
    } finally {
      setQueryLoading(false);
    }
  }, [metricInput, serviceFilter, getTimeParams]);

  // ========================================================================
  // 加载 RED Dashboard 面板数据
  // ========================================================================

  const loadRedPanels = useCallback(async () => {
    if (!redService) return;

    const { start, end, step } = getTimeParams();

    // 初始化所有面板为 loading 状态
    const initialState: Record<string, { series: ChartSeries[]; loading: boolean; error: string }> = {};
    for (const panel of RED_PANELS) {
      initialState[panel.id] = { series: [], loading: true, error: '' };
    }
    setRedPanelData(initialState);

    // 并行请求所有面板数据
    await Promise.all(
      RED_PANELS.map(async (panel) => {
        try {
          const resp = await apiClient.metricQueryRange({
            metric: panel.metric,
            service: redService,
            labels: panel.labels
              ? Object.entries(panel.labels).map(([k, v]) => `${k}:${v}`).join(',')
              : undefined,
            start,
            end,
            step,
          });

          const series = seriesToChartSeries(resp);
          setRedPanelData(prev => ({
            ...prev,
            [panel.id]: { series, loading: false, error: '' },
          }));
        } catch (err: unknown) {
          const apiErr = err as { message?: string };
          setRedPanelData(prev => ({
            ...prev,
            [panel.id]: { series: [], loading: false, error: apiErr.message ?? 'Query failed' },
          }));
        }
      }),
    );
  }, [redService, getTimeParams]);

  // 当 RED service 变更时自动加载面板
  useEffect(() => {
    if (redService && activeTab === 'red') {
      loadRedPanels();
    }
  }, [redService, activeTab, loadRedPanels]);

  // ========================================================================
  // 示例 Metric 查询
  // ========================================================================

  const exampleMetrics = [
    { label: 'HTTP Request Duration', metric: 'http_server_request_duration_seconds' },
    { label: 'HTTP Request Count', metric: 'http_server_request_duration_seconds_count' },
    { label: 'CPU Usage', metric: 'process_cpu_seconds_total' },
    { label: 'Memory Usage', metric: 'process_resident_memory_bytes' },
    { label: 'Go Goroutines', metric: 'go_goroutines' },
  ];

  // ========================================================================
  // 渲染
  // ========================================================================

  return (
    <div className="fade-in">
      {/* Page Header */}
      <div className="mb-6">
        <h2 className="text-2xl font-bold text-gray-800 flex items-center gap-3">
          <i className="fas fa-chart-line text-primary-600" />
          Metrics
        </h2>
        <p className="text-gray-500 mt-1">
          查询和可视化 OTel 指标数据
        </p>
      </div>

      {/* Metrics Not Available */}
      {metricsAvailable === false && (
        <div className="bg-white rounded-xl shadow-sm border border-gray-200 p-12 text-center">
          <div className="w-16 h-16 bg-yellow-50 rounded-full flex items-center justify-center mx-auto mb-4">
            <i className="fas fa-exclamation-triangle text-yellow-500 text-xl" />
          </div>
          <h3 className="text-lg font-semibold text-gray-700 mb-2">Metric Storage Not Available</h3>
          <p className="text-gray-500 text-sm max-w-md mx-auto">
            请在 Collector 配置中启用 Observability Storage Extension 以启用 Metric 查询功能。
          </p>
        </div>
      )}

      {/* Main Content */}
      {metricsAvailable === true && (
        <>
          {/* Tab Switcher */}
          <div className="flex items-center gap-1 mb-6 bg-gray-100 rounded-lg p-1 w-fit">
            <button
              onClick={() => setActiveTab('query')}
              className={`px-4 py-2 rounded-md text-sm font-medium transition ${
                activeTab === 'query'
                  ? 'bg-white text-primary-700 shadow-sm'
                  : 'text-gray-500 hover:text-gray-700'
              }`}
            >
              <i className="fas fa-terminal mr-2" />
              Metric Query
            </button>
            <button
              onClick={() => setActiveTab('red')}
              className={`px-4 py-2 rounded-md text-sm font-medium transition ${
                activeTab === 'red'
                  ? 'bg-white text-primary-700 shadow-sm'
                  : 'text-gray-500 hover:text-gray-700'
              }`}
            >
              <i className="fas fa-tachometer-alt mr-2" />
              RED Dashboard
            </button>
          </div>

          {/* Time Range Selector (共用) */}
          <div className="flex items-center gap-3 mb-6">
            <span className="text-sm text-gray-500 flex-shrink-0">
              <i className="fas fa-clock mr-1" />
              Range:
            </span>
            <div className="flex items-center gap-1">
              {TIME_RANGE_PRESETS.map(preset => (
                <button
                  key={preset.value}
                  onClick={() => setTimeRange(preset.value)}
                  className={`px-3 py-1.5 rounded-md text-xs font-medium transition whitespace-nowrap ${
                    timeRange === preset.value
                      ? 'bg-primary-600 text-white shadow-sm'
                      : 'bg-gray-100 text-gray-500 hover:bg-gray-200 hover:text-gray-700'
                  }`}
                >
                  {preset.label}
                </button>
              ))}
            </div>
          </div>

          {/* ===================== Metric Query Tab ===================== */}
          {activeTab === 'query' && (
            <div>
              {/* Query Input */}
              <div className="bg-white rounded-xl shadow-sm border border-gray-200 p-6 mb-6">
                <div className="flex items-center gap-3">
                  <div className="flex-1">
                    <label className="block text-sm font-medium text-gray-700 mb-1">Metric Name</label>
                    <div className="flex gap-2">
                      <input
                        type="text"
                        value={metricInput}
                        onChange={(e) => setMetricInput(e.target.value)}
                        onKeyDown={(e) => e.key === 'Enter' && executeQuery()}
                        placeholder='e.g. http_server_request_duration_seconds'
                        list="metric-names-datalist"
                        className="flex-1 px-4 py-2 border border-gray-200 rounded-lg text-sm font-mono focus:ring-2 focus:ring-primary-500 focus:border-primary-500"
                      />
                      <datalist id="metric-names-datalist">
                        {metricNames.slice(0, 50).map(name => (
                          <option key={name} value={name} />
                        ))}
                      </datalist>
                      <input
                        type="text"
                        value={serviceFilter}
                        onChange={(e) => setServiceFilter(e.target.value)}
                        placeholder="Service filter (optional)"
                        className="w-48 px-3 py-2 border border-gray-200 rounded-lg text-sm focus:ring-2 focus:ring-primary-500 focus:border-primary-500"
                      />
                      <button
                        onClick={executeQuery}
                        disabled={queryLoading || !metricInput.trim()}
                        className="px-6 py-2 bg-primary-600 text-white rounded-lg hover:bg-primary-700 transition flex items-center gap-2 disabled:opacity-50"
                      >
                        {queryLoading ? <i className="fas fa-spinner fa-spin" /> : <i className="fas fa-play" />}
                        <span>Query</span>
                      </button>
                    </div>
                  </div>
                </div>

                {/* Example Metrics */}
                <div className="mt-3 flex items-center gap-2 flex-wrap">
                  <span className="text-xs text-gray-400">Examples:</span>
                  {exampleMetrics.map(eq => (
                    <button
                      key={eq.label}
                      onClick={() => setMetricInput(eq.metric)}
                      className="px-2 py-0.5 bg-gray-100 text-gray-600 text-xs rounded hover:bg-gray-200 transition"
                    >
                      {eq.label}
                    </button>
                  ))}
                </div>

                {/* Error */}
                {queryError && (
                  <div className="mt-4 px-4 py-2 bg-red-50 text-red-600 rounded-lg text-sm">
                    <i className="fas fa-exclamation-circle mr-2" />
                    {queryError}
                  </div>
                )}
              </div>

              {/* Chart Result */}
              <div className="bg-white rounded-xl shadow-sm border border-gray-200 p-6">
                <div className="flex items-center justify-between mb-4">
                  <h3 className="text-sm font-semibold text-gray-700">Query Result</h3>
                  {chartSeries.length > 0 && (
                    <span className="text-xs text-gray-400">{chartSeries.length} series</span>
                  )}
                </div>
                <TimeSeriesChart
                  series={chartSeries}
                  chartType="line"
                  loading={queryLoading}
                  height={360}
                />
              </div>
            </div>
          )}

          {/* ===================== RED Dashboard Tab ===================== */}
          {activeTab === 'red' && (
            <div>
              {/* Service Selector */}
              <div className="bg-white rounded-xl shadow-sm border border-gray-200 p-6 mb-6">
                <div className="flex items-center gap-4">
                  <div className="flex-1 max-w-sm">
                    <label className="block text-sm font-medium text-gray-700 mb-1">Service</label>
                    <select
                      value={redService}
                      onChange={(e) => setRedService(e.target.value)}
                      className="w-full px-3 py-2 border border-gray-200 rounded-lg text-sm focus:ring-2 focus:ring-primary-500 focus:border-primary-500"
                    >
                      <option value="">Select a service...</option>
                      {services.map(s => (
                        <option key={s} value={s}>{s}</option>
                      ))}
                    </select>
                  </div>

                  {redService && (
                    <div className="flex items-center gap-2 mt-6">
                      <button
                        onClick={loadRedPanels}
                        className="px-4 py-2 bg-gray-100 text-gray-600 rounded-lg hover:bg-gray-200 transition flex items-center gap-2 text-sm"
                      >
                        <i className="fas fa-sync-alt" />
                        Refresh
                      </button>
                      <button
                        onClick={() => navigate(`/traces?service=${encodeURIComponent(redService)}&lookback=${timeRange}`)}
                        className="px-4 py-2 bg-primary-50 text-primary-600 rounded-lg hover:bg-primary-100 transition flex items-center gap-2 text-sm"
                        title={`查看 ${redService} 的 Traces`}
                      >
                        <i className="fas fa-route" />
                        View Traces
                      </button>
                    </div>
                  )}
                </div>
              </div>

              {/* RED Panels */}
              {redService ? (
                <div className="grid grid-cols-1 lg:grid-cols-2 gap-6">
                  {RED_PANELS.map(panel => {
                    const data = redPanelData[panel.id];
                    return (
                      <div
                        key={panel.id}
                        className="bg-white rounded-xl shadow-sm border border-gray-200 p-5"
                      >
                        <div className="flex items-center justify-between mb-3">
                          <div>
                            <h4 className="text-sm font-semibold text-gray-700">{panel.title}</h4>
                            <p className="text-xs text-gray-400 mt-0.5">{panel.description}</p>
                          </div>
                          {data?.error && (
                            <span className="text-xs text-red-400" title={data.error}>
                              <i className="fas fa-exclamation-circle" />
                            </span>
                          )}
                        </div>
                        <TimeSeriesChart
                          series={data?.series ?? []}
                          chartType={panel.chartType ?? 'line'}
                          unit={panel.unit}
                          loading={data?.loading ?? false}
                          height={220}
                        />
                      </div>
                    );
                  })}
                </div>
              ) : (
                <div className="bg-white rounded-xl shadow-sm border border-gray-200 p-12 text-center">
                  <div className="w-16 h-16 bg-primary-50 rounded-full flex items-center justify-center mx-auto mb-4">
                    <i className="fas fa-tachometer-alt text-primary-400 text-xl" />
                  </div>
                  <h3 className="text-lg font-semibold text-gray-600 mb-2">RED Dashboard</h3>
                  <p className="text-gray-400 text-sm max-w-md mx-auto">
                    选择一个 Service 查看 RED 指标面板（Rate / Error / Duration）
                  </p>
                </div>
              )}
            </div>
          )}
        </>
      )}

      {/* Loading State */}
      {metricsAvailable === null && (
        <div className="bg-white rounded-xl shadow-sm border border-gray-200 p-12 text-center">
          <i className="fas fa-spinner fa-spin text-primary-400 text-2xl mb-4" />
          <p className="text-gray-500">Checking metric backend...</p>
        </div>
      )}
    </div>
  );
}
