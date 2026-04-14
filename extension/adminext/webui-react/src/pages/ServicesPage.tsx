/**
 * ServicesPage — 服务管理页面（左右两栏布局）
 *
 * 布局参考 InstancesPage 模式：
 *   - 顶部单行：标题 + App 下拉筛选 + 统计 + 刷新
 *   - 左侧：服务列表面板（搜索 + 实例状态筛选 + 可滚动列表）
 *   - 右侧：紧凑头部 + Tab 面板
 *     - Info：基本信息 / 描述 / 标签 / 生命周期
 *     - Instances：关联实例列表
 */

import { useState, useCallback, useEffect, useMemo } from 'react';
import { useNavigate } from 'react-router-dom';
import { apiClient } from '@/api/client';
import { useToast } from '@/contexts/ToastContext';
import { useConfirm } from '@/components/ConfirmDialog';
import SearchableSelect, { type SelectOption } from '@/components/SearchableSelect';
import ServiceInfoTab from '@/components/ServiceInfoTab';
import ServiceInstancesTab from '@/components/ServiceInstancesTab';
import ServiceConfigTab from '@/components/ServiceConfigTab';
import type { ServiceDetail, App, ApiError } from '@/types/api';

// ── 常量 & 类型 ──────────────────────────────────────

type DetailTab = 'info' | 'instances' | 'config';
type InstanceFilter = 'all' | 'has_instances' | 'no_instances';

const FILTER_ITEMS: { label: string; filter: InstanceFilter; icon: string; activeColor: string }[] = [
  { label: 'All',          filter: 'all',            icon: 'fas fa-layer-group', activeColor: 'text-blue-600 bg-blue-50 ring-blue-200' },
  { label: 'Has Inst.',    filter: 'has_instances',   icon: 'fas fa-check-circle', activeColor: 'text-green-600 bg-green-50 ring-green-200' },
  { label: 'No Inst.',     filter: 'no_instances',    icon: 'fas fa-ghost',       activeColor: 'text-orange-600 bg-orange-50 ring-orange-200' },
];

// ── 主组件 ──────────────────────────────────────────────

