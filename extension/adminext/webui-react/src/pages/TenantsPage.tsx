/**
 * Tenants 页面 - 多租户管理（租户 CRUD + 读路径 API Key 管理）
 *
 * 参照 AppsPage 的交互模式：表格 + 创建/编辑模态框 + Key 管理模态框。
 */

import { useState, useEffect, useCallback } from 'react';
import { apiClient } from '@/api/client';
import { useToast } from '@/contexts/ToastContext';
import EmptyState from '@/components/EmptyState';
import type { Tenant, TenantAPIKey } from '@/types/api';

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

  // Key 管理模态框
  const [keysTenant, setKeysTenant] = useState<Tenant | null>(null);
  const [keys, setKeys] = useState<TenantAPIKey[]>([]);
  const [keysLoading, setKeysLoading] = useState(false);
  const [keyName, setKeyName] = useState('');
  const [keyType, setKeyType] = useState<'tk' | 'ok'>('tk');
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
    setShowCreateModal(true);
  };

  const openEdit = (t: Tenant) => {
    setEditTenant(t);
    setFormName(t.name);
    setFormDesc(t.description || '');
    setFormStatus(t.status);
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
    try {
      await apiClient.updateTenant(editTenant.id, {
        name: formName.trim(),
        description: formDesc.trim() || undefined,
        status: formStatus,
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
    try {
      const resp = await apiClient.createTenantKey(keysTenant.id, {
        name: keyName.trim(),
        key_type: keyType,
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
        <div className="fixed inset-0 bg-black/40 flex items-center justify-center z-50">
          <div className="bg-white rounded-lg shadow-xl w-96 p-6">
            <h2 className="text-lg font-semibold mb-4">{editTenant ? 'Edit Tenant' : 'New Tenant'}</h2>
            <label className="block text-sm text-gray-600 mb-1">Name</label>
            <input
              value={formName}
              onChange={e => setFormName(e.target.value)}
              className="w-full border rounded px-3 py-2 mb-3 text-sm"
              placeholder="e.g. acme"
            />
            <label className="block text-sm text-gray-600 mb-1">Description</label>
            <input
              value={formDesc}
              onChange={e => setFormDesc(e.target.value)}
              className="w-full border rounded px-3 py-2 mb-3 text-sm"
              placeholder="Optional"
            />
            {editTenant && (
              <>
                <label className="block text-sm text-gray-600 mb-1">Status</label>
                <select
                  value={formStatus}
                  onChange={e => setFormStatus(e.target.value as 'active' | 'disabled')}
                  className="w-full border rounded px-3 py-2 mb-3 text-sm"
                >
                  <option value="active">active</option>
                  <option value="disabled">disabled</option>
                </select>
              </>
            )}
            <div className="flex justify-end gap-2 mt-4">
              <button
                onClick={() => {
                  setShowCreateModal(false);
                  setEditTenant(null);
                }}
                className="px-3 py-1.5 text-sm text-gray-600 hover:bg-gray-100 rounded"
              >
                Cancel
              </button>
              <button
                onClick={editTenant ? submitEdit : submitCreate}
                className="px-3 py-1.5 text-sm bg-blue-600 text-white rounded hover:bg-blue-700"
              >
                {editTenant ? 'Save' : 'Create'}
              </button>
            </div>
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
