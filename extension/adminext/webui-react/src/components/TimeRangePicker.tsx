/**
 * TimeRangePicker 组件 - 弹出式时间范围选择器
 *
 * 模仿 Kibana/Trace 风格：
 * - 触发按钮显示当前选中的范围
 * - 点击后弹出面板，内含 12 个快捷预设 + 2 个 datetime-local 输入 + 确定按钮
 * - 选定后通过 onChange 回调通知父组件
 */

import { useState, useRef, useEffect } from 'react';

const PRESETS: { label: string; value: string }[] = [
  { label: '近 5 分钟', value: '5m' },
  { label: '近 15 分钟', value: '15m' },
  { label: '近 30 分钟', value: '30m' },
  { label: '近 1 小时', value: '1h' },
  { label: '近 3 小时', value: '3h' },
  { label: '近 6 小时', value: '6h' },
  { label: '近 12 小时', value: '12h' },
  { label: '近 24 小时', value: '24h' },
  { label: '近 2 天', value: '2d' },
  { label: '近 7 天', value: '7d' },
  { label: '近 15 天', value: '15d' },
  { label: '近 30 天', value: '30d' },
];

const PRESET_MS: Record<string, number> = {
  '5m': 5 * 60 * 1000,
  '15m': 15 * 60 * 1000,
  '30m': 30 * 60 * 1000,
  '1h': 60 * 60 * 1000,
  '3h': 3 * 60 * 60 * 1000,
  '6h': 6 * 60 * 60 * 1000,
  '12h': 12 * 60 * 60 * 1000,
  '24h': 24 * 60 * 60 * 1000,
  '2d': 2 * 24 * 60 * 60 * 1000,
  '7d': 7 * 24 * 60 * 60 * 1000,
  '15d': 15 * 24 * 60 * 60 * 1000,
  '30d': 30 * 24 * 60 * 60 * 1000,
};

function formatDateTime(d: Date): string {
  // datetime-local expects LOCAL time (no timezone marker).
  // Use toISOString-aligned LOCAL components to avoid UTC drift.
  const pad = (n: number) => String(n).padStart(2, '0');
  return (
    d.getFullYear() +
    '-' + pad(d.getMonth() + 1) +
    '-' + pad(d.getDate()) +
    'T' + pad(d.getHours()) +
    ':' + pad(d.getMinutes())
  );
}

function applyPreset(value: string): { start: string; end: string } {
  const end = new Date();
  const start = new Date(end.getTime() - (PRESET_MS[value] ?? 60 * 60 * 1000));
  return { start: formatDateTime(start), end: formatDateTime(end) };
}

function formatRangeLabel(start: string, end: string): string {
  // If matches a known preset, show preset name; otherwise show date range
  for (const p of PRESETS) {
    const r = applyPreset(p.value);
    if (r.start === start && r.end === end) return p.label;
  }
  return `${start} → ${end}`;
}

interface TimeRangePickerProps {
  start: string;
  end: string;
  onChange: (start: string, end: string) => void;
}

export default function TimeRangePicker({ start, end, onChange }: TimeRangePickerProps) {
  const [open, setOpen] = useState(false);
  const [draftStart, setDraftStart] = useState(start);
  const [draftEnd, setDraftEnd] = useState(end);
  const [activePreset, setActivePreset] = useState<string | null>(null);
  const ref = useRef<HTMLDivElement>(null);

  useEffect(() => {
    if (open) {
      setDraftStart(start);
      setDraftEnd(end);
      setActivePreset(null);
    }
  }, [open, start, end]);

  useEffect(() => {
    const onClick = (e: MouseEvent) => {
      if (ref.current && !ref.current.contains(e.target as Node)) setOpen(false);
    };
    if (open) document.addEventListener('mousedown', onClick);
    return () => document.removeEventListener('mousedown', onClick);
  }, [open]);

  const applyPresetLocal = (value: string) => {
    setActivePreset(value);
    const r = applyPreset(value);
    setDraftStart(r.start);
    setDraftEnd(r.end);
  };

  const confirm = () => {
    onChange(draftStart, draftEnd);
    setOpen(false);
  };

  return (
    <div className="relative" ref={ref}>
      <button
        type="button"
        onClick={() => setOpen(o => !o)}
        className="w-full px-3 py-2 border border-gray-300 rounded-lg text-sm text-left bg-white hover:border-primary-400 focus:ring-2 focus:ring-primary-500 focus:border-primary-500 flex items-center justify-between gap-1"
      >
        <span>{formatRangeLabel(start, end)}</span>
        <i className="fas fa-chevron-down text-gray-400 text-xs" />
      </button>

      {open && (
        <div className="absolute z-20 mt-1 right-0 bg-white rounded-lg shadow-lg border border-gray-200 p-4 w-[420px]">
          <div className="text-sm font-medium text-gray-700 mb-3">时间选择</div>

          {/* Preset grid: 4 columns */}
          <div className="grid grid-cols-4 gap-2 mb-3">
            {PRESETS.map(p => (
              <button
                key={p.value}
                type="button"
                onClick={() => applyPresetLocal(p.value)}
                className={`px-2 py-1.5 text-xs border rounded transition-colors ${
                  activePreset === p.value
                    ? 'bg-primary-600 text-white border-primary-600'
                    : 'border-gray-200 text-gray-700 hover:border-primary-400 hover:bg-primary-50'
                }`}
              >
                {p.label}
              </button>
            ))}
          </div>

          {/* Custom range inputs + confirm */}
          <div className="flex gap-2 items-center mb-2">
            <input
              type="datetime-local"
              value={draftStart}
              onChange={e => { setDraftStart(e.target.value); setActivePreset(null); }}
              className="flex-1 px-2 py-1.5 border border-gray-200 rounded text-sm"
            />
            <span className="text-gray-400">—</span>
            <input
              type="datetime-local"
              value={draftEnd}
              onChange={e => { setDraftEnd(e.target.value); setActivePreset(null); }}
              className="flex-1 px-2 py-1.5 border border-gray-200 rounded text-sm"
            />
          </div>
          <button
            type="button"
            onClick={confirm}
            className="w-full px-3 py-1.5 bg-primary-600 text-white text-sm rounded hover:bg-primary-700 transition-colors"
          >
            确定
          </button>
        </div>
      )}
    </div>
  );
}
