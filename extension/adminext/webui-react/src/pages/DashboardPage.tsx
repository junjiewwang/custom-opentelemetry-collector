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
        {/* Total Apps */}
        <div className="bg-white rounded-xl shadow-sm p-6 border border-gray-100">
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
        <div className="bg-white rounded-xl shadow-sm p-6 border border-gray-100">
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

        {/* Unhealthy */}
        <div className="bg-white rounded-xl shadow-sm p-6 border border-gray-100">
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
        <div className="bg-white rounded-xl shadow-sm p-6 border border-gray-100">
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
      </div>

      {/* Quick Actions */}
      <div className="bg-white rounded-xl shadow-sm p-6 border border-gray-100">
        <h3 className="text-lg font-semibold text-gray-800 mb-4">Quick Actions</h3>
        <div className="flex flex-wrap gap-3">
          <button
            onClick={() => navigate('/apps')}
            className="px-4 py-2 bg-blue-600 text-white rounded-lg hover:bg-blue-700 transition flex items-center gap-2"
          >
            <i className="fas fa-cube" /> Applications
          </button>
          <button
            onClick={() => navigate('/instances')}
            className="px-4 py-2 bg-gray-100 text-gray-700 rounded-lg hover:bg-gray-200 transition flex items-center gap-2"
          >
            <i className="fas fa-server" /> Instances
          </button>
          <button
            onClick={() => navigate('/tasks')}
            className="px-4 py-2 bg-gray-100 text-gray-700 rounded-lg hover:bg-gray-200 transition flex items-center gap-2"
          >
            <i className="fas fa-tasks" /> Tasks
          </button>
          <button
            onClick={() => navigate('/traces')}
            className="px-4 py-2 bg-gray-100 text-gray-700 rounded-lg hover:bg-gray-200 transition flex items-center gap-2"
          >
            <i className="fas fa-route" /> Traces
          </button>
          <button
            onClick={() => navigate('/metrics')}
            className="px-4 py-2 bg-gray-100 text-gray-700 rounded-lg hover:bg-gray-200 transition flex items-center gap-2"
          >
            <i className="fas fa-chart-line" /> Metrics
          </button>
        </div>
      </div>
    </div>
  );
}
