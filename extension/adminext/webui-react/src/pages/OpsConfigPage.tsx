/**
 * 运维配置页面 - 管理全局运行时配置。
 *
 * 信息架构：按配置键的域分组；每行统一为「键/说明/状态」+「值/操作」两栏，
 * 左侧竖线（变更态轨道）编码该键的下发状态：灰=未设置、蓝=运营端已下发、
 * 琥珀=有未保存改动。优先级链只在页头说明一次，避免逐行重复。
 *
 * 优先级：本地 -D > 运营端下发 > 探针推导。若实例被本地 -D 覆盖，
 * 页面通过实例列表的「Tunnel 来源」徽标提示，避免误判下发失败。
 */

import { useCallback, useEffect, useMemo, useState } from 'react';
import { apiClient } from '@/api/client';
import { useToast } from '@/contexts/ToastContext';
import { useConfirm } from '@/components/ConfirmDialog';
import { Badge } from '@/components/ui/badge';
import { Button } from '@/components/ui/button';
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from '@/components/ui/card';
import { Input } from '@/components/ui/input';
import { cn } from '@/lib/utils';

interface SchemaEntry {
  key: string;
  description: string;
  value_type: string;
}

interface OpsConfigView {
  revision: number;
  configs: Record<string, string>;
  schema: SchemaEntry[];
}

interface AuditEntry {
  key: string;
  old_value?: string;
  new_value?: string;
  operator?: string;
  revision: number;
  timestamp: number;
}

type EntryStatus = {
  label: string;
  tone: 'neutral' | 'blue' | 'amber';
  rail: string;
};

