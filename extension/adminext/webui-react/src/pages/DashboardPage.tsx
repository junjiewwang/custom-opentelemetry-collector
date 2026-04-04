/**
 * Dashboard 页面 - 统计概览 + Quick Actions
 * 
 * 从旧版 Alpine.js dashboard.html 迁移。
 */

import { useState, useEffect, useCallback } from 'react';
import { useNavigate } from 'react-router-dom';
import { apiClient } from '@/api/client';
import { useToast } from '@/contexts/ToastContext';
import type { DashboardOverview } from '@/types/api';

/** 自动刷新间隔（毫秒） */
const AUTO_REFRESH_INTERVAL = 30000;

/** Quick Action 配置 */
const QUICK_ACTIONS = [
  { label: 'Applications', icon: 'fas fa-cube', path: '/apps', color: 'bg-blue-600 hover:bg-blue-700 text-white' },
  { label: 'Instances', icon: 'fas fa-server', path: '/instances', color: 'bg-gray-100 text-gray-700 hover:bg-gray-200' },
  { label: 'Tasks', icon: 'fas fa-tasks', path: '/tasks', color: 'bg-gray-100 text-gray-700 hover:bg-gray-200' },
  { label: 'Traces', icon: 'fas fa-route', path: '/traces', color: 'bg-gray-100 text-gray-700 hover:bg-gray-200' },
  { label: 'Metrics', icon: 'fas fa-chart-line', path: '/metrics', color: 'bg-gray-100 text-gray-700 hover:bg-gray-200' },
  { label: 'Service Map', icon: 'fas fa-project-diagram', path: '/service-map', color: 'bg-gray-100 text-gray-700 hover:bg-gray-200' },
];

