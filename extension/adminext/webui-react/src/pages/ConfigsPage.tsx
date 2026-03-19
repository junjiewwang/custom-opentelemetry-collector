/**
 * ConfigsPage - 配置编辑器页面
 *
 * 从旧版 Alpine.js configs 视图完整移植到 React。
 *
 * 功能：
 *   - 左侧服务树（App → Service → Instance 三级结构）
 *   - 右侧 JSON 编辑器（textarea + 语法校验）
 *   - 模板推荐（空配置时推荐模板）
 *   - 缺失字段检测补全
 *   - Save / Reset / Delete 操作
 *   - 脏状态检测 + 未保存提示
 */

import { useState, useCallback, useEffect, useRef, useMemo } from 'react';
import { apiClient } from '@/api/client';
import { useToast } from '@/contexts/ToastContext';
import { useConfirm } from '@/components/ConfirmDialog';
import TreeNav, { type TreeNode } from '@/components/TreeNav';
import type { App, Instance, AgentConfig, ApiError } from '@/types/api';

// ── 类型定义 ──────────────────────────────────────────

/** 树节点附加的配置元数据 */
interface ConfigNodeData {
  type: 'app' | 'service' | 'instance';
  appId: string;
  serviceName: string;
  agentId?: string;
  status?: string;
}

/** 编辑状态 */
interface EditingState {
  appId: string;
  serviceName: string;
  loading: boolean;
  saving: boolean;
  content: string;
  originalContent: string;
  isDirty: boolean;
  version: string;
  reference: Record<string, unknown> | null;
  missingFields: string[];
  hintType: '' | 'template' | 'missing';
  showHint: boolean;
  jsonError: string;
}

const INITIAL_EDITING: EditingState = {
  appId: '',
  serviceName: '',
  loading: false,
  saving: false,
  content: '',
  originalContent: '',
  isDirty: false,
  version: '',
  reference: null,
  missingFields: [],
  hintType: '',
  showHint: false,
  jsonError: '',
};

// ── 过滤元数据字段的辅助 ──────────────────────────────

const META_FIELDS = new Set(['version', 'updated_at', 'etag']);

function stripMetaFields(obj: Record<string, unknown>): Record<string, unknown> {
  const result: Record<string, unknown> = {};
  for (const [key, val] of Object.entries(obj)) {
    if (!META_FIELDS.has(key)) {
      result[key] = val;
    }
  }
  return result;
}

// ── 组件 ──────────────────────────────────────────────

