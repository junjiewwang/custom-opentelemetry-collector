/**
 * Tenants 页面 - 多租户管理（租户 CRUD + 读路径 API Key 管理）
 *
 * 参照 AppsPage 的交互模式：表格 + 创建/编辑模态框 + Key 管理模态框。
 */

import { useState, useEffect, useCallback } from 'react';
import { apiClient } from '@/api/client';
import { useToast } from '@/contexts/ToastContext';
import EmptyState from '@/components/EmptyState';
import { Badge } from '@/components/ui/badge';
import { Button } from '@/components/ui/button';
import { Field } from '@/components/ui/field';
import { Input } from '@/components/ui/input';
import { Switch } from '@/components/ui/switch';
import { Tabs } from '@/components/ui/tabs';
import { cn } from '@/lib/utils';
import type { Tenant, TenantAPIKey, TenantObservabilityQueryConfig } from '@/types/api';

type QueryLimitKey = 'query_timeout' | 'max_time_range' | 'metric_series' | 'traces' | 'logs' | 'log_context_lines';

const QUERY_LIMIT_FIELDS: { key: QueryLimitKey; label: string; kind: 'number' | 'text'; hint: string }[] = [
  { key: 'query_timeout', label: 'Query timeout (s)', kind: 'number', hint: '20' },
  { key: 'max_time_range', label: 'Max time range', kind: 'text', hint: '24h' },
  { key: 'metric_series', label: 'Metric series', kind: 'number', hint: '50' },
  { key: 'traces', label: 'Traces', kind: 'number', hint: '20' },
  { key: 'logs', label: 'Logs', kind: 'number', hint: '100' },
  { key: 'log_context_lines', label: 'Log context lines', kind: 'number', hint: '50' },
];

const allQueryInherited = (): Record<QueryLimitKey, boolean> => ({
  query_timeout: true, max_time_range: true, metric_series: true,
  traces: true, logs: true, log_context_lines: true,
});

const emptyQueryValues = (): Record<QueryLimitKey, string> => ({
  query_timeout: '', max_time_range: '', metric_series: '',
  traces: '', logs: '', log_context_lines: '',
});

