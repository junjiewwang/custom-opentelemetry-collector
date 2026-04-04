/**
 * InstancesPage - 实例管理页面（左右两栏布局）
 *
 * 布局优化：
 *   - 顶部单行：标题 + App→Service 级联下拉 + 统计 + 刷新
 *   - 左侧：实例列表面板（搜索 + 紧凑状态筛选 + 可滚动列表）
 *   - 右侧：紧凑头部 + Tab 面板
 *     - Overview：基本信息 / Arthas 状态 / 元数据 / 生命周期
 *     - Tasks：关联任务列表（创建/取消/详情）
 *   - Arthas 终端面板
 */

import { useState, useCallback, useEffect, useMemo, lazy, Suspense } from 'react';
import { apiClient } from '@/api/client';
import { useToast } from '@/contexts/ToastContext';
import { useConfirm } from '@/components/ConfirmDialog';
import SearchableSelect, { type SelectOption } from '@/components/SearchableSelect';
import InstanceTasksTab from '@/components/InstanceTasksTab';
import type { Instance, EnrichedInstance, ArthasAgent, App, AppService, ApiError } from '@/types/api';

// 懒加载终端面板（包含 xterm.js，约 200KB）
const TerminalPanel = lazy(() => import('@/components/Terminal/TerminalPanel'));

// ── 常量 ──────────────────────────────────────────────

type StatusFilter = 'all' | 'online' | 'offline' | 'arthas_ready' | 'arthas_not_ready';
type DetailTab = 'overview' | 'tasks';

const STAT_ITEMS: { label: string; filter: StatusFilter; icon: string; activeColor: string; dotColor: string }[] = [
  { label: 'All',     filter: 'all',              icon: 'fas fa-layer-group', activeColor: 'text-blue-600 bg-blue-50 ring-blue-200',     dotColor: 'bg-gray-400' },
  { label: 'Online',  filter: 'online',           icon: 'fas fa-circle',      activeColor: 'text-green-600 bg-green-50 ring-green-200',  dotColor: 'bg-green-500' },
  { label: 'Offline', filter: 'offline',          icon: 'fas fa-circle',      activeColor: 'text-gray-600 bg-gray-100 ring-gray-300',    dotColor: 'bg-gray-300' },
  { label: 'Arthas',  filter: 'arthas_ready',     icon: 'fas fa-bug',         activeColor: 'text-purple-600 bg-purple-50 ring-purple-200', dotColor: 'bg-purple-500' },
  { label: 'N/A',     filter: 'arthas_not_ready', icon: 'fas fa-unlink',      activeColor: 'text-orange-600 bg-orange-50 ring-orange-200', dotColor: 'bg-orange-400' },
];

// ── 工具函数 ──────────────────────────────────────────

function formatTimestamp(ts: number): string {
  if (!ts) return '-';
  const ms = ts < 10000000000 ? ts * 1000 : ts;
  return new Date(ms).toLocaleString('zh-CN');
}

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
  return formatTimestamp(ts);
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
  if (mins > 0) return `${mins}m ${secs % 60}s`;
  return `${secs}s`;
}

// ── 合并 Arthas 状态 ──────────────────────────────────

function enrichInstances(instances: Instance[], arthasAgents: ArthasAgent[]): EnrichedInstance[] {
  const tunnelMap = new Map<string, ArthasAgent>();
  for (const a of arthasAgents) {
    if (a.agent_id) tunnelMap.set(a.agent_id, a);
  }
  return instances.map(inst => ({
    ...inst,
    arthasStatus: {
      state: tunnelMap.has(inst.agent_id) ? 'running' as const : 'stopped' as const,
      arthasVersion: '',
      tunnelReady: tunnelMap.has(inst.agent_id),
      tunnelAgentId: tunnelMap.get(inst.agent_id)?.agent_id || '',
    },
  }));
}

// ── 组件 ──────────────────────────────────────────────

