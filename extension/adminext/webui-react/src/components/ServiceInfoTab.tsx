/**
 * ServiceInfoTab — 服务信息详情 Tab
 *
 * 展示服务的基本信息、元数据（tags）、生命周期时间戳。
 * 支持编辑 description 和 tags，提交后调用 API 更新。
 */

import { useState, useCallback } from 'react';
import { apiClient } from '@/api/client';
import { useToast } from '@/contexts/ToastContext';
import type { ServiceDetail, ApiError } from '@/types/api';

// ── Props ──────────────────────────────────────────────

interface ServiceInfoTabProps {
  service: ServiceDetail;
  onServiceUpdated: (updated: ServiceDetail) => void;
}

// ── 工具函数 ──────────────────────────────────────────

function formatTimestamp(ts: string | undefined): string {
  if (!ts) return '-';
  try {
    return new Date(ts).toLocaleString('zh-CN');
  } catch {
    return ts;
  }
}

function formatRelativeTime(ts: string | undefined): string {
  if (!ts) return '-';
  try {
    const diff = Date.now() - new Date(ts).getTime();
    if (diff < 0) return '-';
    if (diff < 30_000) return 'just now';
    const mins = Math.floor(diff / 60_000);
    if (mins < 60) return `${mins}m ago`;
    const hrs = Math.floor(mins / 60);
    if (hrs < 24) return `${hrs}h ago`;
    const days = Math.floor(hrs / 24);
    if (days < 30) return `${days}d ago`;
    return formatTimestamp(ts);
  } catch {
    return ts || '-';
  }
}

// ── 子组件 ──────────────────────────────────────────────

function SectionTitle({ icon, title }: { icon: string; title: string }) {
  return (
    <h4 className="text-[10px] font-bold text-gray-400 uppercase tracking-widest mb-2 flex items-center gap-1.5">
      <i className={`${icon} text-[8px]`} /> {title}
    </h4>
  );
}

function InfoCard({ label, value, mono = false }: { label: string; value: string; mono?: boolean }) {
  return (
    <div className="bg-gray-50 p-2.5 rounded-lg border border-gray-100">
      <p className="text-[9px] font-bold text-gray-400 uppercase">{label}</p>
      <p className={`text-gray-700 break-all mt-0.5 ${mono ? 'font-mono text-[10px]' : 'text-[11px] font-semibold'}`}>
        {value || '-'}
      </p>
    </div>
  );
}

// ── 主组件 ──────────────────────────────────────────────

