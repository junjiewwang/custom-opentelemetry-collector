/**
 * Grafana-inspired chart theme for ECharts time series.
 *
 * Features:
 * - Sparse grid lines (not distracting)
 * - Calibrated color palette (Grafana Classic adapted to light theme)
 * - Cross-hair tooltip with value-desc sort
 * - DataZoom slider with minimal chrome
 * - Legend table with stats (min/max/avg/last)
 * - Threshold line support (SLO / alert level)
 */

import * as echarts from 'echarts/core';
import type { ChartSeries } from '@/types/metric';

/** Grafana-inspired color palette, light theme optimized */
export const GRAFANA_COLORS = [
  '#5470c6',  // blue
  '#91cc75',  // green
  '#fac858',  // yellow
  '#ee6666',  // red
  '#73c0de',  // light blue
  '#fc8452',  // orange
  '#9a60b4',  // purple
  '#ea7ccc',  // pink
];

/** Statistics computed for each series (for legend display) */
export interface SeriesStats {
  name: string;
  min: number;
  max: number;
  avg: number;
  last: number;
}

/** Shared axis style matching Grafana's sparse grid aesthetic */
const gridLineStyle = {
  color: 'rgba(0, 0, 0, 0.04)',
  type: 'dashed' as const,
};

const axisLabelStyle = {
  color: '#6b7280',
  fontSize: 11,
  fontFamily: 'Inter, system-ui, sans-serif',
};

const axisLineStyle = {
  lineStyle: { color: 'rgba(0, 0, 0, 0.06)' },
};

/** Compute min/max/avg/last for a ChartSeries (raw values, no formatting) */
export function computeSeriesStats(series: ChartSeries[]): SeriesStats[] {
  return series.map(s => {
    const values = s.data.map(d => d.value);
    if (values.length === 0) return { name: s.name, min: 0, max: 0, avg: 0, last: 0 };
    return {
      name: s.name,
      min: Math.min(...values),
      max: Math.max(...values),
      avg: values.reduce((a, b) => a + b, 0) / values.length,
      // eslint-disable-next-line @typescript-eslint/no-non-null-assertion
      last: values[values.length - 1]!,
    };
  });
}

