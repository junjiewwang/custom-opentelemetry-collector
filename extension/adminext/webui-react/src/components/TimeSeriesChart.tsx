/**
 * TimeSeriesChart 组件 - Grafana-style ECharts 时间序列图
 *
 * 基于 echarts-for-react 封装，参考 Grafana Time Series Panel 设计：
 * - DataZoom 框选缩放 + 底部滑块
 * - Cross-hair tooltip（按值降序排列）
 * - Legend 统计表（min/max/avg/last）
 * - 阈值线（SLO / alert level）
 * - 渐变面积图（可选）
 * - 自适应 Y 轴格式化（按 unit 类型）
 * - 响应式尺寸
 * - 图表点击回调（跨信号联动）
 */

import { useMemo, useCallback } from 'react';
import ReactEChartsCore from 'echarts-for-react/lib/core';
import * as echarts from 'echarts/core';
import { LineChart } from 'echarts/charts';
import {
  TooltipComponent,
  LegendComponent,
  GridComponent,
  DataZoomComponent,
  MarkLineComponent,
} from 'echarts/components';
import { CanvasRenderer } from 'echarts/renderers';
import EmptyState from '@/components/EmptyState';
import type { ChartSeries } from '@/types/metric';
import { formatYAxisValue } from '@/utils/metric';
import {
  buildTimeSeriesOption,
  computeSeriesStats,
} from '@/components/charts/chartTheme';

// 注册 ECharts 组件（Tree-shaking 按需加载）
echarts.use([
  LineChart,
  TooltipComponent,
  LegendComponent,
  GridComponent,
  DataZoomComponent,
  MarkLineComponent,
  CanvasRenderer,
]);

interface TimeSeriesChartProps {
  series: ChartSeries[];
  chartType?: 'line' | 'area';
  unit?: string;
  height?: number;
  loading?: boolean;
  showDataZoom?: boolean;
  legendPlacement?: 'bottom' | 'right';
  /** Show min/max/avg/last in legend (default false) */
  showLegendStats?: boolean;
  /** Horizontal threshold line (SLO / alert) */
  threshold?: { value: number; label: string; color?: string };
  /** Callback when user clicks a data point on the chart */
  onChartClick?: (params: { seriesName: string; time: number; value: number }) => void;
}

export default function TimeSeriesChart({
  series,
  chartType = 'line',
  unit,
  height = 280,
  loading = false,
  showDataZoom = true,
  legendPlacement = 'bottom',
  showLegendStats = false,
  threshold,
  onChartClick,
}: TimeSeriesChartProps) {
  const yAxisFormatter = useMemo(
    () => (value: number) => formatYAxisValue(value, unit),
    [unit],
  );

  const option = useMemo(() => {
    if (series.length === 0) return {};
    return buildTimeSeriesOption({
      series,
      chartType,
      yAxisFormatter,
      showDataZoom,
      legendPlacement,
      showLegendStats,
      threshold,
    });
  }, [series, chartType, yAxisFormatter, showDataZoom, legendPlacement, showLegendStats, threshold]);

  // Stats for potential external use (e.g. PanelCard footer)
  const stats = useMemo(
    () => (showLegendStats ? computeSeriesStats(series) : undefined),
    [series, yAxisFormatter, showLegendStats],
  );

  // ECharts click event handler
  const handleChartClick = useCallback(
    (params: unknown) => {
      if (!onChartClick) return;
      const p = params as { seriesName?: string; value?: [number, number] };
      if (p.seriesName && p.value?.[0]) {
        onChartClick({ seriesName: p.seriesName, time: p.value[0], value: p.value[1] });
      }
    },
    [onChartClick],
  );

  const onEvents = onChartClick ? { click: handleChartClick } : undefined;

  // -- Loading State ----------------------------------------------------
  if (loading) {
    return (
      <div style={{ height }} className="bg-gray-50 rounded-lg">
        <EmptyState
          icon="fas fa-spinner fa-spin"
          iconColor="text-blue-300"
          iconBg="bg-blue-50"
          title="Loading..."
          size="sm"
        />
      </div>
    );
  }

  // -- Empty State ------------------------------------------------------
  if (series.length === 0) {
    return (
      <div style={{ height }} className="bg-gray-50 rounded-lg">
        <EmptyState icon="fas fa-chart-line" title="No data" size="sm" />
      </div>
    );
  }

  // -- Chart Render -----------------------------------------------------
  return (
    <div>
      <ReactEChartsCore
        echarts={echarts}
        option={option}
        style={{ height, width: '100%' }}
        notMerge={true}
        lazyUpdate={true}
        onEvents={onEvents}
      />
      {/* Legend Stats Footer (below chart, Grafana-style) */}
      {stats && stats.length > 0 && (
        <div className="mt-1 flex flex-wrap gap-x-4 gap-y-1 text-xs text-gray-400">
          {stats.map((s, i) => (
            <div key={s.name} className="flex items-center gap-2">
              <span
                className="inline-block w-3 h-3 rounded-full flex-shrink-0"
                style={{
                  backgroundColor: [
                    '#5470c6', '#91cc75', '#fac858', '#ee6666',
                  ][i % 4],
                }}
              />
              <span className="truncate max-w-[120px]">{s.name}</span>
              <span className="tabular-nums">
                min:{yAxisFormatter(s.min)} max:{yAxisFormatter(s.max)} avg:{yAxisFormatter(s.avg)}
              </span>
            </div>
          ))}
        </div>
      )}
    </div>
  );
}
