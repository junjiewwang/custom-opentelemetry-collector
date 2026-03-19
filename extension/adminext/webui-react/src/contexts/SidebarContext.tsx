/**
 * SidebarContext — 侧边栏状态管理
 *
 * 提供：
 * - collapsed: 桌面端侧边栏是否折叠为 mini 模式（仅图标）
 * - mobileOpen: 移动端侧边栏抽屉是否打开
 * - toggleCollapsed / toggleMobile / closeMobile
 */

import { createContext, useContext, useState, useEffect, useCallback, type ReactNode } from 'react';

interface SidebarState {
  /** 桌面端是否折叠（mini 模式，仅显示图标） */
  collapsed: boolean;
  /** 移动端抽屉是否打开 */
  mobileOpen: boolean;
  /** 切换桌面端折叠状态 */
  toggleCollapsed: () => void;
  /** 切换移动端抽屉 */
  toggleMobile: () => void;
  /** 关闭移动端抽屉 */
  closeMobile: () => void;
  /** 当前是否为移动端视口 (<1024px) */
  isMobile: boolean;
}

const SidebarContext = createContext<SidebarState | null>(null);

const LG_BREAKPOINT = 1024;

export function SidebarProvider({ children }: { children: ReactNode }) {
  const [collapsed, setCollapsed] = useState(false);
  const [mobileOpen, setMobileOpen] = useState(false);
  const [isMobile, setIsMobile] = useState(false);

  // 监听窗口尺寸变化
  useEffect(() => {
    const check = () => {
      const mobile = window.innerWidth < LG_BREAKPOINT;
      setIsMobile(mobile);
      // 切换到移动端时自动关闭抽屉
      if (mobile) {
        setMobileOpen(false);
      }
    };
    check();
    window.addEventListener('resize', check);
    return () => window.removeEventListener('resize', check);
  }, []);

  const toggleCollapsed = useCallback(() => setCollapsed(v => !v), []);
  const toggleMobile = useCallback(() => setMobileOpen(v => !v), []);
  const closeMobile = useCallback(() => setMobileOpen(false), []);

  return (
    <SidebarContext.Provider value={{ collapsed, mobileOpen, toggleCollapsed, toggleMobile, closeMobile, isMobile }}>
      {children}
    </SidebarContext.Provider>
  );
}

export function useSidebar(): SidebarState {
  const ctx = useContext(SidebarContext);
  if (!ctx) throw new Error('useSidebar must be used within SidebarProvider');
  return ctx;
}