export function buildTimeSeriesOption(params: {
  series: ChartSeries[];
  chartType: 'line' | 'area';
  yAxisFormatter: (value: number) => string;
  showDataZoom?: boolean;
  legendPlacement?: 'bottom' | 'right';
  /** Show min/max/avg/last in legend (default false) */
  showLegendStats?: boolean;
  /** Horizontal threshold line value (e.g. SLO) */
  threshold?: { value: number; label: string; color?: string };
}): echarts.EChartsCoreOption {
  const {
    series, chartType, yAxisFormatter,
    showDataZoom = true,
    legendPlacement = 'bottom',
    showLegendStats = false,
    threshold,
  } = params;
  const isArea = chartType === 'area';
  const showLegend = series.length > 1;
  const stats = showLegendStats ? computeSeriesStats(series) : [];

  // -- Tooltip ----------------------------------------------------------
  const tooltip: echarts.EChartsCoreOption['tooltip'] = {
    trigger: 'axis',
    backgroundColor: 'rgba(255,255,255,0.98)',
    borderColor: '#e5e7eb',
    borderWidth: 1,
    textStyle: { color: '#374151', fontSize: 12 },
    axisPointer: { type: 'cross', crossStyle: { color: '#d1d5db' } },
    order: 'valueDesc' as const,
    formatter: (params: unknown) => {
      const items = (params as Array<{ seriesName: string; value: [number, number]; color: string }>);
      if (!items?.length) return '';
      const first = items[0]!;
      const time = new Date(first.value[0]).toLocaleString('zh-CN');
      let html = `<div style="font-weight:600;margin-bottom:4px;font-size:12px">${time}</div>`;
      for (const p of items) {
        const val = yAxisFormatter(p.value[1]);
        html += `<div style="display:flex;align-items:center;gap:6px;margin:2px 0;font-size:12px">
          <span style="display:inline-block;width:8px;height:8px;border-radius:50%;background:${p.color};flex-shrink:0"></span>
          <span style="flex:1;overflow:hidden;text-overflow:ellipsis;white-space:nowrap;max-width:200px">${p.seriesName}</span>
          <span style="font-weight:600;font-variant-numeric:tabular-nums">${val}</span>
        </div>`;
      }
      return html;
    },
  };

  // -- Legend -----------------------------------------------------------
  const legend: echarts.EChartsCoreOption['legend'] = showLegend
    ? legendPlacement === 'right'
      ? {
          type: 'scroll' as const,
          orient: 'vertical' as const,
          right: 0,
          top: 'middle',
          textStyle: { fontSize: 11, color: '#6b7280' },
          itemWidth: 12,
          itemHeight: 8,
          formatter: showLegendStats ? (name: string) => {
            const s = stats.find(st => st.name === name);
            if (!s) return name;
            return `${name}  min:${yAxisFormatter(s.min)}  max:${yAxisFormatter(s.max)}  avg:${yAxisFormatter(s.avg)}`;
          } : undefined,
        }
      : {
          bottom: 0,
          type: 'scroll' as const,
          textStyle: { fontSize: 11, color: '#6b7280' },
          itemWidth: 12,
          itemHeight: 8,
        }
    : { show: false };

  // -- Grid -------------------------------------------------------------
  const legendBottomHeight = showLegend && legendPlacement === 'bottom' ? 36 : 0;
  const dataZoomHeight = showDataZoom ? 24 : 0;
  const grid = {
    top: 12,
    right: legendPlacement === 'right' && showLegend ? 130 : 16,
    bottom: legendBottomHeight + dataZoomHeight + 4,
    left: 0,
    containLabel: true,
  };

  // -- X Axis -----------------------------------------------------------
  const xAxis: echarts.EChartsCoreOption['xAxis'] = {
    type: 'time',
    axisLine: axisLineStyle,
    axisTick: { show: false },
    axisLabel: {
      ...axisLabelStyle,
      formatter: (value: unknown) => {
        const d = new Date(value as number);
        return `${d.getHours().toString().padStart(2, '0')}:${d.getMinutes().toString().padStart(2, '0')}`;
      },
    },
    splitLine: { show: false },
  };

  // -- Y Axis -----------------------------------------------------------
  const yAxis: echarts.EChartsCoreOption['yAxis'] = {
    type: 'value',
    axisLine: { show: false },
    axisTick: { show: false },
    axisLabel: {
      ...axisLabelStyle,
      fontVariantNumeric: 'tabular-nums',
      formatter: (value: unknown) => yAxisFormatter(value as number),
    },
    splitLine: { lineStyle: gridLineStyle },
  };

  // -- DataZoom ---------------------------------------------------------
  const dataZoom: echarts.EChartsCoreOption['dataZoom'] = showDataZoom
    ? [
        {
          type: 'inside' as const,
          xAxisIndex: 0,
          zoomOnMouseWheel: true,
          moveOnMouseMove: true,
          moveOnMouseWheel: false,
        },
        {
          type: 'slider' as const,
          xAxisIndex: 0,
          height: 20,
          bottom: legendBottomHeight,
          borderColor: 'transparent',
          backgroundColor: 'rgba(0,0,0,0.03)',
          fillerColor: 'rgba(84,112,198,0.12)',
          handleStyle: { color: '#5470c6', width: 20 },
          textStyle: { fontSize: 10, color: '#9ca3af' },
          showDetail: false,
          brushSelect: true,
        },
      ]
    : undefined;

  // -- Threshold markLine (applied to first series) --------------------
  const thresholdMarkLine = threshold
    ? {
        silent: true,
        symbol: 'none',
        lineStyle: { type: 'dashed' as const, color: threshold.color ?? '#ef4444', width: 1 },
        label: {
          show: true,
          position: 'end' as const,
          formatter: `${threshold.label}: ${yAxisFormatter(threshold.value)}`,
          fontSize: 11,
          color: threshold.color ?? '#ef4444',
        },
        data: [{ yAxis: threshold.value }],
      }
    : undefined;

  // -- Series -----------------------------------------------------------
  const echartsSeries: echarts.EChartsCoreOption['series'] = series.map((s, i) => ({
    name: s.name,
    type: 'line' as const,
    data: s.data.map(d => [d.time, d.value]),
    smooth: true,
    symbol: 'none',
    lineStyle: {
      width: 1.5,
      color: GRAFANA_COLORS[i % GRAFANA_COLORS.length],
    },
    itemStyle: {
      color: GRAFANA_COLORS[i % GRAFANA_COLORS.length],
    },
    areaStyle: isArea
      ? {
          color: new echarts.graphic.LinearGradient(0, 0, 0, 1, [
            { offset: 0, color: GRAFANA_COLORS[i % GRAFANA_COLORS.length] + '40' },
            { offset: 1, color: GRAFANA_COLORS[i % GRAFANA_COLORS.length] + '05' },
          ]),
        }
      : undefined,
    // Apply threshold to first series only (avoids duplicate markLines)
    markLine: (i === 0 && thresholdMarkLine) ? thresholdMarkLine : undefined,
  }));

  return {
    tooltip,
    legend,
    grid,
    xAxis,
    yAxis,
    dataZoom,
    series: echartsSeries,
  };
}

export default buildTimeSeriesOption;
