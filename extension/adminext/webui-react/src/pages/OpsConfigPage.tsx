/**
 * 运维配置页面 - 管理全局运行时配置（P0：Arthas Tunnel Server 地址）
 *
 * 优先级：本地 -D > 运营端下发 > 探针推导。若实例被本地 -D 覆盖，
 * 页面通过实例列表的「Tunnel 来源」徽标提示，避免误判下发失败。
 */

import { useCallback, useEffect, useState } from 'react';
import { apiClient } from '@/api/client';
import { useToast } from '@/contexts/ToastContext';
import { useConfirm } from '@/components/ConfirmDialog';

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
    let unknown = 0;
    for (const inst of instances) {
      const source = String(inst.arthas_tunnel_source || '');
      if (source === 'local') local += 1;
      else if (source === 'ops') ops += 1;
      else unknown += 1;
    }
    return { local, ops, unknown };
  })();

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
    return <div className="p-6 text-gray-500">Loading…</div>;
  }

  return (
    <div className="p-6 space-y-6">
      <div>
        <h1 className="text-2xl font-semibold text-gray-800">Ops Config</h1>
        <p className="mt-1 text-sm text-gray-500">
          Global runtime configuration delivered to probes via the control plane.
          Revision: <span className="font-mono">{view?.revision ?? 0}</span>
        </p>
      </div>

      {error && <div className="rounded border border-red-200 bg-red-50 px-4 py-2 text-sm text-red-700">{error}</div>}

      <div className="grid grid-cols-1 gap-3 sm:grid-cols-3">
        <div className="rounded border border-gray-200 bg-white px-4 py-3">
          <div className="text-xs text-gray-500">Applied from ops</div>
          <div className="mt-1 text-xl font-semibold text-green-700">{counts.ops}</div>
        </div>
        <a href="/instances" className="rounded border border-gray-200 bg-white px-4 py-3 hover:border-amber-300">
          <div className="text-xs text-gray-500">Overridden by local -D</div>
          <div className="mt-1 text-xl font-semibold text-amber-700">{counts.local}</div>
        </a>
        <div className="rounded border border-gray-200 bg-white px-4 py-3">
          <div className="text-xs text-gray-500">Unknown (legacy probe)</div>
          <div className="mt-1 text-xl font-semibold text-gray-600">{counts.unknown}</div>
        </div>
      </div>

      <div className="rounded border border-gray-200 bg-white">
        <div className="border-b border-gray-200 px-4 py-3 text-sm font-medium text-gray-700">Configuration</div>
        <div className="divide-y divide-gray-100">
          {(view?.schema || []).map((entry) => (
            <div key={entry.key} className="px-4 py-4">
              <div className="flex items-start justify-between gap-4">
                <div className="min-w-0">
                  <div className="font-mono text-sm text-gray-800">{entry.key}</div>
                  <div className="mt-1 text-xs text-gray-500">{entry.description}</div>
                </div>
                <div className="text-xs text-gray-400">{entry.value_type}</div>
              </div>
              <div className="mt-3 flex flex-wrap items-center gap-2">
                <input
                  className="min-w-[280px] flex-1 rounded border border-gray-300 px-3 py-2 font-mono text-sm"
                  value={drafts[entry.key] || ''}
                  placeholder="ws://collector.example/v1/arthas/ws"
                  onChange={(e) => setDrafts({ ...drafts, [entry.key]: e.target.value })}
                />
                <button
                  className="rounded bg-blue-600 px-4 py-2 text-sm text-white disabled:opacity-50"
                  disabled={saving === entry.key}
                  onClick={() => void save(entry.key)}
                >
                  {saving === entry.key ? 'Saving…' : 'Save'}
                </button>
                {view?.configs?.[entry.key] && (
                  <button
                    className="rounded border border-gray-300 px-3 py-2 text-sm text-gray-600 disabled:opacity-50"
                    disabled={saving === entry.key}
                    onClick={() => void remove(entry.key)}
                  >
                    Delete
                  </button>
                )}
              </div>
              <div className="mt-2 text-xs text-gray-400">
                Priority: local -D &gt; ops config &gt; derived endpoint. Local overrides are shown on the Instances page.
              </div>
            </div>
          ))}
        </div>
      </div>

      <div className="rounded border border-gray-200 bg-white">
        <div className="border-b border-gray-200 px-4 py-3 text-sm font-medium text-gray-700">Recent changes</div>
        {audit.length === 0 ? (
          <div className="px-4 py-4 text-sm text-gray-400">No changes yet.</div>
        ) : (
          <table className="w-full text-left text-sm">
            <thead className="bg-gray-50 text-xs text-gray-500">
              <tr>
                <th className="px-4 py-2">Time</th>
                <th className="px-4 py-2">Key</th>
                <th className="px-4 py-2">Old</th>
                <th className="px-4 py-2">New</th>
                <th className="px-4 py-2">Operator</th>
                <th className="px-4 py-2">Revision</th>
              </tr>
            </thead>
            <tbody className="divide-y divide-gray-100">
              {audit.map((item, index) => (
                <tr key={`${item.revision}-${index}`}>
                  <td className="px-4 py-2 text-gray-500">{new Date(item.timestamp).toLocaleString()}</td>
                  <td className="px-4 py-2 font-mono text-xs">{item.key}</td>
                  <td className="px-4 py-2 font-mono text-xs text-gray-500">{item.old_value || '—'}</td>
                  <td className="px-4 py-2 font-mono text-xs">{item.new_value || '—'}</td>
                  <td className="px-4 py-2">{item.operator || '—'}</td>
                  <td className="px-4 py-2">{item.revision}</td>
                </tr>
              ))}
            </tbody>
          </table>
        )}
      </div>
    </div>
  );
}
