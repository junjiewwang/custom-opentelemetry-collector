/**
 * useMetricAvailability — Metric 后端可用性检测 hook
 *
 * 职责：
 * - 检测 metric storage backend 是否可用
 * - 加载 metric names 列表（autocomplete）
 * - 返回 null/true/false 三态
 */

import { useState, useEffect } from 'react';
import { apiClient } from '@/api/client';

export interface UseMetricAvailabilityReturn {
  available: boolean | null; // null=checking, true=available, false=unavailable
  metricNames: string[];
}

export function useMetricAvailability(): UseMetricAvailabilityReturn {
  const [available, setAvailable] = useState<boolean | null>(null);
  const [metricNames, setMetricNames] = useState<string[]>([]);

  useEffect(() => {
    checkAvailability();
  }, []);

  useEffect(() => {
    if (available) {
      loadMetricNames();
    }
  }, [available]);

  const checkAvailability = async () => {
    try {
      await apiClient.getMetricLabels();
      setAvailable(true);
    } catch {
      setAvailable(false);
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

  return { available, metricNames };
}

export default useMetricAvailability;
