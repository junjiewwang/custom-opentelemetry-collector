/**
 * 侧边栏组件 - 主导航
 *
 * 从旧版 Alpine.js 前端移植，保持一致的 UI 风格。
 */

import { NavLink } from 'react-router-dom';
import { useAuth } from '@/contexts/AuthContext';

/** 菜单配置 */
const MENU_ITEMS = [
  { id: 'dashboard', label: 'Dashboard', icon: 'fas fa-chart-pie', path: '/dashboard' },
  { id: 'apps', label: 'Applications', icon: 'fas fa-cube', path: '/apps' },
  { id: 'instances', label: 'Instances', icon: 'fas fa-server', path: '/instances' },
  { id: 'services', label: 'Services', icon: 'fas fa-sitemap', path: '/services' },
  { id: 'tasks', label: 'Tasks', icon: 'fas fa-tasks', path: '/tasks' },
  { id: 'configs', label: 'Configs', icon: 'fas fa-cog', path: '/configs' },
  // --- 新增的可观测性页面 ---
  { id: 'traces', label: 'Traces', icon: 'fas fa-route', path: '/traces' },
  { id: 'metrics', label: 'Metrics', icon: 'fas fa-chart-line', path: '/metrics' },
  { id: 'service-map', label: 'Service Map', icon: 'fas fa-project-diagram', path: '/service-map' },
];

export default function Sidebar() {
  const { logout } = useAuth();

  return (
    <aside className="fixed left-0 top-0 h-full w-64 bg-gray-900 text-white shadow-xl z-50">
      {/* Logo */}
      <div className="p-6 border-b border-gray-700">
        <h1 className="text-xl font-bold flex items-center gap-2">
          <i className="fas fa-satellite-dish text-primary-400" />
          OTel Admin
        </h1>
        <p className="text-gray-400 text-sm mt-1">Control Plane Dashboard</p>
      </div>

      {/* Navigation */}
      <nav className="p-4 space-y-2">
        {MENU_ITEMS.map((item) => (
          <NavLink
            key={item.id}
            to={item.path}
            className={({ isActive }) =>
              `w-full flex items-center gap-3 px-4 py-3 rounded-lg transition-all duration-200 ${
                isActive
                  ? 'bg-primary-600 text-white'
                  : 'text-gray-300 hover:bg-gray-800'
              }`
            }
          >
            <i className={`${item.icon} w-5 text-center`} />
            <span>{item.label}</span>
          </NavLink>
        ))}
      </nav>

      {/* Footer */}
      <div className="absolute bottom-0 left-0 right-0 p-4 border-t border-gray-700">
        <div className="flex items-center justify-between text-sm text-gray-400">
          <div className="flex items-center gap-2">
            <span className="w-2 h-2 rounded-full bg-green-500" />
            <span>Connected</span>
          </div>
          <button
            onClick={logout}
            className="text-gray-400 hover:text-white transition"
            title="Logout"
          >
            <i className="fas fa-sign-out-alt" />
          </button>
        </div>
      </div>
    </aside>
  );
}
