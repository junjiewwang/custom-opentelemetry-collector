/**
 * InstancesPage - 实例管理页面
 *
 * 从旧版 Alpine.js instances 视图完整移植到 React。
 *
 * 功能：
 *   - 左侧 App→Service 两级树（TreeNav）
 *   - 右侧实例卡片列表
 *   - 5 个可点击统计卡片（全部/在线/离线/Arthas就绪/Arthas未注册）
 *   - 搜索过滤（Agent ID / Hostname / Service / IP / App）
 *   - 树-列表联动筛选
 *   - 详情抽屉（基本信息 / Arthas 状态 / 元数据 / 生命周期）
 *   - 下线实例操作
 */

import { useState, useCallback, useEffect, useMemo, lazy, Suspense } from 'react';
import { apiClient } from '@/api/client';
import { useToast } from '@/contexts/ToastContext';
import { useConfirm } from '@/components/ConfirmDialog';
import TreeNav, { type TreeNode } from '@/components/TreeNav';
import DetailDrawer, { DrawerSection } from '@/components/DetailDrawer';
import type { Instance, EnrichedInstance, ArthasAgent, ApiError } from '@/types/api';

// 懒加载终端面板（包含 xterm.js，约 200KB）
const TerminalPanel = lazy(() => import('@/components/Terminal/TerminalPanel'));

// ── 常量 ──────────────────────────────────────────────

type StatusFilter = 'all' | 'online' | 'offline' | 'arthas_ready' | 'arthas_not_ready';

const STAT_CARDS: { label: string; filter: StatusFilter; icon: string; color: string }[] = [
  { label: 'Total',          filter: 'all',               icon: 'fas fa-server',       color: 'text-gray-500' },
  { label: 'Online',         filter: 'online',            icon: 'fas fa-check-circle', color: 'text-green-500' },
  { label: 'Offline',        filter: 'offline',           icon: 'fas fa-moon',         color: 'text-gray-400' },
  { label: 'Arthas Ready',   filter: 'arthas_ready',      icon: 'fas fa-bug',          color: 'text-purple-500' },
  { label: 'Arthas N/A',     filter: 'arthas_not_ready',  icon: 'fas fa-unlink',       color: 'text-orange-500' },
];

// ── 工具函数 ──────────────────────────────────────────

/** 毫秒时间戳 → 本地日期时间 */
function formatTimestamp(ts: number): string {
  if (!ts) return '-';
  const ms = ts < 10000000000 ? ts * 1000 : ts;
  return new Date(ms).toLocaleString('zh-CN');
}

/** 毫秒时间戳 → 相对时间 */
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

