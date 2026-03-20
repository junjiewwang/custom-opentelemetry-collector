/**
 * Modal - 通用弹窗组件
 *
 * 居中弹窗，支持多种尺寸，使用 createPortal 渲染到 document.body。
 * 参考 DetailDrawer 的设计模式（Escape 键关闭、遮罩层、body 滚动锁定）。
 *
 * @example
 * <Modal
 *   isOpen={showDetail}
 *   onClose={() => setShowDetail(false)}
 *   size="full"
 * >
 *   <TraceDetail trace={trace} onClose={() => setShowDetail(false)} />
 * </Modal>
 *
 * @example
 * <Modal
 *   isOpen={showConfirm}
 *   onClose={() => setShowConfirm(false)}
 *   size="md"
 *   title="Confirm Action"
 * >
 *   <p>Are you sure you want to proceed?</p>
 * </Modal>
 */

import { useEffect, type ReactNode } from 'react';
import { createPortal } from 'react-dom';

// ── 类型定义 ──────────────────────────────────────────

export interface ModalProps {
  /** 是否打开 */
  isOpen: boolean;
  /** 关闭回调 */
  onClose: () => void;
  /** 弹窗尺寸：md / lg / xl（默认）/ full */
  size?: 'md' | 'lg' | 'xl' | 'full';
  /** 可选标题（提供时显示标题栏 + 关闭按钮，不提供则直接渲染 children） */
  title?: string;
  /** 子内容 */
  children: ReactNode;
}

// ── 尺寸映射 ──────────────────────────────────────────

const SIZE_CLASSES: Record<string, string> = {
  md:   'max-w-2xl',
  lg:   'max-w-4xl',
  xl:   'max-w-6xl',
  full: 'max-w-[95vw] max-h-[95vh]',
};

// ── 组件实现 ──────────────────────────────────────────

export default function Modal({
  isOpen,
  onClose,
  size = 'xl',
  title,
  children,
}: ModalProps) {

  // ── Escape 键关闭 ──────────────────────────────────

  useEffect(() => {
    if (!isOpen) return;

    const handleEsc = (e: KeyboardEvent) => {
      if (e.key === 'Escape') onClose();
    };
    document.addEventListener('keydown', handleEsc);
    return () => document.removeEventListener('keydown', handleEsc);
  }, [isOpen, onClose]);

  // ── 打开时禁止 body 滚动 ──────────────────────────

  useEffect(() => {
    if (isOpen) {
      document.body.style.overflow = 'hidden';
    } else {
      document.body.style.overflow = '';
    }
    return () => {
      document.body.style.overflow = '';
    };
  }, [isOpen]);

  // ── 关闭状态不渲染 ────────────────────────────────

  if (!isOpen) return null;

  const sizeClass = SIZE_CLASSES[size] ?? SIZE_CLASSES.xl;

  // ── Portal 渲染到 document.body ──────────────────

  return createPortal(
    <div
      className="fixed inset-0 z-50 flex items-center justify-center transition-all duration-300"
      role="dialog"
      aria-modal="true"
    >
      {/* 遮罩层 */}
      <div
        className="fixed inset-0 bg-black/50 backdrop-blur-sm transition-opacity duration-300"
        onClick={onClose}
      />

      {/* 弹窗卡片 */}
      <div
        className={`relative w-full ${sizeClass} mx-4 bg-white rounded-2xl shadow-2xl flex flex-col overflow-hidden animate-modal-slide-up`}
        style={{
          maxHeight: size === 'full' ? '95vh' : '90vh',
        }}
      >
        {/* 可选标题栏 */}
        {title && (
          <div className="flex items-center justify-between px-6 py-4 border-b border-gray-200 flex-shrink-0">
            <h3 className="text-lg font-bold text-gray-800 truncate">{title}</h3>
            <button
              onClick={onClose}
              className="ml-4 p-2 text-gray-400 hover:text-gray-600 hover:bg-gray-100 rounded-lg transition flex-shrink-0"
              title="Close"
            >
              <i className="fas fa-times" />
            </button>
          </div>
        )}

        {/* 内容区域（可滚动） */}
        <div className="flex-1 overflow-y-auto">
          {children}
        </div>
      </div>
    </div>,
    document.body,
  );
}
