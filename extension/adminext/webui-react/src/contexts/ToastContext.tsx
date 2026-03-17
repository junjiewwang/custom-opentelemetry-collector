/**
 * Toast 通知上下文 - 全局提示消息
 */

import { createContext, useContext, useState, useCallback, useRef, type ReactNode } from 'react';

type ToastType = 'success' | 'error' | 'info';

interface ToastState {
  show: boolean;
  message: string;
  type: ToastType;
}

interface ToastContextType {
  toast: ToastState;
  showToast: (message: string, type?: ToastType) => void;
}

const ToastContext = createContext<ToastContextType | null>(null);

const TOAST_DURATION = 3000;

export function ToastProvider({ children }: { children: ReactNode }) {
  const [toast, setToast] = useState<ToastState>({ show: false, message: '', type: 'info' });
  const timerRef = useRef<ReturnType<typeof setTimeout> | null>(null);

  const showToast = useCallback((message: string, type: ToastType = 'info') => {
    if (timerRef.current) {
      clearTimeout(timerRef.current);
    }

    setToast({ show: true, message, type });

    timerRef.current = setTimeout(() => {
      setToast(prev => ({ ...prev, show: false }));
    }, TOAST_DURATION);
  }, []);

  return (
    <ToastContext.Provider value={{ toast, showToast }}>
      {children}
      {/* Toast 渲染 */}
      {toast.show && (
        <div className="fixed bottom-6 right-6 z-50 animate-slide-up">
          <div
            className={`px-6 py-3 rounded-lg shadow-lg flex items-center gap-3 text-white ${
              toast.type === 'success' ? 'bg-green-600' :
              toast.type === 'error' ? 'bg-red-600' :
              'bg-blue-600'
            }`}
          >
            <i className={`fas ${
              toast.type === 'success' ? 'fa-check-circle' :
              toast.type === 'error' ? 'fa-exclamation-circle' :
              'fa-info-circle'
            }`} />
            <span>{toast.message}</span>
          </div>
        </div>
      )}
    </ToastContext.Provider>
  );
}

export function useToast(): ToastContextType {
  const context = useContext(ToastContext);
  if (!context) {
    throw new Error('useToast must be used within a ToastProvider');
  }
  return context;
}