/** 毫秒时间戳 → 运行时长 */
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
  const [selectedTreeNodeId, setSelectedTreeNodeId] = useState<string | undefined>();
  const [drawerOpen, setDrawerOpen] = useState(false);
  const [selectedInstance, setSelectedInstance] = useState<EnrichedInstance | null>(null);

  // 终端面板
  const [terminalInstance, setTerminalInstance] = useState<EnrichedInstance | null>(null);

  // ── 加载实例 ──────────────────────────────────────

  const loadInstances = useCallback(async () => {
    if (loading) return;
    setLoading(true);
    try {
      const [instancesRes, arthasRes] = await Promise.allSettled([
        apiClient.getInstances('all'),
        apiClient.getArthasAgents(),
      ]);

      const instancesList = instancesRes.status === 'fulfilled' ? instancesRes.value : [];
      const tunnelAgents = arthasRes.status === 'fulfilled' ? (arthasRes.value || []) : [];

      setInstances(enrichInstances(instancesList, tunnelAgents));
    } catch (e) {
      const err = e as ApiError;
      showToast(`Failed to load instances: ${err.message}`, 'error');
    } finally {
      setLoading(false);
    }
  }, [loading, showToast]);

  useEffect(() => {
    loadInstances();
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  // ── 统计数据 ──────────────────────────────────────

  const stats = useMemo(() => {
    const result: Record<StatusFilter, number> = {
      all: instances.length,
      online: 0,
      offline: 0,
      arthas_ready: 0,
      arthas_not_ready: 0,
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

    // 状态过滤
    if (statusFilter === 'online') {
      list = list.filter(i => i.status?.state === 'online');
    } else if (statusFilter === 'offline') {
      list = list.filter(i => i.status?.state !== 'online');
    } else if (statusFilter === 'arthas_ready') {
      list = list.filter(i => i.arthasStatus.tunnelReady);
    } else if (statusFilter === 'arthas_not_ready') {
      list = list.filter(i => !i.arthasStatus.tunnelReady);
    }

    // 搜索过滤
    const q = search.toLowerCase().trim();
    if (q) {
      list = list.filter(i =>
        i.agent_id.toLowerCase().includes(q) ||
        (i.hostname || '').toLowerCase().includes(q) ||
        (i.service_name || '').toLowerCase().includes(q) ||
        (i.ip || '').includes(q) ||
        (i.app_id || '').toLowerCase().includes(q),
      );
    }

    // 树节点联动
    if (selectedTreeNodeId) {
      list = list.filter(inst => {
        const appId = inst.app_id || '_global_';
        const svcName = inst.service_name || '_unknown_';
        return `svc-${appId}-${svcName}` === selectedTreeNodeId;
      });
    }

    return list;
  }, [instances, statusFilter, search, selectedTreeNodeId]);

  // ── 构建左侧树 ──────────────────────────────────

  const treeData = useMemo((): TreeNode[] => {
    // 使用已过滤的实例（按状态 + 搜索过滤后），但不按树节点过滤
    let list = instances;

    if (statusFilter === 'online') {
      list = list.filter(i => i.status?.state === 'online');
    } else if (statusFilter === 'offline') {
      list = list.filter(i => i.status?.state !== 'online');
    } else if (statusFilter === 'arthas_ready') {
      list = list.filter(i => i.arthasStatus.tunnelReady);
    } else if (statusFilter === 'arthas_not_ready') {
      list = list.filter(i => !i.arthasStatus.tunnelReady);
    }

    const q = search.toLowerCase().trim();
    if (q) {
      list = list.filter(i =>
        i.agent_id.toLowerCase().includes(q) ||
        (i.hostname || '').toLowerCase().includes(q) ||
        (i.service_name || '').toLowerCase().includes(q) ||
        (i.ip || '').includes(q) ||
        (i.app_id || '').toLowerCase().includes(q),
      );
    }

    const appMap = new Map<string, {
      id: string;
      name: string;
      count: number;
      services: Map<string, { id: string; name: string; count: number; onlineCount: number; offlineCount: number }>;
    }>();

    for (const inst of list) {
      const appId = inst.app_id || '_global_';
      const svcName = inst.service_name || '_unknown_';

      if (!appMap.has(appId)) {
        appMap.set(appId, {
          id: `app-${appId}`,
          name: appId === '_global_' ? 'Global' : appId,
          count: 0,
          services: new Map(),
        });
      }
      const appNode = appMap.get(appId)!;
      appNode.count++;

      if (!appNode.services.has(svcName)) {
        appNode.services.set(svcName, {
          id: `svc-${appId}-${svcName}`,
          name: svcName === '_unknown_' ? 'Unknown Service' : svcName,
          count: 0,
          onlineCount: 0,
          offlineCount: 0,
        });
      }
      const svcNode = appNode.services.get(svcName)!;
      svcNode.count++;
      if (inst.status?.state === 'online') svcNode.onlineCount++;
      else svcNode.offlineCount++;
    }

    // 转为 TreeNode[]
    const result: TreeNode[] = [];
    for (const [, appNode] of appMap) {
      const svcChildren: TreeNode[] = [];
      for (const [, svc] of appNode.services) {
        svcChildren.push({
          id: svc.id,
          name: svc.name,
          icon: 'fas fa-sitemap',
          iconColor: 'text-purple-400',
          badge: svc.count,
          badgeColor: 'bg-gray-100 text-gray-500',
        });
      }
      // 按 count 降序
      svcChildren.sort((a, b) => ((b.badge as number) || 0) - ((a.badge as number) || 0));

      result.push({
        id: appNode.id,
        name: appNode.name,
        icon: 'fas fa-cube',
        iconColor: 'text-blue-500',
        badge: appNode.count,
        badgeColor: 'bg-gray-100 text-gray-500',
        defaultExpanded: true,
        children: svcChildren,
      });
    }
    // 按 count 降序
    result.sort((a, b) => ((b.badge as number) || 0) - ((a.badge as number) || 0));
    return result;
  }, [instances, statusFilter, search]);

  // ── 树节点选中 ──────────────────────────────────

  const handleTreeSelect = useCallback((node: TreeNode | null) => {
    if (!node) {
      setSelectedTreeNodeId(undefined);
      return;
    }
    // 只允许选中 Service 节点（id 以 svc- 开头）
    if (node.id.startsWith('svc-')) {
      setSelectedTreeNodeId(node.id);
    }
  }, []);

  // ── 选中树节点的标签 ──────────────────────────────

  const selectedTreeLabel = useMemo(() => {
    if (!selectedTreeNodeId) return '';
    for (const app of treeData) {
      for (const svc of (app.children || [])) {
        if (svc.id === selectedTreeNodeId) {
          return `${app.name} / ${svc.name}`;
        }
      }
    }
    return '';
  }, [selectedTreeNodeId, treeData]);

  // ── 显示详情 ──────────────────────────────────────

  const showDetails = useCallback((inst: EnrichedInstance) => {
    setSelectedInstance(inst);
    setDrawerOpen(true);
  }, []);

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
      setDrawerOpen(false);
      loadInstances();
    } catch (e) {
      const err = e as ApiError;
      showToast(`Failed to remove instance: ${err.message}`, 'error');
    }
  }, [confirm, showToast, loadInstances]);

  // ── 复制到剪贴板 ──────────────────────────────────

  const copyToClipboard = useCallback(async (text: string) => {
    try {
      await navigator.clipboard.writeText(text);
      showToast('Copied to clipboard', 'success');
    } catch {
      showToast('Failed to copy', 'error');
    }
  }, [showToast]);

  // ── 渲染 ──────────────────────────────────────────

  return (
    <div className="space-y-6">
      {/* 头部: 标题 & 操作 */}
      <div className="flex items-center justify-between">
        <h2 className="text-2xl font-bold text-gray-800">Instance Management</h2>
        <div className="flex items-center gap-3">
          <div className="relative">
            <input
              type="text"
              value={search}
              onChange={e => setSearch(e.target.value)}
              placeholder="Search Agent ID, Host, Service..."
              className="pl-9 pr-4 py-2 border border-gray-200 rounded-lg focus:ring-2 focus:ring-blue-500 focus:border-blue-500 w-56 text-sm"
            />
            <i className="fas fa-search absolute left-3 top-1/2 -translate-y-1/2 text-gray-400" />
          </div>
          <button
            onClick={loadInstances}
            className="px-4 py-2 bg-gray-100 text-gray-700 rounded-lg hover:bg-gray-200 transition text-sm"
          >
            <i className={`fas fa-sync ${loading ? 'fa-spin' : ''}`} />
          </button>
        </div>
      </div>

      {/* 统计卡片 */}
      <div className="grid grid-cols-2 md:grid-cols-3 lg:grid-cols-5 gap-4">
        {STAT_CARDS.map(stat => (
          <div
            key={stat.filter}
            onClick={() => setStatusFilter(stat.filter)}
            className={`p-4 rounded-xl shadow-sm border border-gray-100 cursor-pointer hover:shadow-md transition group ${
              statusFilter === stat.filter
                ? 'ring-2 ring-blue-500 bg-blue-50'
                : 'bg-white'
            }`}
          >
            <div className="flex items-center justify-between mb-1">
              <p className="text-[10px] font-bold text-gray-400 uppercase tracking-widest">{stat.label}</p>
              <i className={`${stat.icon} ${stat.color} opacity-40 group-hover:opacity-100 transition text-xs`} />
            </div>
            <p className="text-2xl font-bold text-gray-800">{stats[stat.filter]}</p>
          </div>
        ))}
      </div>

      {/* 主体：左侧树 + 右侧列表 */}
      <div className="flex gap-6" style={{ minHeight: 500 }}>
        {/* 左侧导航树 */}
        <div className="w-72 flex-shrink-0">
          <div className="bg-white rounded-xl shadow-sm border border-gray-100 overflow-hidden sticky top-4 flex flex-col" style={{ maxHeight: 'calc(100vh - 320px)' }}>
            <div className="px-4 py-3 bg-gray-50 border-b border-gray-100 flex items-center justify-between flex-shrink-0">
              <h3 className="text-xs font-bold text-gray-500 uppercase tracking-widest">App / Service</h3>
            </div>

            {/* "全部实例" 节点 */}
            <button
              onClick={() => setSelectedTreeNodeId(undefined)}
              className={`w-full flex items-center gap-2 px-4 py-2.5 text-left transition select-none border-b border-gray-50 ${
                !selectedTreeNodeId
                  ? 'bg-blue-50 border-l-2 border-l-blue-500'
                  : 'border-l-2 border-l-transparent hover:bg-gray-50'
              }`}
            >
              <i className="fas fa-globe text-blue-500 text-xs" />
              <span className={`text-xs font-bold ${!selectedTreeNodeId ? 'text-blue-700' : 'text-gray-600'}`}>
                All Instances
              </span>
              <span className="text-[9px] font-bold text-gray-400 bg-gray-100 px-1.5 py-0.5 rounded ml-auto">
                {instances.length}
              </span>
            </button>

            <TreeNav
              data={treeData}
              selectedId={selectedTreeNodeId}
              onSelect={handleTreeSelect}
              allowSelectParent={false}
              emptyText={loading ? 'Loading...' : 'No services found'}
            />
          </div>
        </div>

        {/* 右侧实例列表 */}
        <div className="flex-1 min-w-0">
          {/* 面包屑 */}
          {selectedTreeNodeId ? (
            <div className="mb-4 flex items-center gap-2">
              <div className="bg-blue-50 border border-blue-200 rounded-lg px-3 py-1.5 flex items-center gap-2">
                <i className="fas fa-sitemap text-blue-500 text-xs" />
                <span className="text-xs font-bold text-blue-700">{selectedTreeLabel}</span>
                <button
                  onClick={() => setSelectedTreeNodeId(undefined)}
                  className="text-blue-400 hover:text-blue-600 transition ml-1"
                >
                  <i className="fas fa-times text-[10px]" />
                </button>
              </div>
              <span className="text-[10px] text-gray-400">{filteredInstances.length} instances</span>
            </div>
          ) : (
            <div className="mb-4">
              <p className="text-xs text-gray-400">
                <i className="fas fa-info-circle mr-1" />
                Click a service node on the left to filter the instance list
              </p>
            </div>
          )}

          {/* 实例卡片列表 */}
          <div className="space-y-3">
            {filteredInstances.map(inst => (
              <div
                key={inst.agent_id}
                onClick={() => showDetails(inst)}
                className="bg-white border border-gray-100 rounded-xl p-4 hover:border-blue-300 hover:shadow-md transition cursor-pointer group"
              >
                <div className="flex items-center justify-between">
                  {/* 左侧信息 */}
                  <div className="flex items-center gap-3 overflow-hidden flex-1">
                    <div className={`w-10 h-10 rounded-lg flex items-center justify-center flex-shrink-0 ${
                      inst.status?.state === 'online' ? 'bg-green-50 text-green-600' : 'bg-gray-50 text-gray-400'
                    }`}>
                      <i className="fas fa-server text-sm" />
                    </div>
                    <div className="overflow-hidden flex-1 min-w-0">
                      <div className="flex items-center gap-2">
                        <span className="text-sm font-bold text-gray-800 truncate">
                          {inst.agent_id.substring(0, 16)}
                        </span>
                        <span className="text-[10px] bg-gray-100 text-gray-500 px-1 rounded font-normal flex-shrink-0">
                          v{inst.version || '0.0.0'}
                        </span>
                        <span className={`px-1.5 py-0.5 rounded-full text-[9px] font-bold uppercase tracking-tight flex-shrink-0 ${
                          inst.status?.state === 'online' ? 'bg-green-100 text-green-700' : 'bg-gray-100 text-gray-500'
                        }`}>
                          {inst.status?.state === 'online' ? 'ONLINE' : 'OFFLINE'}
                        </span>
                      </div>
                      <div className="flex items-center gap-3 mt-1">
                        <span className="text-[10px] text-gray-400 truncate flex items-center gap-1">
                          <i className="fas fa-network-wired text-[8px]" />
                          {(inst.hostname || 'Unknown') + ' / ' + (inst.ip || '0.0.0.0')}
                        </span>
                        <span className="text-[9px] text-gray-300">|</span>
                        <span className="text-[10px] text-gray-400 truncate">
                          {(inst.app_id ? inst.app_id.substring(0, 8) : 'Global') + ' / ' + (inst.service_name || 'default')}
                        </span>
                      </div>
                    </div>
                  </div>

                  {/* 右侧状态 + 操作 */}
                  <div className="flex items-center gap-4 flex-shrink-0 ml-3">
                    <div className="flex flex-col items-end gap-0.5">
                      <div className="flex items-center gap-1.5">
                        <span className={`inline-flex rounded-full h-2 w-2 ${
                          inst.arthasStatus.tunnelReady ? 'bg-purple-500' : 'bg-gray-300'
                        }`} />
                        <span className={`text-[10px] font-medium ${
                          inst.arthasStatus.tunnelReady ? 'text-purple-600' : 'text-gray-400'
                        }`}>
                          {inst.arthasStatus.tunnelReady ? 'Arthas Ready' : 'Arthas N/A'}
                        </span>
                      </div>
                      <div className="text-[9px] text-gray-400">
                        <span>heartbeat {formatRelativeTime(inst.last_heartbeat)}</span>
                        {inst.start_time > 0 && (
                          <span className="ml-1">· up {formatUptime(inst.start_time)}</span>
                        )}
                      </div>
                    </div>

                    <button
                      onClick={e => { e.stopPropagation(); showDetails(inst); }}
                      className="p-1.5 text-gray-400 hover:text-blue-600 hover:bg-blue-50 rounded-md transition"
                      title="View Details"
                    >
                      <i className="fas fa-external-link-alt text-sm" />
                    </button>
                  </div>
                </div>
              </div>
            ))}

            {/* 空状态 */}
            {filteredInstances.length === 0 && (
              <div className="bg-white rounded-xl shadow-sm border border-gray-100 p-12 text-center">
                <i className="fas fa-server text-4xl mb-3 text-gray-200" />
                <p className="text-gray-400">No instances match the current filters</p>
                {search && (
                  <p className="text-[10px] text-gray-300 mt-1">Try modifying search terms or clearing filters</p>
                )}
              </div>
            )}
          </div>
        </div>
      </div>

      {/* ── 详情抽屉 ────────────────────────────────── */}
      <DetailDrawer
        open={drawerOpen}
        onClose={() => setDrawerOpen(false)}
        width="xl"
        title={selectedInstance?.agent_id || ''}
        subtitle={
          selectedInstance
            ? `${selectedInstance.status?.state === 'online' ? '● ONLINE' : '○ OFFLINE'} · ${selectedInstance.hostname || ''} / ${selectedInstance.ip || ''} · v${selectedInstance.version || '0.0.0'}`
            : undefined
        }
        footer={
          selectedInstance && (
            <div className="flex gap-3">
              <button
                onClick={() => copyToClipboard(selectedInstance.agent_id)}
                className="flex-1 px-4 py-3 bg-white border border-gray-200 text-gray-600 rounded-xl font-bold hover:bg-gray-50 transition flex items-center justify-center gap-2"
              >
                <i className="fas fa-copy" /> Copy Agent ID
              </button>
              <button
                onClick={() => handleUnregister(selectedInstance)}
                disabled={selectedInstance.status?.state === 'online'}
                className="flex-1 px-4 py-3 bg-red-50 text-red-600 rounded-xl font-bold hover:bg-red-100 transition flex items-center justify-center gap-2 disabled:opacity-30 disabled:grayscale"
              >
                <i className="fas fa-sign-out-alt" /> Remove Instance
              </button>
            </div>
          )
        }
      >
        {selectedInstance && (
          <div className="space-y-8">
            {/* 基本信息 */}
            <DrawerSection title="Basic Information">
              <div className="grid grid-cols-2 gap-4">
                <InfoCard label="Agent ID" value={selectedInstance.agent_id} mono />
                <InfoCard label="Version" value={selectedInstance.version || '-'} />
                <InfoCard label="Hostname" value={selectedInstance.hostname || '-'} />
                <InfoCard label="IP Address" value={selectedInstance.ip || '-'} />
                <InfoCard label="App ID" value={selectedInstance.app_id || 'Global'} mono />
                <InfoCard label="Service Name" value={selectedInstance.service_name || 'default'} />
              </div>
            </DrawerSection>

            {/* Arthas 状态 */}
            <DrawerSection title="Arthas Status">
              <div className="bg-gray-50 p-5 rounded-xl border border-gray-100">
                <div className="flex items-center justify-between">
                  <div className="flex items-center gap-3">
                    <div className={`w-10 h-10 rounded-lg flex items-center justify-center ${
                      selectedInstance.arthasStatus.tunnelReady ? 'bg-purple-100 text-purple-600' : 'bg-gray-100 text-gray-400'
                    }`}>
                      <i className="fas fa-bug text-sm" />
                    </div>
                    <div>
                      <div className="flex items-center gap-2">
                        <span className={`inline-flex rounded-full h-2.5 w-2.5 ${
                          selectedInstance.arthasStatus.tunnelReady ? 'bg-purple-500' : 'bg-gray-300'
                        }`} />
                        <span className={`text-sm font-bold ${
                          selectedInstance.arthasStatus.tunnelReady ? 'text-purple-700' : 'text-gray-500'
                        }`}>
                          {selectedInstance.arthasStatus.tunnelReady ? 'Tunnel Connected' : 'Tunnel Not Connected'}
                        </span>
                      </div>
                      {selectedInstance.arthasStatus.arthasVersion && (
                        <p className="text-[10px] text-gray-400 mt-0.5">
                          Arthas Version: {selectedInstance.arthasStatus.arthasVersion}
                        </p>
                      )}
                    </div>
                  </div>
                  {/* 打开终端按钮 */}
                  {selectedInstance.status?.state === 'online' && (
                    <button
                      onClick={() => {
                        setTerminalInstance(selectedInstance);
                        setDrawerOpen(false);
                      }}
                      className={`px-4 py-2.5 rounded-xl text-sm font-bold transition flex items-center gap-2 shadow-sm ${
                        selectedInstance.arthasStatus.tunnelReady
                          ? 'bg-purple-600 text-white hover:bg-purple-700'
                          : 'bg-gray-800 text-white hover:bg-gray-900'
                      }`}
                    >
                      <i className="fas fa-terminal text-xs" />
                      {selectedInstance.arthasStatus.tunnelReady ? 'Open Terminal' : 'Attach & Connect'}
                    </button>
                  )}
                </div>
              </div>
            </DrawerSection>

            {/* 元数据 */}
            <DrawerSection title="Metadata (Labels)">
              {Object.keys(selectedInstance.labels || {}).length > 0 ? (
                <div className="grid grid-cols-2 gap-3">
                  {Object.entries(selectedInstance.labels || {}).map(([key, val]) => (
                    <div key={key} className="p-3 bg-gray-50 rounded-lg border border-gray-100 overflow-hidden">
                      <div className="text-[10px] text-gray-400 font-mono truncate">{key}</div>
                      <div className="text-sm font-medium text-gray-800 mt-1 truncate">{val}</div>
                    </div>
                  ))}
                </div>
              ) : (
                <div className="text-sm text-gray-400 bg-gray-50 border border-gray-100 rounded-lg p-4 text-center">
                  <i className="fas fa-inbox text-gray-200 text-xl block mb-2" />
                  No metadata
                </div>
              )}
            </DrawerSection>

            {/* 生命周期 */}
            <DrawerSection title="Lifecycle">
              <div className="bg-white p-5 rounded-xl border border-gray-100 shadow-sm">
                <div className="grid grid-cols-2 gap-y-4 gap-x-6">
                  <div>
                    <p className="text-[9px] font-bold text-gray-400 uppercase mb-1">Start Time</p>
                    <p className="text-xs text-gray-700 font-medium">{formatTimestamp(selectedInstance.start_time)}</p>
                    {selectedInstance.start_time > 0 && (
                      <p className="text-[10px] text-blue-600 mt-0.5">
                        up {formatUptime(selectedInstance.start_time)}
                      </p>
                    )}
                  </div>
                  <div>
                    <p className="text-[9px] font-bold text-gray-400 uppercase mb-1">Registered At</p>
                    <p className="text-xs text-gray-700 font-medium">{formatTimestamp(selectedInstance.registered_at)}</p>
                  </div>
                  <div>
                    <p className="text-[9px] font-bold text-gray-400 uppercase mb-1">Last Heartbeat</p>
                    <p className="text-xs text-gray-700 font-medium">{formatTimestamp(selectedInstance.last_heartbeat)}</p>
                    <p className="text-[10px] text-green-600 mt-0.5">{formatRelativeTime(selectedInstance.last_heartbeat)}</p>
                  </div>
                  <div>
                    <p className="text-[9px] font-bold text-gray-400 uppercase mb-1">Status</p>
                    <div className="flex items-center gap-2 mt-0.5">
                      <div className="relative flex h-2 w-2">
                        {selectedInstance.status?.state === 'online' && (
                          <span className="animate-ping absolute inline-flex h-full w-full rounded-full bg-green-400 opacity-75" />
                        )}
                        <span className={`relative inline-flex rounded-full h-2 w-2 ${
                          selectedInstance.status?.state === 'online' ? 'bg-green-500' : 'bg-gray-400'
                        }`} />
                      </div>
                      <span className={`text-xs font-bold ${
                        selectedInstance.status?.state === 'online' ? 'text-green-600' : 'text-gray-500'
                      }`}>
                        {selectedInstance.status?.state === 'online' ? 'Online' : 'Offline'}
                      </span>
                    </div>
                  </div>
                </div>
              </div>
            </DrawerSection>
          </div>
        )}
      </DetailDrawer>
      {/* ── Arthas 终端面板（懒加载） ──────────── */}
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
            onStatusChange={loadInstances}
          />
        </Suspense>
      )}
    </div>
  );
}

// ── 信息卡片子组件 ──────────────────────────────────

function InfoCard({ label, value, mono = false }: { label: string; value: string; mono?: boolean }) {
  return (
    <div className="bg-gray-50 p-4 rounded-xl border border-gray-100">
      <p className="text-[10px] font-bold text-gray-400 mb-1 uppercase">{label}</p>
      <p className={`text-gray-800 break-all ${mono ? 'font-mono text-xs' : 'font-bold'}`}>{value}</p>
    </div>
  );
}
