/**
 * MetricQueryPanel — Metric 查询输入面板
 *
 * 对标 Grafana InfluxDB Query Builder：
 * - Metric name 搜索（Combobox）
 * - Aggregation 函数选择（下拉）
 * - Service / Group By / Fill 参数
 * - GROUP BY labels 多选标签下拉框
 * - 示例 metric 快捷按钮
 */

import { useState, useRef, useEffect, useCallback, useMemo } from 'react';
import MetricNameCombobox from '@/features/metrics/components/MetricNameCombobox';
import { AGGREGATION_OPTIONS, FILL_OPTIONS } from '@/types/metric';
import type { UseMetricQueryReturn } from '@/features/metrics/hooks/useMetricQuery';

interface MetricQueryPanelProps {
  query: UseMetricQueryReturn;
  metricNames: string[];
  labelNames: string[];
}

const EXAMPLE_METRICS = [
  { label: 'HTTP Duration', metric: 'http_server_request_duration_seconds' },
  { label: 'HTTP Count', metric: 'http_server_request_duration_seconds_count' },
  { label: 'CPU Usage', metric: 'process_cpu_seconds_total' },
  { label: 'Memory', metric: 'process_resident_memory_bytes' },
  { label: 'Goroutines', metric: 'go_goroutines' },
];

