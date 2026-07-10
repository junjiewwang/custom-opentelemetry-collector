/**
 * useRedPanels — RED Dashboard 面板数据管理 hook
 *
 * 职责：
 * - 管理 service 选择和 service 列表
 * - 管理 6 个 RED 面板的 loading / error / series 状态
 * - 封装并行 API 调用和数据转换
 */

import { useState, useEffect, useCallback } from 'react';
import { apiClient } from '@/api/client';
import { seriesToChartSeries, RED_PANELS } from '@/utils/metric';
import type { ChartSeries } from '@/types/metric';
import type { TimeParams } from './useTimeRange';

export interface PanelData {
  series: ChartSeries[];
  loading: boolean;
  error: string;
}

export interface UseRedPanelsReturn {
  redService: string;
  setRedService: (v: string) => void;
  services: string[];
  panelData: Record<string, PanelData>;
  loadPanels: () => Promise<void>;
}

export function useRedPanels(
  getTimeParams: () => TimeParams,
  activeTab: string,
  initialService?: string,
): UseRedPanelsReturn {
  const [redService, setRedService] = useState(initialService ?? '');
  const [services, setServices] = useState<string[]>([]);
  const [panelData, setPanelData] = useState<Record<string, PanelData>>({});

  // Apply initialService from URL params (e.g. from Trace page deep link)
  useEffect(() => {
    if (initialService && !redService) {
      setRedService(initialService);
    }
  }, [initialService, redService]);

  // Load service list
  useEffect(() => {
    loadServices();
  }, []);

  const loadServices = async () => {
    try {
      const resp = await apiClient.getMetricLabelValues('service_name');
      if (resp.data) {
        setServices(resp.data.sort());
        return;
      }
    } catch {
      try {
        const resp = await apiClient.getTraceServices();
        setServices(resp.data.map(s => s.name).sort());
      } catch {
        setServices([]);
      }
    }
  };

  const loadPanels = useCallback(async () => {
    if (!redService) return;

    const { start, end, step } = getTimeParams();

    // Initialize all panels to loading
    const initialState: Record<string, PanelData> = {};
    for (const panel of RED_PANELS) {
      initialState[panel.id] = { series: [], loading: true, error: '' };
    }
    setPanelData(initialState);

    // Parallel fetch all panels
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
          setPanelData(prev => ({
            ...prev,
            [panel.id]: { series, loading: false, error: '' },
          }));
        } catch (err: unknown) {
          const apiErr = err as { message?: string };
          setPanelData(prev => ({
            ...prev,
            [panel.id]: { series: [], loading: false, error: apiErr.message ?? 'Query failed' },
          }));
        }
      }),
    );
  }, [redService, getTimeParams]);

  // Auto-load panels when service changes and RED tab is active
  useEffect(() => {
    if (redService && activeTab === 'red') {
      loadPanels();
    }
  }, [redService, activeTab, loadPanels]);

  return { redService, setRedService, services, panelData, loadPanels };
}

export default useRedPanels;
