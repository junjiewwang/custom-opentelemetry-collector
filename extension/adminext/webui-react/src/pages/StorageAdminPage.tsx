/**
 * Storage Admin 页面 - 存储管理与状态监控
 *
 * 功能：
 * - 存储健康状态概览
 * - 磁盘使用量（总量/已用/可用 + 按信号类型分布）
 * - 按天存储用量趋势图（按 App 分线，按信号分线）
 * - 索引列表（名称、文档数、大小、信号类型）
 * - 数据清除操作
 *
 * 注意：保留策略（retention）已移至 App 配置页面管理，属于业务配置而非存储运维。
 */

import { useState, useEffect, useCallback, useMemo } from 'react';
import { apiClient } from '@/api/client';
import type { App } from '@/types/api';
import type {
  StorageStatus,
  StorageHealth,
  DiskUsage,
  IndexInfo,
  SignalType,
  DailyStorageResponse,
} from '@/types/storage';
import { formatBytes, DAILY_RANGES } from '@/types/storage';
import TimeSeriesChart from '@/components/TimeSeriesChart';
import type { ChartSeries } from '@/types/metric';

// ============================================================================
// Component
// ============================================================================

export default function StorageAdminPage() {
  // ========================================================================
  // State
  // ========================================================================

  const [status, setStatus] = useState<StorageStatus | null>(null);
  const [health, setHealth] = useState<StorageHealth | null>(null);
  const [diskUsage, setDiskUsage] = useState<DiskUsage | null>(null);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState('');
  const [available, setAvailable] = useState<boolean | null>(null);

  // Apps
  const [apps, setApps] = useState<App[]>([]);
  const [selectedAppId, setSelectedAppId] = useState('');

  // Daily usage chart
  const [dailyData, setDailyData] = useState<DailyStorageResponse | null>(null);
  const [dailyLoading, setDailyLoading] = useState(false);
  const [dailyRangeDays, setDailyRangeDays] = useState(7);

  // ========================================================================
  // Load data
  // ========================================================================

  const loadData = useCallback(async () => {
    setLoading(true);
    setError('');

    try {
      const [statusRes, healthRes, diskRes, appsRes] = await Promise.allSettled([
        apiClient.getStorageStatus(),
        apiClient.getStorageHealth(),
        apiClient.getStorageDiskUsage(),
        apiClient.getApps(),
      ]);

      if (statusRes.status === 'fulfilled') setStatus(statusRes.value);
      else if ((statusRes.reason as { status?: number })?.status === 503) {
        setAvailable(false);
        setLoading(false);
        return;
      }

      if (healthRes.status === 'fulfilled') setHealth(healthRes.value);
      if (diskRes.status === 'fulfilled') setDiskUsage(diskRes.value);
      if (appsRes.status === 'fulfilled') setApps(appsRes.value);

      setAvailable(true);
    } catch (err: unknown) {
      const e = err as { message?: string };
      setError(e.message || 'Failed to load storage status');
    } finally {
      setLoading(false);
    }
  }, []);

  useEffect(() => {
    loadData();
  }, [loadData]);

  // Load daily usage data when range or app changes
  const loadDailyUsage = useCallback(async () => {
    setDailyLoading(true);
    try {
      const end = new Date();
      const start = new Date(end.getTime() - dailyRangeDays * 24 * 60 * 60 * 1000);
      const resp = await apiClient.getStorageDailyUsage({
        start: start.toISOString(),
        end: end.toISOString(),
        appId: selectedAppId || undefined,
      });
      setDailyData(resp);
    } catch {
      // Daily API is optional — silently ignore if not available
    } finally {
      setDailyLoading(false);
    }
  }, [dailyRangeDays, selectedAppId]);

  useEffect(() => {
    if (available) {
      loadDailyUsage();
    }
  }, [available, loadDailyUsage]);

  // ========================================================================
  // Handlers
  // ========================================================================

  // ========================================================================
  // Render: Not Available
  // ========================================================================

  if (available === false) {
    return (
      <div className="p-6">
        <div className="bg-yellow-50 border border-yellow-200 rounded-lg p-4 text-yellow-800">
          <h3 className="font-semibold text-lg mb-2">存储管理不可用</h3>
          <p className="text-sm">
            存储管理需要配置 <code className="bg-yellow-100 px-1 rounded">observability.storage_extension</code>。
            请确保 observabilitystorageext 扩展已启用并正确配置。
          </p>
        </div>
      </div>
    );
  }

  if (loading) {
    return (
      <div className="p-6 flex items-center justify-center min-h-[300px]">
        <div className="text-gray-500 animate-pulse">加载存储状态...</div>
      </div>
    );
  }

  // ========================================================================
  // Render
  // ========================================================================

  return (
    <div className="p-6 space-y-6">
      {/* Page Header */}
      <div className="flex items-center justify-between gap-4">
        <h1 className="text-2xl font-bold text-gray-900 dark:text-gray-100">存储管理</h1>
        {apps.length > 0 && (
          <select
            value={selectedAppId}
            onChange={e => setSelectedAppId(e.target.value)}
            className="px-3 py-1.5 text-sm border border-gray-300 dark:border-gray-600 rounded-md
              bg-white dark:bg-gray-700 text-gray-700 dark:text-gray-200"
          >
            <option value="">全部 App</option>
            {apps.map(a => (
              <option key={a.id} value={a.id}>{a.name || a.id}</option>
            ))}
          </select>
        )}
        <button
          onClick={loadData}
          className="px-3 py-1.5 text-sm border border-gray-300 dark:border-gray-600 rounded-md
            hover:bg-gray-50 dark:hover:bg-gray-700 transition-colors"
        >
          刷新
        </button>
      </div>

      {/* Error */}
      {error && (
        <div className="bg-red-50 border border-red-200 rounded-lg p-3 text-red-700 text-sm">
          {error}
          <button onClick={() => setError('')} className="ml-2 underline">关闭</button>
        </div>
      )}

      {/* Health + Overview Cards */}
      <div className="grid grid-cols-1 md:grid-cols-3 gap-4">
        {/* Health Card */}
        <div className="bg-white dark:bg-gray-800 rounded-lg shadow-sm border border-gray-200 dark:border-gray-700 p-4">
          <h3 className="text-sm font-medium text-gray-500 dark:text-gray-400 mb-2">健康状态</h3>
          <div className="flex items-center gap-2">
            <span className={`w-3 h-3 rounded-full ${health?.healthy ? 'bg-green-500' : 'bg-red-500'}`} />
            <span className="text-lg font-semibold text-gray-900 dark:text-gray-100">
              {health?.healthy ? '健康' : '异常'}
            </span>
          </div>
          {health?.latency_ms !== undefined && (
            <p className="text-xs text-gray-500 mt-1">延迟: {health.latency_ms}ms</p>
          )}
        </div>

        {/* Provider Card */}
        <div className="bg-white dark:bg-gray-800 rounded-lg shadow-sm border border-gray-200 dark:border-gray-700 p-4">
          <h3 className="text-sm font-medium text-gray-500 dark:text-gray-400 mb-2">存储后端</h3>
          <p className="text-lg font-semibold text-gray-900 dark:text-gray-100">
            {status?.provider || '—'}
          </p>
          {status?.version && (
            <p className="text-xs text-gray-500 mt-1">版本: {status.version}</p>
          )}
        </div>

        {/* Disk Usage Card */}
        <div className="bg-white dark:bg-gray-800 rounded-lg shadow-sm border border-gray-200 dark:border-gray-700 p-4">
          <h3 className="text-sm font-medium text-gray-500 dark:text-gray-400 mb-2">磁盘使用</h3>
          {diskUsage ? (
            <>
              <p className="text-lg font-semibold text-gray-900 dark:text-gray-100">
                {formatBytes(diskUsage.usedBytes)} / {formatBytes(diskUsage.totalBytes)}
              </p>
              <div className="mt-2 h-2 bg-gray-200 dark:bg-gray-700 rounded-full overflow-hidden">
                <div
                  className="h-full bg-blue-500 rounded-full transition-all"
                  style={{ width: `${diskUsage.totalBytes > 0 ? (diskUsage.usedBytes / diskUsage.totalBytes * 100) : 0}%` }}
                />
              </div>
              <p className="text-xs text-gray-500 mt-1">可用: {formatBytes(diskUsage.availableBytes)}</p>
            </>
          ) : (
            <p className="text-gray-400">—</p>
          )}
        </div>
      </div>

      {/* Disk Usage by Signal */}
      {diskUsage?.bySignal && Object.keys(diskUsage.bySignal).length > 0 && (
        <div className="bg-white dark:bg-gray-800 rounded-lg shadow-sm border border-gray-200 dark:border-gray-700 p-4">
          <h3 className="text-sm font-medium text-gray-700 dark:text-gray-300 mb-3">按信号类型分布</h3>
          <div className="grid grid-cols-3 gap-4">
            {(['trace', 'metric', 'log'] as SignalType[]).map(signal => (
              <div key={signal} className="text-center">
                <p className="text-xs text-gray-500 uppercase">{signal}</p>
                <p className="text-lg font-semibold text-gray-900 dark:text-gray-100">
                  {diskUsage.bySignal?.[signal] ? formatBytes(diskUsage.bySignal[signal]) : '—'}
                </p>
              </div>
            ))}
          </div>
        </div>
      )}

      {/* Daily Storage Usage Chart */}
      <DailyUsageSection
        data={dailyData}
        loading={dailyLoading}
        rangeDays={dailyRangeDays}
        onRangeChange={setDailyRangeDays}
        appName={selectedAppId ? (apps.find(a => a.id === selectedAppId)?.name || selectedAppId) : undefined}
        apps={apps}
      />

      {/* Indices Table */}
      {status?.indices && status.indices.length > 0 && (
        <div className="bg-white dark:bg-gray-800 rounded-lg shadow-sm border border-gray-200 dark:border-gray-700 overflow-hidden">
          <div className="px-4 py-3 border-b border-gray-200 dark:border-gray-700">
            <h3 className="text-sm font-medium text-gray-700 dark:text-gray-300">索引列表</h3>
          </div>
          <div className="overflow-x-auto">
            <table className="w-full text-sm">
              <thead className="bg-gray-50 dark:bg-gray-900/50">
                <tr>
                  <th className="px-4 py-2 text-left text-xs font-medium text-gray-500 uppercase">名称</th>
                  <th className="px-4 py-2 text-left text-xs font-medium text-gray-500 uppercase">信号类型</th>
                  <th className="px-4 py-2 text-right text-xs font-medium text-gray-500 uppercase">文档数</th>
                  <th className="px-4 py-2 text-right text-xs font-medium text-gray-500 uppercase">大小</th>
                </tr>
              </thead>
              <tbody className="divide-y divide-gray-100 dark:divide-gray-700">
                {status.indices.map((idx: IndexInfo) => (
                  <tr key={idx.name} className="hover:bg-gray-50 dark:hover:bg-gray-700/50">
                    <td className="px-4 py-2 font-mono text-xs text-gray-800 dark:text-gray-200">
                      {idx.name}
                    </td>
                    <td className="px-4 py-2">
                      <SignalBadge signal={idx.signal} />
                    </td>
                    <td className="px-4 py-2 text-right text-gray-700 dark:text-gray-300">
                      {idx.docsCount.toLocaleString()}
                    </td>
                    <td className="px-4 py-2 text-right text-gray-700 dark:text-gray-300">
                      {formatBytes(idx.sizeBytes)}
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        </div>
      )}
    </div>
  );
}

// ============================================================================
// Helper Components
// ============================================================================

// ============================================================================
// DailyUsageSection
// ============================================================================

interface DailyUsageSectionProps {
  data: DailyStorageResponse | null;
  loading: boolean;
  rangeDays: number;
  onRangeChange: (days: number) => void;
  appName?: string;
  apps?: App[];
}

function DailyUsageSection({ data, loading, rangeDays, onRangeChange, appName, apps }: DailyUsageSectionProps) {
  const appNameMap = useMemo(() => {
    const m: Record<string, string> = {};
    apps?.forEach(a => { m[a.id] = a.name || a.id; });
    return m;
  }, [apps]);

  const series: ChartSeries[] = useMemo(() => {
    if (!data || data.points.length === 0) return [];

    if (appName) {
      // Specific App → one line per signal
      const signals = ['trace', 'metric', 'log'];
      return signals
        .filter(signal => data.points.some(p => (p.bySignal?.[signal] ?? 0) > 0))
        .map(signal => ({
          name: `${appName} - ${signal}`,
          labels: {},
          data: data.points.map(p => ({
            time: new Date(p.date).getTime(),
            value: p.bySignal?.[signal] ?? 0,
          })),
        }));
    }

    // All Apps → one line per app (show as "appId (名称)")
    const appIds = new Set<string>();
    data.points.forEach(p => Object.keys(p.byApp || {}).forEach(id => appIds.add(id)));
    return Array.from(appIds).sort().map(appId => ({
      name: appNameMap[appId] ? `${appId} (${appNameMap[appId]})` : appId,
      labels: {},
      data: data.points.map(p => ({
        time: new Date(p.date).getTime(),
        value: p.byApp?.[appId] ?? 0,
      })),
    }));
  }, [data, appName, appNameMap]);

  return (
    <div className="bg-white dark:bg-gray-800 rounded-lg shadow-sm border border-gray-200 dark:border-gray-700 p-4">
      <div className="flex items-center justify-between mb-3">
        <h3 className="text-sm font-medium text-gray-700 dark:text-gray-300">
          按天存储用量{appName ? <span className="ml-1 text-blue-600">{appName}</span> : null}
        </h3>
        <div className="flex gap-1">
          {DAILY_RANGES.map(r => (
            <button
              key={r.days}
              onClick={() => onRangeChange(r.days)}
              className={`px-2 py-1 text-xs rounded transition-colors ${
                rangeDays === r.days
                  ? 'bg-blue-100 text-blue-700 dark:bg-blue-900/30 dark:text-blue-300'
                  : 'text-gray-500 hover:bg-gray-100 dark:hover:bg-gray-700'
              }`}
            >
              {r.label}
            </button>
          ))}
        </div>
      </div>
      <TimeSeriesChart
        series={series}
        chartType="area"
        unit="bytes"
        height={200}
        loading={loading}
      />
    </div>
  );
}

// ============================================================================
// SignalBadge
// ============================================================================

function SignalBadge({ signal }: { signal: SignalType }) {
  const colors: Record<SignalType, string> = {
    trace: 'bg-purple-100 text-purple-700 dark:bg-purple-900/30 dark:text-purple-300',
    metric: 'bg-green-100 text-green-700 dark:bg-green-900/30 dark:text-green-300',
    log: 'bg-blue-100 text-blue-700 dark:bg-blue-900/30 dark:text-blue-300',
  };

  return (
    <span className={`px-1.5 py-0.5 text-[10px] font-medium rounded ${colors[signal] || 'bg-gray-100 text-gray-700'}`}>
      {signal}
    </span>
  );
}
