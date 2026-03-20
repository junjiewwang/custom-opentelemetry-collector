/**
 * 侧边栏组件 - 主导航
 *
 * 从旧版 Alpine.js 前端移植，增强了：
 * - 导航分组（管理 / 可观测性）
 * - 选中态左边框指示器
 * - Footer 增强
 * - 响应式：桌面端支持折叠为 mini 模式，移动端抽屉式
 */

import { NavLink, useLocation } from 'react-router-dom';
import { useAuth } from '@/contexts/AuthContext';
import { useSidebar } from '@/contexts/SidebarContext';

/** 菜单分组 */
const MENU_GROUPS = [
  {
    label: 'Management',
    items: [
      { id: 'dashboard', label: 'Dashboard', icon: 'fas fa-chart-pie', path: '/dashboard' },
      { id: 'apps', label: 'Applications', icon: 'fas fa-cube', path: '/apps' },
      { id: 'instances', label: 'Instances', icon: 'fas fa-server', path: '/instances' },
      { id: 'services', label: 'Services', icon: 'fas fa-sitemap', path: '/services' },
      { id: 'tasks', label: 'Tasks', icon: 'fas fa-tasks', path: '/tasks' },
      { id: 'configs', label: 'Configs', icon: 'fas fa-cog', path: '/configs' },
    ],
  },
  {
    label: 'Observability',
    items: [
      { id: 'traces', label: 'Traces', icon: 'fas fa-route', path: '/traces' },
      { id: 'metrics', label: 'Metrics', icon: 'fas fa-chart-line', path: '/metrics' },
    ],
  },
];

export default function Sidebar() {
  const { logout } = useAuth();
  const { collapsed, mobileOpen, isMobile, closeMobile } = useSidebar();
  const location = useLocation();

  // 移动端：点击导航后自动关闭抽屉
  const handleNavClick = () => {
    if (isMobile) closeMobile();
  };

  // 侧边栏宽度：折叠时 w-16（64px），展开时 w-64（256px）
  const sidebarWidth = collapsed && !isMobile ? 'w-16' : 'w-64';

  // 移动端：控制抽屉滑入/滑出
  const mobileTransform = isMobile
    ? mobileOpen ? 'translate-x-0' : '-translate-x-full'
    : '';

  return (
    <>
      {/* 移动端遮罩层 */}
      {isMobile && mobileOpen && (
        <div
          className="fixed inset-0 bg-black/50 backdrop-blur-[2px] z-40 transition-opacity"
          onClick={closeMobile}
        />
      )}

      {/* 侧边栏 */}
      <aside
        className={`fixed left-0 top-0 h-full ${sidebarWidth} bg-gray-900 text-white shadow-xl z-50 flex flex-col transition-all duration-300 ${mobileTransform} ${
          isMobile ? '' : ''
        }`}
      >
        {/* Logo */}
        <div className="p-6 border-b border-gray-700/60 flex-shrink-0">
          {collapsed && !isMobile ? (
            <div className="flex items-center justify-center">
              <i className="fas fa-satellite-dish text-primary-400 text-lg" />
            </div>
          ) : (
            <>
              <h1 className="text-xl font-bold flex items-center gap-2.5">
                <i className="fas fa-satellite-dish text-primary-400" />
                OTel Admin
              </h1>
              <p className="text-gray-500 text-xs mt-1.5 tracking-wide">Control Plane Dashboard</p>
            </>
          )}
        </div>

        {/* Navigation Groups */}
        <nav className="flex-1 overflow-y-auto py-3 px-3">
          {MENU_GROUPS.map((group, idx) => (
            <div key={group.label} className={idx > 0 ? 'mt-4' : ''}>
              {/* 分组标签 */}
              {collapsed && !isMobile ? (
                <div className="px-1 mb-2">
                  <div className="h-px bg-gray-700/60 mx-1" />
                </div>
              ) : (
                <div className="px-3 mb-2">
                  <span className="text-[10px] font-semibold uppercase tracking-widest text-gray-500">
                    {group.label}
                  </span>
                </div>
              )}
              {/* 菜单项 */}
              <div className="space-y-0.5">
                {group.items.map((item) => {
                  const isActive = location.pathname === item.path || location.pathname.startsWith(item.path + '/');
                  return (
                    <NavLink
                      key={item.id}
                      to={item.path}
                      onClick={handleNavClick}
                      className={`w-full flex items-center ${collapsed && !isMobile ? 'justify-center' : ''} gap-3 ${collapsed && !isMobile ? 'px-2' : 'px-4'} py-2.5 rounded-lg transition-all duration-200 relative group ${
                        isActive
                          ? 'bg-primary-600/20 text-white'
                          : 'text-gray-400 hover:bg-gray-800 hover:text-gray-200'
                      }`}
                      title={collapsed && !isMobile ? item.label : undefined}
                    >
                      {/* 左侧选中指示条 */}
                      {isActive && (
                        <span className="absolute left-0 top-1/2 -translate-y-1/2 w-[3px] h-5 bg-primary-400 rounded-r-full" />
                      )}
                      <i className={`${item.icon} w-5 text-center text-sm ${isActive ? 'text-primary-400' : 'text-gray-500 group-hover:text-gray-400'}`} />
                      {(!collapsed || isMobile) && (
                        <span className="text-sm font-medium">{item.label}</span>
                      )}
                    </NavLink>
                  );
                })}
              </div>
            </div>
          ))}
        </nav>

        {/* Footer */}
        <div className="p-4 border-t border-gray-700/60 flex-shrink-0">
          {collapsed && !isMobile ? (
            <div className="flex flex-col items-center gap-3">
              <span className="w-2 h-2 rounded-full bg-green-500 animate-pulse" />
              <button
                onClick={logout}
                className="text-gray-500 hover:text-red-400 transition-colors"
                title="Logout"
              >
                <i className="fas fa-sign-out-alt text-sm" />
              </button>
            </div>
          ) : (
            <>
              <div className="flex items-center justify-between">
                <div className="flex items-center gap-2 text-xs">
                  <span className="w-2 h-2 rounded-full bg-green-500 animate-pulse" />
                  <span className="text-gray-400">Connected</span>
                </div>
                <button
                  onClick={logout}
                  className="flex items-center gap-1.5 text-xs text-gray-500 hover:text-red-400 transition-colors"
                  title="Logout"
                >
                  <i className="fas fa-sign-out-alt" />
                  <span>Logout</span>
                </button>
              </div>
              <p className="text-[10px] text-gray-600 mt-2">v1.0.0 · React</p>
            </>
          )}
        </div>
      </aside>
    </>
  );
}
