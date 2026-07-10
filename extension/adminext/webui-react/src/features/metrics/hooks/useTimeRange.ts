/**
 * useTimeRange — 时间范围管理 hook
 *
 * 职责：
 * - 管理当前选中的时间范围预设值
 * - 提供当前时间范围的 start/end/step 参数（供 API 调用）
 * - 解耦时间计算逻辑和视图
 */

import { useState, useCallback, useMemo } from 'react';
import { calculateStep, TIME_RANGE_PRESETS } from '@/utils/metric';

export interface TimeParams {
  start: number;   // Unix ms
  end: number;     // Unix ms
  step: string;    // e.g. "15s"
  seconds: number; // total range in seconds
}

export interface UseTimeRangeReturn {
  timeRange: string;
  setTimeRange: (value: string) => void;
  getParams: () => TimeParams;
  /** Current time params for reactive use (derived from timeRange) */
  params: TimeParams;
}

export function useTimeRange(initialValue = '1h'): UseTimeRangeReturn {
  const [timeRange, setTimeRange] = useState(initialValue);

  const getParams = useCallback((): TimeParams => {
    const preset = TIME_RANGE_PRESETS.find(p => p.value === timeRange);
    const seconds = preset?.seconds ?? 3600;
    const end = Date.now();
    const start = end - seconds * 1000;
    const step = calculateStep(seconds);
    return { start, end, step, seconds };
  }, [timeRange]);

  // Derived reactive params (recomputed when timeRange changes)
  const params = useMemo(() => getParams(), [getParams]);

  return { timeRange, setTimeRange, getParams, params };
}

export default useTimeRange;