export default function TenantsPage() {
  const { showToast } = useToast();

  const [tenants, setTenants] = useState<Tenant[]>([]);
  const [loading, setLoading] = useState(false);

  // 创建 / 编辑模态框
  const [showCreateModal, setShowCreateModal] = useState(false);
  const [editTenant, setEditTenant] = useState<Tenant | null>(null);
  const [formName, setFormName] = useState('');
  const [formDesc, setFormDesc] = useState('');
  const [formStatus, setFormStatus] = useState<'active' | 'disabled'>('active');
  const [formInstrumentation, setFormInstrumentation] = useState(true);
  const [formInstrumentationInherit, setFormInstrumentationInherit] = useState(true);
  const [formQueryInheritAll, setFormQueryInheritAll] = useState(true);
  const [formQueryDefaults, setFormQueryDefaults] = useState<Record<QueryLimitKey, boolean>>(allQueryInherited);
  const [formQueryValues, setFormQueryValues] = useState<Record<QueryLimitKey, string>>(emptyQueryValues);
  const [editTab, setEditTab] = useState<'basic' | 'capabilities' | 'query'>('basic');

  // Key 管理模态框
  const [keysTenant, setKeysTenant] = useState<Tenant | null>(null);
  const [keys, setKeys] = useState<TenantAPIKey[]>([]);
  const [keysLoading, setKeysLoading] = useState(false);
  const [keyName, setKeyName] = useState('');
  const [keyType, setKeyType] = useState<'tk' | 'ok'>('tk');
  const [keyInstrumentationWrite, setKeyInstrumentationWrite] = useState(false);
  const [keyMetricRead, setKeyMetricRead] = useState(false);
  const [keyTraceRead, setKeyTraceRead] = useState(false);
  const [keyLogRead, setKeyLogRead] = useState(false);
  const [newPlainKey, setNewPlainKey] = useState<string | null>(null);

  const loadTenants = useCallback(async () => {
    setLoading(true);
    try {
      setTenants(await apiClient.getTenants());
    } catch (e: unknown) {
      const err = e as { message?: string };
      showToast(err.message || 'Failed to load tenants', 'error');
    } finally {
      setLoading(false);
    }
  }, [showToast]);

  useEffect(() => {
    loadTenants();
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  // ========================================================================
  // Tenant CRUD
  // ========================================================================

  const openCreate = () => {
    setFormName('');
    setFormDesc('');
    setFormStatus('active');
    setFormInstrumentation(true);
    setFormInstrumentationInherit(true);
    setFormQueryInheritAll(true);
    setFormQueryDefaults(allQueryInherited());
    setFormQueryValues(emptyQueryValues());
    setEditTab('basic');
    setShowCreateModal(true);
  };

  const openEdit = (t: Tenant) => {
    setEditTenant(t);
    setFormName(t.name);
    setFormDesc(t.description || '');
    setFormStatus(t.status);
    const capability = t.capabilities?.dynamic_instrumentation;
    setFormInstrumentationInherit(capability === undefined);
    setFormInstrumentation(capability ?? true);

    const query = t.capabilities?.observability_query;
    const defaults = allQueryInherited();
    const values = emptyQueryValues();
    if (query) {
      for (const field of QUERY_LIMIT_FIELDS) {
        const override = query[field.key];
        if (override !== undefined && override !== null) {
          defaults[field.key] = false;
          values[field.key] = String(override);
        }
      }
    }
    setFormQueryInheritAll(!query);
    setFormQueryDefaults(defaults);
    setFormQueryValues(values);
    setEditTab('basic');
  };

  const submitCreate = async () => {
    if (!formName.trim()) {
      showToast('Tenant name is required', 'error');
      return;
    }
    try {
      await apiClient.createTenant({ name: formName.trim(), description: formDesc.trim() || undefined });
      showToast('Tenant created', 'success');
      setShowCreateModal(false);
      loadTenants();
    } catch (e: unknown) {
      const err = e as { message?: string };
      showToast(err.message || 'Failed to create tenant', 'error');
    }
  };

  const submitEdit = async () => {
    if (!editTenant || !formName.trim()) {
      showToast('Tenant name is required', 'error');
      return;
    }
    let observabilityQuery: TenantObservabilityQueryConfig | undefined;
    if (!formQueryInheritAll) {
      const overrides: TenantObservabilityQueryConfig = {};
      for (const field of QUERY_LIMIT_FIELDS) {
        if (formQueryDefaults[field.key]) continue;
        const raw = formQueryValues[field.key].trim();
        if (!raw) {
          showToast(`${field.label} is required when not inheriting`, 'error');
          return;
        }
        if (field.kind === 'number') {
          const parsed = Number(raw);
          if (!Number.isInteger(parsed) || parsed <= 0) {
            showToast(`${field.label} must be a positive integer`, 'error');
            return;
          }
          Object.assign(overrides, { [field.key]: parsed });
        } else {
          Object.assign(overrides, { [field.key]: raw });
        }
      }
      observabilityQuery = overrides;
    }
    try {
      await apiClient.updateTenant(editTenant.id, {
        name: formName.trim(),
        description: formDesc.trim() || undefined,
        status: formStatus,
        dynamic_instrumentation: formInstrumentationInherit ? undefined : formInstrumentation,
        clear_dynamic_instrumentation: formInstrumentationInherit,
        observability_query: observabilityQuery,
        clear_observability_query: formQueryInheritAll,
      });
      showToast('Tenant updated', 'success');
      setEditTenant(null);
      loadTenants();
    } catch (e: unknown) {
      const err = e as { message?: string };
      showToast(err.message || 'Failed to update tenant', 'error');
    }
  };

  const deleteTenant = async (t: Tenant) => {
    if (!window.confirm(`Delete tenant "${t.name}"? Only empty tenants can be deleted.`)) return;
    try {
      await apiClient.deleteTenant(t.id);
      showToast('Tenant deleted', 'success');
      loadTenants();
    } catch (e: unknown) {
      const err = e as { message?: string };
      showToast(err.message || 'Failed to delete tenant', 'error');
    }
  };

  // ========================================================================
  // API Key management
  // ========================================================================

  const openKeys = async (t: Tenant) => {
    setKeysTenant(t);
    setNewPlainKey(null);
    setKeyName('');
    setKeyType('tk');
    setKeyInstrumentationWrite(false);
    setKeyMetricRead(false);
    setKeyTraceRead(false);
    setKeyLogRead(false);
    setKeysLoading(true);
    try {
      setKeys(await apiClient.getTenantKeys(t.id));
    } catch (e: unknown) {
      const err = e as { message?: string };
      showToast(err.message || 'Failed to load keys', 'error');
    } finally {
      setKeysLoading(false);
    }
  };

  const createKey = async () => {
    if (!keysTenant || !keyName.trim()) {
      showToast('Key name is required', 'error');
      return;
    }
    const scopes: string[] = [];
    if (keyInstrumentationWrite) scopes.push('instrumentation:write');
    if (keyMetricRead) scopes.push('metric:read');
    if (keyTraceRead) scopes.push('trace:read');
    if (keyLogRead) scopes.push('log:read');
    try {
      const resp = await apiClient.createTenantKey(keysTenant.id, {
        name: keyName.trim(),
        key_type: keyType,
        scopes,
      });
      // 明文仅返回一次，醒目展示
      setNewPlainKey(resp.key);
      setKeyName('');
      setKeys(await apiClient.getTenantKeys(keysTenant.id));
    } catch (e: unknown) {
      const err = e as { message?: string };
      showToast(err.message || 'Failed to create key', 'error');
    }
  };

  const revokeKey = async (k: TenantAPIKey) => {
    if (!keysTenant || !window.confirm(`Revoke key "${k.name}"? This cannot be undone.`)) return;
    try {
      await apiClient.revokeTenantKey(keysTenant.id, k.id);
      showToast('Key revoked', 'success');
      setKeys(await apiClient.getTenantKeys(keysTenant.id));
    } catch (e: unknown) {
      const err = e as { message?: string };
      showToast(err.message || 'Failed to revoke key', 'error');
    }
  };

  const formatDate = (s: string) => (s ? new Date(s).toLocaleString() : '-');

  // ========================================================================
  // Render
  // ========================================================================

  return (
    <div className="p-6">
      <div className="flex items-center justify-between mb-4">
        <div>
          <h1 className="text-2xl font-semibold text-gray-800">Tenants</h1>
          <p className="text-sm text-gray-500 mt-1">
            Multi-tenancy isolation: group apps into tenants and manage read-path API keys.
          </p>
        </div>
        <button
          onClick={openCreate}
          className="px-4 py-2 bg-blue-600 hover:bg-blue-700 text-white rounded-md text-sm font-medium"
        >
          + New Tenant
        </button>
      </div>

      {loading ? (
        <div className="text-gray-500 text-sm">Loading tenants…</div>
      ) : tenants.length === 0 ? (
        <EmptyState title="No tenants" description="Create a tenant to start grouping apps for isolation." />
      ) : (
        <div className="bg-white rounded-lg shadow overflow-x-auto">
          <table className="min-w-full text-sm">
            <thead className="bg-gray-50 text-gray-600 text-left">
              <tr>
                <th className="px-4 py-3 font-medium">Name</th>
                <th className="px-4 py-3 font-medium">ID</th>
                <th className="px-4 py-3 font-medium">Status</th>
                <th className="px-4 py-3 font-medium">Instrumentation</th>
                <th className="px-4 py-3 font-medium">Created</th>
                <th className="px-4 py-3 font-medium text-right">Actions</th>
              </tr>
            </thead>
            <tbody className="divide-y divide-gray-100">
              {tenants.map(t => (
                <tr key={t.id} className="hover:bg-gray-50">
                  <td className="px-4 py-3">
                    <div className="font-medium text-gray-800">{t.name}</div>
                    <div className="text-xs text-gray-400">{t.description || '—'}</div>
                  </td>
                  <td className="px-4 py-3 font-mono text-xs text-gray-500">{t.id}</td>
                  <td className="px-4 py-3">
                    <span
                      className={`px-2 py-0.5 rounded-full text-xs ${
                        t.status === 'active' ? 'bg-green-100 text-green-700' : 'bg-gray-200 text-gray-600'
                      }`}
                    >
                      {t.status}
                    </span>
                  </td>
                  <td className="px-4 py-3">
                    <span
                      className={`px-2 py-0.5 rounded-full text-xs ${
                        t.capabilities?.dynamic_instrumentation === undefined
                          ? 'bg-blue-100 text-blue-700'
                          : t.capabilities.dynamic_instrumentation
                            ? 'bg-green-100 text-green-700'
                            : 'bg-gray-200 text-gray-600'
                      }`}
                    >
                      {t.capabilities?.dynamic_instrumentation === undefined
                        ? 'inherited'
                        : t.capabilities.dynamic_instrumentation
                          ? 'enabled'
                          : 'disabled'}
                    </span>
                  </td>
                  <td className="px-4 py-3 text-gray-500">{formatDate(t.created_at)}</td>
                  <td className="px-4 py-3 text-right whitespace-nowrap">
                    <button onClick={() => openKeys(t)} className="text-blue-600 hover:underline mr-3">
                      Keys
                    </button>
                    <button onClick={() => openEdit(t)} className="text-gray-600 hover:underline mr-3">
                      Edit
                    </button>
                    <button onClick={() => deleteTenant(t)} className="text-red-600 hover:underline">
                      Delete
                    </button>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}

      {/* ── 创建 / 编辑模态框 ── */}
      {(showCreateModal || editTenant) && (
        <div className="fixed inset-0 z-50 flex items-center justify-center bg-slate-900/40 p-4">
          <div
            role="dialog"
            aria-modal="true"
            aria-label={editTenant ? 'Edit tenant' : 'New tenant'}
            className="flex max-h-[88vh] w-full max-w-3xl flex-col overflow-hidden rounded-ui border border-line bg-surface shadow-ui"
          >
            <header className="flex items-start justify-between gap-4 border-b border-line px-6 py-4">
              <div>
                <div className="text-[11px] font-semibold uppercase tracking-[0.14em] text-primary-700">
                  Tenant settings
                </div>
                <h2 className="mt-0.5 text-lg font-semibold tracking-tight text-ink">
                  {editTenant ? 'Edit tenant' : 'New tenant'}
                </h2>
                <p className="mt-1 text-xs text-ink-muted">
                  {editTenant
                    ? `Editing ${editTenant.name}. Changes take effect when saved.`
                    : 'Create a tenant, then grant capabilities and API keys.'}
                </p>
              </div>
              <div className="flex items-center gap-2">
                {editTenant && (
                  <Badge tone={formStatus === 'active' ? 'green' : 'neutral'}>{formStatus}</Badge>
                )}
                <Button
                  variant="ghost"
                  size="icon"
                  aria-label="Close dialog"
                  onClick={() => { setShowCreateModal(false); setEditTenant(null); }}
                >
                  <span aria-hidden="true" className="text-lg leading-none">×</span>
                </Button>
              </div>
            </header>

            {editTenant && (
              <div className="px-6 pt-2">
                <Tabs
                  items={[
                    { id: 'basic', label: 'Basic' },
                    { id: 'capabilities', label: 'Capabilities' },
                    { id: 'query', label: 'Query limits' },
                  ]}
                  value={editTab}
                  onChange={(id) => setEditTab(id as 'basic' | 'capabilities' | 'query')}
                />
              </div>
            )}

            <div className="flex-1 overflow-y-auto px-6 py-5">
              {(!editTenant || editTab === 'basic') && (
                <div className="grid gap-4 sm:grid-cols-2">
                  <Field label="Name" htmlFor="tenant-name" hint="Shown in the tenant switcher.">
                    <Input
                      id="tenant-name"
                      value={formName}
                      onChange={e => setFormName(e.target.value)}
                      placeholder="e.g. acme"
                    />
                  </Field>
                  <Field label="Description" htmlFor="tenant-desc" hint="Optional context for other operators.">
                    <Input
                      id="tenant-desc"
                      value={formDesc}
                      onChange={e => setFormDesc(e.target.value)}
                      placeholder="Optional"
                    />
                  </Field>
                  {editTenant && (
                    <Field label="Status" htmlFor="tenant-status" hint="Disabled tenants cannot authenticate.">
                      <select
                        id="tenant-status"
                        value={formStatus}
                        onChange={e => setFormStatus(e.target.value as 'active' | 'disabled')}
                        className="h-10 w-full rounded-ui border border-line bg-surface px-3 text-sm text-ink focus-visible:border-primary-400 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-primary-100"
                      >
                        <option value="active">active</option>
                        <option value="disabled">disabled</option>
                      </select>
                    </Field>
                  )}
                </div>
              )}

              {editTenant && editTab === 'capabilities' && (
                <div className="space-y-4">
                  <p className="text-sm leading-relaxed text-ink-muted">
                    Controls whether agents owned by this tenant may be instrumented.
                    This is independent of who calls the MCP tools.
                  </p>
                  <div className="space-y-4 rounded-ui border border-line bg-canvas p-4">
                    <Switch
                      checked={formInstrumentationInherit}
                      onChange={setFormInstrumentationInherit}
                      label="Inherit deployment default"
                      description="Recommended. Leave this tenant without an explicit override."
                    />
                    <div className="border-t border-line pt-4">
                      <Switch
                        checked={formInstrumentation}
                        onChange={setFormInstrumentation}
                        disabled={formInstrumentationInherit}
                        label="Allow dynamic instrumentation"
                        description="Applies to this tenant's agents only."
                      />
                    </div>
                  </div>
                </div>
              )}

              {editTenant && editTab === 'query' && (
                <div className="space-y-4">
                  <p className="text-sm leading-relaxed text-ink-muted">
                    Read-only MCP query limits for callers authenticated as this tenant.
                    Overrides may be larger than the deployment defaults.
                  </p>
                  <div className="rounded-ui border border-line bg-canvas p-4">
                    <Switch
                      checked={formQueryInheritAll}
                      onChange={setFormQueryInheritAll}
                      label="Inherit all query limits"
                      description="Use the deployment defaults for all six values."
                    />
                  </div>
                  {!formQueryInheritAll && (
                    <div className="grid gap-3 sm:grid-cols-2">
                      {QUERY_LIMIT_FIELDS.map(field => (
                        <div
                          key={field.key}
                          className={cn(
                            'rounded-ui border border-line p-3 transition-colors',
                            formQueryDefaults[field.key] ? 'bg-canvas' : 'bg-surface',
                          )}
                        >
                          <Switch
                            checked={!formQueryDefaults[field.key]}
                            onChange={(override) => setFormQueryDefaults(prev => ({ ...prev, [field.key]: !override }))}
                            label={`Override ${field.label}`}
                          />
                          <div className="mt-3 flex items-center gap-2">
                            <span className="w-32 shrink-0 text-xs text-ink-muted">{field.label}</span>
                            <Input
                              mono
                              aria-label={`${field.label} override`}
                              value={formQueryValues[field.key]}
                              disabled={formQueryDefaults[field.key]}
                              onChange={e => setFormQueryValues(prev => ({ ...prev, [field.key]: e.target.value }))}
                              placeholder={`default ${field.hint}`}
                              className="h-9"
                            />
                          </div>
                        </div>
                      ))}
                    </div>
                  )}
                </div>
              )}
            </div>

            <footer className="flex items-center justify-between gap-3 border-t border-line px-6 py-3">
              <span className="text-xs text-ink-muted">
                {editTenant ? 'Changes apply immediately after saving.' : 'You can add API keys after creation.'}
              </span>
              <div className="flex gap-2">
                <Button
                  variant="outline"
                  onClick={() => { setShowCreateModal(false); setEditTenant(null); }}
                >
                  Cancel
                </Button>
                <Button onClick={editTenant ? submitEdit : submitCreate}>
                  {editTenant ? 'Save changes' : 'Create tenant'}
                </Button>
              </div>
            </footer>
          </div>
        </div>
      )}

      {/* ── Key 管理模态框 ── */}
      {keysTenant && (
        <div className="fixed inset-0 bg-black/40 flex items-center justify-center z-50">
          <div className="bg-white rounded-lg shadow-xl w-[560px] p-6">
            <div className="flex items-center justify-between mb-4">
              <h2 className="text-lg font-semibold">
                API Keys — <span className="text-blue-600">{keysTenant.name}</span>
              </h2>
              <button onClick={() => setKeysTenant(null)} className="text-gray-500 hover:text-gray-700 text-xl">
                ×
              </button>
            </div>

            {/* 新建 key */}
            <div className="flex items-end gap-2 mb-4 pb-4 border-b">
              <div className="flex-1">
                <label className="block text-xs text-gray-600 mb-1">Key name</label>
                <input
                  value={keyName}
                  onChange={e => setKeyName(e.target.value)}
                  className="w-full border rounded px-3 py-1.5 text-sm"
                  placeholder="e.g. Grafana Tempo"
                />
              </div>
              <div>
                <label className="block text-xs text-gray-600 mb-1">Type</label>
                <select
                  value={keyType}
                  onChange={e => setKeyType(e.target.value as 'tk' | 'ok')}
                  className="border rounded px-2 py-1.5 text-sm"
                >
                  <option value="tk">tk (tenant)</option>
                  <option value="ok">ok (operator)</option>
                </select>
              </div>
              <button
                onClick={createKey}
                className="px-3 py-1.5 text-sm bg-blue-600 text-white rounded hover:bg-blue-700"
              >
                Create
              </button>
            </div>

            {/* Scopes —— tk_ keys need explicit read scopes for query tools */}
            <div className="mb-4 -mt-1">
              <div className="text-xs text-gray-600 mb-1">
                Scopes (tk_ keys only; super / ok_ keys are unrestricted)
              </div>
              <div className="flex flex-wrap gap-x-4 gap-y-1">
                {([
                  ['instrumentation:write', keyInstrumentationWrite, setKeyInstrumentationWrite],
                  ['metric:read', keyMetricRead, setKeyMetricRead],
                  ['trace:read', keyTraceRead, setKeyTraceRead],
                  ['log:read', keyLogRead, setKeyLogRead],
                ] as const).map(([label, checked, setter]) => (
                  <label key={label} className="flex items-center gap-1.5 text-xs text-gray-600">
                    <input
                      type="checkbox"
                      checked={checked}
                      onChange={e => setter(e.target.checked)}
                      className="rounded"
                    />
                    {label}
                  </label>
                ))}
              </div>
            </div>

            {/* 一次性明文 key */}
            {newPlainKey && (
              <div className="mb-4 p-3 bg-yellow-50 border border-yellow-300 rounded text-sm">
                <div className="font-medium text-yellow-800 mb-1">Copy this key now — it won't be shown again:</div>
                <code className="block font-mono text-xs break-all bg-white p-2 rounded">{newPlainKey}</code>
              </div>
            )}

            {/* key 列表 */}
            {keysLoading ? (
              <div className="text-sm text-gray-500">Loading keys…</div>
            ) : keys.length === 0 ? (
              <div className="text-sm text-gray-400">No API keys yet.</div>
            ) : (
              <table className="w-full text-sm">
                <thead className="text-left text-gray-500 text-xs">
                  <tr>
                    <th className="py-2">Name</th>
                    <th className="py-2">Type</th>
                    <th className="py-2">Prefix</th>
                    <th className="py-2">Status</th>
                    <th className="py-2 text-right">Action</th>
                  </tr>
                </thead>
                <tbody className="divide-y divide-gray-100">
                  {keys.map(k => (
                    <tr key={k.id}>
                      <td className="py-2">{k.name}</td>
                      <td className="py-2">
                        <span className="font-mono text-xs bg-gray-100 px-1.5 py-0.5 rounded">{k.key_type}</span>
                      </td>
                      <td className="py-2 font-mono text-xs text-gray-500">{k.key_prefix}…</td>
                      <td className="py-2">
                        <span
                          className={`px-2 py-0.5 rounded-full text-xs ${
                            k.status === 'active' ? 'bg-green-100 text-green-700' : 'bg-red-100 text-red-600'
                          }`}
                        >
                          {k.status}
                        </span>
                      </td>
                      <td className="py-2 text-right">
                        {k.status === 'active' && (
                          <button onClick={() => revokeKey(k)} className="text-red-600 hover:underline text-xs">
                            Revoke
                          </button>
                        )}
                      </td>
                    </tr>
                  ))}
                </tbody>
              </table>
            )}
          </div>
        </div>
      )}
    </div>
  );
}
