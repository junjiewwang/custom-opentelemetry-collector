/**
 * ServiceConfigTab — 服务配置编辑 Tab
 *
 * 服务配置编辑核心逻辑，作为 ServicesPage 的第三个 Tab 使用。
 * （原 ConfigsPage 已废弃，/configs 路由重定向到 /services）
 *
 * 功能：
 *   - JSON 配置编辑器（textarea + 实时语法校验）
 *   - 模板推荐（空配置时推荐模板）
 *   - 缺失字段检测补全
 *   - Save / Reset / Delete 操作
 *   - 脏状态检测 + 版本/字符数/校验状态
 *
 * Props: appId + serviceName → 自动加载对应配置
 */

import { useState, useCallback, useEffect, useRef } from 'react';
import { apiClient } from '@/api/client';
import { useToast } from '@/contexts/ToastContext';
import { useConfirm } from '@/components/ConfirmDialog';
import type { AgentConfig, ApiError } from '@/types/api';

// ── 类型定义 ──────────────────────────────────────────

interface ServiceConfigTabProps {
  appId: string;
  serviceName: string;
}

/** 编辑状态 */
interface EditingState {
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

// ── 计算提示（模板推荐 / 缺失字段） ──────────────────

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

// ── 组件 ──────────────────────────────────────────────

export default function ServiceConfigTab({ appId, serviceName }: ServiceConfigTabProps) {
  const { showToast } = useToast();
  const confirm = useConfirm();

  const [editing, setEditing] = useState<EditingState>(INITIAL_EDITING);
  const textareaRef = useRef<HTMLTextAreaElement>(null);

  // ── 加载配置 ──────────────────────────────────────

  const loadConfig = useCallback(async (targetAppId: string, targetServiceName: string) => {
    setEditing({
      ...INITIAL_EDITING,
      loading: true,
    });

    try {
      const configRes = await apiClient.getAppServiceConfig(targetAppId, targetServiceName);

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

  // Props 变化时自动加载配置
  useEffect(() => {
    if (appId && serviceName) {
      loadConfig(appId, serviceName);
    }
  }, [appId, serviceName, loadConfig]);

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
      await apiClient.setAppServiceConfig(appId, serviceName, configData);
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
  }, [editing.saving, editing.content, appId, serviceName, showToast]);

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
      message: `Delete configuration for service "${serviceName}"?\n\nThis will reset the service to use default settings.`,
      confirmText: 'Delete',
      variant: 'danger',
    });
    if (!ok) return;

