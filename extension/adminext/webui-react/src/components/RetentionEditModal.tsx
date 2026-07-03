/**
 * RetentionEditModal — 单个信号的 retention 设置弹窗
 *
 * 独立于详情页面，高内聚低耦合：
 *   - 接收 onSave 回调，不关心父组件如何刷新
 *   - 预设列表和自定义输入完全自包含
 */

import { useState } from 'react';

const PRESETS = [
  { label: '7d', days: 7 },
  { label: '14d', days: 14 },
  { label: '30d', days: 30 },
  { label: '60d', days: 60 },
  { label: '90d', days: 90 },
];

const SIGNAL_ICONS: Record<string, string> = {
  trace: 'fa-bolt',
  metric: 'fa-chart-line',
  log: 'fa-file-lines',
};
const SIGNAL_COLORS: Record<string, string> = {
  trace: 'text-purple-600',
  metric: 'text-emerald-600',
  log: 'text-blue-600',
};

interface Props {
  open: boolean;
  signal: string;
  currentDays: number | null;
  onSave: (days: number) => Promise<void>;
  onClose: () => void;
}

export default function RetentionEditModal({ open, signal, currentDays, onSave, onClose }: Props) {
  const [selected, setSelected] = useState<number | null>(currentDays);
  const [customOpen, setCustomOpen] = useState(false);
  const [customVal, setCustomVal] = useState('');
  const [saving, setSaving] = useState(false);

  if (!open) return null;

  const isPresetMatch = (d: number) => selected === d && !customOpen;

  const handlePreset = (days: number) => {
    setSelected(days);
    setCustomOpen(false);
  };

  const handleConfirm = async () => {
    const days = customOpen ? parseInt(customVal, 10) : selected;
    if (!days || days <= 0) return;
    setSaving(true);
    try { await onSave(days); onClose(); }
    catch { /* error handled by parent via toast */ }
    finally { setSaving(false); }
  };

  // Reset state when opened
  const handleCancel = () => { setSelected(currentDays); setCustomOpen(false); onClose(); };

  return (
    <div className="fixed inset-0 z-50 flex items-center justify-center">
      <div className="fixed inset-0 bg-black/30 backdrop-blur-sm" onClick={handleCancel} />
      <div className="relative bg-white dark:bg-slate-900 rounded-xl shadow-2xl w-full max-w-sm p-6 z-10">
        {/* Header */}
        <div className="flex items-center justify-between mb-4">
          <h3 className="text-base font-bold text-slate-800 dark:text-slate-100 flex items-center gap-2">
            <i className={`fas ${SIGNAL_ICONS[signal]} ${SIGNAL_COLORS[signal]}`} />
            设置 {signalLabel(signal)} Retention
          </h3>
          <button onClick={handleCancel} className="text-slate-400 hover:text-slate-600 dark:hover:text-slate-300">
            <i className="fas fa-times" />
          </button>
        </div>

        {/* Preset chips */}
        <div className="flex flex-wrap gap-2 mb-3">
          {PRESETS.map(p => (
            <button key={p.days} type="button"
              onClick={() => handlePreset(p.days)}
              className={`px-3 py-1.5 text-sm rounded-lg border transition-colors cursor-pointer ${
                isPresetMatch(p.days)
                  ? 'bg-sky-600 text-white border-sky-600'
                  : 'border-slate-200 dark:border-slate-600 text-slate-700 dark:text-slate-300 hover:border-sky-400'
              }`}>
              {p.label}
            </button>
          ))}
          <button type="button"
            onClick={() => { setCustomOpen(true); setSelected(null); }}
            className={`px-3 py-1.5 text-sm rounded-lg border transition-colors cursor-pointer ${
              customOpen
                ? 'bg-sky-600 text-white border-sky-600'
                : 'border-slate-200 dark:border-slate-600 text-slate-500 dark:text-slate-400 hover:border-slate-400'
            }`}>
            自定义
          </button>
        </div>

        {/* Custom input */}
        {customOpen && (
          <div className="flex items-center gap-2 mb-3">
            <input type="number" placeholder="30" autoFocus
              value={customVal}
              onChange={e => setCustomVal(e.target.value)}
              className="w-20 px-3 py-1.5 text-sm border border-slate-300 dark:border-slate-600 rounded-md bg-white dark:bg-slate-800 text-slate-800 dark:text-slate-200" />
            <span className="text-sm text-slate-500 dark:text-slate-400">天</span>
          </div>
        )}

        {/* Actions */}
        <div className="flex gap-2 justify-end">
          <button onClick={handleCancel}
            className="px-4 py-1.5 text-sm border border-slate-300 dark:border-slate-600 rounded-lg text-slate-600 dark:text-slate-300 hover:bg-slate-50 dark:hover:bg-slate-800 cursor-pointer">
            取消
          </button>
          <button onClick={handleConfirm} disabled={saving}
            className="px-4 py-1.5 text-sm bg-sky-600 text-white rounded-lg hover:bg-sky-700 cursor-pointer disabled:opacity-50">
            {saving ? '保存中...' : '确定'}
          </button>
        </div>
      </div>
    </div>
  );
}

function signalLabel(s: string): string { return s.charAt(0).toUpperCase() + s.slice(1); }
