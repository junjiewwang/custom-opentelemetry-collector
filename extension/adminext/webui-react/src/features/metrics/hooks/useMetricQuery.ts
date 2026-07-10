/**
 * useMetricQuery — Metric 查询面板状态管理 hook
 *
 * 职责：
 * - 管理 metric name / service filter / aggregation / groupBy / fill 状态
 * - 管理查询 loading / error / result 状态
 * - 封装 API 调用和数据转换
 */

import { useState, useCallback } from 'react';
import { apiClient } from '@/api/client';
import { seriesToChartSeries } from '@/utils/metric';
import type { ChartSeries } from '@/types/metric';
import type { TimeParams } from './useTimeRange';

export interface UseMetricQueryReturn {
  metricInput: string;
  setMetricInput: (v: string) => void;
  serviceFilter: string;
  setServiceFilter: (v: string) => void;
  aggregation: string;
  setAggregation: (v: string) => void;
  groupBy: string;
  setGroupBy: (v: string) => void;
  fill: string;
  setFill: (v: string) => void;
  chartSeries: ChartSeries[];
  loading: boolean;
  error: string;
  executeQuery: () => Promise<void>;
}

export function useMetricQuery(getTimeParams: () => TimeParams): UseMetricQueryReturn {
  const [metricInput, setMetricInput] = useState('');
  const [serviceFilter, setServiceFilter] = useState('');
  const [aggregation, setAggregation] = useState('avg');
  const [groupBy, setGroupBy] = useState('');
  const [fill, setFill] = useState('null');
  const [chartSeries, setChartSeries] = useState<ChartSeries[]>([]);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState('');

  const executeQuery = useCallback(async () => {
    if (!metricInput.trim()) {
      setError('Please enter a metric name');
      return;
    }

    setLoading(true);
    setError('');

    try {
      const { start, end, step } = getTimeParams();
      const resp = await apiClient.metricQueryRange({
        metric: metricInput.trim(),
        service: serviceFilter || undefined,
        start,
        end,
        step,
        aggregation: aggregation !== 'avg' ? aggregation : undefined,
        groupBy: groupBy || undefined,
        fill: fill !== 'null' ? fill : undefined,
      });

      const series = seriesToChartSeries(resp);
      setChartSeries(series);

      if (series.length === 0) {
        setError('Query returned no data');
      }
    } catch (err: unknown) {
      const apiErr = err as { message?: string };
      setError(apiErr.message ?? 'Query failed');
      setChartSeries([]);
    } finally {
      setLoading(false);
    }
  }, [metricInput, serviceFilter, aggregation, groupBy, fill, getTimeParams]);

  return {
    metricInput,
    setMetricInput,
    serviceFilter,
    setServiceFilter,
    aggregation,
    setAggregation,
    groupBy,
    setGroupBy,
    fill,
    setFill,
    chartSeries,
    loading,
    error,
    executeQuery,
  };
}

export default useMetricQuery;
