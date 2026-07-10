/**
 * RedDashboard — RED (Rate/Error/Duration) Dashboard 组件
 *
 * 功能：
 * - Service 选择器 + Refresh / View Traces 按钮
 * - 6 个预设面板 grid（Rate / Error / P50/P95/P99 / By Status）
 * - 点击图表数据点 → 跳转到该时间点的 Traces（跨信号联动）
 *
 * 纯展示组件，状态和逻辑由 useRedPanels hook 管理。
 */

import { useCallback } from 'react';
import TimeSeriesChart from '@/components/TimeSeriesChart';
import { RED_PANELS } from '@/utils/metric';
import type { UseRedPanelsReturn } from '@/features/metrics/hooks/useRedPanels';

interface RedDashboardProps {
  red: UseRedPanelsReturn;
  onViewTraces: () => void;
  /** Called when user clicks a data point on a RED panel chart */
  onTraceClick?: (params: { service: string; time: number; panelId: string }) => void;
}

export default function RedDashboard({ red, onViewTraces, onTraceClick }: RedDashboardProps) {
  const { redService, setRedService, services, panelData, loadPanels } = red;

  const handleChartClick = useCallback(
    (panelId: string) => (params: { seriesName: string; time: number; value: number }) => {
      onTraceClick?.({ service: redService, time: params.time, panelId });
    },
    [redService, onTraceClick],
  );

  return (
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
                onClick={loadPanels}
                className="px-4 py-2 bg-gray-100 text-gray-600 rounded-lg hover:bg-gray-200 transition flex items-center gap-2 text-sm"
              >
                <i className="fas fa-sync-alt" />
                Refresh
              </button>
              <button
                onClick={onViewTraces}
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

      {/* RED Panels Grid */}
      {redService ? (
        <div className="grid grid-cols-1 lg:grid-cols-2 gap-6">
          {RED_PANELS.map(panel => {
            const data = panelData[panel.id];
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
                  showLegendStats={true}
                  onChartClick={onTraceClick ? handleChartClick(panel.id) : undefined}
                  // Hover cursor to indicate clickability
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
            选择一个 Service 查看 RED 指标面板（Rate / Error / Duration），点击图表数据点可跳转对应 Traces
          </p>
        </div>
      )}
    </div>
  );
}
