/**
 * Services 页面 - 服务卡片列表
 *
 * 从旧版 Alpine.js services.html 迁移。
 */

import { useState, useEffect, useCallback } from 'react';
import { useNavigate } from 'react-router-dom';
import { apiClient } from '@/api/client';
import { useToast } from '@/contexts/ToastContext';
import type { Service } from '@/types/api';

export default function ServicesPage() {
  const navigate = useNavigate();
  const { showToast } = useToast();

  const [services, setServices] = useState<Service[]>([]);
  const [loading, setLoading] = useState(false);

  const loadServices = useCallback(async () => {
    if (loading) return;
    setLoading(true);
    try {
      const data = await apiClient.getServices();
      // API 可能返回 {services: [...]} 或直接返回数组
      setServices(Array.isArray(data) ? data : (data as unknown as { services: Service[] }).services || []);
    } catch (e: unknown) {
      const err = e as { message?: string };
      showToast(err.message || 'Failed to load services', 'error');
    } finally {
      setLoading(false);
    }
  }, [loading, showToast]);

  useEffect(() => {
    loadServices();
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  return (
    <div>
      <div className="flex items-center justify-between mb-6">
        <h2 className="text-2xl font-bold text-gray-800">Services</h2>
        <button
          onClick={loadServices}
          disabled={loading}
          className="px-4 py-2 bg-gray-100 text-gray-700 rounded-lg hover:bg-gray-200 transition disabled:opacity-50"
        >
          <i className={`fas fa-sync ${loading ? 'fa-spin' : ''}`} /> Refresh
        </button>
      </div>

      <div className="grid grid-cols-1 md:grid-cols-2 lg:grid-cols-3 gap-6">
        {services.map((service) => (
          <div
            key={`${service.service_name}-${service.app_name}`}
            className="bg-white rounded-xl shadow-sm border border-gray-100 p-6 hover:shadow-md transition"
          >
            <div className="flex items-start justify-between mb-4">
              <div>
                <h3 className="font-semibold text-gray-800">{service.service_name}</h3>
                <p className="text-sm text-gray-500">{service.app_name}</p>
              </div>
              <span className="px-3 py-1 bg-blue-100 text-blue-700 rounded-full text-sm font-medium">
                {service.instance_count} instances
              </span>
            </div>
            {service.online_count !== undefined && (
              <div className="flex items-center gap-2 mb-4 text-sm">
                <span className="flex items-center gap-1 text-green-600">
                  <span className="w-2 h-2 rounded-full bg-green-500" />
                  {service.online_count} online
                </span>
                {service.instance_count - service.online_count > 0 && (
                  <span className="flex items-center gap-1 text-gray-400">
                    <span className="w-2 h-2 rounded-full bg-gray-300" />
                    {service.instance_count - service.online_count} offline
                  </span>
                )}
              </div>
            )}
            <button
              onClick={() => navigate('/instances')}
              className="w-full mt-2 px-4 py-2 bg-gray-100 text-gray-700 rounded-lg hover:bg-gray-200 transition text-sm"
            >
              View Instances
            </button>
          </div>
        ))}

        {services.length === 0 && !loading && (
          <div className="col-span-full text-center py-12 text-gray-500">
            <i className="fas fa-sitemap text-4xl mb-3 text-gray-300 block" />
            <p>No services found</p>
          </div>
        )}

        {loading && services.length === 0 && (
          <div className="col-span-full text-center py-12 text-gray-400">
            <i className="fas fa-spinner fa-spin text-3xl mb-3 block" />
            <p>Loading services...</p>
          </div>
        )}
      </div>
    </div>
  );
}
