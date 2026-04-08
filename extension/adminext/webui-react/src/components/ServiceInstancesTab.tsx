/**
 * ServiceInstancesTab — 服务实例列表 Tab
 *
 * 展示指定服务下的所有实例，支持状态筛选和基本信息展示。
 * 点击实例可跳转到 Instances 页面进行详细管理。
 */

import { useState, useEffect, useCallback, useMemo } from 'react';
import { useNavigate } from 'react-router-dom';
import { apiClient } from '@/api/client';
import { useToast } from '@/contexts/ToastContext';
import EmptyState from '@/components/EmptyState';
import type { Instance, ApiError } from '@/types/api';

// ── Props ──────────────────────────────────────────────

interface ServiceInstancesTabProps {
  appId: string;
  serviceName: string;
}

// ── 工具函数 ──────────────────────────────────────────

function formatRelativeTime(ts: number): string {
  if (!ts) return '-';
  const ms = ts < 10000000000 ? ts * 1000 : ts;
  const diff = Date.now() - ms;
  if (diff < 0) return '-';
  if (diff < 30_000) return 'just now';
  const mins = Math.floor(diff / 60_000);
  if (mins < 60) return `${mins}m ago`;
  const hrs = Math.floor(mins / 60);
  if (hrs < 24) return `${hrs}h ago`;
  const days = Math.floor(hrs / 24);
  if (days < 30) return `${days}d ago`;
  return new Date(ms).toLocaleString('zh-CN');
}

function formatUptime(ts: number): string {
  if (!ts) return '-';
  const ms = ts < 10000000000 ? ts * 1000 : ts;
  const diff = Date.now() - ms;
  if (diff < 0) return '-';
  const secs = Math.floor(diff / 1000);
  const mins = Math.floor(secs / 60);
  const hrs = Math.floor(mins / 60);
  const days = Math.floor(hrs / 24);
  if (days > 0) return `${days}d ${hrs % 24}h`;
  if (hrs > 0) return `${hrs}h ${mins % 60}m`;
  if (mins > 0) return `${mins}m`;
  return `${secs}s`;
}

// ── 状态筛选 ──────────────────────────────────────────

type StatusFilter = 'all' | 'online' | 'offline';

// ── 主组件 ──────────────────────────────────────────────