export default function InstancesPage() {
  const { showToast } = useToast();
  const confirm = useConfirm();

  // 数据
  const [instances, setInstances] = useState<EnrichedInstance[]>([]);
  const [loading, setLoading] = useState(false);
  const [search, setSearch] = useState('');
  const [statusFilter, setStatusFilter] = useState<StatusFilter>('all');

  // 级联下拉选择器
  const [apps, setApps] = useState<App[]>([]);
  const [services, setServices] = useState<AppService[]>([]);
  const [selectedAppId, setSelectedAppId] = useState<string>('');
  const [selectedServiceName, setSelectedServiceName] = useState<string>('');
  const [appsLoading, setAppsLoading] = useState(false);
  const [servicesLoading, setServicesLoading] = useState(false);

  // 选中实例 & Tab
  const [selectedInstance, setSelectedInstance] = useState<EnrichedInstance | null>(null);
  const [activeTab, setActiveTab] = useState<DetailTab>('overview');

  // 终端面板
  const [terminalInstance, setTerminalInstance] = useState<EnrichedInstance | null>(null);

  // ── 加载实例（带过滤参数） ──────────────────────

  const loadInstances = useCallback(async (appId?: string, serviceName?: string) => {
    setLoading(true);
    try {
      const [instancesRes, arthasRes] = await Promise.allSettled([
        apiClient.getInstances('', {
          status: 'all',
          app_id: appId || undefined,
          service_name: serviceName || undefined,
        }),
        apiClient.getArthasAgents(),
      ]);

      const instancesList = instancesRes.status === 'fulfilled' ? instancesRes.value : [];
      const tunnelAgents = arthasRes.status === 'fulfilled' ? (arthasRes.value || []) : [];
      const enriched = enrichInstances(instancesList, tunnelAgents);
      setInstances(enriched);

      // 自动选中第一个实例
      setSelectedInstance(prev => {
        if (prev && enriched.some(i => i.agent_id === prev.agent_id)) {
          return enriched.find(i => i.agent_id === prev.agent_id) || prev;
        }
        return enriched.length > 0 ? enriched[0]! : null;
      });
    } catch (e) {
      showToast(`Failed to load instances: ${(e as ApiError).message}`, 'error');
    } finally {
      setLoading(false);
    }
  }, [showToast]);

  // ── 加载 App 下的 Services ──────────────────────

  const loadServices = useCallback(async (appId: string, autoSelectFirst = true) => {
    if (!appId) {
      setServices([]);
      setSelectedServiceName('');
      return;
    }
    setServicesLoading(true);
    try {
      const svcList = await apiClient.getAppServices(appId);
      setServices(svcList);
      if (autoSelectFirst && svcList.length > 0) {
        const firstSvc = svcList[0]!.service_name;
        setSelectedServiceName(firstSvc);
        loadInstances(appId, firstSvc);
      } else {
        setSelectedServiceName('');
        loadInstances(appId);
      }
    } catch (e) {
      showToast(`Failed to load services: ${(e as ApiError).message}`, 'error');
      setServices([]);
    } finally {
      setServicesLoading(false);
    }
  }, [showToast, loadInstances]);

  // ── 加载 Apps 列表 ──────────────────────────────

  const loadApps = useCallback(async (autoSelectFirst = true) => {
    setAppsLoading(true);
    try {
      const appsList = await apiClient.getApps();
      setApps(appsList);
      if (autoSelectFirst && appsList.length > 0) {
        const firstApp = appsList[0]!.id;
        setSelectedAppId(firstApp);
        loadServices(firstApp, true);
      } else if (appsList.length === 0) {
        setSelectedAppId('');
        setServices([]);
        setSelectedServiceName('');
        setInstances([]);
        setSelectedInstance(null);
      }
    } catch (e) {
      showToast(`Failed to load apps: ${(e as ApiError).message}`, 'error');
    } finally {
      setAppsLoading(false);
    }
  }, [showToast, loadServices]);

  // 初始加载
  useEffect(() => {
    loadApps(true);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  // ── App 切换 ──────────────────────────────────────

  const handleAppChange = useCallback((appId: string) => {
    if (!appId) return;
    setSelectedAppId(appId);
    loadServices(appId, true);
  }, [loadServices]);

  // ── Service 切换 ──────────────────────────────────

  const handleServiceChange = useCallback((serviceName: string) => {
    if (!serviceName) return;
    setSelectedServiceName(serviceName);
    loadInstances(selectedAppId || undefined, serviceName);
  }, [selectedAppId, loadInstances]);

  // ── SearchableSelect 选项 ──────────────────────────

  const appOptions: SelectOption[] = useMemo(() =>
    apps.map(app => ({
      value: app.id,
      label: `${app.name || app.id}`,
      description: `${app.service_count ?? 0} services · ${app.agent_count ?? 0} instances`,
    })),
  [apps]);

  const serviceOptions: SelectOption[] = useMemo(() =>
    services.map(svc => ({
      value: svc.service_name,
      label: svc.service_name === '_unknown_' ? 'Unknown Service' : svc.service_name,
      description: `${svc.instance_count} instances`,
    })),
  [services]);

  // ── 统计数据 ──────────────────────────────────────

  const stats = useMemo(() => {
    const result: Record<StatusFilter, number> = {
      all: instances.length,
      online: 0, offline: 0,
      arthas_ready: 0, arthas_not_ready: 0,
    };
    for (const inst of instances) {
      if (inst.status?.state === 'online') result.online++;
      else result.offline++;
      if (inst.arthasStatus.tunnelReady) result.arthas_ready++;
      else result.arthas_not_ready++;
    }
    return result;
  }, [instances]);

  // ── 过滤逻辑 ──────────────────────────────────────

  const filteredInstances = useMemo(() => {
    let list = instances;
    if (statusFilter === 'online') list = list.filter(i => i.status?.state === 'online');
    else if (statusFilter === 'offline') list = list.filter(i => i.status?.state !== 'online');
    else if (statusFilter === 'arthas_ready') list = list.filter(i => i.arthasStatus.tunnelReady);
    else if (statusFilter === 'arthas_not_ready') list = list.filter(i => !i.arthasStatus.tunnelReady);

    const q = search.toLowerCase().trim();
    if (q) {
      list = list.filter(i =>
        i.agent_id.toLowerCase().includes(q) ||
        (i.hostname || '').toLowerCase().includes(q) ||
        (i.service_name || '').toLowerCase().includes(q) ||
        (i.ip || '').includes(q),
      );
    }
    return list;
  }, [instances, statusFilter, search]);

  // ── 下线实例 ──────────────────────────────────────

  const handleUnregister = useCallback(async (inst: EnrichedInstance) => {
    const ok = await confirm({
      title: 'Remove Instance',
      message: `Remove instance ${inst.agent_id} from the registry?\n\nIf the instance is still running, it may re-register on its next heartbeat.`,
      confirmText: 'Remove',
      variant: 'danger',
    });
    if (!ok) return;
    try {
      await apiClient.unregisterAgent(inst.agent_id);
      showToast('Instance removed successfully', 'success');
      loadInstances(selectedAppId || undefined, selectedServiceName || undefined);
    } catch (e) {
      showToast(`Failed to remove instance: ${(e as ApiError).message}`, 'error');
    }
  }, [confirm, showToast, loadInstances, selectedAppId, selectedServiceName]);

  // ── 复制到剪贴板 ──────────────────────────────────

  const copyToClipboard = useCallback(async (text: string) => {
    try { await navigator.clipboard.writeText(text); showToast('Copied', 'success'); }
    catch { showToast('Failed to copy', 'error'); }
  }, [showToast]);

  // ── 空状态类型判断 ──────────────────────────────────

  type EmptyReason = 'loading' | 'no_apps' | 'no_services' | 'no_instances' | 'search_empty' | 'has_data';

  const emptyReason: EmptyReason = useMemo(() => {
    if (loading || appsLoading || servicesLoading) return 'loading';
    if (apps.length === 0) return 'no_apps';
    if (selectedAppId && services.length === 0) return 'no_services';
    if (instances.length === 0) return 'no_instances';
    if (filteredInstances.length === 0 && (search || statusFilter !== 'all')) return 'search_empty';
    return 'has_data';
  }, [loading, appsLoading, servicesLoading, apps.length, selectedAppId, services.length, instances.length, filteredInstances.length, search, statusFilter]);

  // ── 渲染 ──────────────────────────────────────────

  return (
    <div className="flex flex-col h-full">
      {/* ═══ 顶部栏：标题 + 级联筛选 + 统计 + 刷新（单行） ═══ */}
      <div className="flex-shrink-0 flex items-center gap-4 pb-3">
        {/* 标题 */}
        <h2 className="text-base font-bold text-gray-800 whitespace-nowrap">Instances</h2>

        {/* 分隔线 */}
        <div className="w-px h-5 bg-gray-200 flex-shrink-0" />

        {/* 级联筛选 */}
        <div className="flex items-center gap-2 flex-shrink-0">
          <SearchableSelect
            options={appOptions}
            value={selectedAppId}
            onChange={handleAppChange}
            placeholder="App..."
            searchKeys={['label', 'description']}
            loading={appsLoading}
            className="w-44"
          />
          <i className="fas fa-chevron-right text-gray-300 text-[8px] flex-shrink-0" />
          <SearchableSelect
            options={serviceOptions}
            value={selectedServiceName}
            onChange={handleServiceChange}
            placeholder="Service..."
            searchKeys={['label', 'description']}
            loading={servicesLoading}
            disabled={!selectedAppId}
            className="w-56"
          />
        </div>

        {/* 弹性空间 */}
        <div className="flex-1" />

        {/* 实例计数 */}
        <span className="text-[11px] text-gray-400 tabular-nums whitespace-nowrap">
          <span className="font-bold text-gray-600">{filteredInstances.length}</span> instances
        </span>

        {/* 刷新按钮 */}
        <button
          onClick={() => {
            loadApps(false);
            loadInstances(selectedAppId || undefined, selectedServiceName || undefined);
          }}
          className="w-8 h-8 flex items-center justify-center rounded-lg text-gray-400 hover:text-gray-600 hover:bg-gray-100 transition"
          title="Refresh"
        >
          <i className={`fas fa-sync-alt text-xs ${loading ? 'fa-spin' : ''}`} />
        </button>
      </div>

      {/* ═══ 主体：左右两栏 ═══ */}
      <div className="flex-1 flex gap-3 min-h-0">
        {/* ── 左侧：实例列表面板 ── */}
        <div className="w-72 flex-shrink-0 flex flex-col bg-white border border-gray-200/80 rounded-xl overflow-hidden">
          {/* 搜索框 */}
          <div className="flex-shrink-0 p-2.5 border-b border-gray-100">
            <div className="relative">
              <i className="fas fa-search absolute left-2.5 top-1/2 -translate-y-1/2 text-gray-300 text-[10px]" />
              <input
                type="text"
                value={search}
                onChange={e => setSearch(e.target.value)}
                placeholder="Search ID, Host, IP..."
                className="w-full pl-7 pr-3 py-1.5 border border-gray-200 rounded-lg focus:ring-2 focus:ring-blue-500/40 focus:border-blue-400 text-xs bg-gray-50/50 placeholder:text-gray-300 transition"
              />
            </div>
          </div>

          {/* 状态筛选标签（紧凑单行）- 无数据时淡化 */}
          <div className={`flex-shrink-0 px-2.5 py-1.5 border-b border-gray-100 flex items-center gap-1 transition-opacity duration-300 ${
            instances.length === 0 ? 'opacity-30 pointer-events-none' : 'opacity-100'
          }`}>
            {STAT_ITEMS.map(stat => {
              const isActive = statusFilter === stat.filter;
              const count = stats[stat.filter];
              return (
                <button
                  key={stat.filter}
                  onClick={() => setStatusFilter(stat.filter)}
                  className={`inline-flex items-center gap-1 px-1.5 py-0.5 rounded-md text-[10px] font-medium transition select-none whitespace-nowrap ${
                    isActive
                      ? `${stat.activeColor} ring-1`
                      : 'text-gray-400 hover:text-gray-600 hover:bg-gray-50'
                  }`}
                  title={`${stat.label}: ${count}`}
                >
                  <span className={`w-1.5 h-1.5 rounded-full flex-shrink-0 ${isActive ? stat.dotColor : 'bg-gray-300'}`} />
                  <span className="font-bold tabular-nums">{count}</span>
                </button>
              );
            })}
          </div>

          {/* 实例列表（可滚动） */}
          <div className="flex-1 overflow-y-auto">
            {emptyReason === 'loading' ? (
              <div className="p-2 space-y-1">
                {Array.from({ length: 6 }).map((_, i) => (
                  <div key={i} className="flex items-center gap-2 px-3 py-2 border-b border-gray-50" style={{ animationDelay: `${i * 80}ms` }}>
                    <div className="w-7 h-7 rounded-lg skeleton-shimmer flex-shrink-0" />
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
                <p className="text-xs font-semibold text-gray-500 mb-1">暂无应用</p>
                <p className="text-[10px] text-gray-400 leading-relaxed">还没有注册任何应用<br/>实例上线后将自动注册</p>
              </div>
            ) : emptyReason === 'no_services' ? (
              <div className="py-12 px-6 text-center content-fade-in">
                <div className="w-14 h-14 rounded-2xl bg-amber-50 flex items-center justify-center mx-auto mb-3">
                  <i className="fas fa-sitemap text-amber-300 text-xl" />
                </div>
                <p className="text-xs font-semibold text-gray-500 mb-1">该应用下暂无服务</p>
                <p className="text-[10px] text-gray-400 leading-relaxed">请选择其他应用<br/>或等待服务注册</p>
              </div>
            ) : emptyReason === 'no_instances' ? (
              <div className="py-12 px-6 text-center content-fade-in">
                <div className="w-14 h-14 rounded-2xl bg-green-50 flex items-center justify-center mx-auto mb-3">
                  <i className="fas fa-server text-green-300 text-xl" />
                </div>
                <p className="text-xs font-semibold text-gray-500 mb-1">该服务下暂无实例</p>
                <p className="text-[10px] text-gray-400 leading-relaxed">实例上线后将自动出现</p>
              </div>
            ) : emptyReason === 'search_empty' ? (
              <div className="py-12 px-6 text-center content-fade-in">
                <div className="w-14 h-14 rounded-2xl bg-purple-50 flex items-center justify-center mx-auto mb-3">
                  <i className="fas fa-search text-purple-300 text-xl" />
                </div>
                <p className="text-xs font-semibold text-gray-500 mb-1">没有匹配的实例</p>
                <p className="text-[10px] text-gray-400 leading-relaxed">尝试调整搜索条件或筛选状态</p>
                {search && (
                  <button
                    onClick={() => setSearch('')}
                    className="mt-2 text-[10px] text-blue-500 hover:text-blue-600 font-medium transition"
                  >
                    <i className="fas fa-times mr-1 text-[8px]" />清除搜索
                  </button>
                )}
                {statusFilter !== 'all' && (
                  <button
                    onClick={() => setStatusFilter('all')}
                    className="mt-2 ml-2 text-[10px] text-blue-500 hover:text-blue-600 font-medium transition"
                  >
                    <i className="fas fa-filter mr-1 text-[8px]" />重置筛选
                  </button>
                )}
              </div>
            ) : (
              <div className="content-fade-in">
                {filteredInstances.map(inst => {
                  const isSelected = selectedInstance?.agent_id === inst.agent_id;
                  const isOnline = inst.status?.state === 'online';
                  return (
                    <button
                      key={inst.agent_id}
                      onClick={() => { setSelectedInstance(inst); setActiveTab('overview'); }}
                      className={`w-full text-left px-3 py-2 transition-all border-b border-gray-50 ${
                        isSelected
                          ? 'bg-blue-50/80 border-l-[3px] border-l-blue-500'
                          : 'hover:bg-gray-50/80 border-l-[3px] border-l-transparent'
                      }`}
                    >
                      <div className="flex items-center gap-2">
                        {/* 状态指示器 */}
                        <div className="relative flex-shrink-0">
                          <div className={`w-7 h-7 rounded-lg flex items-center justify-center text-[10px] ${
                            isOnline ? 'bg-green-50 text-green-500' : 'bg-gray-50 text-gray-300'
                          }`}>
                            <i className="fas fa-server" />
                          </div>
                          {inst.arthasStatus.tunnelReady && (
                            <span className="absolute -top-0.5 -right-0.5 w-2 h-2 bg-purple-500 rounded-full border border-white" />
                          )}
                        </div>
                        {/* 信息 */}
                        <div className="overflow-hidden flex-1 min-w-0">
                          <div className="flex items-center gap-1.5">
                            <span className={`text-[11px] font-semibold truncate ${isSelected ? 'text-blue-700' : 'text-gray-700'}`}>
                              {inst.hostname || inst.agent_id.substring(0, 12)}
                            </span>
                            <span className={`w-1.5 h-1.5 rounded-full flex-shrink-0 ${
                              isOnline ? 'bg-green-500' : 'bg-gray-300'
                            }`} />
                          </div>
                          <div className="flex items-center gap-1 mt-0.5">
                            <span className="text-[9px] text-gray-400 font-mono truncate">
                              {inst.ip || '0.0.0.0'}
                            </span>
                            <span className="text-[8px] text-gray-200">·</span>
                            <span className="text-[9px] text-gray-400 truncate">
                              {formatRelativeTime(inst.last_heartbeat)}
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
          {selectedInstance ? (
            <>
              {/* 紧凑头部：实例摘要 + Tab + 操作按钮 */}
              <div className="flex-shrink-0 border-b border-gray-100">
                {/* 第一行：实例摘要信息 + 操作按钮 */}
                <div className="px-4 pt-3 pb-0 flex items-center justify-between">
                  <div className="flex items-center gap-2.5 overflow-hidden min-w-0">
                    {/* 状态指示点 */}
                    <div className="relative flex-shrink-0">
                      <div className={`w-2 h-2 rounded-full ${
                        selectedInstance.status?.state === 'online' ? 'bg-green-500' : 'bg-gray-300'
                      }`} />
                      {selectedInstance.status?.state === 'online' && (
                        <div className="absolute inset-0 rounded-full bg-green-400 animate-ping opacity-40" />
                      )}
                    </div>
                    {/* 名称 + 标签 */}
                    <h3 className="text-sm font-bold text-gray-800 truncate">
                      {selectedInstance.hostname || selectedInstance.agent_id.substring(0, 16)}
                    </h3>
                    <span className={`px-1.5 py-0.5 rounded text-[9px] font-bold uppercase flex-shrink-0 ${
                      selectedInstance.status?.state === 'online'
                        ? 'bg-green-50 text-green-600'
                        : 'bg-gray-100 text-gray-400'
                    }`}>
                      {selectedInstance.status?.state === 'online' ? 'online' : 'offline'}
                    </span>
                    {selectedInstance.arthasStatus.tunnelReady && (
                      <span className="px-1.5 py-0.5 rounded text-[9px] font-bold bg-purple-50 text-purple-600 flex-shrink-0">arthas</span>
                    )}
                    <span className="text-[10px] text-gray-400 flex-shrink-0">v{selectedInstance.version || '?'}</span>
                    {/* Agent ID（可复制） */}
                    <div className="flex items-center gap-1 overflow-hidden ml-1">
                      <code className="text-[9px] font-mono text-gray-300 truncate">{selectedInstance.agent_id}</code>
                      <button onClick={() => copyToClipboard(selectedInstance.agent_id)} className="text-gray-300 hover:text-blue-500 transition flex-shrink-0">
                        <i className="fas fa-copy text-[8px]" />
                      </button>
                    </div>
                  </div>
                  {/* 操作按钮 */}
                  <div className="flex items-center gap-1.5 flex-shrink-0 ml-3">
                    {selectedInstance.status?.state === 'online' && (
                      <button
                        onClick={() => setTerminalInstance(selectedInstance)}
                        className={`px-2.5 py-1 rounded-lg text-[11px] font-semibold transition flex items-center gap-1.5 ${
                          selectedInstance.arthasStatus.tunnelReady
                            ? 'bg-purple-600 text-white hover:bg-purple-700 shadow-sm shadow-purple-200'
                            : 'bg-gray-800 text-white hover:bg-gray-900 shadow-sm'
                        }`}
                        title={selectedInstance.arthasStatus.tunnelReady ? 'Connect to Arthas diagnostic console' : 'Arthas tunnel not connected'}
                      >
                        <i className="fas fa-bug text-[9px]" />
                        Arthas
                      </button>
                    )}
                    <button
                      onClick={() => handleUnregister(selectedInstance)}
                      disabled={selectedInstance.status?.state === 'online'}
                      className="w-7 h-7 flex items-center justify-center rounded-lg text-gray-300 hover:text-red-500 hover:bg-red-50 transition disabled:opacity-20 disabled:cursor-not-allowed"
                      title="Remove instance"
                    >
                      <i className="fas fa-trash-alt text-[10px]" />
                    </button>
                  </div>
                </div>

                {/* 第二行：Tab 切换 */}
                <div className="px-4 flex items-center gap-0 mt-1">
                  {([
                    { id: 'overview' as DetailTab, label: 'Overview', icon: 'fas fa-info-circle' },
                    { id: 'tasks' as DetailTab, label: 'Tasks', icon: 'fas fa-tasks' },
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
                    </button>
                  ))}
                </div>
              </div>

              {/* Tab 内容 */}
              <div className="flex-1 overflow-y-auto p-4">
                {activeTab === 'overview' ? (
                  <OverviewTab instance={selectedInstance} copyToClipboard={copyToClipboard} />
                ) : (
                  <InstanceTasksTab
                    agentId={selectedInstance.agent_id}
                    appId={selectedInstance.app_id}
                    serviceName={selectedInstance.service_name}
                    isOnline={selectedInstance.status?.state === 'online'}
                  />
                )}
              </div>
            </>
          ) : (
            /* 未选中实例的占位 - 根据级联状态显示不同引导 */
            <div className="flex-1 flex items-center justify-center content-fade-in">
              {emptyReason === 'no_apps' ? (
                <div className="text-center max-w-xs">
                  <div className="w-16 h-16 rounded-2xl bg-gradient-to-br from-blue-50 to-indigo-50 flex items-center justify-center mx-auto mb-4 shadow-sm">
                    <i className="fas fa-rocket text-blue-400 text-2xl" />
                  </div>
                  <p className="text-sm font-semibold text-gray-600 mb-1">欢迎使用实例管理</p>
                  <p className="text-[11px] text-gray-400 leading-relaxed">当前还没有注册任何应用，请先部署并启动您的服务实例，系统将自动完成注册。</p>
                  <div className="mt-4 flex items-center justify-center gap-4 text-[10px] text-gray-300">
                    <div className="flex items-center gap-1.5">
                      <span className="w-5 h-5 rounded-md bg-gray-50 flex items-center justify-center"><i className="fas fa-cube text-[8px]" /></span>
                      <span>部署应用</span>
                    </div>
                    <i className="fas fa-chevron-right text-[6px]" />
                    <div className="flex items-center gap-1.5">
                      <span className="w-5 h-5 rounded-md bg-gray-50 flex items-center justify-center"><i className="fas fa-heartbeat text-[8px]" /></span>
                      <span>自动注册</span>
                    </div>
                    <i className="fas fa-chevron-right text-[6px]" />
                    <div className="flex items-center gap-1.5">
                      <span className="w-5 h-5 rounded-md bg-gray-50 flex items-center justify-center"><i className="fas fa-cog text-[8px]" /></span>
                      <span>开始管理</span>
                    </div>
                  </div>
                </div>
              ) : emptyReason === 'no_services' ? (
                <div className="text-center max-w-xs">
                  <div className="w-16 h-16 rounded-2xl bg-gradient-to-br from-amber-50 to-orange-50 flex items-center justify-center mx-auto mb-4 shadow-sm">
                    <i className="fas fa-sitemap text-amber-400 text-2xl" />
                  </div>
                  <p className="text-sm font-semibold text-gray-600 mb-1">该应用下暂无服务</p>
                  <p className="text-[11px] text-gray-400 leading-relaxed">应用 <span className="font-semibold text-gray-500">{apps.find(a => a.id === selectedAppId)?.name || selectedAppId}</span> 下还没有注册任何服务，请尝试选择其他应用或等待服务上线。</p>
                </div>
              ) : emptyReason === 'no_instances' ? (
                <div className="text-center max-w-xs">
                  <div className="w-16 h-16 rounded-2xl bg-gradient-to-br from-green-50 to-emerald-50 flex items-center justify-center mx-auto mb-4 shadow-sm">
                    <i className="fas fa-server text-green-400 text-2xl" />
                  </div>
                  <p className="text-sm font-semibold text-gray-600 mb-1">该服务下暂无实例</p>
                  <p className="text-[11px] text-gray-400 leading-relaxed">服务 <span className="font-semibold text-gray-500">{selectedServiceName}</span> 下目前没有在线实例，实例启动后将自动出现在列表中。</p>
                </div>
              ) : emptyReason === 'search_empty' ? (
                <div className="text-center max-w-xs">
                  <div className="w-16 h-16 rounded-2xl bg-gradient-to-br from-purple-50 to-fuchsia-50 flex items-center justify-center mx-auto mb-4 shadow-sm">
                    <i className="fas fa-search text-purple-400 text-2xl" />
                  </div>
                  <p className="text-sm font-semibold text-gray-600 mb-1">没有匹配的实例</p>
                  <p className="text-[11px] text-gray-400 leading-relaxed">当前筛选条件下没有找到实例，请尝试调整搜索关键词或重置筛选条件。</p>
                </div>
              ) : (
                <div className="text-center">
                  <div className="w-12 h-12 rounded-2xl bg-gray-50 flex items-center justify-center mx-auto mb-3">
                    <i className="fas fa-mouse-pointer text-gray-300 text-lg" />
                  </div>
                  <p className="text-sm text-gray-400 font-medium">选择一个实例</p>
                  <p className="text-[10px] text-gray-300 mt-1">查看详情、管理任务等</p>
                </div>
              )}
            </div>
          )}
        </div>
      </div>

      {/* ═══ Arthas 终端面板（懒加载） ═══ */}
      {terminalInstance && (
        <Suspense fallback={
          <div className="fixed inset-4 z-[80] bg-gray-900 rounded-xl shadow-2xl flex items-center justify-center">
            <div className="text-gray-400 flex items-center gap-3">
              <i className="fas fa-spinner fa-spin text-xl" />
              <span className="text-sm">Loading terminal...</span>
            </div>
          </div>
        }>
          <TerminalPanel
            instance={terminalInstance}
            onClose={() => setTerminalInstance(null)}
            onStatusChange={() => loadInstances(selectedAppId || undefined, selectedServiceName || undefined)}
          />
        </Suspense>
      )}
    </div>
  );
}

// ── Overview Tab 子组件 ──────────────────────────────

function OverviewTab({ instance, copyToClipboard }: { instance: EnrichedInstance; copyToClipboard: (text: string) => void }) {
  return (
    <div className="space-y-5">
      {/* 基本信息 */}
      <section>
        <SectionTitle icon="fas fa-id-card" title="Basic Information" />
        <div className="grid grid-cols-2 gap-2">
          <InfoCard label="Agent ID" value={instance.agent_id} mono onCopy={() => copyToClipboard(instance.agent_id)} />
          <InfoCard label="Version" value={instance.version || '-'} />
          <InfoCard label="Hostname" value={instance.hostname || '-'} />
          <InfoCard label="IP Address" value={instance.ip || '-'} />
          <InfoCard label="App ID" value={instance.app_id || 'Global'} mono />
          <InfoCard label="Service" value={instance.service_name || 'default'} />
          <InfoCard label="PID" value={instance.pid ? String(instance.pid) : '-'} />
        </div>
      </section>

      {/* Arthas 状态 */}
      <section>
        <SectionTitle icon="fas fa-bug" title="Arthas Status" />
        <div className={`p-3 rounded-lg border ${
          instance.arthasStatus.tunnelReady
            ? 'bg-purple-50/50 border-purple-200/60'
            : 'bg-gray-50 border-gray-100'
        }`}>
          <div className="flex items-center gap-2.5">
            <div className={`w-8 h-8 rounded-lg flex items-center justify-center ${
              instance.arthasStatus.tunnelReady ? 'bg-purple-100 text-purple-600' : 'bg-gray-100 text-gray-400'
            }`}>
              <i className="fas fa-bug text-xs" />
            </div>
            <div>
              <div className="flex items-center gap-2">
                <span className={`inline-flex rounded-full h-1.5 w-1.5 ${
                  instance.arthasStatus.tunnelReady ? 'bg-purple-500' : 'bg-gray-300'
                }`} />
                <span className={`text-[11px] font-semibold ${
                  instance.arthasStatus.tunnelReady ? 'text-purple-700' : 'text-gray-500'
                }`}>
                  {instance.arthasStatus.tunnelReady ? 'Tunnel Connected' : 'Tunnel Not Connected'}
                </span>
              </div>
              {instance.arthasStatus.arthasVersion && (
                <p className="text-[10px] text-gray-400 mt-0.5 ml-3.5">Version: {instance.arthasStatus.arthasVersion}</p>
              )}
            </div>
          </div>
        </div>
      </section>

      {/* 元数据 */}
      <section>
        <SectionTitle icon="fas fa-tags" title="Metadata" />
        {Object.keys(instance.labels || {}).length > 0 ? (
          <div className="grid grid-cols-2 gap-2">
            {Object.entries(instance.labels || {}).map(([key, val]) => (
              <div key={key} className="p-2 bg-gray-50 rounded-lg border border-gray-100 overflow-hidden">
                <div className="text-[9px] text-gray-400 font-mono truncate">{key}</div>
                <div className="text-[11px] font-medium text-gray-700 mt-0.5 truncate">{val}</div>
              </div>
            ))}
          </div>
        ) : (
          <div className="text-[11px] text-gray-400 bg-gray-50 border border-gray-100 rounded-lg p-3 text-center">
            <i className="fas fa-inbox text-gray-200 text-sm block mb-1" />
            No metadata
          </div>
        )}
      </section>

      {/* 生命周期 */}
      <section>
        <SectionTitle icon="fas fa-clock" title="Lifecycle" />
        <div className="grid grid-cols-2 gap-2">
          <div className="bg-gray-50 p-2.5 rounded-lg border border-gray-100">
            <p className="text-[9px] font-bold text-gray-400 uppercase mb-0.5">Start Time</p>
            <p className="text-[11px] text-gray-700">{formatTimestamp(instance.start_time)}</p>
            {instance.start_time > 0 && (
              <p className="text-[10px] text-blue-600 mt-0.5 font-medium">up {formatUptime(instance.start_time)}</p>
            )}
          </div>
          <div className="bg-gray-50 p-2.5 rounded-lg border border-gray-100">
            <p className="text-[9px] font-bold text-gray-400 uppercase mb-0.5">Registered At</p>
            <p className="text-[11px] text-gray-700">{formatTimestamp(instance.registered_at)}</p>
          </div>
          <div className="bg-gray-50 p-2.5 rounded-lg border border-gray-100">
            <p className="text-[9px] font-bold text-gray-400 uppercase mb-0.5">Last Heartbeat</p>
            <p className="text-[11px] text-gray-700">{formatTimestamp(instance.last_heartbeat)}</p>
            <p className="text-[10px] text-green-600 mt-0.5 font-medium">{formatRelativeTime(instance.last_heartbeat)}</p>
          </div>
          <div className="bg-gray-50 p-2.5 rounded-lg border border-gray-100">
            <p className="text-[9px] font-bold text-gray-400 uppercase mb-0.5">Status</p>
            <div className="flex items-center gap-1.5 mt-1">
              <div className="relative flex h-2 w-2">
                {instance.status?.state === 'online' && (
                  <span className="animate-ping absolute inline-flex h-full w-full rounded-full bg-green-400 opacity-75" />
                )}
                <span className={`relative inline-flex rounded-full h-2 w-2 ${
                  instance.status?.state === 'online' ? 'bg-green-500' : 'bg-gray-400'
                }`} />
              </div>
              <span className={`text-[11px] font-bold ${
                instance.status?.state === 'online' ? 'text-green-600' : 'text-gray-500'
              }`}>
                {instance.status?.state === 'online' ? 'Online' : 'Offline'}
              </span>
            </div>
          </div>
        </div>
      </section>
    </div>
  );
}

// ── Section 标题子组件 ──────────────────────────────

function SectionTitle({ icon, title }: { icon: string; title: string }) {
  return (
    <h4 className="text-[10px] font-bold text-gray-400 uppercase tracking-widest mb-2 flex items-center gap-1.5">
      <i className={`${icon} text-[8px]`} /> {title}
    </h4>
  );
}

// ── 信息卡片子组件 ──────────────────────────────────

function InfoCard({ label, value, mono = false, onCopy }: { label: string; value: string; mono?: boolean; onCopy?: () => void }) {
  return (
    <div className="bg-gray-50 p-2.5 rounded-lg border border-gray-100 group">
      <div className="flex items-center justify-between">
        <p className="text-[9px] font-bold text-gray-400 uppercase">{label}</p>
        {onCopy && (
          <button onClick={onCopy} className="text-gray-300 hover:text-blue-500 transition opacity-0 group-hover:opacity-100">
            <i className="fas fa-copy text-[8px]" />
          </button>
        )}
      </div>
      <p className={`text-gray-700 break-all mt-0.5 ${mono ? 'font-mono text-[10px]' : 'text-[11px] font-semibold'}`}>{value}</p>
    </div>
  );
}