export default function DashboardPage() {
  const navigate = useNavigate();
  const { showToast } = useToast();

  const [dashboard, setDashboard] = useState<DashboardOverview | null>(null);
  const [loading, setLoading] = useState(false);

  const loadDashboard = useCallback(async () => {
    if (loading) return;
    setLoading(true);
    try {
      const data = await apiClient.getDashboard();
      setDashboard(data);
    } catch (e: unknown) {
      const err = e as { message?: string };
      showToast(err.message || 'Failed to load dashboard', 'error');
    } finally {
      setLoading(false);
    }
  }, [loading, showToast]);

  // 初始加载 + 自动刷新
  useEffect(() => {
    loadDashboard();
    const timer = setInterval(() => {
      loadDashboard();
    }, AUTO_REFRESH_INTERVAL);
    return () => clearInterval(timer);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  return (
    <div>
      <h2 className="text-2xl font-bold text-gray-800 mb-6">Dashboard Overview</h2>

      {/* 统计卡片 */}
      <div className="grid grid-cols-1 md:grid-cols-2 lg:grid-cols-4 gap-6 mb-8">
        {!dashboard ? (
          /* 骨架屏占位 */
          <>
            {Array.from({ length: 4 }).map((_, i) => (
              <div key={i} className="bg-white rounded-xl shadow-sm p-6 border border-gray-100">
                <div className="flex items-center justify-between">
                  <div className="space-y-3 flex-1">
                    <div className="h-3.5 skeleton-shimmer rounded w-24" />
                    <div className="h-8 skeleton-shimmer rounded w-16" />
                  </div>
                  <div className="w-12 h-12 rounded-lg skeleton-shimmer flex-shrink-0" />
                </div>
              </div>
            ))}
          </>
        ) : (
        <>
        {/* Total Apps */}
        <div className="bg-white rounded-xl shadow-sm p-6 border border-gray-100 hover:shadow-md transition-shadow cursor-pointer content-fade-in" onClick={() => navigate('/apps')}>
          <div className="flex items-center justify-between">
            <div>
              <p className="text-gray-500 text-sm">Total Apps</p>
              <p className="text-3xl font-bold text-gray-800">
                {dashboard?.total_apps ?? 0}
              </p>
            </div>
            <div className="w-12 h-12 bg-blue-100 rounded-lg flex items-center justify-center">
              <i className="fas fa-cube text-blue-600 text-xl" />
            </div>
          </div>
        </div>

        {/* Online Instances */}
        <div className="bg-white rounded-xl shadow-sm p-6 border border-gray-100 hover:shadow-md transition-shadow cursor-pointer content-fade-in" onClick={() => navigate('/instances')}>
          <div className="flex items-center justify-between">
            <div>
              <p className="text-gray-500 text-sm">Online Instances</p>
              <p className="text-3xl font-bold text-green-600">
                {dashboard?.online_instances ?? 0}
              </p>
            </div>
            <div className="w-12 h-12 bg-green-100 rounded-lg flex items-center justify-center">
              <i className="fas fa-server text-green-600 text-xl" />
            </div>
          </div>
          <div className="mt-3 flex gap-4 text-sm">
            <span className="text-gray-500">
              Total: <span className="text-gray-700">{dashboard?.total_instances ?? 0}</span>
            </span>
            <span className="text-red-500">
              Offline: {(dashboard?.total_instances ?? 0) - (dashboard?.online_instances ?? 0)}
            </span>
          </div>
        </div>

        {/* Total Services */}
        <div className="bg-white rounded-xl shadow-sm p-6 border border-gray-100 hover:shadow-md transition-shadow cursor-pointer content-fade-in" onClick={() => navigate('/services')}>
          <div className="flex items-center justify-between">
            <div>
              <p className="text-gray-500 text-sm">Total Services</p>
              <p className="text-3xl font-bold text-gray-800">
                {dashboard?.total_services ?? 0}
              </p>
            </div>
            <div className="w-12 h-12 bg-purple-100 rounded-lg flex items-center justify-center">
              <i className="fas fa-sitemap text-purple-600 text-xl" />
            </div>
          </div>
        </div>

        {/* Pending Tasks */}
        <div className="bg-white rounded-xl shadow-sm p-6 border border-gray-100 hover:shadow-md transition-shadow cursor-pointer content-fade-in" onClick={() => navigate('/tasks')}>
          <div className="flex items-center justify-between">
            <div>
              <p className="text-gray-500 text-sm">Pending Tasks</p>
              <p className="text-3xl font-bold text-yellow-500">
                {dashboard?.pending_tasks ?? 0}
              </p>
            </div>
            <div className="w-12 h-12 bg-yellow-100 rounded-lg flex items-center justify-center">
              <i className="fas fa-tasks text-yellow-600 text-xl" />
            </div>
          </div>
          {(dashboard?.running_tasks ?? 0) > 0 && (
            <div className="mt-3 text-sm text-blue-500">
              Running: {dashboard?.running_tasks ?? 0}
            </div>
          )}
        </div>
        </>
        )}
      </div>

      {/* 下半部分：Quick Actions + 系统信息 */}
      <div className="grid grid-cols-1 lg:grid-cols-3 gap-6">
        {/* Quick Actions — 占 2 列 */}
        <div className="lg:col-span-2 bg-white rounded-xl shadow-sm p-6 border border-gray-100">
          <h3 className="text-lg font-semibold text-gray-800 mb-4 flex items-center gap-2">
            <i className="fas fa-bolt text-yellow-500" />
            Quick Actions
          </h3>
          <div className="grid grid-cols-2 sm:grid-cols-3 gap-3">
            {QUICK_ACTIONS.map((action) => (
              <button
                key={action.label}
                onClick={() => navigate(action.path)}
                className={`px-4 py-3 rounded-lg transition flex items-center gap-2.5 text-sm font-medium ${action.color}`}
              >
                <i className={action.icon} />
                {action.label}
              </button>
            ))}
          </div>
        </div>

        {/* 系统信息 — 占 1 列 */}
        <div className="bg-white rounded-xl shadow-sm p-6 border border-gray-100">
          <h3 className="text-lg font-semibold text-gray-800 mb-4 flex items-center gap-2">
            <i className="fas fa-info-circle text-blue-500" />
            System Info
          </h3>
          <div className="space-y-3">
            <div className="flex items-center justify-between text-sm">
              <span className="text-gray-500">Status</span>
              <span className="flex items-center gap-1.5 text-green-600 font-medium">
                <span className="w-2 h-2 rounded-full bg-green-500 animate-pulse" />
                Healthy
              </span>
            </div>
            <div className="border-t border-gray-100" />
            <div className="flex items-center justify-between text-sm">
              <span className="text-gray-500">Apps</span>
              <span className="text-gray-700 font-medium">{dashboard?.total_apps ?? 0}</span>
            </div>
            <div className="flex items-center justify-between text-sm">
              <span className="text-gray-500">Instances</span>
              <span className="text-gray-700 font-medium">
                <span className="text-green-600">{dashboard?.online_instances ?? 0}</span>
                <span className="text-gray-400 mx-1">/</span>
                {dashboard?.total_instances ?? 0}
              </span>
            </div>
            <div className="flex items-center justify-between text-sm">
              <span className="text-gray-500">Services</span>
              <span className="text-gray-700 font-medium">{dashboard?.total_services ?? 0}</span>
            </div>
            <div className="border-t border-gray-100" />
            <div className="flex items-center justify-between text-sm">
              <span className="text-gray-500">Auto Refresh</span>
              <span className="text-gray-700 font-medium">30s</span>
            </div>
            <div className="flex items-center justify-between text-sm">
              <span className="text-gray-500">Version</span>
              <span className="text-gray-400 font-mono text-xs">v1.0.0</span>
            </div>
          </div>
        </div>
      </div>

      {/* 健康度概览 */}
      <div className="mt-6 bg-white rounded-xl shadow-sm p-6 border border-gray-100">
        <h3 className="text-lg font-semibold text-gray-800 mb-4 flex items-center gap-2">
          <i className="fas fa-heartbeat text-red-500" />
          Health Overview
        </h3>
        <div className="grid grid-cols-1 md:grid-cols-3 gap-4">
          {/* Instance 健康度 */}
          <div className="bg-gray-50 rounded-lg p-4">
            <div className="flex items-center justify-between mb-2">
              <span className="text-sm font-medium text-gray-700">Instance Health</span>
              <span className="text-sm font-bold text-green-600">
                {dashboard?.total_instances
                  ? Math.round(((dashboard?.online_instances ?? 0) / dashboard.total_instances) * 100)
                  : 0}%
              </span>
            </div>
            <div className="w-full bg-gray-200 rounded-full h-2">
              <div
                className="bg-green-500 h-2 rounded-full transition-all duration-500"
                style={{
                  width: `${
                    dashboard?.total_instances
                      ? Math.round(((dashboard?.online_instances ?? 0) / dashboard.total_instances) * 100)
                      : 0
                  }%`,
                }}
              />
            </div>
            <p className="text-xs text-gray-500 mt-1.5">
              {dashboard?.online_instances ?? 0} online / {dashboard?.total_instances ?? 0} total
            </p>
          </div>

          {/* Task 成功率 */}
          <div className="bg-gray-50 rounded-lg p-4">
            <div className="flex items-center justify-between mb-2">
              <span className="text-sm font-medium text-gray-700">Task Queue</span>
              <span className="text-sm font-bold text-yellow-600">
                {dashboard?.pending_tasks ?? 0} pending
              </span>
            </div>
            <div className="flex gap-2 mt-1">
              {(dashboard?.running_tasks ?? 0) > 0 && (
                <span className="inline-flex items-center gap-1 px-2 py-0.5 bg-blue-100 text-blue-700 rounded text-xs font-medium">
                  <i className="fas fa-spinner fa-spin text-[10px]" />
                  {dashboard?.running_tasks} running
                </span>
              )}
              {(dashboard?.pending_tasks ?? 0) > 0 && (
                <span className="inline-flex items-center gap-1 px-2 py-0.5 bg-yellow-100 text-yellow-700 rounded text-xs font-medium">
                  <i className="fas fa-clock text-[10px]" />
                  {dashboard?.pending_tasks} pending
                </span>
              )}
              {(dashboard?.pending_tasks ?? 0) === 0 && (dashboard?.running_tasks ?? 0) === 0 && (
                <span className="inline-flex items-center gap-1 px-2 py-0.5 bg-green-100 text-green-700 rounded text-xs font-medium">
                  <i className="fas fa-check text-[10px]" />
                  All clear
                </span>
              )}
            </div>
          </div>

          {/* Observability 状态 */}
          <div className="bg-gray-50 rounded-lg p-4">
            <div className="flex items-center justify-between mb-2">
              <span className="text-sm font-medium text-gray-700">Observability</span>
            </div>
            <div className="space-y-1.5">
              <button onClick={() => navigate('/traces')} className="w-full flex items-center gap-2 text-xs text-gray-600 hover:text-primary-600 transition-colors">
                <i className="fas fa-route w-4 text-center" />
                <span>Traces — Distributed Tracing</span>
                <i className="fas fa-chevron-right text-[10px] ml-auto text-gray-400" />
              </button>
              <button onClick={() => navigate('/metrics')} className="w-full flex items-center gap-2 text-xs text-gray-600 hover:text-primary-600 transition-colors">
                <i className="fas fa-chart-line w-4 text-center" />
                <span>Metrics — RED Dashboard</span>
                <i className="fas fa-chevron-right text-[10px] ml-auto text-gray-400" />
              </button>
              <button onClick={() => navigate('/service-map')} className="w-full flex items-center gap-2 text-xs text-gray-600 hover:text-primary-600 transition-colors">
                <i className="fas fa-project-diagram w-4 text-center" />
                <span>Service Map — Topology</span>
                <i className="fas fa-chevron-right text-[10px] ml-auto text-gray-400" />
              </button>
            </div>
          </div>
        </div>
      </div>
    </div>
  );
}