export default function ServiceInfoTab({ service, onServiceUpdated }: ServiceInfoTabProps) {
  const { showToast } = useToast();

  // ── 编辑 Description ────────────────────────────────
  const [editingDesc, setEditingDesc] = useState(false);
  const [descDraft, setDescDraft] = useState('');
  const [savingDesc, setSavingDesc] = useState(false);

  const startEditDesc = useCallback(() => {
    setDescDraft(service.description || '');
    setEditingDesc(true);
  }, [service.description]);

  const saveDesc = useCallback(async () => {
    setSavingDesc(true);
    try {
      const updated = await apiClient.updateService(service.app_id, service.service_name, {
        description: descDraft,
      });
      onServiceUpdated(updated);
      setEditingDesc(false);
      showToast('Description updated', 'success');
    } catch (e) {
      showToast(`Failed to update: ${(e as ApiError).message}`, 'error');
    } finally {
      setSavingDesc(false);
    }
  }, [service.app_id, service.service_name, descDraft, onServiceUpdated, showToast]);

  // ── 编辑 Tags ────────────────────────────────────────
  const [editingTags, setEditingTags] = useState(false);
  const [tagsDraft, setTagsDraft] = useState('');
  const [savingTags, setSavingTags] = useState(false);

  const startEditTags = useCallback(() => {
    const tags = service.tags || {};
    setTagsDraft(
      Object.entries(tags)
        .map(([k, v]) => `${k}=${v}`)
        .join('\n')
    );
    setEditingTags(true);
  }, [service.tags]);

  const saveTags = useCallback(async () => {
    setSavingTags(true);
    try {
      const tags: Record<string, string> = {};
      for (const line of tagsDraft.split('\n')) {
        const trimmed = line.trim();
        if (!trimmed) continue;
        const eqIdx = trimmed.indexOf('=');
        if (eqIdx <= 0) {
          showToast(`Invalid tag format: "${trimmed}" (expected key=value)`, 'error');
          setSavingTags(false);
          return;
        }
        tags[trimmed.substring(0, eqIdx).trim()] = trimmed.substring(eqIdx + 1).trim();
      }
      const updated = await apiClient.updateService(service.app_id, service.service_name, { tags });
      onServiceUpdated(updated);
      setEditingTags(false);
      showToast('Tags updated', 'success');
    } catch (e) {
      showToast(`Failed to update: ${(e as ApiError).message}`, 'error');
    } finally {
      setSavingTags(false);
    }
  }, [service.app_id, service.service_name, tagsDraft, onServiceUpdated, showToast]);

  // ── Render ──────────────────────────────────────────

  const tags = service.tags || {};

  return (
    <div className="space-y-5">
      {/* 基本信息 */}
      <section>
        <SectionTitle icon="fas fa-id-card" title="Basic Information" />
        <div className="grid grid-cols-2 gap-2">
          <InfoCard label="Service ID" value={service.id} mono />
          <InfoCard label="App ID" value={service.app_id} mono />
          <InfoCard label="Service Name" value={service.service_name} />
          <InfoCard label="App Name" value={service.app_name || '-'} />
          <InfoCard label="Instances" value={`${service.online_count} online / ${service.instance_count} total`} />
          {service.has_config !== undefined && (
            <InfoCard
              label="Config"
              value={service.has_config ? `Yes (${service.config_source || 'unknown'})` : 'No'}
            />
          )}
        </div>
      </section>

      {/* 描述 */}
      <section>
        <div className="flex items-center justify-between mb-2">
          <SectionTitle icon="fas fa-align-left" title="Description" />
          {!editingDesc && (
            <button
              onClick={startEditDesc}
              className="text-[10px] text-blue-500 hover:text-blue-600 font-medium transition"
            >
              <i className="fas fa-pencil-alt mr-1 text-[8px]" />Edit
            </button>
          )}
        </div>
        {editingDesc ? (
          <div className="space-y-2">
            <textarea
              value={descDraft}
              onChange={e => setDescDraft(e.target.value)}
              rows={3}
              className="w-full px-3 py-2 border border-gray-200 rounded-lg focus:ring-2 focus:ring-blue-500/40 focus:border-blue-400 text-xs bg-gray-50/50 transition resize-none"
              placeholder="Enter service description..."
              autoFocus
            />
            <div className="flex items-center gap-2">
              <button
                onClick={saveDesc}
                disabled={savingDesc}
                className="px-3 py-1 bg-blue-600 text-white rounded-lg text-[11px] font-semibold hover:bg-blue-700 transition disabled:opacity-50"
              >
                {savingDesc ? <><i className="fas fa-spinner fa-spin mr-1" />Saving...</> : 'Save'}
              </button>
              <button
                onClick={() => setEditingDesc(false)}
                disabled={savingDesc}
                className="px-3 py-1 bg-gray-100 text-gray-600 rounded-lg text-[11px] font-medium hover:bg-gray-200 transition"
              >
                Cancel
              </button>
            </div>
          </div>
        ) : (
          <div className="bg-gray-50 border border-gray-100 rounded-lg p-3">
            <p className="text-[11px] text-gray-600 whitespace-pre-wrap">
              {service.description || <span className="text-gray-300 italic">No description</span>}
            </p>
          </div>
        )}
      </section>

      {/* 标签 */}
      <section>
        <div className="flex items-center justify-between mb-2">
          <SectionTitle icon="fas fa-tags" title="Tags" />
          {!editingTags && (
            <button
              onClick={startEditTags}
              className="text-[10px] text-blue-500 hover:text-blue-600 font-medium transition"
            >
              <i className="fas fa-pencil-alt mr-1 text-[8px]" />Edit
            </button>
          )}
        </div>
        {editingTags ? (
          <div className="space-y-2">
            <textarea
              value={tagsDraft}
              onChange={e => setTagsDraft(e.target.value)}
              rows={Math.max(3, Object.keys(tags).length + 1)}
              className="w-full px-3 py-2 border border-gray-200 rounded-lg focus:ring-2 focus:ring-blue-500/40 focus:border-blue-400 text-xs font-mono bg-gray-50/50 transition resize-none"
              placeholder="key=value (one per line)"
              autoFocus
            />
            <p className="text-[9px] text-gray-400">One tag per line, format: <code className="bg-gray-100 px-1 rounded">key=value</code></p>
            <div className="flex items-center gap-2">
              <button
                onClick={saveTags}
                disabled={savingTags}
                className="px-3 py-1 bg-blue-600 text-white rounded-lg text-[11px] font-semibold hover:bg-blue-700 transition disabled:opacity-50"
              >
                {savingTags ? <><i className="fas fa-spinner fa-spin mr-1" />Saving...</> : 'Save'}
              </button>
              <button
                onClick={() => setEditingTags(false)}
                disabled={savingTags}
                className="px-3 py-1 bg-gray-100 text-gray-600 rounded-lg text-[11px] font-medium hover:bg-gray-200 transition"
              >
                Cancel
              </button>
            </div>
          </div>
        ) : Object.keys(tags).length > 0 ? (
          <div className="grid grid-cols-2 gap-2">
            {Object.entries(tags).map(([key, val]) => (
              <div key={key} className="p-2 bg-gray-50 rounded-lg border border-gray-100 overflow-hidden">
                <div className="text-[9px] text-gray-400 font-mono truncate">{key}</div>
                <div className="text-[11px] font-medium text-gray-700 mt-0.5 truncate">{val}</div>
              </div>
            ))}
          </div>
        ) : (
          <div className="text-[11px] text-gray-400 bg-gray-50 border border-gray-100 rounded-lg p-3 text-center">
            <i className="fas fa-inbox text-gray-200 text-sm block mb-1" />
            No tags
          </div>
        )}
      </section>

      {/* 生命周期 */}
      <section>
        <SectionTitle icon="fas fa-clock" title="Lifecycle" />
        <div className="grid grid-cols-2 gap-2">
          <div className="bg-gray-50 p-2.5 rounded-lg border border-gray-100">
            <p className="text-[9px] font-bold text-gray-400 uppercase mb-0.5">Created At</p>
            <p className="text-[11px] text-gray-700">{formatTimestamp(service.created_at)}</p>
          </div>
          <div className="bg-gray-50 p-2.5 rounded-lg border border-gray-100">
            <p className="text-[9px] font-bold text-gray-400 uppercase mb-0.5">Updated At</p>
            <p className="text-[11px] text-gray-700">{formatTimestamp(service.updated_at)}</p>
          </div>
          <div className="bg-gray-50 p-2.5 rounded-lg border border-gray-100 col-span-2">
            <p className="text-[9px] font-bold text-gray-400 uppercase mb-0.5">Last Seen</p>
            <p className="text-[11px] text-gray-700">
              {formatTimestamp(service.last_seen_at)}
              {service.last_seen_at && (
                <span className="text-[10px] text-green-600 ml-2 font-medium">
                  {formatRelativeTime(service.last_seen_at)}
                </span>
              )}
            </p>
          </div>
        </div>
      </section>
    </div>
  );
}