export default function ConfigsPage() {
  const { showToast } = useToast();
  const confirm = useConfirm();

  // 数据
  const [treeData, setTreeData] = useState<TreeNode[]>([]);
  const [treeLoading, setTreeLoading] = useState(false);
  const [selectedNodeId, setSelectedNodeId] = useState<string | undefined>();
  const [selectedNodeData, setSelectedNodeData] = useState<ConfigNodeData | null>(null);
  const [editing, setEditing] = useState<EditingState>(INITIAL_EDITING);

  const textareaRef = useRef<HTMLTextAreaElement>(null);

  // ── 加载服务树 ──────────────────────────────────────

  const loadTree = useCallback(async () => {
    setTreeLoading(true);
    try {
      const [apps, instances] = await Promise.all([
        apiClient.getApps(),
        apiClient.getInstances('all'),
      ]);

      const tree = buildConfigTree(apps, instances);
      setTreeData(tree);
    } catch (e) {
      const err = e as ApiError;
      showToast(`Failed to load config tree: ${err.message}`, 'error');
    } finally {
      setTreeLoading(false);
    }
  }, [showToast]);

  useEffect(() => {
    loadTree();
  }, [loadTree]);

  // ── 构建配置树 ──────────────────────────────────────

  function buildConfigTree(apps: App[], instances: Instance[]): TreeNode[] {
    return apps.map(app => {
      // 使用 app_id 匹配（API 返回的 Instance 中只有 app_id，没有 app_name）
      const appInstances = instances.filter(inst => inst.app_id === app.id);

      // 按服务分组
      const serviceMap = new Map<string, Instance[]>();
      for (const inst of appInstances) {
        const svcName = inst.service_name || '_unknown_';
        if (!serviceMap.has(svcName)) {
          serviceMap.set(svcName, []);
        }
        serviceMap.get(svcName)!.push(inst);
      }

      const serviceNodes: TreeNode[] = Array.from(serviceMap.entries()).map(([svcName, svcInstances]) => ({
        id: `svc-${app.id}-${svcName}`,
        name: svcName === '_unknown_' ? 'Unknown Service' : svcName,
        icon: 'fas fa-sitemap',
        iconColor: 'text-purple-500',
        badge: svcInstances.length,
        badgeColor: 'bg-gray-100 text-gray-500',
        defaultExpanded: false,
        data: {
          type: 'service',
          appId: app.id,
          serviceName: svcName,
        } as ConfigNodeData,
        children: svcInstances.map(inst => ({
          id: `inst-${inst.agent_id}`,
          name: inst.hostname || inst.ip || inst.agent_id.substring(0, 8),
          icon: inst.status?.state === 'online' ? 'fas fa-circle' : 'fas fa-circle',
          iconColor: inst.status?.state === 'online' ? 'text-green-500' : 'text-gray-300',
          data: {
            type: 'instance',
            appId: app.id,
            serviceName: svcName,
            agentId: inst.agent_id,
            status: inst.status?.state,
          } as ConfigNodeData,
        })),
      }));

      return {
        id: `app-${app.id}`,
        name: app.name,
        icon: 'fas fa-cube',
        iconColor: 'text-blue-500',
        defaultExpanded: true,
        data: {
          type: 'app',
          appId: app.id,
          serviceName: '',
        } as ConfigNodeData,
        children: serviceNodes,
      } as TreeNode;
    });
  }

  // ── 选择节点 ──────────────────────────────────────

  const handleSelectNode = useCallback(async (node: TreeNode | null) => {
    if (!node) {
      setSelectedNodeId(undefined);
      setSelectedNodeData(null);
      setEditing(INITIAL_EDITING);
      return;
    }

    const nodeData = node.data as ConfigNodeData;

    // App 节点：仅设置选中状态，不加载配置（App 级别没有独立的配置 API）
    if (nodeData.type === 'app') {
      setSelectedNodeId(node.id);
      setSelectedNodeData(null);
      setEditing(INITIAL_EDITING);
      return;
    }

    // 实例节点：点击时不做配置加载（配置以 Service 为粒度）
    if (nodeData.type === 'instance') {
      return;
    }

    // 如果有未保存的更改，先确认
    if (editing.isDirty) {
      const ok = await confirm({
        title: 'Unsaved Changes',
        message: 'You have unsaved changes. Discard them?',
        confirmText: 'Discard',
        variant: 'danger',
      });
      if (!ok) return;
    }

    setSelectedNodeId(node.id);
    setSelectedNodeData(nodeData);

    // 加载配置
    await loadConfig(nodeData);
  }, [editing.isDirty, confirm]);

  // ── 加载配置 ──────────────────────────────────────

  const loadConfig = useCallback(async (nodeData: ConfigNodeData) => {
    setEditing({
      ...INITIAL_EDITING,
      appId: nodeData.appId,
      serviceName: nodeData.serviceName,
      loading: true,
    });

    try {
      const configRes = await apiClient.getAppServiceConfig(
        nodeData.appId,
        nodeData.serviceName,
      );

      // V2 API 返回 { config, reference } 或直接返回配置对象
      const responseData = (configRes as Record<string, unknown>) || {};
      const fullConfig = (responseData.config || responseData || {}) as Record<string, unknown>;
      const reference = (responseData.reference || null) as Record<string, unknown> | null;

      // 提取元数据
      const version = String(fullConfig.version || '');

      // 提取业务配置（过滤掉元数据字段）
      const businessConfig = stripMetaFields(fullConfig);
      const jsonStr = JSON.stringify(businessConfig, null, 2);

      // 计算提示
      const hints = computeHints(businessConfig, reference, version);

      setEditing(prev => ({
        ...prev,
        loading: false,
        content: jsonStr,
        originalContent: jsonStr,
        version,
        reference,
        ...hints,
      }));
    } catch (e) {
      const err = e as ApiError;
      // 404 表示没有配置，显示空对象
      if (err.status === 404) {
        const emptyJson = '{}';
        const hints = computeHints({}, null, 'none');
        setEditing(prev => ({
          ...prev,
          loading: false,
          content: emptyJson,
          originalContent: emptyJson,
          version: 'none',
          ...hints,
        }));
      } else {
        showToast(`Failed to load configuration: ${err.message}`, 'error');
        setEditing(prev => ({ ...prev, loading: false }));
      }
    }
  }, [showToast]);

  // ── 计算提示（模板推荐 / 缺失字段） ──────────────

  function computeHints(
    current: Record<string, unknown>,
    reference: Record<string, unknown> | null,
    version: string,
  ): Pick<EditingState, 'showHint' | 'hintType' | 'missingFields'> {
    if (!reference) {
      return { showHint: false, hintType: '', missingFields: [] };
    }

    // Case 1: 空配置或初始版本
    const isEmpty = Object.keys(current).length === 0 || version === '0';
    if (isEmpty) {
      return { showHint: true, hintType: 'template', missingFields: [] };
    }

    // Case 2: 检查缺失的顶层字段
    const missing: string[] = [];
    for (const key in reference) {
      if (META_FIELDS.has(key)) continue;
      if (!(key in current)) {
        missing.push(key);
      }
    }

    if (missing.length > 0) {
      return { showHint: true, hintType: 'missing', missingFields: missing };
    }

    return { showHint: false, hintType: '', missingFields: [] };
  }

  // ── JSON 实时校验 ──────────────────────────────────

  const handleContentChange = useCallback((value: string) => {
    let jsonError = '';
    if (value.trim()) {
      try {
        JSON.parse(value);
      } catch (e) {
        jsonError = (e as Error).message;
      }
    }

    setEditing(prev => ({
      ...prev,
      content: value,
      isDirty: value !== prev.originalContent,
      jsonError,
    }));
  }, []);

  // ── 保存配置 ──────────────────────────────────────

  const handleSave = useCallback(async () => {
    if (editing.saving) return;

    let configData: AgentConfig;
    try {
      configData = JSON.parse(editing.content) as AgentConfig;
    } catch {
      showToast('Invalid JSON format. Please fix the syntax errors before saving.', 'error');
      return;
    }

    setEditing(prev => ({ ...prev, saving: true }));
    try {
      await apiClient.setAppServiceConfig(editing.appId, editing.serviceName, configData);
      setEditing(prev => ({
        ...prev,
        saving: false,
        originalContent: prev.content,
        isDirty: false,
      }));
      showToast('Configuration saved successfully', 'success');
    } catch (e) {
      const err = e as ApiError;
      showToast(`Failed to save configuration: ${err.message}`, 'error');
      setEditing(prev => ({ ...prev, saving: false }));
    }
  }, [editing.saving, editing.content, editing.appId, editing.serviceName, showToast]);

  // ── 重置配置 ──────────────────────────────────────

  const handleReset = useCallback(async () => {
    const ok = await confirm({
      title: 'Reset Changes',
      message: 'Reset all changes to the original state?',
      confirmText: 'Reset',
      variant: 'default',
    });
    if (!ok) return;

    setEditing(prev => ({
      ...prev,
      content: prev.originalContent,
      isDirty: false,
      jsonError: '',
    }));
  }, [confirm]);

  // ── 删除配置 ──────────────────────────────────────

  const handleDelete = useCallback(async () => {
    const ok = await confirm({
      title: 'Delete Configuration',
      message: `Delete configuration for service "${editing.serviceName}"?\n\nThis will reset the service to use default settings.`,
      confirmText: 'Delete',
      variant: 'danger',
    });
    if (!ok) return;

    setEditing(prev => ({ ...prev, saving: true }));
    try {
      await apiClient.deleteAppServiceConfig(editing.appId, editing.serviceName);
      showToast(`Configuration for "${editing.serviceName}" deleted`, 'success');
      // 重新加载配置
      if (selectedNodeData) {
        await loadConfig(selectedNodeData);
      }
    } catch (e) {
      const err = e as ApiError;
      showToast(`Failed to delete configuration: ${err.message}`, 'error');
      setEditing(prev => ({ ...prev, saving: false }));
    }
  }, [editing.appId, editing.serviceName, selectedNodeData, confirm, showToast, loadConfig]);

  // ── 应用模板 ──────────────────────────────────────

  const handleApplyTemplate = useCallback(async () => {
    if (!editing.reference) return;

    if (editing.content !== '{}') {
      const ok = await confirm({
        title: 'Apply Template',
        message: 'This will overwrite the current configuration with the recommended template. Continue?',
        confirmText: 'Apply',
        variant: 'default',
      });
      if (!ok) return;
    }

    const template = stripMetaFields(editing.reference);
    const jsonStr = JSON.stringify(template, null, 2);

    setEditing(prev => ({
      ...prev,
      content: jsonStr,
      isDirty: jsonStr !== prev.originalContent,
      showHint: false,
      jsonError: '',
    }));
    showToast('Template applied. Remember to save changes.', 'info');
  }, [editing.reference, editing.content, confirm, showToast]);

  // ── 补全缺失字段 ──────────────────────────────────

  const handleFillMissing = useCallback(() => {
    if (!editing.reference || editing.missingFields.length === 0) return;

    try {
      const current = JSON.parse(editing.content) as Record<string, unknown>;
      for (const field of editing.missingFields) {
        if (editing.reference[field] !== undefined) {
          current[field] = editing.reference[field];
        }
      }

      const jsonStr = JSON.stringify(current, null, 2);
      setEditing(prev => ({
        ...prev,
        content: jsonStr,
        isDirty: jsonStr !== prev.originalContent,
        showHint: false,
        jsonError: '',
      }));
      showToast('Missing fields added from template.', 'success');
    } catch {
      showToast('Error parsing current JSON. Fix it before filling fields.', 'error');
    }
  }, [editing.reference, editing.missingFields, editing.content, showToast]);

  // ── 选中节点的显示名称 ──────────────────────────────

  const selectedNodeName = useMemo(() => {
    if (!selectedNodeData) return '';
    if (selectedNodeData.type === 'service') {
      return selectedNodeData.serviceName === '_unknown_' ? 'Unknown Service' : selectedNodeData.serviceName;
    }
    return '';
  }, [selectedNodeData]);

  // ── 渲染 ──────────────────────────────────────────

  return (
    <div className="flex h-[calc(100vh-120px)] gap-6">
      {/* ── 左侧：服务树 ────────────────────────────── */}
      <div className="w-1/3 bg-white rounded-xl shadow-sm border border-gray-100 flex flex-col overflow-hidden">
        <div className="p-4 border-b border-gray-100 flex items-center justify-between">
          <h3 className="font-bold text-gray-800">Service Tree</h3>
          <button
            onClick={loadTree}
            className="text-gray-400 hover:text-blue-600 transition"
            title="Refresh"
          >
            <i className={`fas fa-sync ${treeLoading ? 'fa-spin' : ''}`} />
          </button>
        </div>

        <TreeNav
          data={treeData}
          selectedId={selectedNodeId}
          onSelect={handleSelectNode}
          searchable
          placeholder="Search services/instances..."
          allowSelectParent
          emptyText={treeLoading ? 'Loading...' : 'No services found'}
        />
      </div>

      {/* ── 右侧：配置编辑器 ────────────────────────── */}
      <div className="flex-1 bg-white rounded-xl shadow-sm border border-gray-100 flex flex-col overflow-hidden relative">
        {/* Loading Overlay */}
        {editing.loading && (
          <div className="absolute inset-0 bg-white/80 z-10 flex items-center justify-center">
            <div className="text-center">
              <i className="fas fa-spinner fa-spin text-3xl text-blue-600 mb-2" />
              <p className="text-gray-500">Loading configuration...</p>
            </div>
          </div>
        )}

        {/* 编辑器头部 */}
        <div className="p-4 border-b border-gray-100 flex items-center justify-between flex-shrink-0">
          <div className="flex items-center gap-3">
            <div className="w-10 h-10 bg-gray-100 rounded-lg flex items-center justify-center">
              <i className={`fas ${selectedNodeData ? 'fa-sitemap text-purple-500' : 'fa-cog text-gray-400'}`} />
            </div>
            <div>
              <h3 className="font-bold text-gray-800">
                {selectedNodeName || 'Select a service'}
              </h3>
              <p className="text-xs text-gray-400">Service Configuration</p>
            </div>
          </div>

          {selectedNodeData && (
            <div className="flex items-center gap-2">
              <button
                onClick={handleDelete}
                className="px-3 py-2 text-red-600 hover:bg-red-50 rounded-lg transition text-sm flex items-center gap-2"
              >
                <i className="fas fa-trash-alt" /> Delete Config
              </button>
              <button
                onClick={handleReset}
                disabled={!editing.isDirty}
                className="px-3 py-2 text-gray-600 hover:bg-gray-100 rounded-lg transition text-sm flex items-center gap-2 disabled:opacity-50 disabled:cursor-not-allowed"
              >
                <i className="fas fa-undo" /> Reset
              </button>
              <button
                onClick={handleSave}
                disabled={!editing.isDirty || editing.saving || !!editing.jsonError}
                className="px-4 py-2 bg-blue-600 text-white rounded-lg hover:bg-blue-700 transition text-sm flex items-center gap-2 disabled:opacity-50 disabled:cursor-not-allowed shadow-sm"
              >
                <i className={`fas ${editing.saving ? 'fa-spinner fa-spin' : 'fa-save'}`} />
                {editing.saving ? 'Saving...' : 'Save Changes'}
              </button>
            </div>
          )}
        </div>

        {/* 编辑器主体 */}
        <div className="flex-1 flex flex-col bg-gray-50 overflow-hidden">
          {!selectedNodeData ? (
            /* 空状态 */
            <div className="flex-1 flex flex-col items-center justify-center text-gray-400 p-8 text-center">
              <i className="fas fa-cog text-6xl mb-4 opacity-20" />
              <p className="text-lg font-medium">Please select a service from the tree</p>
              <p className="text-sm mt-1">
                Configurations are applied to all instances belonging to the selected service.
              </p>
            </div>
          ) : (
            <div className="flex-1 flex flex-col overflow-hidden">
              {/* 提示条 */}
              <div className="px-4 py-2 bg-blue-50 text-blue-700 text-xs border-b border-blue-100 flex items-center gap-2 flex-shrink-0">
                <i className="fas fa-info-circle" />
                <span>Configuration is stored in JSON format. Use service name as DataId in Nacos.</span>
                {editing.isDirty && (
                  <div className="ml-auto flex items-center gap-1 font-bold animate-pulse">
                    <i className="fas fa-circle text-[6px]" /> Unsaved Changes
                  </div>
                )}
              </div>

              {/* 模板推荐 / 缺失字段提示 Banner */}
              {editing.showHint && (
                <div className={`px-4 py-3 border-b flex items-center justify-between flex-shrink-0 ${
                  editing.hintType === 'template'
                    ? 'bg-amber-50 border-amber-100 text-amber-800'
                    : 'bg-blue-50 border-blue-100 text-blue-800'
                }`}>
                  <div className="flex items-center gap-3">
                    <i className={`fas ${
                      editing.hintType === 'template' ? 'fa-magic text-amber-500' : 'fa-lightbulb text-blue-500'
                    }`} />
                    <div className="text-xs">
                      {editing.hintType === 'template' ? (
                        <span>
                          <span className="font-bold">Recommendation:</span> No configuration found. Would you like to use the recommended template?
                        </span>
                      ) : (
                        <span>
                          <span className="font-bold">Optimization:</span> Found {editing.missingFields.length} missing fields compared to latest template:{' '}
                          <code className="bg-blue-100 px-1 rounded">
                            {editing.missingFields.join(', ')}
                          </code>
                        </span>
                      )}
                    </div>
                  </div>
                  <div className="flex items-center gap-2">
                    {editing.hintType === 'template' ? (
                      <button
                        onClick={handleApplyTemplate}
                        className="px-3 py-1 bg-amber-600 text-white rounded text-xs hover:bg-amber-700 transition"
                      >
                        Apply Template
                      </button>
                    ) : (
                      <button
                        onClick={handleFillMissing}
                        className="px-3 py-1 bg-blue-600 text-white rounded text-xs hover:bg-blue-700 transition"
                      >
                        Fill Missing Fields
                      </button>
                    )}
                    <button
                      onClick={() => setEditing(prev => ({ ...prev, showHint: false }))}
                      className="p-1 hover:bg-black/5 rounded"
                    >
                      <i className="fas fa-times opacity-50" />
                    </button>
                  </div>
                </div>
              )}

              {/* JSON 语法错误提示 */}
              {editing.jsonError && (
                <div className="px-4 py-2 bg-red-50 text-red-700 text-xs border-b border-red-100 flex items-center gap-2 flex-shrink-0">
                  <i className="fas fa-exclamation-triangle" />
                  <span className="font-mono">{editing.jsonError}</span>
                </div>
              )}

              {/* JSON 编辑区域 */}
              <div className="flex-1 relative overflow-hidden">
                <textarea
                  ref={textareaRef}
                  value={editing.content}
                  onChange={e => handleContentChange(e.target.value)}
                  className={`absolute inset-0 w-full h-full p-4 font-mono text-sm bg-white border-none focus:ring-0 resize-none outline-none leading-relaxed ${
                    editing.jsonError ? 'text-red-600' : 'text-gray-800'
                  }`}
                  placeholder='{ "sampling": { "rate": 0.5 } }'
                  spellCheck={false}
                />
              </div>

              {/* 底部状态栏 */}
              <div className="px-4 py-2 bg-white border-t border-gray-100 text-[10px] text-gray-400 flex justify-between items-center flex-shrink-0">
                <div className="flex items-center gap-4">
                  <span>Target: {editing.serviceName || '-'}</span>
                </div>
                <div className="flex items-center gap-4">
                  <span>Version: {editing.version || 'none'}</span>
                  <span>Chars: {editing.content.length}</span>
                  {editing.jsonError ? (
                    <span className="text-red-400 flex items-center gap-1">
                      <i className="fas fa-times-circle" /> Invalid JSON
                    </span>
                  ) : editing.content.trim() ? (
                    <span className="text-green-400 flex items-center gap-1">
                      <i className="fas fa-check-circle" /> Valid JSON
                    </span>
                  ) : null}
                </div>
              </div>
            </div>
          )}
        </div>
      </div>
    </div>
  );
}