export default function OpsConfigPage() {
  const [view, setView] = useState<OpsConfigView | null>(null);
  const [drafts, setDrafts] = useState<Record<string, string>>({});
  const [audit, setAudit] = useState<AuditEntry[]>([]);
  const [instances, setInstances] = useState<Record<string, unknown>[]>([]);
  const [loading, setLoading] = useState(true);
  const [saving, setSaving] = useState<string | null>(null);
  const [error, setError] = useState('');
  const { showToast } = useToast();
  const confirm = useConfirm();

  const load = useCallback(async () => {
    setLoading(true);
    setError('');
    try {
      const [configData, auditData, instanceData] = await Promise.all([
        apiClient.request<OpsConfigView>('GET', '/ops/configs'),
        apiClient.request<{ entries: AuditEntry[] }>('GET', '/ops/configs/audit'),
        apiClient.request<{ instances: Record<string, unknown>[] }>('GET', '/instances?status=all'),
      ]);
      setView(configData);
      setAudit(auditData.entries || []);
      setInstances(instanceData.instances || []);
      const next: Record<string, string> = {};
      for (const entry of configData.schema || []) {
        next[entry.key] = configData.configs?.[entry.key] || '';
      }
      setDrafts(next);
    } catch (e) {
      setError((e as { message?: string }).message || 'Failed to load ops config');
    } finally {
      setLoading(false);
    }
  }, []);

  useEffect(() => { void load(); }, [load]);

  const counts = (() => {
    let local = 0;
    let ops = 0;
    let derived = 0;
    let unknown = 0;
    for (const inst of instances) {
      const source = String(inst.arthas_tunnel_source || '');
      if (source === 'local') local += 1;
      else if (source === 'ops') ops += 1;
      else if (source === 'derived') derived += 1;
      else unknown += 1;
    }
    return { local, ops, derived, unknown };
  })();

  const groups = useMemo(() => {
    const byDomain = new Map<string, SchemaEntry[]>();
    for (const entry of view?.schema || []) {
      const dot = entry.key.indexOf('.');
      const domain = dot > 0 ? entry.key.slice(0, dot) : 'general';
      const bucket = byDomain.get(domain) || [];
      bucket.push(entry);
      byDomain.set(domain, bucket);
    }
    return Array.from(byDomain.entries());
  }, [view]);

  const statusOf = (entry: SchemaEntry): EntryStatus => {
    const saved = view?.configs?.[entry.key] || '';
    const draft = drafts[entry.key] || '';
    if (draft !== saved) {
      return { label: 'Unsaved', tone: 'amber', rail: 'bg-warning-500' };
    }
    if (saved) {
      return { label: 'Applied', tone: 'blue', rail: 'bg-primary-600' };
    }
    return { label: 'Not set', tone: 'neutral', rail: 'bg-line-strong' };
  };

  const dirtyCount = (view?.schema || []).filter(
    (entry) => (drafts[entry.key] || '') !== (view?.configs?.[entry.key] || ''),
  ).length;

  const save = async (key: string) => {
    const value = (drafts[key] || '').trim();
    setSaving(key);
    try {
      await apiClient.request('PUT', `/ops/configs/${encodeURIComponent(key)}`, { value });
      showToast(`Saved ${key}`, 'success');
      await load();
    } catch (e) {
      showToast((e as { message?: string }).message || 'Save failed', 'error');
    } finally {
      setSaving(null);
    }
  };

  const remove = async (key: string) => {
    if (!(await confirm({ title: 'Delete ops config', message: `Delete ${key}? Probes will fall back to the derived endpoint.` }))) {
      return;
    }
    setSaving(key);
    try {
      await apiClient.request('DELETE', `/ops/configs/${encodeURIComponent(key)}`);
      showToast(`Deleted ${key}`, 'success');
      await load();
    } catch (e) {
      showToast((e as { message?: string }).message || 'Delete failed', 'error');
    } finally {
      setSaving(null);
    }
  };

  if (loading && !view) {
    return <div className="p-8 text-sm text-ink-muted">Loading configuration…</div>;
  }

  const stats = [
    { label: 'Auto-derived endpoint', value: counts.derived, dot: 'bg-success-500' },
    { label: 'Applied from ops', value: counts.ops, dot: 'bg-primary-600' },
    { label: 'Overridden by local -D', value: counts.local, dot: 'bg-warning-500' },
    { label: 'Unknown / legacy probe', value: counts.unknown, dot: 'bg-line-strong' },
  ];

  return (
    <div className="min-h-full bg-canvas p-6 lg:p-8">
      <div className="mx-auto max-w-6xl space-y-5">

        {/* 页头：把优先级链作为页面的结构说明，而不是逐行重复的脚注 */}
        <header className="flex flex-wrap items-end justify-between gap-4">
          <div>
            <div className="text-[11px] font-semibold uppercase tracking-[0.14em] text-primary-700">
              Control plane / runtime configuration
            </div>
            <h1 className="mt-1 text-2xl font-semibold tracking-tight text-ink">Ops Config</h1>
            <p className="mt-1.5 max-w-2xl text-sm leading-relaxed text-ink-muted">
              Values delivered to probes through the control plane. A local -D override wins over
              ops config, which wins over the derived endpoint.
            </p>
          </div>
          <div className="flex items-center gap-2">
            <Badge tone="neutral">rev {view?.revision ?? 0}</Badge>
            {dirtyCount > 0 && <Badge tone="amber">{dirtyCount} unsaved</Badge>}
          </div>
        </header>

        <div className="flex flex-wrap items-center gap-2 rounded-ui border border-line bg-surface px-4 py-3 text-xs text-ink-muted shadow-ui">
          <span className="font-medium text-ink-soft">Resolution order</span>
          <Badge tone="amber">local -D</Badge>
          <span aria-hidden="true" className="text-ink-faint">→</span>
          <Badge tone="blue">ops config</Badge>
          <span aria-hidden="true" className="text-ink-faint">→</span>
          <Badge tone="neutral">derived endpoint</Badge>
          <span className="ml-1">Local overrides are shown on the Instances page.</span>
        </div>

        {error && (
          <div role="alert" className="rounded-ui border border-danger-100 bg-danger-50 px-4 py-3 text-sm text-danger-600">
            Could not load configuration: {error}
          </div>
        )}

        {/* 来源分布：统一的等宽数字与语义色点，取代大号彩色数字卡片 */}
        <div className="grid gap-3 sm:grid-cols-2 lg:grid-cols-4">
          {stats.map((stat) => (
            <Card key={stat.label} className="px-4 py-3">
              <div className="flex items-center justify-between gap-3">
                <div className="flex items-center gap-2">
                  <span aria-hidden="true" className={cn('h-2 w-2 rounded-full', stat.dot)} />
                  <span className="text-xs font-medium text-ink-muted">{stat.label}</span>
                </div>
                <span className="font-mono text-xl font-semibold tabular-nums text-ink">{stat.value}</span>
              </div>
            </Card>
          ))}
        </div>

        {/* 配置域分组 */}
        {groups.map(([domain, entries]) => (
          <Card key={domain}>
            <CardHeader>
              <div>
                <CardTitle>{domain}</CardTitle>
                <CardDescription>
                  {entries.length} {entries.length === 1 ? 'setting' : 'settings'} · changes are published per key
                </CardDescription>
              </div>
              <Badge tone="neutral">{domain}</Badge>
            </CardHeader>
            <CardContent className="p-0">
              <div className="divide-y divide-line">
                {entries.map((entry) => {
                  const status = statusOf(entry);
                  return (
                    <div key={entry.key} className="relative grid grid-cols-1 gap-4 px-5 py-4 lg:grid-cols-12 lg:gap-6">
                      {/* 变更态轨道：颜色直接编码下发状态 */}
                      <span
                        aria-hidden="true"
                        className={cn('absolute left-0 top-0 h-full w-[3px] rounded-l-ui', status.rail)}
                      />
                      <div className="lg:col-span-5">
                        <div className="flex flex-wrap items-center gap-2">
                          <span className="font-mono text-[13px] font-medium text-ink">{entry.key}</span>
                          <Badge tone={status.tone}>{status.label}</Badge>
                        </div>
                        <p className="mt-1.5 text-xs leading-relaxed text-ink-muted">{entry.description}</p>
                        <div className="mt-2 text-[11px] text-ink-faint">type · {entry.value_type}</div>
                      </div>
                      <div className="flex flex-col gap-2 sm:flex-row sm:items-start lg:col-span-7">
                        <Input
                          id={`ops-${entry.key}`}
                          mono
                          aria-label={`Value for ${entry.key}`}
                          className="flex-1"
                          value={drafts[entry.key] || ''}
                          placeholder="wss://gw.example/otel"
                          onChange={(e) => setDrafts({ ...drafts, [entry.key]: e.target.value })}
                        />
                        <div className="flex shrink-0 items-center gap-2">
                          <Button
                            size="md"
                            disabled={saving === entry.key}
                            onClick={() => void save(entry.key)}
                          >
                            {saving === entry.key ? 'Saving…' : 'Save'}
                          </Button>
                          {view?.configs?.[entry.key] && (
                            <Button
                              variant="outline"
                              size="md"
                              disabled={saving === entry.key}
                              onClick={() => void remove(entry.key)}
                            >
                              Delete
                            </Button>
                          )}
                        </div>
                      </div>
                    </div>
                  );
                })}
              </div>
            </CardContent>
          </Card>
        ))}

        {/* 变更记录 */}
        <Card>
          <CardHeader>
            <div>
              <CardTitle>Recent changes</CardTitle>
              <CardDescription>Newest operator actions on this configuration</CardDescription>
            </div>
            <Badge tone="neutral">{audit.length} entries</Badge>
          </CardHeader>
          <CardContent className="p-0">
            {audit.length === 0 ? (
              <p className="px-5 py-6 text-sm text-ink-muted">
                No changes yet. Saving a key will create the first entry here.
              </p>
            ) : (
              <div className="overflow-x-auto">
                <table className="w-full text-left text-sm">
                  <thead className="bg-canvas text-[11px] uppercase tracking-wide text-ink-muted">
                    <tr>
                      {['Time', 'Key', 'Old', 'New', 'Operator', 'Revision'].map((heading) => (
                        <th key={heading} scope="col" className="whitespace-nowrap px-5 py-2.5 font-medium">{heading}</th>
                      ))}
                    </tr>
                  </thead>
                  <tbody className="divide-y divide-line">
                    {audit.map((item, index) => (
                      <tr key={`${item.revision}-${index}`} className="hover:bg-canvas/60">
                        <td className="whitespace-nowrap px-5 py-2.5 text-xs text-ink-muted">
                          {new Date(item.timestamp).toLocaleString()}
                        </td>
                        <td className="px-5 py-2.5 font-mono text-xs text-ink">{item.key}</td>
                        <td className="px-5 py-2.5 font-mono text-xs text-ink-muted">{item.old_value || '—'}</td>
                        <td className="px-5 py-2.5 font-mono text-xs text-ink">{item.new_value || '—'}</td>
                        <td className="px-5 py-2.5 text-xs text-ink-muted">{item.operator || '—'}</td>
                        <td className="px-5 py-2.5 font-mono text-xs text-ink-muted">{item.revision}</td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              </div>
            )}
          </CardContent>
        </Card>
      </div>
    </div>
  );
}
