/**
 * TimeSeriesChart 组件 - ECharts 时间序列折线/面积图
 *
 * 基于 echarts-for-react 封装，用于 Prometheus 指标数据可视化。
 *
 * 功能：
 * - 支持 line / area 图表类型
 * - 自适应 Y 轴格式化（按 unit 类型）
 * - 自适应 X 轴时间刻度
 * - 图例展示（可点击切换系列）
 * - Tooltip 数值格式化
 * - 响应式尺寸
 */

import { useMemo } from 'react';
import ReactEChartsCore from 'echarts-for-react/lib/core';
import * as echarts from 'echarts/core';
import { LineChart } from 'echarts/charts';
import {
  TooltipComponent,
  LegendComponent,
  GridComponent,
  DataZoomComponent,
} from 'echarts/components';
import { CanvasRenderer } from 'echarts/renderers';
import type { ChartSeries } from '@/types/metric';
import { formatYAxisValue, CHART_COLORS } from '@/utils/metric';

// 注册 ECharts 组件（Tree-shaking 按需加载）
echarts.use([
  LineChart,
  TooltipComponent,
  LegendComponent,
  GridComponent,
  DataZoomComponent,
  CanvasRenderer,
]);

interface TimeSeriesChartProps {
  /** 图表系列数据 */
  series: ChartSeries[];
  /** 图表类型 */
  chartType?: 'line' | 'area';
  /** Y 轴单位 */
  unit?: string;
  /** 图表高度 */
  height?: number;
  /** 是否加载中 */
  loading?: boolean;
}

export default function TimeSeriesChart({
  series,
  chartType = 'line',
  unit,
  height = 280,
  loading = false,
}: TimeSeriesChartProps) {
  const option = useMemo(() => {
    if (series.length === 0) return {};

    const isArea = chartType === 'area';

    return {
      tooltip: {
        trigger: 'axis' as const,
        backgroundColor: 'rgba(255, 255, 255, 0.95)',
        borderColor: '#e5e7eb',
        borderWidth: 1,
        textStyle: {
          color: '#374151',
          fontSize: 12,
        },
        formatter: (params: Array<{ seriesName: string; value: [number, number]; color: string }>) => {
          if (!params || params.length === 0) return '';
          const firstParam = params[0]!;
          const time = new Date(firstParam.value[0]).toLocaleString('zh-CN');
          let html = `<div style="font-weight:600;margin-bottom:4px">${time}</div>`;
          for (const p of params) {
            const val = formatYAxisValue(p.value[1], unit);
            html += `<div style="display:flex;align-items:center;gap:6px;margin:2px 0">
              <span style="display:inline-block;width:8px;height:8px;border-radius:50%;background:${p.color}"></span>
              <span style="flex:1;overflow:hidden;text-overflow:ellipsis;white-space:nowrap;max-width:200px">${p.seriesName}</span>
              <span style="font-weight:600">${val}</span>
            </div>`;
          }
          return html;
        },
      },
      legend: {
        show: series.length > 1 && series.length <= 10,
        bottom: 0,
        textStyle: { fontSize: 11, color: '#6b7280' },
        itemWidth: 12,
        itemHeight: 8,
        type: 'scroll' as const,
      },
      grid: {
        top: 16,
        right: 16,
        bottom: series.length > 1 && series.length <= 10 ? 40 : 8,
        left: 0,
        containLabel: true,
      },
      xAxis: {
        type: 'time' as const,
        axisLine: { lineStyle: { color: '#e5e7eb' } },
        axisTick: { show: false },
        axisLabel: {
          color: '#9ca3af',
          fontSize: 11,
          formatter: (value: number) => {
            const date = new Date(value);
            const hours = date.getHours().toString().padStart(2, '0');
            const mins = date.getMinutes().toString().padStart(2, '0');
            return `${hours}:${mins}`;
          },
        },
        splitLine: { show: false },
      },
      yAxis: {
        type: 'value' as const,
        axisLine: { show: false },
        axisTick: { show: false },
        axisLabel: {
          color: '#9ca3af',
          fontSize: 11,
          formatter: (value: number) => formatYAxisValue(value, unit),
        },
        splitLine: {
          lineStyle: { color: '#f3f4f6', type: 'dashed' as const },
        },
      },
      series: series.map((s, i) => ({
        name: s.name,
        type: 'line' as const,
        data: s.data.map(d => [d.time, d.value]),
        smooth: true,
        symbol: 'none',
        lineStyle: {
          width: 2,
          color: CHART_COLORS[i % CHART_COLORS.length],
        },
        itemStyle: {
          color: CHART_COLORS[i % CHART_COLORS.length],
        },
        areaStyle: isArea
          ? {
              color: new echarts.graphic.LinearGradient(0, 0, 0, 1, [
                { offset: 0, color: CHART_COLORS[i % CHART_COLORS.length] + '40' },
                { offset: 1, color: CHART_COLORS[i % CHART_COLORS.length] + '05' },
              ]),
            }
          : undefined,
      })),
    };
  }, [series, chartType, unit]);

  if (loading) {
    return (
      <div style={{ height }} className="flex items-center justify-center bg-gray-50 rounded-lg">
        <div className="flex items-center gap-2 text-gray-400">
          <i className="fas fa-spinner fa-spin" />
          <span className="text-sm">加载中...</span>
        </div>
      </div>
    );
  }

  if (series.length === 0) {
    return (
      <div style={{ height }} className="flex items-center justify-center bg-gray-50 rounded-lg">
        <div className="text-center text-gray-400">
          <i className="fas fa-chart-line text-2xl mb-2" />
          <p className="text-sm">No data</p>
        </div>
      </div>
    );
  }

  return (
    <ReactEChartsCore
      echarts={echarts}
      option={option}
      style={{ height, width: '100%' }}
      notMerge={true}
      lazyUpdate={true}
    />
  );
}
