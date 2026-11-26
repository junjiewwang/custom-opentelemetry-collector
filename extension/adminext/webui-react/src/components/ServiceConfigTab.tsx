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
import { Badge } from '@/components/ui/badge';
import { Button } from '@/components/ui/button';
import { cn } from '@/lib/utils';
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
    <div className="relative flex h-full flex-col bg-surface">
      {/* Loading Overlay */}
      {editing.loading && (
        <div className="absolute inset-0 z-10 flex items-center justify-center rounded-ui bg-surface/85">
          <div className="text-center">
            <i className="fas fa-spinner fa-spin mb-2 text-2xl text-primary-600" />
            <p className="text-xs text-ink-muted">Loading configuration...</p>
          </div>
        </div>
      )}

      {/* 模板推荐 / 缺失字段提示：统一控件与语义色 */}
      {editing.showHint && (
        <div className={cn(
          'flex shrink-0 items-center justify-between gap-2 border-b px-4 py-2',
          editing.hintType === 'template'
            ? 'border-warning-100 bg-warning-50 text-warning-700'
            : 'border-primary-100 bg-primary-50 text-primary-700',
        )}>
          <div className="flex min-w-0 items-center gap-2">
            <i className={cn('fas flex-shrink-0 text-xs',
              editing.hintType === 'template' ? 'fa-magic text-warning-500' : 'fa-lightbulb text-primary-500')} />
            <span className="truncate text-xs">
              {editing.hintType === 'template' ? (
                <><span className="font-semibold">Recommendation:</span> No configuration found. Use the recommended template?</>
              ) : (
                <><span className="font-semibold">Optimization:</span> {editing.missingFields.length} missing fields: <code className="rounded bg-primary-100 px-1 font-mono text-[10px]">{editing.missingFields.join(', ')}</code></>
              )}
            </span>
          </div>
          <div className="ml-2 flex shrink-0 items-center gap-1.5">
            <Button
              size="sm"
              className="h-7 px-2.5 text-[11px]"
              onClick={editing.hintType === 'template' ? handleApplyTemplate : handleFillMissing}
            >
              {editing.hintType === 'template' ? 'Apply Template' : 'Fill Missing'}
            </Button>
            <Button
              variant="ghost"
              size="icon"
              className="h-7 w-7"
              aria-label="Dismiss hint"
              onClick={() => setEditing(prev => ({ ...prev, showHint: false }))}
            >
              <span aria-hidden="true" className="text-base leading-none">×</span>
            </Button>
          </div>
        </div>
      )}

      {/* JSON 编辑区域 — 保持绝对定位以占满可用高度 */}
      <div className="relative flex-1 overflow-hidden">
        <textarea
          ref={textareaRef}
          value={editing.content}
          onChange={e => handleContentChange(e.target.value)}
          className={cn(
            'absolute inset-0 h-full w-full resize-none border-none bg-surface px-4 py-3 font-mono text-xs leading-relaxed outline-none focus:ring-0',
            editing.jsonError ? 'text-danger-600' : 'text-ink',
          )}
          placeholder={'// JSON configuration for this service\n// Example:\n{\n  "sampler": { "type": 1, "ratio": 0.5 },\n  "batch": { "max_export_batch_size": 512 }\n}'}
          spellCheck={false}
        />
      </div>

      {/* 底部工具栏：状态徽标 + 统一按钮 */}
      <div className="flex shrink-0 items-center justify-between gap-2 border-t border-line bg-canvas px-3 py-2">
        <div className="flex min-w-0 items-center gap-2 text-[11px] text-ink-muted">
          {editing.jsonError ? (
            <Badge tone="red"><span title={editing.jsonError}>JSON Error</span></Badge>
          ) : editing.content.trim() ? (
            <Badge tone="green">Valid</Badge>
          ) : null}
          {editing.isDirty && <Badge tone="amber">Unsaved</Badge>}
          <span className="font-mono text-ink-faint">v{editing.version || 'none'}</span>
          <span className="font-mono text-ink-faint">{editing.content.length} chars</span>
        </div>

        <div className="flex shrink-0 items-center gap-1.5">
          <Button
            variant="ghost"
            size="sm"
            className="h-7 px-2 text-[11px] text-danger-600 hover:bg-danger-50 hover:text-danger-700"
            title="Delete configuration"
            onClick={handleDelete}
          >
            <i className="fas fa-trash-alt text-[10px]" /> Delete
          </Button>
          <Button
            variant="outline"
            size="sm"
            className="h-7 px-2 text-[11px]"
            disabled={!editing.isDirty}
            title="Reset changes"
            onClick={handleReset}
          >
            <i className="fas fa-undo text-[10px]" /> Reset
          </Button>
          <Button
            size="sm"
            className="h-7 px-2.5 text-[11px]"
            disabled={!editing.isDirty || editing.saving || !!editing.jsonError}
            onClick={handleSave}
          >
            <i className={`fas ${editing.saving ? 'fa-spinner fa-spin' : 'fa-save'} text-[10px]`} />
            {editing.saving ? 'Saving...' : 'Save'}
          </Button>
        </div>
      </div>
    </div>
  );
}
