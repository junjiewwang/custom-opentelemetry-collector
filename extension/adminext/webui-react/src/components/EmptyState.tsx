/**
 * EmptyState — 通用空状态组件
 *
 * 统一全站空状态展示风格：圆形图标背景 + 标题 + 描述 + 可选操作按钮
 * 支持 3 种尺寸变体：sm（图表内嵌）、md（卡片内嵌）、lg（页面级）
 */

import type { ReactNode } from 'react';

export interface EmptyStateProps {
  /** Font Awesome 图标类名，如 "fas fa-search" */
  icon?: string;
  /** 图标颜色（Tailwind text 色），默认 text-gray-300 */
  iconColor?: string;
  /** 图标背景色（Tailwind bg 色），默认 bg-gray-50 */
  iconBg?: string;
  /** 标题文字 */
  title?: string;
  /** 描述文字或 JSX */
  description?: ReactNode;
  /** 操作按钮区域 */
  action?: ReactNode;
  /** 尺寸变体：sm=图表内嵌, md=卡片内嵌, lg=页面级 */
  size?: 'sm' | 'md' | 'lg';
  /** 额外 className */
  className?: string;
}

const sizeConfig = {
  sm: {
    wrapper: 'py-6',
    iconBox: 'w-10 h-10',
    iconText: 'text-base',
    title: 'text-sm font-medium text-gray-500',
    desc: 'text-xs text-gray-400 max-w-xs',
  },
  md: {
    wrapper: 'py-10',
    iconBox: 'w-14 h-14',
    iconText: 'text-xl',
    title: 'text-base font-semibold text-gray-600',
    desc: 'text-sm text-gray-400 max-w-sm',
  },
  lg: {
    wrapper: 'py-16',
    iconBox: 'w-16 h-16',
    iconText: 'text-2xl',
    title: 'text-lg font-semibold text-gray-600',
    desc: 'text-sm text-gray-400 max-w-md',
  },
};

export default function EmptyState({
  icon = 'fas fa-inbox',
  iconColor = 'text-gray-300',
  iconBg = 'bg-gray-50',
  title,
  description,
  action,
  size = 'md',
  className = '',
}: EmptyStateProps) {
  const cfg = sizeConfig[size];

  return (
    <div className={`flex flex-col items-center justify-center text-center ${cfg.wrapper} ${className}`}>
      <div className={`${cfg.iconBox} ${iconBg} rounded-full flex items-center justify-center mx-auto mb-3`}>
        <i className={`${icon} ${iconColor} ${cfg.iconText}`} />
      </div>
      {title && <h3 className={`${cfg.title} mb-1`}>{title}</h3>}
      {description && <p className={`${cfg.desc} mx-auto`}>{description}</p>}
      {action && <div className="mt-4">{action}</div>}
    </div>
  );
}
