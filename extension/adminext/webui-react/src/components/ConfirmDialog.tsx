/**
 * ConfirmDialog - 确认弹窗组件 + Context + useConfirm Hook
 *
 * 替代 window.confirm()，统一确认弹窗样式，支持自定义标题、消息和按钮文本。
 * 采用 Promise 模式，调用者可以 await 结果。
 *
 * @example
 * const confirm = useConfirm();
 *
 * const ok = await confirm({
 *   title: 'Delete Application',
 *   message: 'Are you sure? This cannot be undone.',
 *   confirmText: 'Delete',
 *   variant: 'danger',
 * });
 * if (ok) { ... }
 */

import { createContext, useContext, useState, useCallback, useRef, type ReactNode } from 'react';

// ── 类型定义 ──────────────────────────────────────────

export interface ConfirmOptions {
  /** 弹窗标题 */
  title?: string;
  /** 弹窗消息 */
  message: string;
  /** 确认按钮文本（默认 "Confirm"） */
  confirmText?: string;
  /** 取消按钮文本（默认 "Cancel"） */
  cancelText?: string;
  /** 风格变体：danger 红色确认按钮，默认蓝色 */
  variant?: 'default' | 'danger';
}

type ConfirmFn = (options: ConfirmOptions) => Promise<boolean>;

interface ConfirmState {
  open: boolean;
  options: ConfirmOptions;
}

const ConfirmContext = createContext<ConfirmFn | null>(null);

// ── Provider ──────────────────────────────────────────

export function ConfirmProvider({ children }: { children: ReactNode }) {
  const [state, setState] = useState<ConfirmState>({
    open: false,
    options: { message: '' },
  });

  // 用 ref 存 resolve 函数，避免重渲染丢失
  const resolveRef = useRef<((value: boolean) => void) | null>(null);

  const confirm = useCallback<ConfirmFn>((options) => {
    return new Promise<boolean>((resolve) => {
      resolveRef.current = resolve;
      setState({ open: true, options });
    });
  }, []);

  const handleConfirm = useCallback(() => {
    resolveRef.current?.(true);
    resolveRef.current = null;
    setState(prev => ({ ...prev, open: false }));
  }, []);

  const handleCancel = useCallback(() => {
    resolveRef.current?.(false);
    resolveRef.current = null;
    setState(prev => ({ ...prev, open: false }));
  }, []);

  const { open, options } = state;
  const {
    title = 'Confirm',
    message,
    confirmText = 'Confirm',
    cancelText = 'Cancel',
    variant = 'default',
  } = options;

  const confirmBtnClass = variant === 'danger'
    ? 'bg-red-600 hover:bg-red-700 text-white'
    : 'bg-blue-600 hover:bg-blue-700 text-white';

  return (
    <ConfirmContext.Provider value={confirm}>
      {children}

      {/* 弹窗渲染 */}
      {open && (
        <div className="fixed inset-0 z-[60] flex items-center justify-center">
          {/* 遮罩层 */}
          <div className="fixed inset-0 bg-gray-900/60 backdrop-blur-sm" onClick={handleCancel} />

          {/* 弹窗卡片 */}
          <div className="relative bg-white rounded-2xl shadow-2xl w-full max-w-md p-6 z-10 fade-in">
            {/* 图标 */}
            <div className="flex items-start gap-4">
              <div className={`flex-shrink-0 w-10 h-10 rounded-full flex items-center justify-center ${
                variant === 'danger' ? 'bg-red-100' : 'bg-blue-100'
              }`}>
                <i className={`fas ${
                  variant === 'danger' ? 'fa-exclamation-triangle text-red-600' : 'fa-question-circle text-blue-600'
                }`} />
              </div>

              <div className="flex-1 min-w-0">
                <h3 className="text-lg font-bold text-gray-800">{title}</h3>
                <p className="mt-2 text-sm text-gray-600 whitespace-pre-line">{message}</p>
              </div>
            </div>

            {/* 按钮 */}
            <div className="flex gap-3 mt-6">
              <button
                onClick={handleCancel}
                className="flex-1 px-4 py-2 bg-gray-100 text-gray-700 rounded-lg hover:bg-gray-200 transition text-sm font-medium"
              >
                {cancelText}
              </button>
              <button
                onClick={handleConfirm}
                autoFocus
                className={`flex-1 px-4 py-2 rounded-lg transition text-sm font-medium ${confirmBtnClass}`}
              >
                {confirmText}
              </button>
            </div>
          </div>
        </div>
      )}
    </ConfirmContext.Provider>
  );
}

// ── Hook ──────────────────────────────────────────────

export function useConfirm(): ConfirmFn {
  const context = useContext(ConfirmContext);
  if (!context) {
    throw new Error('useConfirm must be used within a ConfirmProvider');
  }
  return context;
}
