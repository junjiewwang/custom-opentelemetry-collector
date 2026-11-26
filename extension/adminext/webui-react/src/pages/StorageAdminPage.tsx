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
import { Badge } from '@/components/ui/badge';
import { Button } from '@/components/ui/button';
import { Card, CardDescription, CardHeader, CardTitle, CardContent } from '@/components/ui/card';
import { cn } from '@/lib/utils';

// 统一的下拉框样式（与 Tenants 弹窗、Ops Config 保持一致）
const selectClass =
  'h-9 rounded-ui border border-line bg-surface px-3 text-sm text-ink ' +
  'focus-visible:border-primary-400 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-primary-100';

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
      <div className="min-h-full bg-canvas p-6 lg:p-8">
        <Card className="mx-auto max-w-2xl border-warning-100 bg-warning-50 p-5">
          <div className="flex items-center gap-2">
            <Badge tone="amber">Unavailable</Badge>
            <CardTitle>存储管理不可用</CardTitle>
          </div>
          <CardDescription className="mt-2 text-warning-700">
            存储管理需要配置 <code className="rounded bg-warning-100 px-1 font-mono text-xs">observability.storage_extension</code>。
            请确保 observabilitystorageext 扩展已启用并正确配置。
          </CardDescription>
        </Card>
      </div>
    );
  }

  if (loading) {
    return (
      <div className="flex min-h-[300px] items-center justify-center bg-canvas">
        <span className="animate-pulse text-sm text-ink-muted">加载存储状态…</span>
      </div>
    );
  }

  // ========================================================================
  // Render
  // ========================================================================

  return (
    <div className="min-h-full bg-canvas p-6 lg:p-8">
      <div className="mx-auto max-w-6xl space-y-5">
      {/* Page Header */}
      <header className="flex flex-wrap items-end justify-between gap-4">
        <div>
          <div className="text-[11px] font-semibold uppercase tracking-[0.14em] text-primary-700">
            Observability / storage operations
          </div>
          <h1 className="mt-1 text-2xl font-semibold tracking-tight text-ink">存储管理</h1>
          <p className="mt-1.5 max-w-2xl text-sm leading-relaxed text-ink-muted">
            后端健康、磁盘用量与索引分布。保留策略按 App 在应用配置页面管理。
          </p>
        </div>
        <div className="flex items-center gap-2">
          {apps.length > 0 && (
            <select
              aria-label="Filter by app"
              value={selectedAppId}
              onChange={e => setSelectedAppId(e.target.value)}
              className={selectClass}
            >
              <option value="">全部 App</option>
              {apps.map(a => (
                <option key={a.id} value={a.id}>{a.name || a.id}</option>
              ))}
            </select>
          )}
          <Button variant="outline" size="sm" className="h-9" onClick={loadData}>刷新</Button>
        </div>
      </header>

      {/* Error */}
      {error && (
        <div role="alert" className="flex items-center justify-between gap-3 rounded-ui border border-danger-100 bg-danger-50 px-4 py-3 text-sm text-danger-600">
          <span>{error}</span>
          <Button variant="ghost" size="sm" onClick={() => setError('')}>关闭</Button>
        </div>
      )}

      {/* Health + Overview Cards */}
      <div className="grid grid-cols-1 gap-3 md:grid-cols-3">
        <Card className="p-4">
          <div className="text-xs font-medium text-ink-muted">健康状态</div>
          <div className="mt-2 flex items-center gap-2">
            <span aria-hidden="true" className={cn('h-2 w-2 rounded-full', health?.healthy ? 'bg-success-500' : 'bg-danger-500')} />
            <Badge tone={health?.healthy ? 'green' : 'red'}>{health?.healthy ? '健康' : '异常'}</Badge>
          </div>
          {health?.latency_ms !== undefined && (
            <p className="mt-2 font-mono text-xs text-ink-muted">latency · {health.latency_ms}ms</p>
          )}
        </Card>

        <Card className="p-4">
          <div className="text-xs font-medium text-ink-muted">存储后端</div>
          <div className="mt-2 truncate font-mono text-lg font-semibold text-ink">{status?.provider || '—'}</div>
          {status?.version && <p className="mt-1 font-mono text-xs text-ink-muted">v{status.version}</p>}
        </Card>

        <Card className="p-4">
          <div className="text-xs font-medium text-ink-muted">磁盘使用</div>
          {diskUsage ? (
            <>
              <div className="mt-2 font-mono text-sm font-semibold tabular-nums text-ink">
                {formatBytes(diskUsage.usedBytes)} / {formatBytes(diskUsage.totalBytes)}
              </div>
              <div className="mt-2 h-2 overflow-hidden rounded-full bg-line">
                <div
                  className="h-full rounded-full bg-primary-600 transition-all"
                  style={{ width: `${diskUsage.totalBytes > 0 ? (diskUsage.usedBytes / diskUsage.totalBytes * 100) : 0}%` }}
                />
              </div>
              <p className="mt-1.5 text-xs text-ink-muted">可用 {formatBytes(diskUsage.availableBytes)}</p>
            </>
          ) : (
            <p className="mt-2 text-ink-faint">—</p>
          )}
        </Card>
      </div>

      {/* Disk Usage by Signal */}
      {diskUsage?.bySignal && Object.keys(diskUsage.bySignal).length > 0 && (
        <Card>
          <CardHeader>
            <CardTitle>按信号类型分布</CardTitle>
            <Badge tone="neutral">3 signals</Badge>
          </CardHeader>
          <CardContent className="grid grid-cols-3 gap-4">
            {(['trace', 'metric', 'log'] as SignalType[]).map(signal => (
              <div key={signal} className="text-center">
                <div className="text-[11px] uppercase tracking-wide text-ink-muted">{signal}</div>
                <div className="mt-1 font-mono text-lg font-semibold tabular-nums text-ink">
                  {diskUsage.bySignal?.[signal] ? formatBytes(diskUsage.bySignal[signal]) : '—'}
                </div>
              </div>
            ))}
          </CardContent>
        </Card>
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
        <Card className="overflow-hidden">
          <CardHeader>
            <CardTitle>索引列表</CardTitle>
            <Badge tone="neutral">{status.indices.length} indices</Badge>
          </CardHeader>
          <CardContent className="p-0">
            <div className="overflow-x-auto">
              <table className="w-full text-left text-sm">
                <thead className="bg-canvas text-[11px] uppercase tracking-wide text-ink-muted">
                  <tr>
                    <th scope="col" className="px-5 py-2.5 font-medium">名称</th>
                    <th scope="col" className="px-5 py-2.5 font-medium">信号类型</th>
                    <th scope="col" className="px-5 py-2.5 text-right font-medium">文档数</th>
                    <th scope="col" className="px-5 py-2.5 text-right font-medium">大小</th>
                  </tr>
                </thead>
                <tbody className="divide-y divide-line">
                  {status.indices.map((idx: IndexInfo) => (
                    <tr key={idx.name} className="hover:bg-canvas/60">
                      <td className="px-5 py-2.5 font-mono text-xs text-ink">{idx.name}</td>
                      <td className="px-5 py-2.5"><SignalBadge signal={idx.signal} /></td>
                      <td className="px-5 py-2.5 text-right font-mono text-xs text-ink-soft">
                        {idx.docsCount.toLocaleString()}
                      </td>
                      <td className="px-5 py-2.5 text-right font-mono text-xs text-ink-soft">
                        {formatBytes(idx.sizeBytes)}
                      </td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          </CardContent>
        </Card>
      )}
      </div>
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
    <Card>
      <CardHeader>
        <div className="flex items-center gap-2">
          <CardTitle>按天存储用量</CardTitle>
          {appName && <Badge tone="blue">{appName}</Badge>}
        </div>
        <div className="flex gap-1" role="group" aria-label="Time range">
          {DAILY_RANGES.map(r => (
            <Button
              key={r.days}
              size="sm"
              variant={rangeDays === r.days ? 'default' : 'ghost'}
              aria-pressed={rangeDays === r.days}
              className="h-7 px-2.5 text-xs"
              onClick={() => onRangeChange(r.days)}
            >
              {r.label}
            </Button>
          ))}
        </div>
      </CardHeader>
      <CardContent>
        <TimeSeriesChart
          series={series}
          chartType="area"
          unit="bytes"
          height={200}
          loading={loading}
        />
      </CardContent>
    </Card>
  );
}

// ============================================================================
// SignalBadge
// ============================================================================

function SignalBadge({ signal }: { signal: SignalType }) {
  const tones: Record<string, 'blue' | 'green' | 'neutral'> = {
    trace: 'blue',
    metric: 'green',
    log: 'neutral',
  };
  return <Badge tone={tones[signal] ?? 'neutral'}>{signal}</Badge>;
}