export default function MetricQueryPanel({ query, metricNames, labelNames }: MetricQueryPanelProps) {
  const {
    metricInput, setMetricInput,
    serviceFilter, setServiceFilter,
    aggregation, setAggregation,
    groupBy, setGroupBy,
    fill, setFill,
    loading, error, executeQuery,
  } = query;

  // -- GROUP BY tag multi-select state ---------------------------------
  const [dropdownOpen, setDropdownOpen] = useState(false);
  const [filterText, setFilterText] = useState('');
  const dropdownRef = useRef<HTMLDivElement>(null);
  const inputRef = useRef<HTMLInputElement>(null);

  // Parse current groupBy string into tag array
  const selectedTags = useMemo(
    () => groupBy ? groupBy.split(',').map(s => s.trim()).filter(Boolean) : [],
    [groupBy],
  );

  // Available labels not yet selected, filtered by typed text
  const availableLabels = useMemo(
    () => labelNames.filter(l => !selectedTags.includes(l) && l.toLowerCase().includes(filterText.toLowerCase())),
    [labelNames, selectedTags, filterText],
  );

  // Close dropdown when clicking outside
  useEffect(() => {
    const handler = (e: MouseEvent) => {
      if (dropdownRef.current && !dropdownRef.current.contains(e.target as Node)) {
        setDropdownOpen(false);
        setFilterText('');
      }
    };
    document.addEventListener('mousedown', handler);
    return () => document.removeEventListener('mousedown', handler);
  }, []);

  const addTag = useCallback((label: string) => {
    const newTags = [...selectedTags, label].filter((v, i, a) => a.indexOf(v) === i);
    setGroupBy(newTags.join(','));
    setFilterText('');
    inputRef.current?.focus();
  }, [selectedTags, setGroupBy]);

  const removeTag = useCallback((label: string) => {
    setGroupBy(selectedTags.filter(t => t !== label).join(','));
  }, [selectedTags, setGroupBy]);

  const handleKeyDown = useCallback((e: React.KeyboardEvent) => {
    if (e.key === 'Enter') {
      if (filterText.trim() && labelNames.includes(filterText.trim())) {
        e.preventDefault();
        addTag(filterText.trim());
      } else {
        setDropdownOpen(false);
        executeQuery();
      }
    } else if (e.key === 'Escape') {
      setDropdownOpen(false);
      setFilterText('');
    } else if (e.key === 'Backspace' && filterText === '' && selectedTags.length > 0) {
      const lastTag = selectedTags[selectedTags.length - 1];
      if (lastTag) removeTag(lastTag);
    }
  }, [filterText, labelNames, addTag, removeTag, selectedTags, executeQuery]);

  return (
    <div className="bg-white rounded-xl shadow-sm border border-gray-200 p-6">
      {/* Main Row: Metric + Aggregation + Query Button */}
      <div className="flex items-end gap-3">
        <div className="flex-1">
          <label className="block text-sm font-medium text-gray-700 mb-1">FROM</label>
          <MetricNameCombobox
            value={metricInput}
            onChange={setMetricInput}
            onSelect={executeQuery}
            names={metricNames}
            placeholder="e.g. http_server_request_duration_seconds"
            disabled={loading}
          />
        </div>

        <div className="w-36">
          <label className="block text-sm font-medium text-gray-500 mb-1">SELECT</label>
          <select
            value={aggregation}
            onChange={(e) => setAggregation(e.target.value)}
            className="w-full px-3 py-2 border border-gray-200 rounded-lg text-sm bg-white focus:ring-2 focus:ring-primary-500 focus:border-primary-500"
          >
            {AGGREGATION_OPTIONS.map(opt => (
              <option key={opt.value} value={opt.value}>{opt.label}</option>
            ))}
          </select>
        </div>

        <button
          onClick={executeQuery}
          disabled={loading || !metricInput.trim()}
          className="px-6 py-2 bg-primary-600 text-white rounded-lg hover:bg-primary-700 transition flex items-center gap-2 disabled:opacity-50 h-[42px]"
        >
          {loading ? <i className="fas fa-spinner fa-spin" /> : <i className="fas fa-play" />}
          <span>Run Query</span>
        </button>
      </div>

      {/* Advanced Row: Service / GroupBy / Fill */}
      <div className="flex items-end gap-3 mt-3">
        <div className="w-44">
          <label className="block text-xs font-medium text-gray-400 mb-1">WHERE service</label>
          <input
            type="text"
            value={serviceFilter}
            onChange={(e) => setServiceFilter(e.target.value)}
            onKeyDown={(e) => e.key === 'Enter' && executeQuery()}
            placeholder="Service (optional)"
            className="w-full px-3 py-2 border border-gray-200 rounded-lg text-sm focus:ring-2 focus:ring-primary-500 focus:border-primary-500"
          />
        </div>

        <div className="flex-1" ref={dropdownRef}>
          <label className="block text-xs font-medium text-gray-400 mb-1">GROUP BY labels</label>
          <div
            className={`flex items-center flex-wrap gap-1 px-2 py-1.5 border rounded-lg text-sm cursor-text transition-colors ${
              dropdownOpen ? 'ring-2 ring-primary-500 border-primary-500' : 'border-gray-200'
            }`}
            onClick={() => { setDropdownOpen(true); inputRef.current?.focus(); }}
          >
            {selectedTags.map(tag => (
              <span
                key={tag}
                className="inline-flex items-center gap-1 px-2 py-0.5 bg-primary-50 text-primary-700 text-xs rounded-full border border-primary-200"
              >
                {tag}
                <button
                  type="button"
                  onClick={(e) => { e.stopPropagation(); removeTag(tag); }}
                  className="text-primary-400 hover:text-primary-600 leading-none"
                >
                  ×
                </button>
              </span>
            ))}
            <input
              ref={inputRef}
              type="text"
              value={filterText}
              onChange={(e) => { setFilterText(e.target.value); setDropdownOpen(true); }}
              onFocus={() => setDropdownOpen(true)}
              onKeyDown={handleKeyDown}
              placeholder={selectedTags.length === 0 ? 'Select labels to group by...' : ''}
              className="flex-1 min-w-[100px] bg-transparent outline-none text-sm py-0.5"
            />
          </div>
          {dropdownOpen && labelNames.length > 0 && (
            <div className="relative">
              <div className="absolute z-20 mt-1 w-full bg-white border border-gray-200 rounded-lg shadow-lg max-h-48 overflow-y-auto">
                {availableLabels.length > 0 ? (
                  availableLabels.map(label => (
                    <div
                      key={label}
                      onClick={() => addTag(label)}
                      className="px-3 py-2 text-sm cursor-pointer hover:bg-primary-50 transition-colors"
                    >
                      <span className="text-gray-700">{label}</span>
                    </div>
                  ))
                ) : (
                  <div className="px-3 py-2 text-sm text-gray-400">
                    {filterText ? 'No matching labels' : 'All labels selected'}
                  </div>
                )}
              </div>
            </div>
          )}
        </div>

        <div className="w-32">
          <label className="block text-xs font-medium text-gray-400 mb-1">FILL</label>
          <select
            value={fill}
            onChange={(e) => setFill(e.target.value)}
            className="w-full px-3 py-2 border border-gray-200 rounded-lg text-sm bg-white focus:ring-2 focus:ring-primary-500 focus:border-primary-500"
          >
            {FILL_OPTIONS.map(opt => (
              <option key={opt.value} value={opt.value}>{opt.label}</option>
            ))}
          </select>
        </div>
      </div>

      {/* Example Metrics */}
      <div className="mt-3 flex items-center gap-2 flex-wrap">
        <span className="text-xs text-gray-400">Examples:</span>
        {EXAMPLE_METRICS.map(eq => (
          <button
            key={eq.label}
            onClick={() => setMetricInput(eq.metric)}
            className="px-2 py-0.5 bg-gray-100 text-gray-600 text-xs rounded hover:bg-gray-200 transition"
          >
            {eq.label}
          </button>
        ))}
      </div>

      {/* Error */}
      {error && (
        <div className="mt-4 px-4 py-2 bg-red-50 text-red-600 rounded-lg text-sm">
          <i className="fas fa-exclamation-circle mr-2" />
          {error}
        </div>
      )}
    </div>
  );
}
