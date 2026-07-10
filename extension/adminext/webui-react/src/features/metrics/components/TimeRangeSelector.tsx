/**
 * TimeRangeSelector — 时间范围选择器 + 自动刷新控制
 *
 * 功能：
 * - 预设时间范围按钮组 (15m ~ 7d)
 * - Custom Range 弹窗（From/To datetime-local picker）
 * - Auto-refresh 下拉控制（Off / 5s / 10s / 30s / 1m / 5m）
 *
 * 纯展示组件，通过 props 接收状态和回调。
 */

import { useState, useRef, useEffect } from 'react';
import { TIME_RANGE_PRESETS } from '@/utils/metric';
import { REFRESH_OPTIONS } from '@/features/metrics/hooks/useAutoRefresh';
import type { RefreshInterval } from '@/features/metrics/hooks/useAutoRefresh';

interface TimeRangeSelectorProps {
  value: string;
  onChange: (value: string) => void;
  /** Auto-refresh control (optional — omit to hide refresh UI) */
  refreshInterval?: RefreshInterval;
  onRefreshChange?: (v: RefreshInterval) => void;
}

export default function TimeRangeSelector({
  value,
  onChange,
  refreshInterval = 'off',
  onRefreshChange,
}: TimeRangeSelectorProps) {
  // --- Custom Range Popover State ---
  const [showCustom, setShowCustom] = useState(false);
  const [customFrom, setCustomFrom] = useState('');
  const [customTo, setCustomTo] = useState('');
  const popoverRef = useRef<HTMLDivElement>(null);

  // Close on outside click
  useEffect(() => {
    if (!showCustom) return;
    const handler = (e: MouseEvent) => {
      if (popoverRef.current && !popoverRef.current.contains(e.target as Node)) {
        setShowCustom(false);
      }
    };
    document.addEventListener('mousedown', handler);
    return () => document.removeEventListener('mousedown', handler);
  }, [showCustom]);

  const applyCustomRange = () => {
    if (customFrom && customTo) {
      const from = new Date(customFrom).getTime();
      const to = new Date(customTo).getTime();
      if (to > from) {
        const diffSec = Math.round((to - from) / 1000);
        onChange(`custom_${diffSec}`); // encoded seconds as range identifier
        // Update getTimeParams in useTimeRange to handle this custom prefix
      }
    }
    setShowCustom(false);
  };

  // --- Auto-refresh state ---
  const isRefreshing = refreshInterval !== 'off';

  return (
    <div className="flex items-center gap-3">
      <span className="text-sm text-gray-500 flex-shrink-0">
        <i className="fas fa-clock mr-1" />
        Range:
      </span>

      {/* Preset Buttons */}
      <div className="flex items-center gap-1 flex-wrap">
        {TIME_RANGE_PRESETS.map(preset => (
          <button
            key={preset.value}
            onClick={() => onChange(preset.value)}
            className={`px-3 py-1.5 rounded-md text-xs font-medium transition whitespace-nowrap ${
              value === preset.value
                ? 'bg-primary-600 text-white shadow-sm'
                : 'bg-gray-100 text-gray-500 hover:bg-gray-200 hover:text-gray-700'
            }`}
          >
            {preset.label}
          </button>
        ))}

        {/* Custom Range Trigger */}
        <div className="relative" ref={popoverRef}>
          <button
            onClick={() => setShowCustom(!showCustom)}
            className={`px-3 py-1.5 rounded-md text-xs font-medium transition whitespace-nowrap ${
              value.startsWith('custom_')
                ? 'bg-primary-600 text-white shadow-sm'
                : 'bg-gray-100 text-gray-500 hover:bg-gray-200 hover:text-gray-700'
            }`}
          >
            <i className="fas fa-calendar-alt mr-1" />
            Custom
          </button>

          {/* Custom Range Popover */}
          {showCustom && (
            <div className="absolute top-full left-0 mt-2 bg-white border border-gray-200 rounded-lg shadow-lg p-4 z-50 w-72">
              <div className="space-y-3">
                <div>
                  <label className="block text-xs font-medium text-gray-500 mb-1">From</label>
                  <input
                    type="datetime-local"
                    value={customFrom}
                    onChange={(e) => setCustomFrom(e.target.value)}
                    className="w-full px-3 py-1.5 border border-gray-200 rounded text-sm focus:ring-2 focus:ring-primary-500 focus:border-primary-500"
                  />
                </div>
                <div>
                  <label className="block text-xs font-medium text-gray-500 mb-1">To</label>
                  <input
                    type="datetime-local"
                    value={customTo}
                    onChange={(e) => setCustomTo(e.target.value)}
                    className="w-full px-3 py-1.5 border border-gray-200 rounded text-sm focus:ring-2 focus:ring-primary-500 focus:border-primary-500"
                  />
                </div>
                <div className="flex gap-2">
                  <button
                    onClick={applyCustomRange}
                    disabled={!customFrom || !customTo}
                    className="flex-1 px-3 py-1.5 bg-primary-600 text-white rounded text-xs font-medium hover:bg-primary-700 transition disabled:opacity-50"
                  >
                    Apply
                  </button>
                  <button
                    onClick={() => setShowCustom(false)}
                    className="px-3 py-1.5 bg-gray-100 text-gray-600 rounded text-xs hover:bg-gray-200 transition"
                  >
                    Cancel
                  </button>
                </div>
              </div>
            </div>
          )}
        </div>
      </div>

      {/* Auto-refresh (optional) */}
      {onRefreshChange && (
        <div className="flex items-center gap-2 ml-auto">
          {isRefreshing && (
            <span className="text-xs text-gray-400">
              <i className="fas fa-sync-alt fa-spin mr-1 text-primary-500" />
              {refreshInterval}
            </span>
          )}
          <select
            value={refreshInterval}
            onChange={(e) => onRefreshChange(e.target.value as RefreshInterval)}
            className="px-2 py-1.5 bg-gray-100 border border-gray-200 rounded-md text-xs text-gray-600 focus:ring-2 focus:ring-primary-500"
          >
            {REFRESH_OPTIONS.map(opt => (
              <option key={opt.value} value={opt.value}>{opt.label}</option>
            ))}
          </select>
        </div>
      )}
    </div>
  );
}