    setEditing(prev => ({ ...prev, saving: true }));
    try {
      await apiClient.deleteAppServiceConfig(appId, serviceName);
      showToast(`Configuration for "${serviceName}" deleted`, 'success');
      // 重新加载配置
      await loadConfig(appId, serviceName);
    } catch (e) {
      const err = e as ApiError;
      showToast(`Failed to delete configuration: ${err.message}`, 'error');
      setEditing(prev => ({ ...prev, saving: false }));
    }
  }, [appId, serviceName, confirm, showToast, loadConfig]);

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

  // ── 渲染 ──────────────────────────────────────────

  return (
    <div className="flex flex-col h-full bg-white">
      {/* Loading Overlay */}
      {editing.loading && (
        <div className="absolute inset-0 bg-white/80 z-10 flex items-center justify-center rounded-xl">
          <div className="text-center">
            <i className="fas fa-spinner fa-spin text-2xl text-blue-600 mb-2" />
            <p className="text-xs text-gray-500">Loading configuration...</p>
          </div>
        </div>
      )}

      {/* 模板推荐 / 缺失字段提示 Banner（可关闭，仅在需要时出现） */}
      {editing.showHint && (
        <div className={`flex-shrink-0 px-4 py-2 border-b flex items-center justify-between ${
          editing.hintType === 'template'
            ? 'bg-amber-50/80 border-amber-100 text-amber-700'
            : 'bg-blue-50/80 border-blue-100 text-blue-700'
        }`}>
          <div className="flex items-center gap-2 min-w-0">
            <i className={`fas ${
              editing.hintType === 'template' ? 'fa-magic text-amber-400' : 'fa-lightbulb text-blue-400'
            } text-xs flex-shrink-0`} />
            <span className="text-[10px] truncate">
              {editing.hintType === 'template' ? (
                <><span className="font-bold">Recommendation:</span> No configuration found. Use the recommended template?</>
              ) : (
                <><span className="font-bold">Optimization:</span> {editing.missingFields.length} missing fields: <code className="bg-blue-100/60 px-1 rounded text-[9px]">{editing.missingFields.join(', ')}</code></>
              )}
            </span>
          </div>
          <div className="flex items-center gap-1.5 flex-shrink-0 ml-2">
            {editing.hintType === 'template' ? (
              <button onClick={handleApplyTemplate} className="px-2.5 py-1 bg-amber-600 text-white rounded text-[10px] hover:bg-amber-700 transition whitespace-nowrap">
                Apply Template
              </button>
            ) : (
              <button onClick={handleFillMissing} className="px-2.5 py-1 bg-blue-600 text-white rounded text-[10px] hover:bg-blue-700 transition whitespace-nowrap">
                Fill Missing
              </button>
            )}
            <button onClick={() => setEditing(prev => ({ ...prev, showHint: false }))} className="p-0.5 hover:bg-black/5 rounded">
              <i className="fas fa-times text-[9px] opacity-40" />
            </button>
          </div>
        </div>
      )}

      {/* JSON 编辑区域 — 最大化垂直空间 */}
      <div className="flex-1 relative overflow-hidden">
        <textarea
          ref={textareaRef}
          value={editing.content}
          onChange={e => handleContentChange(e.target.value)}
          className={`absolute inset-0 w-full h-full px-4 py-3 font-mono text-xs bg-white border-none focus:ring-0 resize-none outline-none leading-relaxed ${
            editing.jsonError ? 'text-red-600' : 'text-gray-800'
          }`}
          placeholder={'// JSON configuration for this service\n// Example:\n{\n  "sampler": { "type": 1, "ratio": 0.5 },\n  "batch": { "max_export_batch_size": 512 }\n}'}
          spellCheck={false}
        />
      </div>

      {/* 底部统一工具栏：状态信息 + 操作按钮 */}
      <div className="flex-shrink-0 px-3 py-1.5 bg-gray-50/80 border-t border-gray-100 flex items-center justify-between gap-2">
        {/* 左侧：状态信息 */}
        <div className="flex items-center gap-2.5 text-[9px] text-gray-400 min-w-0">
          {editing.jsonError ? (
            <span className="text-red-400 flex items-center gap-1 font-medium" title={editing.jsonError}>
              <i className="fas fa-times-circle text-[8px]" /> JSON Error
            </span>
          ) : editing.content.trim() ? (
            <span className="text-green-500 flex items-center gap-1">
              <i className="fas fa-check-circle text-[8px]" /> Valid
            </span>
          ) : null}
          {editing.isDirty && (
            <span className="text-amber-500 flex items-center gap-1 font-semibold">
              <i className="fas fa-circle text-[4px]" /> Unsaved
            </span>
          )}
          <span className="text-gray-300">|</span>
          <span>v{editing.version || 'none'}</span>
          <span className="text-gray-300">·</span>
          <span>{editing.content.length} chars</span>
        </div>

        {/* 右侧：操作按钮 */}
        <div className="flex items-center gap-1 flex-shrink-0">
          <button
            onClick={handleDelete}
            className="px-2 py-1 text-gray-400 hover:text-red-500 hover:bg-red-50 rounded transition text-[10px] flex items-center gap-1"
            title="Delete configuration"
          >
            <i className="fas fa-trash-alt text-[8px]" />
          </button>
          <button
            onClick={handleReset}
            disabled={!editing.isDirty}
            className="px-2 py-1 text-gray-400 hover:text-gray-600 hover:bg-gray-100 rounded transition text-[10px] flex items-center gap-1 disabled:opacity-30 disabled:cursor-not-allowed"
            title="Reset changes"
          >
            <i className="fas fa-undo text-[8px]" />
          </button>
          <button
            onClick={handleSave}
            disabled={!editing.isDirty || editing.saving || !!editing.jsonError}
            className="px-2.5 py-1 bg-blue-600 text-white rounded hover:bg-blue-700 transition text-[10px] flex items-center gap-1.5 disabled:opacity-30 disabled:cursor-not-allowed shadow-sm"
          >
            <i className={`fas ${editing.saving ? 'fa-spinner fa-spin' : 'fa-save'} text-[8px]`} />
            {editing.saving ? 'Saving...' : 'Save'}
          </button>
        </div>
      </div>
    </div>
  );
}
