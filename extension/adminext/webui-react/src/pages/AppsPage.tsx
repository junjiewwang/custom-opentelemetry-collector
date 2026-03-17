/**
 * Applications 页面 - 应用 CRUD + Token 管理
 *
 * 从旧版 Alpine.js apps.html 迁移。
 * 包含：应用表格、创建应用模态框、Token 管理模态框。
 */

import { useState, useEffect, useCallback } from 'react';
import { apiClient } from '@/api/client';
import { useToast } from '@/contexts/ToastContext';
import type { App } from '@/types/api';

/** Token 最大长度（与后端 MaxTokenLength 保持一致） */
const MAX_TOKEN_LENGTH = 64;

export default function AppsPage() {
  const { showToast } = useToast();

  const [apps, setApps] = useState<App[]>([]);
  const [loading, setLoading] = useState(false);

  // 创建应用模态框
  const [showCreateModal, setShowCreateModal] = useState(false);
  const [newAppName, setNewAppName] = useState('');
  const [newAppDesc, setNewAppDesc] = useState('');

  // Token 管理模态框
  const [showTokenModal, setShowTokenModal] = useState(false);
  const [tokenApp, setTokenApp] = useState<App | null>(null);
  const [customToken, setCustomToken] = useState('');

  const loadApps = useCallback(async () => {
    if (loading) return;
    setLoading(true);
    try {
      const data = await apiClient.getApps();
      // API 可能返回 {apps: [...]} 或直接返回数组
      setApps(Array.isArray(data) ? data : (data as unknown as { apps: App[] }).apps || []);
    } catch (e: unknown) {
      const err = e as { message?: string };
      showToast(err.message || 'Failed to load apps', 'error');
    } finally {
      setLoading(false);
    }
  }, [loading, showToast]);

  useEffect(() => {
    loadApps();
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  // ========================================================================
  // App CRUD
  // ========================================================================

  const createApp = async () => {
    if (!newAppName.trim()) {
      showToast('Application name is required', 'error');
      return;
    }
    try {
      await apiClient.createApp({ name: newAppName.trim(), description: newAppDesc.trim() || undefined });
      showToast('Application created successfully', 'success');
      setShowCreateModal(false);
      setNewAppName('');
      setNewAppDesc('');
      await loadApps();
    } catch (e: unknown) {
      const err = e as { message?: string };
      showToast(err.message || 'Failed to create app', 'error');
    }
  };

  const deleteApp = async (app: App) => {
    if (!window.confirm(`Delete "${app.name}"? This action cannot be undone.`)) return;
    try {
      await apiClient.deleteApp(app.id);
      showToast('Application deleted successfully', 'success');
      await loadApps();
    } catch (e: unknown) {
      const err = e as { message?: string };
      showToast(err.message || 'Failed to delete app', 'error');
    }
  };

  // ========================================================================
  // Token Management
  // ========================================================================

  const openTokenModal = (app: App) => {
    setTokenApp(app);
    setCustomToken('');
    setShowTokenModal(true);
  };

  const setCustomTokenForApp = async () => {
    if (!tokenApp || !customToken.trim()) return;
    try {
      await apiClient.setToken(tokenApp.id, customToken.trim());
      showToast('Token updated successfully', 'success');
      setShowTokenModal(false);
      setTokenApp(null);
      setCustomToken('');
      await loadApps();
    } catch (e: unknown) {
      const err = e as { message?: string };
      showToast(err.message || 'Failed to set token', 'error');
    }
  };

  const regenerateToken = async () => {
    if (!tokenApp) return;
    if (!window.confirm(`Generate a new random token for "${tokenApp.name}"? This will invalidate the current token.`)) return;
    try {
      await apiClient.regenerateToken(tokenApp.id);
      showToast('Token regenerated successfully', 'success');
      setShowTokenModal(false);
      setTokenApp(null);
      setCustomToken('');
      await loadApps();
    } catch (e: unknown) {
      const err = e as { message?: string };
      showToast(err.message || 'Failed to regenerate token', 'error');
    }
  };

  // ========================================================================
  // Helpers
  // ========================================================================

  const copyToClipboard = (text: string) => {
    navigator.clipboard.writeText(text);
    showToast('Copied to clipboard', 'success');
  };

  const formatDate = (dateStr: string) => {
    if (!dateStr) return '-';
    const date = new Date(dateStr);
    if (isNaN(date.getTime())) return dateStr;
    return date.toLocaleString('zh-CN');
  };

  return (
    <div>
      {/* Header */}
      <div className="flex items-center justify-between mb-6">
        <h2 className="text-2xl font-bold text-gray-800">Applications</h2>
        <button
          onClick={() => setShowCreateModal(true)}
          className="px-4 py-2 bg-blue-600 text-white rounded-lg hover:bg-blue-700 transition flex items-center gap-2"
        >
          <i className="fas fa-plus" /> Create App
        </button>
      </div>

      {/* Apps Table */}
      <div className="bg-white rounded-xl shadow-sm border border-gray-100 overflow-hidden">
        <table className="w-full">
          <thead className="bg-gray-50 border-b border-gray-100">
            <tr>
              <th className="px-6 py-4 text-left text-sm font-semibold text-gray-600">Name</th>
              <th className="px-6 py-4 text-left text-sm font-semibold text-gray-600">Token</th>
              <th className="px-6 py-4 text-left text-sm font-semibold text-gray-600">Status</th>
              <th className="px-6 py-4 text-left text-sm font-semibold text-gray-600">Created</th>
              <th className="px-6 py-4 text-right text-sm font-semibold text-gray-600">Actions</th>
            </tr>
          </thead>
          <tbody className="divide-y divide-gray-100">
            {apps.map((app) => (
              <tr key={app.id} className="hover:bg-gray-50 transition">
                <td className="px-6 py-4">
                  <div className="font-medium text-gray-800">{app.name}</div>
                  <div className="text-xs text-gray-400 font-mono">{app.id}</div>
                </td>
                <td className="px-6 py-4">
                  <div className="flex items-center gap-2">
                    <code className="text-sm bg-gray-100 px-2 py-1 rounded">
                      {app.token ? app.token.substring(0, 16) + '...' : '-'}
                    </code>
                    {app.token && (
                      <button
                        onClick={() => copyToClipboard(app.token)}
                        className="text-gray-400 hover:text-gray-600"
                        title="Copy token"
                      >
                        <i className="fas fa-copy" />
                      </button>
                    )}
                  </div>
                </td>
                <td className="px-6 py-4">
                  <span className="px-2 py-1 bg-green-100 text-green-700 rounded-full text-sm">
                    active
                  </span>
                </td>
                <td className="px-6 py-4 text-sm text-gray-500">{formatDate(app.created_at)}</td>
                <td className="px-6 py-4 text-right">
                  <div className="flex items-center justify-end gap-2">
                    <button
                      onClick={() => openTokenModal(app)}
                      className="p-2 text-gray-400 hover:text-yellow-500 transition"
                      title="Manage Token"
                    >
                      <i className="fas fa-key" />
                    </button>
                    <button
                      onClick={() => deleteApp(app)}
                      className="p-2 text-gray-400 hover:text-red-500 transition"
                      title="Delete"
                    >
                      <i className="fas fa-trash" />
                    </button>
                  </div>
                </td>
              </tr>
            ))}
            {apps.length === 0 && (
              <tr>
                <td colSpan={5} className="px-6 py-12 text-center text-gray-500">
                  <i className="fas fa-inbox text-4xl mb-3 text-gray-300 block" />
                  <p>No applications found</p>
                </td>
              </tr>
            )}
          </tbody>
        </table>
      </div>

      {/* ================================================================== */}
      {/* Create App Modal */}
      {/* ================================================================== */}
      {showCreateModal && (
        <div className="fixed inset-0 z-50 flex items-center justify-center">
          <div className="fixed inset-0 bg-gray-900/60 backdrop-blur-sm" onClick={() => setShowCreateModal(false)} />
          <div className="relative bg-white rounded-2xl shadow-2xl w-full max-w-md p-6 z-10">
            <h3 className="text-lg font-bold text-gray-800 mb-4">Create Application</h3>
            <div className="space-y-4">
              <div>
                <label className="block text-sm font-medium text-gray-700 mb-1">App Name *</label>
                <input
                  type="text"
                  value={newAppName}
                  onChange={(e) => setNewAppName(e.target.value)}
                  placeholder="my-service"
                  className="w-full px-4 py-2 border border-gray-200 rounded-lg focus:ring-2 focus:ring-blue-500 focus:border-blue-500"
                  onKeyDown={(e) => e.key === 'Enter' && createApp()}
                />
              </div>
              <div>
                <label className="block text-sm font-medium text-gray-700 mb-1">Description</label>
                <input
                  type="text"
                  value={newAppDesc}
                  onChange={(e) => setNewAppDesc(e.target.value)}
                  placeholder="Optional description"
                  className="w-full px-4 py-2 border border-gray-200 rounded-lg focus:ring-2 focus:ring-blue-500 focus:border-blue-500"
                />
              </div>
            </div>
            <div className="flex gap-3 mt-6">
              <button
                onClick={() => setShowCreateModal(false)}
                className="flex-1 px-4 py-2 bg-gray-100 text-gray-700 rounded-lg hover:bg-gray-200 transition"
              >
                Cancel
              </button>
              <button
                onClick={createApp}
                className="flex-1 px-4 py-2 bg-blue-600 text-white rounded-lg hover:bg-blue-700 transition"
              >
                Create
              </button>
            </div>
          </div>
        </div>
      )}

      {/* ================================================================== */}
      {/* Set Token Modal */}
      {/* ================================================================== */}
      {showTokenModal && tokenApp && (
        <div className="fixed inset-0 z-50 flex items-center justify-center">
          <div className="fixed inset-0 bg-gray-900/60 backdrop-blur-sm" onClick={() => setShowTokenModal(false)} />
          <div className="relative bg-white rounded-2xl shadow-2xl w-full max-w-md p-6 z-10">
            <h3 className="text-lg font-bold text-gray-800 mb-1">Manage Token</h3>
            <p className="text-sm text-gray-500 mb-4">{tokenApp.name}</p>

            <div className="space-y-4">
              {/* 当前 Token 展示 */}
              <div>
                <label className="block text-sm font-medium text-gray-700 mb-1">Current Token</label>
                <div className="flex items-center gap-2">
                  <code className="flex-1 text-xs bg-gray-100 px-3 py-2 rounded-lg font-mono break-all">
                    {tokenApp.token || '-'}
                  </code>
                  {tokenApp.token && (
                    <button
                      onClick={() => copyToClipboard(tokenApp.token)}
                      className="p-2 text-gray-400 hover:text-gray-600 transition"
                      title="Copy"
                    >
                      <i className="fas fa-copy" />
                    </button>
                  )}
                </div>
              </div>

              {/* 自定义 Token */}
              <div>
                <label className="block text-sm font-medium text-gray-700 mb-1">Set Custom Token</label>
                <input
                  type="text"
                  value={customToken}
                  onChange={(e) => setCustomToken(e.target.value)}
                  placeholder="Enter custom token..."
                  maxLength={MAX_TOKEN_LENGTH}
                  className="w-full px-4 py-2 border border-gray-200 rounded-lg focus:ring-2 focus:ring-blue-500 focus:border-blue-500 font-mono text-sm"
                />
                <p className="text-xs text-gray-400 mt-1">
                  {customToken.length}/{MAX_TOKEN_LENGTH} characters
                </p>
              </div>
            </div>

            <div className="flex gap-3 mt-6">
              <button
                onClick={() => setShowTokenModal(false)}
                className="flex-1 px-4 py-2 bg-gray-100 text-gray-700 rounded-lg hover:bg-gray-200 transition"
              >
                Cancel
              </button>
              <button
                onClick={regenerateToken}
                className="px-4 py-2 bg-yellow-100 text-yellow-700 rounded-lg hover:bg-yellow-200 transition text-sm"
              >
                <i className="fas fa-random mr-1" /> Random
              </button>
              <button
                onClick={setCustomTokenForApp}
                disabled={!customToken.trim()}
                className="flex-1 px-4 py-2 bg-blue-600 text-white rounded-lg hover:bg-blue-700 transition disabled:opacity-50"
              >
                Set Token
              </button>
            </div>
          </div>
        </div>
      )}
    </div>
  );
}
