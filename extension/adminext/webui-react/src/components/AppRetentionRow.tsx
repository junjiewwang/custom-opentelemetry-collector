/**
 * AppRetentionRow — 单个信号的 retention 只读行
 *
 * 职责：展示当前值，点击打开 RetentionEditModal 进行设置。
 * 高内聚低耦合：编辑逻辑完全在 Modal 中，本组件只有展示 + 回调。
 */

import { useState } from 'react';
import { apiClient } from '@/api/client';
import RetentionEditModal from '@/components/RetentionEditModal';
import type { SignalRetention } from '@/types/storage';

const SIGNAL_ICONS: Record<string, string> = {
  trace: 'fa-bolt',
  metric: 'fa-chart-line',
  log: 'fa-file-lines',
};
const SIGNAL_COLORS: Record<string, string> = {
  trace: 'text-purple-600 dark:text-purple-400',
  metric: 'text-emerald-600 dark:text-emerald-400',
  log: 'text-blue-600 dark:text-blue-400',
};

interface Props {
  appId: string;
  signal: string;
  data: SignalRetention | null;
  onChange: () => void;
}

export default function AppRetentionRow({ appId, signal, data, onChange }: Props) {
  const [modalOpen, setModalOpen] = useState(false);

  const isFromApp = data?.source === 'app';
  const currentDays = parseDays(data?.value ?? '');

  const handleSave = async (days: number): Promise<void> => {
    await apiClient.setAppRetention(appId, signal, `${days * 24}h`);
    onChange();
  };

  const handleReset = async () => {
    await apiClient.deleteAppRetention(appId, signal);
    onChange();
  };

  const PRESETS = [7, 14, 30, 60, 90];
  const isPreset = isFromApp && currentDays != null && PRESETS.includes(currentDays);

  return (
    <>
      <div
        onClick={() => setModalOpen(true)}
        className={`flex items-center gap-2 px-3 py-2.5 rounded-lg border cursor-pointer transition-colors hover:bg-slate-50 dark:hover:bg-slate-800 ${
          isFromApp ? 'border-sky-200 dark:border-sky-800 bg-sky-50/30 dark:bg-sky-900/10' : 'border-slate-200 dark:border-slate-700'
        }`}
      >
        <i className={`fas ${SIGNAL_ICONS[signal]} ${SIGNAL_COLORS[signal]} text-sm`} />
        <span className="text-sm font-medium text-slate-700 dark:text-slate-300 capitalize">{signal}</span>
        <span className="flex-1 text-right text-sm">
          {isFromApp && currentDays != null ? (
            <b className="text-slate-800 dark:text-slate-100 mr-1">{currentDays}d</b>
          ) : (
            <span className="text-slate-400 dark:text-slate-500 mr-1">{parseDays(data?.platform_default ?? '')}d</span>
          )}
        </span>
        {/* Status tag */}
        {isFromApp && !isPreset && (
          <span className="text-[10px] px-1.5 py-0.5 bg-purple-100 text-purple-600 dark:bg-purple-900/30 dark:text-purple-300 rounded">自定义</span>
        )}
        {!isFromApp && (
          <span className="text-[10px] text-slate-400 dark:text-slate-500">默认</span>
        )}
        {/* Reset (stop propagation so it doesn't open modal) */}
        {isFromApp && (
          <button onClick={e => { e.stopPropagation(); handleReset(); }}
            className="text-[10px] text-slate-300 hover:text-red-400 dark:text-slate-600 dark:hover:text-red-400 cursor-pointer ml-1">
            <i className="fas fa-undo" />
          </button>
        )}
      </div>

      <RetentionEditModal
        open={modalOpen}
        signal={signal}
        currentDays={currentDays}
        onSave={handleSave}
        onClose={() => setModalOpen(false)}
      />
    </>
  );
}

function parseDays(dur: string): number | null {
  const m = dur.match(/^(\d+)h/);
  if (!m?.[1]) return null;
  return Math.floor(parseInt(m[1], 10) / 24);
}
