/**
 * Storage Admin 页面 - 存储管理与状态监控
 *
 * 功能：
 * - 存储健康状态概览
 * - 磁盘使用量（总量/已用/可用 + 按信号类型分布）
 * - 索引列表（名称、文档数、大小、信号类型）
 * - 保留策略查看/修改
 * - 数据清除操作
 */

import { useState, useEffect, useCallback } from 'react';
import { apiClient } from '@/api/client';
import type {
  StorageStatus,
  StorageHealth,
  DiskUsage,
  RetentionPolicies,
  IndexInfo,
  SignalType,
} from '@/types/storage';
import { formatBytes, formatRetention } from '@/types/storage';

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
  const [retention, setRetention] = useState<RetentionPolicies | null>(null);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState('');
  const [available, setAvailable] = useState<boolean | null>(null);

  // Retention edit
  const [editingRetention, setEditingRetention] = useState<SignalType | null>(null);
  const [retentionInput, setRetentionInput] = useState('');
  const [retentionSaving, setRetentionSaving] = useState(false);

  // Purge
  const [purging, setPurging] = useState(false);
  const [purgeMessage, setPurgeMessage] = useState('');

  // ========================================================================
  // Load data
  // ========================================================================

  const loadData = useCallback(async () => {
    setLoading(true);
    setError('');

    try {
      const [statusRes, healthRes, diskRes, retentionRes] = await Promise.allSettled([
        apiClient.getStorageStatus(),
        apiClient.getStorageHealth(),
        apiClient.getStorageDiskUsage(),
        apiClient.getStorageRetention(),
      ]);

      if (statusRes.status === 'fulfilled') setStatus(statusRes.value);
      else if ((statusRes.reason as { status?: number })?.status === 503) {
        setAvailable(false);
        setLoading(false);
        return;
      }

      if (healthRes.status === 'fulfilled') setHealth(healthRes.value);
      if (diskRes.status === 'fulfilled') setDiskUsage(diskRes.value);
      if (retentionRes.status === 'fulfilled') setRetention(retentionRes.value);

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

  // ========================================================================
  // Handlers
  // ========================================================================

  const handleSaveRetention = async (signal: SignalType) => {
    if (!retentionInput) return;
    setRetentionSaving(true);
    try {
      await apiClient.setStorageRetention(signal, retentionInput);
      setEditingRetention(null);
      setRetentionInput('');
      // Reload retention
      const res = await apiClient.getStorageRetention();
      setRetention(res);
    } catch (err: unknown) {
      const e = err as { message?: string };
      setError(e.message || 'Failed to update retention');
    } finally {
      setRetentionSaving(false);
    }
  };

  const handlePurge = async (signal: SignalType) => {
    const daysAgo = prompt(`清除 ${signal} 数据：输入清除多少天前的数据（例如 30）`);
    if (!daysAgo) return;

    const days = parseInt(daysAgo, 10);
    if (isNaN(days) || days <= 0) {
      setError('请输入有效的天数');
      return;
    }

    const before = new Date(Date.now() - days * 24 * 60 * 60 * 1000).toISOString();
    setPurging(true);
    setPurgeMessage('');

    try {
      const result = await apiClient.purgeStorage(signal, before);
      setPurgeMessage(`成功清除 ${result.deletedCount} 条记录${result.freedBytes ? `，释放 ${formatBytes(result.freedBytes)}` : ''}`);
      // Reload
      loadData();
    } catch (err: unknown) {
      const e = err as { message?: string };
      setError(e.message || 'Purge failed');
    } finally {
      setPurging(false);
    }
  };

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
      <div className="flex items-center justify-between">
        <h1 className="text-2xl font-bold text-gray-900 dark:text-gray-100">存储管理</h1>
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

      {/* Purge success message */}
      {purgeMessage && (
        <div className="bg-green-50 border border-green-200 rounded-lg p-3 text-green-700 text-sm">
          {purgeMessage}
          <button onClick={() => setPurgeMessage('')} className="ml-2 underline">关闭</button>
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

      {/* Retention Policies */}
      <div className="bg-white dark:bg-gray-800 rounded-lg shadow-sm border border-gray-200 dark:border-gray-700 p-4">
        <h3 className="text-sm font-medium text-gray-700 dark:text-gray-300 mb-3">保留策略</h3>
        <div className="grid grid-cols-1 md:grid-cols-3 gap-4">
          {(['trace', 'metric', 'log'] as SignalType[]).map(signal => {
            const policy = retention?.[signal];
            const isEditing = editingRetention === signal;

            return (
              <div key={signal} className="border border-gray-200 dark:border-gray-700 rounded-lg p-3">
                <div className="flex items-center justify-between mb-1">
                  <span className="text-xs font-medium text-gray-500 uppercase">{signal}</span>
                  <div className="flex gap-1">
                    {!isEditing && (
                      <button
                        onClick={() => {
                          setEditingRetention(signal);
                          setRetentionInput(policy?.duration || '720h');
                        }}
                        className="text-xs text-blue-600 hover:text-blue-700"
                      >
                        修改
                      </button>
                    )}
                    <button
                      onClick={() => handlePurge(signal)}
                      disabled={purging}
                      className="text-xs text-red-600 hover:text-red-700 disabled:opacity-50"
                    >
                      清除
                    </button>
                  </div>
                </div>

                {isEditing ? (
                  <div className="flex gap-1 mt-2">
                    <input
                      type="text"
                      value={retentionInput}
                      onChange={e => setRetentionInput(e.target.value)}
                      placeholder="例如: 720h"
                      className="flex-1 px-2 py-1 text-sm border border-gray-300 dark:border-gray-600 rounded"
                    />
                    <button
                      onClick={() => handleSaveRetention(signal)}
                      disabled={retentionSaving}
                      className="px-2 py-1 text-xs bg-blue-600 text-white rounded hover:bg-blue-700 disabled:opacity-50"
                    >
                      保存
                    </button>
                    <button
                      onClick={() => setEditingRetention(null)}
                      className="px-2 py-1 text-xs border border-gray-300 rounded hover:bg-gray-50"
                    >
                      取消
                    </button>
                  </div>
                ) : (
                  <p className="text-lg font-semibold text-gray-900 dark:text-gray-100">
                    {policy?.duration ? formatRetention(policy.duration) : '未设置'}
                  </p>
                )}
              </div>
            );
          })}
        </div>
      </div>

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
