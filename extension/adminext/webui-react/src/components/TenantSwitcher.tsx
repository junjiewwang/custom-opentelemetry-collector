/**
 * 租户切换器（admin 扮演）- 仅 admin 角色可见。
 *
 * admin（super/operator）可通过下拉选择一个租户扮演（view as tenant），
 * 后续所有请求携带 X-Tenant-Id header，后端把生效租户切到该租户，
 * 数据查询（Traces/Metrics/Logs）随之展示该租户视角。选「全局」退出扮演。
 */

import { useEffect, useState, useCallback } from 'react';
import { apiClient } from '@/api/client';
import type { Tenant } from '@/types/api';
import { useAuth } from '@/contexts/AuthContext';

export default function TenantSwitcher() {
  const { role, impersonating, impersonatedTenantID, impersonate } = useAuth();
  const [tenants, setTenants] = useState<Tenant[]>([]);
  const [loading, setLoading] = useState(false);

  const isAdmin = role === 'admin';

  useEffect(() => {
    if (!isAdmin) return;
    let cancelled = false;
    setLoading(true);
    apiClient.getTenants()
      .then((list) => { if (!cancelled) setTenants(list); })
      .catch(() => { /* 忽略：切换器静默降级，不打断页面 */ })
      .finally(() => { if (!cancelled) setLoading(false); });
    return () => { cancelled = true; };
  }, [isAdmin]);

  const handleChange = useCallback((e: React.ChangeEvent<HTMLSelectElement>) => {
    void impersonate(e.target.value); // '' = 全局视角
  }, [impersonate]);

  if (!isAdmin) return null;

  // admin 租户（account 0）就是全局视角，从下拉里去掉避免歧义
  const impersonatable = tenants.filter((t) => t.id !== 'admin');

  return (
    <div className="px-4 py-3 border-b border-gray-700/60">
      {impersonating && (
        <div className="mb-2 flex items-center gap-1.5 rounded-md bg-amber-500/15 border border-amber-500/30 px-2.5 py-1.5">
          <i className="fas fa-eye text-amber-300 text-xs" />
          <span className="text-[11px] text-amber-300 leading-tight">
            扮演中：{impersonatedTenantID}
          </span>
        </div>
      )}
      <label className="block text-[10px] font-semibold uppercase tracking-widest text-gray-500 mb-1.5">
        视角（View As）
      </label>
      <select
        className="w-full bg-gray-800 text-gray-200 text-sm rounded-md border border-gray-700 px-2 py-1.5 focus:outline-none focus:ring-1 focus:ring-primary-400"
        value={impersonatedTenantID}
        onChange={handleChange}
        disabled={loading}
      >
        <option value="">全局（admin）</option>
        {impersonatable.map((t) => (
          <option key={t.id} value={t.id}>{t.name}</option>
        ))}
      </select>
    </div>
  );
}
