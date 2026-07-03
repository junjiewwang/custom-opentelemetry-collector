/**
 * AppDetailDrawer — 右侧滑动抽屉，展示 App Retention + Token 管理
 *
 * 功能：
 * - 遮罩层点击关闭 / ✕ 按钮关闭 / ESC 关闭
 * - Retention Tab：三行 signal retention 配置
 * - Token 管理（底部）
 * - 300ms ease-out 滑动动画
 */

import { useState, useEffect, useCallback } from 'react';
import { apiClient } from '@/api/client';
import { useToast } from '@/contexts/ToastContext';
import AppRetentionRow from '@/components/AppRetentionRow';
import type { App } from '@/types/api';
import type { AppRetentionResponse } from '@/types/storage';

interface Props {
  app: App | null;
  open: boolean;
  onClose: () => void;
  onDelete?: (appId: string) => void;
}

export default function AppDetailDrawer({ app, open, onClose, onDelete }: Props) {
  const { showToast } = useToast();
  const [retention, setRetention] = useState<AppRetentionResponse | null>(null);
  const [loading, setLoading] = useState(false);
  const [showToken, setShowToken] = useState(false);

  const loadRetention = useCallback(async () => {
    if (!app) return;
    setLoading(true);
    try {
      const r = await apiClient.getAppRetention(app.id);
      setRetention(r);
    } catch { setRetention(null); }
    finally { setLoading(false); }
  }, [app]);

  useEffect(() => {
    if (open && app) loadRetention();
  }, [open, app, loadRetention]);

  useEffect(() => {
    const onKey = (e: KeyboardEvent) => { if (e.key === 'Escape') onClose(); };
    if (open) document.addEventListener('keydown', onKey);
    return () => document.removeEventListener('keydown', onKey);
  }, [open, onClose]);

  if (!open || !app) return null;

  const signals = ['trace', 'metric', 'log'];

  return (
    <>
      {/* Backdrop */}
      <div className="fixed inset-0 z-30 bg-black/30 backdrop-blur-sm transition-opacity" onClick={onClose} />

      {/* Drawer */}
      <div className="fixed top-0 right-0 z-40 h-full w-full max-w-md bg-white dark:bg-slate-900 shadow-2xl transform transition-transform duration-300 ease-out overflow-y-auto">
        {/* Header */}
        <div className="sticky top-0 z-10 flex items-center justify-between px-5 py-4 border-b border-slate-200 dark:border-slate-700 bg-white dark:bg-slate-900">
          <div>
            <h2 className="text-lg font-bold text-slate-900 dark:text-slate-100">{app.name || app.id}</h2>
            <p className="text-xs text-slate-500 dark:text-slate-400 font-mono">{app.id}</p>
          </div>
          <button onClick={onClose} className="w-8 h-8 flex items-center justify-center rounded-lg hover:bg-slate-100 dark:hover:bg-slate-800 cursor-pointer text-slate-400 hover:text-slate-600 dark:hover:text-slate-300">
            <i className="fas fa-times" />
          </button>
        </div>

        <div className="px-5 py-4 space-y-5">
          {/* Retention Section */}
          <div>
            <h3 className="text-sm font-semibold text-slate-700 dark:text-slate-300 mb-3">Retention Policy</h3>
            {loading ? (
              <div className="space-y-3">
                {signals.map(s => (<div key={s} className="h-20 bg-slate-100 dark:bg-slate-800 rounded-lg animate-pulse" />))}
              </div>
            ) : (
              <div className="space-y-3">
                {signals.map(signal => (
                  <AppRetentionRow
                    key={signal}
                    appId={app.id}
                    signal={signal}
                    data={retention?.[signal] ?? null}
                    onChange={loadRetention}
                  />
                ))}
              </div>
            )}
          </div>

          {/* Divider */}
          <hr className="border-slate-200 dark:border-slate-700" />

          {/* Token Section */}
          <div>
            <h3 className="text-sm font-semibold text-slate-700 dark:text-slate-300 mb-3">Token</h3>
            <div className="flex items-center gap-2">
              <code className={`flex-1 px-3 py-2 text-xs rounded-md border border-slate-200 dark:border-slate-700 bg-slate-50 dark:bg-slate-800 text-slate-600 dark:text-slate-300 ${showToken ? '' : 'blur-sm select-none'}`}>
                {app.token}
              </code>
              <button
                onClick={() => {
                  navigator.clipboard.writeText(app.token);
                  showToast('Token copied', 'success');
                }}
                className="w-8 h-8 flex items-center justify-center rounded-lg border border-slate-200 dark:border-slate-700 text-slate-400 hover:text-sky-600 dark:hover:text-sky-400 cursor-pointer text-sm"
                title="Copy Token"
              >
                <i className="far fa-copy" />
              </button>
              <button
                onClick={() => setShowToken(s => !s)}
                className="w-8 h-8 flex items-center justify-center rounded-lg border border-slate-200 dark:border-slate-700 text-slate-400 hover:text-slate-600 dark:hover:text-slate-300 cursor-pointer text-sm"
                title={showToken ? 'Hide' : 'Show'}
              >
                <i className={`fas ${showToken ? 'fa-eye-slash' : 'fa-eye'}`} />
              </button>
            </div>
            <p className="text-xs text-slate-400 mt-1">用于 Agent 连接认证，请妥善保管</p>
          </div>

          {/* Delete */}
          {onDelete && (
            <button
              onClick={() => { onDelete(app.id); onClose(); }}
              className="w-full py-2 text-sm text-red-600 dark:text-red-400 border border-red-200 dark:border-red-800 rounded-lg hover:bg-red-50 dark:hover:bg-red-900/20 cursor-pointer"
            >
              删除应用
            </button>
          )}
        </div>
      </div>
    </>
  );
}
