/**
 * 主布局 - 包含 Sidebar + 内容区域
 *
 * 响应式策略：
 * - 桌面端 (>=1024px): 固定侧边栏，支持折叠为 mini 模式（64px 仅图标）
 * - 移动端 (<1024px): 侧边栏隐藏，顶部显示 hamburger 导航栏
 */

import { Outlet } from 'react-router-dom';
import Sidebar from './Sidebar';
import { useSidebar } from '@/contexts/SidebarContext';

export default function MainLayout() {
  const { collapsed, isMobile, toggleMobile, toggleCollapsed } = useSidebar();

  // 桌面端根据折叠状态决定 main 的 margin-left
  const mainMargin = isMobile ? '' : collapsed ? 'lg:ml-16' : 'lg:ml-64';

  return (
    <div className="min-h-screen bg-gray-50">
      <Sidebar />

      {/* 移动端顶部导航栏 */}
      {isMobile && (
        <header className="fixed top-0 left-0 right-0 h-14 bg-gray-900 text-white flex items-center px-4 z-40 shadow-lg">
          <button
            onClick={toggleMobile}
            className="p-2 -ml-1 rounded-lg hover:bg-gray-800 transition-colors"
            aria-label="Toggle menu"
          >
            <i className="fas fa-bars text-lg" />
          </button>
          <h1 className="ml-3 text-base font-bold flex items-center gap-2">
            <i className="fas fa-satellite-dish text-primary-400 text-sm" />
            OTel Admin
          </h1>
        </header>
      )}

      {/* 桌面端折叠切换按钮（悬浮在侧边栏右侧边缘） */}
      {!isMobile && (
        <button
          onClick={toggleCollapsed}
          className={`fixed top-5 z-[51] w-6 h-6 bg-gray-700 hover:bg-gray-600 text-gray-300 hover:text-white rounded-full flex items-center justify-center transition-all duration-300 shadow-md ${
            collapsed ? 'left-[52px]' : 'left-[244px]'
          }`}
          title={collapsed ? 'Expand sidebar' : 'Collapse sidebar'}
        >
          <i className={`fas fa-chevron-${collapsed ? 'right' : 'left'} text-[10px]`} />
        </button>
      )}

      {/* 主内容区域 */}
      <main className={`transition-all duration-300 ${mainMargin} ${isMobile ? 'pt-14' : ''} p-6 lg:p-8`}>
        <Outlet />
      </main>
    </div>
  );
}
