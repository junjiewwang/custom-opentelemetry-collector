/**
 * DetailDrawer - 可复用的右侧滑入抽屉组件
 *
 * 用于 Instances 和 Tasks 页面的详情展示面板。
 * 从右侧滑入，支持自定义宽度、标题、页脚按钮和关闭动画。
 *
 * @example
 * <DetailDrawer
 *   open={drawerOpen}
 *   onClose={() => setDrawerOpen(false)}
 *   title="Instance Details"
 *   subtitle="agent-001"
 *   width="lg"
 *   footer={<button onClick={handleAction}>Action</button>}
 * >
 *   <div>Detail content...</div>
 * </DetailDrawer>
 */

import { useEffect, useRef, type ReactNode } from 'react';

// ── 类型定义 ──────────────────────────────────────────

export interface DetailDrawerProps {
  /** 是否打开 */
  open: boolean;
  /** 关闭回调 */
  onClose: () => void;
  /** 抽屉标题 */
  title?: string;
  /** 副标题（如实例 ID、任务 ID） */
  subtitle?: string;
  /** 抽屉宽度：sm=384px, md=480px, lg=640px, xl=768px, full=100% */
  width?: 'sm' | 'md' | 'lg' | 'xl' | 'full';
  /** 自定义页脚内容 */
  footer?: ReactNode;
  /** 子内容 */
  children: ReactNode;
  /** 额外的 className（应用到抽屉面板） */
  className?: string;
}

// ── 宽度映射 ──────────────────────────────────────────

const WIDTH_MAP: Record<string, string> = {
  sm: 'max-w-sm',     // 384px
  md: 'max-w-md',     // 448px → 实际 480px
  lg: 'max-w-lg',     // 512px → 实际 640px
  xl: 'max-w-xl',     // 576px → 实际 768px
  full: 'max-w-full',
};

// ── 组件实现 ──────────────────────────────────────────

export default function DetailDrawer({
  open,
  onClose,
  title,
  subtitle,
  width = 'lg',
  footer,
  children,
  className = '',
}: DetailDrawerProps) {
  const drawerRef = useRef<HTMLDivElement>(null);

  // ── Escape 键关闭 ──────────────────────────────────

  useEffect(() => {
    if (!open) return;

    const handleEsc = (e: KeyboardEvent) => {
      if (e.key === 'Escape') onClose();
    };
    document.addEventListener('keydown', handleEsc);
    return () => document.removeEventListener('keydown', handleEsc);
  }, [open, onClose]);

  // ── 打开时禁止 body 滚动 ──────────────────────────

  useEffect(() => {
    if (open) {
      document.body.style.overflow = 'hidden';
    } else {
      document.body.style.overflow = '';
    }
    return () => {
      document.body.style.overflow = '';
    };
  }, [open]);

  if (!open) return null;

  const widthClass = WIDTH_MAP[width] || WIDTH_MAP.lg;

  return (
    <div className="fixed inset-0 z-50 flex">
      {/* 遮罩层 */}
      <div
        className="fixed inset-0 bg-gray-900/40 backdrop-blur-[2px] transition-opacity"
        onClick={onClose}
      />

      {/* 抽屉面板 */}
      <div
        ref={drawerRef}
        className={`fixed right-0 top-0 h-full w-full ${widthClass} bg-white shadow-2xl flex flex-col z-10 animate-slide-in-right ${className}`}
        style={{ animationDuration: '0.25s' }}
      >
        {/* 头部 */}
        <div className="flex items-center justify-between px-6 py-4 border-b border-gray-100 flex-shrink-0">
          <div className="min-w-0 flex-1">
            {title && (
              <h3 className="text-lg font-bold text-gray-800 truncate">{title}</h3>
            )}
            {subtitle && (
              <p className="text-sm text-gray-400 font-mono truncate mt-0.5">{subtitle}</p>
            )}
          </div>
          <button
            onClick={onClose}
            className="ml-4 p-2 text-gray-400 hover:text-gray-600 hover:bg-gray-100 rounded-lg transition flex-shrink-0"
            title="Close"
          >
            <i className="fas fa-times" />
          </button>
        </div>

        {/* 内容区域（可滚动） */}
        <div className="flex-1 overflow-y-auto px-6 py-4">
          {children}
        </div>

        {/* 页脚（可选） */}
        {footer && (
          <div className="flex-shrink-0 px-6 py-4 border-t border-gray-100 bg-gray-50">
            {footer}
          </div>
        )}
      </div>
    </div>
  );
}

// ── 抽屉内容辅助子组件 ──────────────────────────────────

/** 抽屉内的分区标题 */
export function DrawerSection({ title, children, className = '' }: {
  title: string;
  children: ReactNode;
  className?: string;
}) {
  return (
    <div className={`mb-6 ${className}`}>
      <h4 className="text-sm font-semibold text-gray-500 uppercase tracking-wider mb-3">{title}</h4>
      {children}
    </div>
  );
}

/** 抽屉内的 Key-Value 信息行 */
export function DrawerInfoRow({ label, value, mono = false, copyable = false }: {
  label: string;
  value: ReactNode;
  mono?: boolean;
  copyable?: boolean;
}) {
  const handleCopy = () => {
    if (typeof value === 'string') {
      navigator.clipboard.writeText(value);
    }
  };

  return (
    <div className="flex items-start py-2 border-b border-gray-50 last:border-b-0">
      <span className="text-sm text-gray-500 w-36 flex-shrink-0">{label}</span>
      <span className={`text-sm text-gray-800 flex-1 break-all ${mono ? 'font-mono text-xs' : ''}`}>
        {value || <span className="text-gray-300">-</span>}
      </span>
      {copyable && typeof value === 'string' && value && (
        <button
          onClick={handleCopy}
          className="ml-2 text-gray-300 hover:text-gray-500 transition flex-shrink-0"
          title="Copy"
        >
          <i className="fas fa-copy text-xs" />
        </button>
      )}
    </div>
  );
}
