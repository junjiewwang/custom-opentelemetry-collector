/**
 * useAutoRefresh — 自动刷新 hook
 *
 * 职责：
 * - 管理刷新间隔（Off / 5s / 10s / 30s / 1m / 5m）
 * - 定时触发回调（如 executeQuery / loadPanels）
 * - 在时间范围变化时重置 timer
 */

import { useState, useEffect, useRef, useCallback } from 'react';

export type RefreshInterval = 'off' | '5s' | '10s' | '30s' | '1m' | '5m';

const INTERVAL_MS: Record<RefreshInterval, number> = {
  off: 0,
  '5s': 5000,
  '10s': 10000,
  '30s': 30000,
  '1m': 60000,
  '5m': 300000,
};

export const REFRESH_OPTIONS: { label: string; value: RefreshInterval }[] = [
  { label: 'Off', value: 'off' },
  { label: '5s', value: '5s' },
  { label: '10s', value: '10s' },
  { label: '30s', value: '30s' },
  { label: '1m', value: '1m' },
  { label: '5m', value: '5m' },
];

export interface UseAutoRefreshReturn {
  interval: RefreshInterval;
  setInterval: (v: RefreshInterval) => void;
  isActive: boolean;
}

export function useAutoRefresh(
  callback: () => void,
  initialInterval: RefreshInterval = 'off',
): UseAutoRefreshReturn {
  const [interval, setIntervalState] = useState<RefreshInterval>(initialInterval);
  const callbackRef = useRef(callback);

  // Keep callback ref fresh without re-triggering the effect
  useEffect(() => {
    callbackRef.current = callback;
  }, [callback]);

  const setInterval_ = useCallback((v: RefreshInterval) => {
    setIntervalState(v);
  }, []);

  const isActive = interval !== 'off';
  const ms = INTERVAL_MS[interval];

  useEffect(() => {
    if (!ms) return;

    const id = window.setInterval(() => {
      callbackRef.current();
    }, ms);

    return () => window.clearInterval(id);
  }, [ms]);

  return { interval, setInterval: setInterval_, isActive };
}

export default useAutoRefresh;
