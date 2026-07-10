/**
 * Metrics 页面 — 容器组件
 *
 * 功能：
 * - 指标名称查询（支持自定义 metric + 预设面板）
 * - 时间范围选择器
 * - RED Dashboard 面板（Rate / Error / Duration）
 *
 * 架构：页面仅负责编排 hooks 和子组件，业务逻辑和独立组件
 * 分别位于 features/metrics/hooks 和 features/metrics/components。
 */

import { useEffect, useState, useCallback } from 'react';
import { useNavigate, useSearchParams } from 'react-router-dom';
import { useTimeRange } from '@/features/metrics/hooks/useTimeRange';
import { useMetricQuery } from '@/features/metrics/hooks/useMetricQuery';
import { useRedPanels } from '@/features/metrics/hooks/useRedPanels';
import { useMetricAvailability } from '@/features/metrics/hooks/useMetricAvailability';
import { useAutoRefresh } from '@/features/metrics/hooks/useAutoRefresh';
import TimeSeriesChart from '@/components/TimeSeriesChart';
import TimeRangeSelector from '@/features/metrics/components/TimeRangeSelector';
import MetricQueryPanel from '@/features/metrics/components/MetricQueryPanel';
import RedDashboard from '@/features/metrics/components/RedDashboard';

type TabType = 'query' | 'red';

export default function MetricsPage() {
  // -- Routing -----------------------------------------------------------
  const navigate = useNavigate();
  const [searchParams] = useSearchParams();
  const urlService = searchParams.get('service') ?? undefined;
  const urlTab = searchParams.get('tab');

  // -- Tab & Time Range --------------------------------------------------
  const [activeTab, setActiveTab] = useState<TabType>(urlTab === 'red' ? 'red' : 'query');
  const { timeRange, setTimeRange, getParams } = useTimeRange('1h');

  // -- Backend Availability ----------------------------------------------
  const { available, metricNames, labelNames } = useMetricAvailability();

  // -- Metric Query ------------------------------------------------------
  const metricQuery = useMetricQuery(getParams);

  // -- RED Dashboard -----------------------------------------------------
  const redPanels = useRedPanels(getParams, activeTab, urlService);

  // -- Auto Refresh ------------------------------------------------------
  const refreshCallback = useCallback(() => {
    if (activeTab === 'query' && metricQuery.metricInput.trim()) {
      metricQuery.executeQuery();
    }
    if (activeTab === 'red' && redPanels.redService) {
      redPanels.loadPanels();
    }
  }, [activeTab, metricQuery.metricInput, metricQuery.executeQuery, redPanels.redService, redPanels.loadPanels]);

  const { interval: refreshInterval, setInterval: setRefreshInterval } = useAutoRefresh(refreshCallback);

  // -- RED → Trace click-through ----------------------------------------
  const handleTraceClick = useCallback(
    (params: { service: string; time: number; panelId: string }) => {
      // Navigate to traces with a ±5min window around the clicked data point
      const start = params.time - 5 * 60 * 1000;
      const end = params.time + 5 * 60 * 1000;
      navigate(
        `/traces?service=${encodeURIComponent(params.service)}&start=${start}&end=${end}`,
      );
    },
    [navigate],
  );

  // -- Apply URL tab on mount (once) -------------------------------------
  useEffect(() => {
    if (urlTab === 'red') setActiveTab('red');
  }, [urlTab]);

  // ======================================================================
  // Render
  // ======================================================================

  return (
    <div className="fade-in">
      {/* Page Header */}
      <div className="mb-6">
        <h2 className="text-2xl font-bold text-gray-800 flex items-center gap-3">
          <i className="fas fa-chart-line text-primary-600" />
          Metrics
        </h2>
        <p className="text-gray-500 mt-1">查询和可视化 OTel 指标数据</p>
      </div>

      {/* -- Backend unavailable -- */}
      {available === false && (
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

      {/* -- Loading -- */}
      {available === null && (
        <div className="bg-white rounded-xl shadow-sm border border-gray-200 p-12 text-center">
          <i className="fas fa-spinner fa-spin text-primary-400 text-2xl mb-4" />
          <p className="text-gray-500">Checking metric backend...</p>
        </div>
      )}

      {/* -- Main Content (backend available) -- */}
      {available === true && (
        <>
          {/* Tab Switcher */}
          <div className="flex items-center gap-1 mb-6 bg-gray-100 rounded-lg p-1 w-fit">
            {(['query', 'red'] as TabType[]).map(tab => (
              <button
                key={tab}
                onClick={() => setActiveTab(tab)}
                className={`px-4 py-2 rounded-md text-sm font-medium transition ${
                  activeTab === tab
                    ? 'bg-white text-primary-700 shadow-sm'
                    : 'text-gray-500 hover:text-gray-700'
                }`}
              >
                <i className={`fas ${tab === 'query' ? 'fa-terminal' : 'fa-tachometer-alt'} mr-2`} />
                {tab === 'query' ? 'Metric Query' : 'RED Dashboard'}
              </button>
            ))}
          </div>

          {/* Time Range (shared) */}
          <div className="mb-6">
            <TimeRangeSelector
              value={timeRange}
              onChange={setTimeRange}
              refreshInterval={refreshInterval}
              onRefreshChange={setRefreshInterval}
            />
          </div>

          {/* Query Tab */}
          {activeTab === 'query' && (
            <div>
              <div className="mb-6">
                <MetricQueryPanel query={metricQuery} metricNames={metricNames} labelNames={labelNames} />
              </div>
              <div className="bg-white rounded-xl shadow-sm border border-gray-200 p-6">
                <div className="flex items-center justify-between mb-4">
                  <h3 className="text-sm font-semibold text-gray-700">Query Result</h3>
                  {metricQuery.chartSeries.length > 0 && (
                    <span className="text-xs text-gray-400">{metricQuery.chartSeries.length} series</span>
                  )}
                </div>
                <TimeSeriesChart
                  series={metricQuery.chartSeries}
                  chartType="line"
                  loading={metricQuery.loading}
                  height={360}
                />
              </div>
            </div>
          )}

          {/* RED Tab */}
          {activeTab === 'red' && (
            <RedDashboard
              red={redPanels}
              onViewTraces={() =>
                navigate(`/traces?service=${encodeURIComponent(redPanels.redService)}&lookback=${timeRange}`)
              }
              onTraceClick={handleTraceClick}
            />
          )}
        </>
      )}
    </div>
  );
}