export default function ServicesPage() {
  const navigate = useNavigate();
  const { showToast } = useToast();
  const confirm = useConfirm();

  // ── 数据状态 ──────────────────────────────────────
  const [services, setServices] = useState<ServiceDetail[]>([]);
  const [loading, setLoading] = useState(false);
  const [search, setSearch] = useState('');
  const [instanceFilter, setInstanceFilter] = useState<InstanceFilter>('all');

  // App 下拉筛选
  const [apps, setApps] = useState<App[]>([]);
  const [selectedAppId, setSelectedAppId] = useState<string>('');
  const [appsLoading, setAppsLoading] = useState(false);

  // 选中服务 & Tab
  const [selectedService, setSelectedService] = useState<ServiceDetail | null>(null);
  const [activeTab, setActiveTab] = useState<DetailTab>('info');

  // ── 加载全局 Services ──────────────────────────────

  const loadServices = useCallback(async (appId?: string) => {
    setLoading(true);
    try {
      let data: ServiceDetail[];
      if (appId) {
        data = await apiClient.getAppServices(appId);
        // getAppServices 不返回 app_name，用 apps 补充
        const app = apps.find(a => a.id === appId);
        if (app) {
          data = data.map(s => ({ ...s, app_name: s.app_name || app.name }));
        }
      } else {
        data = await apiClient.getServices();
      }
      setServices(data);

      // 自动选中：保持已选或选第一个
      setSelectedService(prev => {
        if (prev) {
          const updated = data.find(s => s.app_id === prev.app_id && s.service_name === prev.service_name);
          if (updated) return updated;
        }
        return data.length > 0 ? data[0]! : null;
      });
    } catch (e) {
      showToast(`Failed to load services: ${(e as ApiError).message}`, 'error');
    } finally {
      setLoading(false);
    }
  }, [showToast, apps]);

  // ── 加载 Apps ──────────────────────────────────────

  const loadApps = useCallback(async () => {
    setAppsLoading(true);
    try {
      const appsList = await apiClient.getApps();
      setApps(appsList);
    } catch (e) {
      showToast(`Failed to load apps: ${(e as ApiError).message}`, 'error');
    } finally {
      setAppsLoading(false);
    }
  }, [showToast]);

  // 初始加载 Apps
  useEffect(() => {
    loadApps();
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  // Apps 加载完成后，自动选择第一个 App 并加载其 services
  useEffect(() => {
    if (apps.length > 0 && !selectedAppId) {
      const firstAppId = apps[0]!.id;
      setSelectedAppId(firstAppId);
      loadServices(firstAppId);
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [apps]);

  // ── App 切换 ──────────────────────────────────────

  const handleAppChange = useCallback((appId: string) => {
    setSelectedAppId(appId);
    loadServices(appId);
  }, [loadServices]);

  // ── SearchableSelect 选项 ──────────────────────────

  const appOptions: SelectOption[] = useMemo(() =>
    apps.map(app => ({
      value: app.id,
      label: app.name || app.id,
      description: `${app.service_count ?? 0} services`,
    })),
  [apps]);

  // ── 统计 & 过滤 ──────────────────────────────────────

  const stats = useMemo(() => {
    const result: Record<InstanceFilter, number> = { all: services.length, has_instances: 0, no_instances: 0 };
    for (const svc of services) {
      if (svc.instance_count > 0) result.has_instances++;
      else result.no_instances++;
    }
    return result;
  }, [services]);

  const filteredServices = useMemo(() => {
    let list = services;
    if (instanceFilter === 'has_instances') list = list.filter(s => s.instance_count > 0);
    else if (instanceFilter === 'no_instances') list = list.filter(s => s.instance_count === 0);

    const q = search.toLowerCase().trim();
    if (q) {
      list = list.filter(s =>
        s.service_name.toLowerCase().includes(q) ||
        (s.app_name || '').toLowerCase().includes(q) ||
        (s.description || '').toLowerCase().includes(q) ||
        (s.app_id || '').toLowerCase().includes(q),
      );
    }
    return list;
  }, [services, instanceFilter, search]);

  // ── 删除服务 ──────────────────────────────────────

  const handleDeleteService = useCallback(async (svc: ServiceDetail) => {
    if (svc.instance_count > 0) {
      showToast('Cannot delete service with active instances. Remove all instances first.', 'error');
      return;
    }

    const ok = await confirm({
      title: 'Delete Service',
      message: `Delete service "${svc.service_name}" from app "${svc.app_name || svc.app_id}"?\n\nThis only removes the service record. Configuration will be preserved.`,
      confirmText: 'Delete',
      variant: 'danger',
    });
    if (!ok) return;

    try {
      await apiClient.deleteService(svc.app_id, svc.service_name);
      showToast('Service deleted', 'success');
      // 刷新列表
      loadServices(selectedAppId);
    } catch (e) {
      showToast(`Failed to delete: ${(e as ApiError).message}`, 'error');
    }
  }, [confirm, showToast, loadServices, selectedAppId]);

  // ── Service 更新回调 ──────────────────────────────

  const handleServiceUpdated = useCallback((updated: ServiceDetail) => {
    setServices(prev => prev.map(s =>
      s.app_id === updated.app_id && s.service_name === updated.service_name
        ? { ...updated, app_name: s.app_name }
        : s
    ));
    setSelectedService(prev =>
      prev && prev.app_id === updated.app_id && prev.service_name === updated.service_name
        ? { ...updated, app_name: prev.app_name }
        : prev
    );
  }, []);

  // ── 空状态判断 ──────────────────────────────────────

  type EmptyReason = 'loading' | 'no_apps' | 'no_services' | 'search_empty' | 'has_data';

  const emptyReason: EmptyReason = useMemo(() => {
    if (loading || appsLoading) return 'loading';
    if (apps.length === 0) return 'no_apps';
    if (services.length === 0) return 'no_services';
    if (filteredServices.length === 0 && (search || instanceFilter !== 'all')) return 'search_empty';
    return 'has_data';
  }, [loading, appsLoading, apps.length, services.length, filteredServices.length, search, instanceFilter]);

  // ── 渲染 ──────────────────────────────────────────

  return (
    <div className="flex flex-col h-full">
      {/* ═══ 顶部栏：标题 + App 筛选 + 统计 + 刷新 ═══ */}
      <div className="flex-shrink-0 flex items-center gap-4 pb-2">
        <h2 className="text-base font-bold text-gray-800 whitespace-nowrap">Services</h2>

        <div className="w-px h-5 bg-gray-200 flex-shrink-0" />

        {/* App 筛选 */}
        <div className="flex items-center gap-2 flex-shrink-0">
          <SearchableSelect
            options={appOptions}
            value={selectedAppId}
            onChange={handleAppChange}
            placeholder="All Apps"
            searchKeys={['label', 'description']}
            loading={appsLoading}
            className="w-48"
          />
        </div>

        <div className="flex-1" />

        {/* 计数 */}
        <span className="text-[11px] text-gray-400 tabular-nums whitespace-nowrap">
          <span className="font-bold text-gray-600">{filteredServices.length}</span> services
        </span>

        {/* 刷新 */}
        <button
          onClick={() => {
            loadApps();
            loadServices(selectedAppId);
          }}
          className="w-8 h-8 flex items-center justify-center rounded-lg text-gray-400 hover:text-gray-600 hover:bg-gray-100 transition"
          title="Refresh"
        >
          <i className={`fas fa-sync-alt text-xs ${loading ? 'fa-spin' : ''}`} />
        </button>
      </div>

      {/* ═══ 主体：左右两栏 ═══ */}
      <div className="flex-1 flex gap-2.5 min-h-0">
        {/* ── 左侧：服务列表 ── */}
        <div className="w-64 flex-shrink-0 flex flex-col bg-white border border-gray-200/80 rounded-xl overflow-hidden">
          {/* 搜索框 */}
          <div className="flex-shrink-0 p-2.5 border-b border-gray-100">
            <div className="relative">
              <i className="fas fa-search absolute left-2.5 top-1/2 -translate-y-1/2 text-gray-300 text-[10px]" />
              <input
                type="text"
                value={search}
                onChange={e => setSearch(e.target.value)}
                placeholder="Search name, app, desc..."
                className="w-full pl-7 pr-3 py-1.5 border border-gray-200 rounded-lg focus:ring-2 focus:ring-blue-500/40 focus:border-blue-400 text-xs bg-gray-50/50 placeholder:text-gray-300 transition"
              />
            </div>
          </div>

          {/* 实例状态筛选 */}
          <div className={`flex-shrink-0 px-2.5 py-1.5 border-b border-gray-100 flex items-center gap-1 transition-opacity duration-300 ${
            services.length === 0 ? 'opacity-30 pointer-events-none' : 'opacity-100'
          }`}>
            {FILTER_ITEMS.map(item => {
              const isActive = instanceFilter === item.filter;
              const count = stats[item.filter];
              return (
                <button
                  key={item.filter}
                  onClick={() => setInstanceFilter(item.filter)}
                  className={`inline-flex items-center gap-1 px-1.5 py-0.5 rounded-md text-[10px] font-medium transition select-none whitespace-nowrap ${
                    isActive ? `${item.activeColor} ring-1` : 'text-gray-400 hover:text-gray-600 hover:bg-gray-50'
                  }`}
                  title={`${item.label}: ${count}`}
                >
                  <i className={`${item.icon} text-[8px]`} />
                  <span className="font-bold tabular-nums">{count}</span>
                </button>
              );
            })}
          </div>

          {/* 服务列表（可滚动） */}
          <div className="flex-1 overflow-y-auto">
            {emptyReason === 'loading' ? (
              <div className="p-2 space-y-1">
                {Array.from({ length: 6 }).map((_, i) => (
                  <div key={i} className="flex items-center gap-2 px-2.5 py-1.5 border-b border-gray-50" style={{ animationDelay: `${i * 80}ms` }}>
                    <div className="w-6 h-6 rounded-md skeleton-shimmer flex-shrink-0" />
                    <div className="flex-1 min-w-0 space-y-1.5">
                      <div className="h-3 skeleton-shimmer rounded w-3/4" />
                      <div className="h-2 skeleton-shimmer rounded w-1/2" />
                    </div>
                  </div>
                ))}
              </div>
            ) : emptyReason === 'no_apps' ? (
              <div className="py-12 px-6 text-center content-fade-in">
                <div className="w-14 h-14 rounded-2xl bg-blue-50 flex items-center justify-center mx-auto mb-3">
                  <i className="fas fa-cube text-blue-300 text-xl" />
                </div>
                <p className="text-xs font-semibold text-gray-500 mb-1">No Apps Registered</p>
                <p className="text-[10px] text-gray-400 leading-relaxed">Deploy your services first.<br/>Services will appear automatically.</p>
              </div>
            ) : emptyReason === 'no_services' ? (
              <div className="py-12 px-6 text-center content-fade-in">
                <div className="w-14 h-14 rounded-2xl bg-amber-50 flex items-center justify-center mx-auto mb-3">
                  <i className="fas fa-sitemap text-amber-300 text-xl" />
                </div>
                <p className="text-xs font-semibold text-gray-500 mb-1">No Services Found</p>
                <p className="text-[10px] text-gray-400 leading-relaxed">Services will appear here<br/>once instances start reporting.</p>
              </div>
            ) : emptyReason === 'search_empty' ? (
              <div className="py-12 px-6 text-center content-fade-in">
                <div className="w-14 h-14 rounded-2xl bg-purple-50 flex items-center justify-center mx-auto mb-3">
                  <i className="fas fa-search text-purple-300 text-xl" />
                </div>
                <p className="text-xs font-semibold text-gray-500 mb-1">No Matching Services</p>
                <p className="text-[10px] text-gray-400 leading-relaxed">Try adjusting your search or filter.</p>
                <div className="mt-2 flex items-center justify-center gap-2">
                  {search && (
                    <button
                      onClick={() => setSearch('')}
                      className="text-[10px] text-blue-500 hover:text-blue-600 font-medium transition"
                    >
                      <i className="fas fa-times mr-1 text-[8px]" />Clear Search
                    </button>
                  )}
                  {instanceFilter !== 'all' && (
                    <button
                      onClick={() => setInstanceFilter('all')}
                      className="text-[10px] text-blue-500 hover:text-blue-600 font-medium transition"
                    >
                      <i className="fas fa-filter mr-1 text-[8px]" />Reset Filter
                    </button>
                  )}
                </div>
              </div>
            ) : (
              <div className="content-fade-in">
                {filteredServices.map(svc => {
                  const isSelected = selectedService?.app_id === svc.app_id && selectedService?.service_name === svc.service_name;
                  const hasInstances = svc.instance_count > 0;
                  return (
                    <button
                      key={`${svc.app_id}:${svc.service_name}`}
                      onClick={() => { setSelectedService(svc); setActiveTab('info'); }}
                      className={`w-full text-left px-2.5 py-1.5 transition-all border-b border-gray-50 ${
                        isSelected
                          ? 'bg-blue-50/80 border-l-[3px] border-l-blue-500'
                          : 'hover:bg-gray-50/80 border-l-[3px] border-l-transparent'
                      }`}
                    >
                      <div className="flex items-center gap-2">
                        {/* 图标 */}
                        <div className={`w-6 h-6 rounded-md flex items-center justify-center text-[9px] flex-shrink-0 ${
                          hasInstances ? 'bg-green-50 text-green-500' : 'bg-gray-50 text-gray-300'
                        }`}>
                          <i className="fas fa-sitemap" />
                        </div>
                        {/* 信息 */}
                        <div className="overflow-hidden flex-1 min-w-0">
                          <div className="flex items-center gap-1.5">
                            <span className={`text-[11px] font-semibold truncate ${isSelected ? 'text-blue-700' : 'text-gray-700'}`}>
                              {svc.service_name === '_unknown_' ? 'Unknown Service' : svc.service_name}
                            </span>
                          </div>
                          <div className="flex items-center gap-1 mt-0.5">
                            <span className="text-[9px] text-gray-400 truncate">{svc.app_name || svc.app_id}</span>
                            <span className="text-[8px] text-gray-200">·</span>
                            <span className={`text-[9px] ${hasInstances ? 'text-green-500' : 'text-gray-300'}`}>
                              {svc.online_count}/{svc.instance_count} inst
                            </span>
                          </div>
                        </div>
                      </div>
                    </button>
                  );
                })}
              </div>
            )}
          </div>
        </div>

        {/* ── 右侧：详情面板 ── */}
        <div className="flex-1 flex flex-col bg-white border border-gray-200/80 rounded-xl overflow-hidden min-w-0">
          {selectedService ? (
            <>
              {/* 紧凑头部 */}
              <div className="flex-shrink-0 border-b border-gray-100">
                {/* 第一行：服务摘要 + 操作按钮 */}
                <div className="px-4 pt-3 pb-0 flex items-center justify-between">
                  <div className="flex items-center gap-2.5 overflow-hidden min-w-0">
                    {/* 状态点 */}
                    <div className="relative flex-shrink-0">
                      <div className={`w-2 h-2 rounded-full ${
                        selectedService.instance_count > 0 ? 'bg-green-500' : 'bg-gray-300'
                      }`} />
                    </div>
                    {/* 服务名 */}
                    <h3 className="text-sm font-bold text-gray-800 truncate">
                      {selectedService.service_name === '_unknown_' ? 'Unknown Service' : selectedService.service_name}
                    </h3>
                    {/* 实例数标签 */}
                    <span className={`px-1.5 py-0.5 rounded text-[9px] font-bold flex-shrink-0 ${
                      selectedService.instance_count > 0
                        ? 'bg-green-50 text-green-600'
                        : 'bg-gray-100 text-gray-400'
                    }`}>
                      {selectedService.online_count}/{selectedService.instance_count} instances
                    </span>
                    {/* App name */}
                    <span className="text-[10px] text-gray-400 flex-shrink-0 truncate">
                      {selectedService.app_name || selectedService.app_id}
                    </span>
                  </div>

                  {/* 操作按钮 */}
                  <div className="flex items-center gap-1.5 flex-shrink-0 ml-3">
                    <button
                      onClick={() => navigate(`/instrumentation?app_id=${encodeURIComponent(selectedService.app_id)}&service_name=${encodeURIComponent(selectedService.service_name)}`)}
                      className="px-2.5 h-7 inline-flex items-center justify-center rounded-lg text-[10px] font-semibold text-primary-700 bg-primary-50 hover:bg-primary-100 transition"
                      title="Open instrumentation workbench"
                    >
                      <i className="fas fa-wave-square mr-1 text-[9px]" />
                      Instrumentation
                    </button>
                    <button
                      onClick={() => handleDeleteService(selectedService)}
                      disabled={selectedService.instance_count > 0}
                      className="w-7 h-7 flex items-center justify-center rounded-lg text-gray-300 hover:text-red-500 hover:bg-red-50 transition disabled:opacity-20 disabled:cursor-not-allowed"
                      title={selectedService.instance_count > 0
                        ? 'Cannot delete: has active instances'
                        : 'Delete service record'
                      }
                    >
                      <i className="fas fa-trash-alt text-[10px]" />
                    </button>
                  </div>
                </div>

                {/* 第二行：Tab 切换 */}
                <div className="px-4 flex items-center gap-0 mt-1">
                  {([
                    { id: 'info' as DetailTab, label: 'Info', icon: 'fas fa-info-circle' },
                    { id: 'instances' as DetailTab, label: 'Instances', icon: 'fas fa-server' },
                    { id: 'config' as DetailTab, label: 'Config', icon: 'fas fa-cog' },
                  ]).map(tab => (
                    <button
                      key={tab.id}
                      onClick={() => setActiveTab(tab.id)}
                      className={`px-3 py-2 text-[11px] font-semibold transition-all border-b-2 ${
                        activeTab === tab.id
                          ? 'border-blue-500 text-blue-600'
                          : 'border-transparent text-gray-400 hover:text-gray-600'
                      }`}
                    >
                      <i className={`${tab.icon} mr-1 text-[9px]`} />
                      {tab.label}
                      {tab.id === 'instances' && (
                        <span className="ml-1 text-[9px] tabular-nums opacity-60">{selectedService.instance_count}</span>
                      )}
                      {tab.id === 'config' && selectedService.has_config && (
                        <span className="ml-1 w-1.5 h-1.5 rounded-full bg-green-400 inline-block" title="Has configuration" />
                      )}
                    </button>
                  ))}
                </div>
              </div>

              {/* Tab 内容 */}
              {activeTab === 'config' ? (
                /* Config Tab 需要全高 flex 布局（内含 textarea 撑满） */
                <div className="flex-1 flex flex-col overflow-hidden relative">
                  <ServiceConfigTab
                    appId={selectedService.app_id}
                    serviceName={selectedService.service_name}
                  />
                </div>
              ) : (
                <div className="flex-1 overflow-y-auto p-4">
                  {activeTab === 'info' ? (
                    <ServiceInfoTab
                      service={selectedService}
                      onServiceUpdated={handleServiceUpdated}
                    />
                  ) : (
                    <ServiceInstancesTab
                      appId={selectedService.app_id}
                      serviceName={selectedService.service_name}
                    />
                  )}
                </div>
              )}
            </>
          ) : (
            /* 未选中服务的占位 */
            <div className="flex-1 flex items-center justify-center content-fade-in">
              {emptyReason === 'no_apps' ? (
                <div className="text-center max-w-xs">
                  <div className="w-16 h-16 rounded-2xl bg-gradient-to-br from-blue-50 to-indigo-50 flex items-center justify-center mx-auto mb-4 shadow-sm">
                    <i className="fas fa-rocket text-blue-400 text-2xl" />
                  </div>
                  <p className="text-sm font-semibold text-gray-600 mb-1">Welcome to Services</p>
                  <p className="text-[11px] text-gray-400 leading-relaxed">No applications registered yet. Deploy your services to get started.</p>
                  <div className="mt-4 flex items-center justify-center gap-4 text-[10px] text-gray-300">
                    <div className="flex items-center gap-1.5">
                      <span className="w-5 h-5 rounded-md bg-gray-50 flex items-center justify-center"><i className="fas fa-cube text-[8px]" /></span>
                      <span>Deploy</span>
                    </div>
                    <i className="fas fa-chevron-right text-[6px]" />
                    <div className="flex items-center gap-1.5">
                      <span className="w-5 h-5 rounded-md bg-gray-50 flex items-center justify-center"><i className="fas fa-heartbeat text-[8px]" /></span>
                      <span>Register</span>
                    </div>
                    <i className="fas fa-chevron-right text-[6px]" />
                    <div className="flex items-center gap-1.5">
                      <span className="w-5 h-5 rounded-md bg-gray-50 flex items-center justify-center"><i className="fas fa-cog text-[8px]" /></span>
                      <span>Manage</span>
                    </div>
                  </div>
                </div>
              ) : emptyReason === 'no_services' ? (
                <div className="text-center max-w-xs">
                  <div className="w-16 h-16 rounded-2xl bg-gradient-to-br from-amber-50 to-orange-50 flex items-center justify-center mx-auto mb-4 shadow-sm">
                    <i className="fas fa-sitemap text-amber-400 text-2xl" />
                  </div>
                  <p className="text-sm font-semibold text-gray-600 mb-1">No Services Found</p>
                  <p className="text-[11px] text-gray-400 leading-relaxed">Services will appear automatically once instances start reporting.</p>
                </div>
              ) : emptyReason === 'search_empty' ? (
                <div className="text-center max-w-xs">
                  <div className="w-16 h-16 rounded-2xl bg-gradient-to-br from-purple-50 to-fuchsia-50 flex items-center justify-center mx-auto mb-4 shadow-sm">
                    <i className="fas fa-search text-purple-400 text-2xl" />
                  </div>
                  <p className="text-sm font-semibold text-gray-600 mb-1">No Matching Services</p>
                  <p className="text-[11px] text-gray-400 leading-relaxed">Try adjusting your search or filter criteria.</p>
                </div>
              ) : (
                <div className="text-center">
                  <div className="w-12 h-12 rounded-2xl bg-gray-50 flex items-center justify-center mx-auto mb-3">
                    <i className="fas fa-mouse-pointer text-gray-300 text-lg" />
                  </div>
                  <p className="text-sm text-gray-400 font-medium">Select a Service</p>
                  <p className="text-[10px] text-gray-300 mt-1">View details, manage metadata, check instances</p>
                </div>
              )}
            </div>
          )}
        </div>
      </div>
    </div>
  );
}