export default function ServiceInstancesTab({ appId, serviceName }: ServiceInstancesTabProps) {
  const navigate = useNavigate();
  const { showToast } = useToast();

  const [instances, setInstances] = useState<Instance[]>([]);
  const [loading, setLoading] = useState(false);
  const [statusFilter, setStatusFilter] = useState<StatusFilter>('all');

  const loadInstances = useCallback(async () => {
    setLoading(true);
    try {
      const data = await apiClient.getInstances('', {
        app_id: appId,
        service_name: serviceName,
      });
      setInstances(data);
    } catch (e) {
      showToast(`Failed to load instances: ${(e as ApiError).message}`, 'error');
    } finally {
      setLoading(false);
    }
  }, [appId, serviceName, showToast]);

  useEffect(() => {
    loadInstances();
  }, [loadInstances]);

  // ── 过滤 ──────────────────────────────────────────

  const filteredInstances = useMemo(() => {
    if (statusFilter === 'online') return instances.filter(i => i.status?.state === 'online');
    if (statusFilter === 'offline') return instances.filter(i => i.status?.state !== 'online');
    return instances;
  }, [instances, statusFilter]);

  const stats = useMemo(() => {
    let online = 0;
    for (const inst of instances) {
      if (inst.status?.state === 'online') online++;
    }
    return { total: instances.length, online, offline: instances.length - online };
  }, [instances]);

  // ── Render ──────────────────────────────────────────

  if (loading && instances.length === 0) {
    return (
      <EmptyState
        icon="fas fa-spinner fa-spin"
        iconColor="text-blue-300"
        iconBg="bg-blue-50"
        title="Loading instances..."
        size="sm"
      />
    );
  }

  if (instances.length === 0) {
    return (
      <EmptyState
        icon="fas fa-server"
        title="No Instances"
        description="This service has no registered instances."
        size="sm"
      />
    );
  }

  return (
    <div className="space-y-3">
      {/* 顶部统计 + 筛选 */}
      <div className="flex items-center justify-between">
        <div className="flex items-center gap-2">
          {(['all', 'online', 'offline'] as StatusFilter[]).map(filter => {
            const count = filter === 'all' ? stats.total : filter === 'online' ? stats.online : stats.offline;
            const isActive = statusFilter === filter;
            const colors: Record<StatusFilter, { active: string; dot: string }> = {
              all: { active: 'text-blue-600 bg-blue-50 ring-blue-200', dot: 'bg-gray-400' },
              online: { active: 'text-green-600 bg-green-50 ring-green-200', dot: 'bg-green-500' },
              offline: { active: 'text-gray-600 bg-gray-100 ring-gray-300', dot: 'bg-gray-300' },
            };
            return (
              <button
                key={filter}
                onClick={() => setStatusFilter(filter)}
                className={`inline-flex items-center gap-1 px-2 py-0.5 rounded-md text-[10px] font-medium transition select-none ${
                  isActive ? `${colors[filter].active} ring-1` : 'text-gray-400 hover:text-gray-600 hover:bg-gray-50'
                }`}
              >
                <span className={`w-1.5 h-1.5 rounded-full ${isActive ? colors[filter].dot : 'bg-gray-300'}`} />
                <span className="capitalize">{filter}</span>
                <span className="font-bold tabular-nums">{count}</span>
              </button>
            );
          })}
        </div>

        <div className="flex items-center gap-2">
          <button
            onClick={loadInstances}
            className="w-7 h-7 flex items-center justify-center rounded-lg text-gray-400 hover:text-gray-600 hover:bg-gray-100 transition"
            title="Refresh"
          >
            <i className={`fas fa-sync-alt text-[10px] ${loading ? 'fa-spin' : ''}`} />
          </button>
          <button
            onClick={() => navigate('/instances')}
            className="text-[10px] text-blue-500 hover:text-blue-600 font-medium transition"
            title="Go to Instances page"
          >
            <i className="fas fa-external-link-alt mr-1 text-[8px]" />Full View
          </button>
        </div>
      </div>

      {/* 实例列表 */}
      <div className="space-y-1.5">
        {filteredInstances.map(inst => {
          const isOnline = inst.status?.state === 'online';
          return (
            <div
              key={inst.agent_id}
              className="flex items-center gap-3 px-3 py-2.5 bg-gray-50 border border-gray-100 rounded-lg hover:bg-gray-100/80 transition group"
            >
              {/* 状态图标 */}
              <div className={`w-8 h-8 rounded-lg flex items-center justify-center flex-shrink-0 ${
                isOnline ? 'bg-green-50 text-green-500' : 'bg-gray-100 text-gray-300'
              }`}>
                <i className="fas fa-server text-xs" />
              </div>

              {/* 主信息 */}
              <div className="flex-1 min-w-0">
                <div className="flex items-center gap-2">
                  <span className="text-[11px] font-semibold text-gray-700 truncate">
                    {inst.hostname || inst.agent_id.substring(0, 12)}
                  </span>
                  <span className={`w-1.5 h-1.5 rounded-full flex-shrink-0 ${isOnline ? 'bg-green-500' : 'bg-gray-300'}`} />
                  <span className="text-[9px] text-gray-400 font-mono truncate">{inst.ip || '0.0.0.0'}</span>
                </div>
                <div className="flex items-center gap-2 mt-0.5 text-[9px] text-gray-400">
                  <span>v{inst.version || '?'}</span>
                  <span className="text-gray-200">·</span>
                  <span>PID {inst.pid || '?'}</span>
                  <span className="text-gray-200">·</span>
                  <span>up {formatUptime(inst.start_time)}</span>
                </div>
              </div>

              {/* 右侧：心跳时间 */}
              <div className="text-right flex-shrink-0">
                <div className="text-[9px] text-gray-400">
                  <i className="fas fa-heartbeat text-[8px] mr-0.5" />
                  {formatRelativeTime(inst.last_heartbeat)}
                </div>
              </div>
            </div>
          );
        })}
      </div>

      {filteredInstances.length === 0 && (
        <EmptyState
          icon="fas fa-filter"
          title="No matching instances"
          description="Try changing the status filter."
          size="sm"
        />
      )}
    </div>
  );
}
