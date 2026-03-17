/**
 * 应用根组件 - 路由配置
 */

import { BrowserRouter, Routes, Route, Navigate } from 'react-router-dom';
import { AuthProvider, useAuth } from '@/contexts/AuthContext';
import { ToastProvider } from '@/contexts/ToastContext';
import MainLayout from '@/layouts/MainLayout';
import LoginPage from '@/pages/LoginPage';
import LegacyPage from '@/pages/LegacyPage';
import DashboardPage from '@/pages/DashboardPage';
import AppsPage from '@/pages/AppsPage';
import ServicesPage from '@/pages/ServicesPage';
import TracesPage from '@/pages/TracesPage';
import MetricsPage from '@/pages/MetricsPage';
import ServiceMapPage from '@/pages/ServiceMapPage';

/**
 * 受保护路由 - 未认证时重定向到登录页
 */
function ProtectedRoutes() {
  const { authenticated } = useAuth();

  if (!authenticated) {
    return <LoginPage />;
  }

  return (
    <Routes>
      <Route element={<MainLayout />}>
        {/* 默认重定向到 Dashboard */}
        <Route index element={<Navigate to="/dashboard" replace />} />

        {/* 已迁移页面 - React 原生实现 */}
        <Route path="dashboard" element={<DashboardPage />} />
        <Route path="apps" element={<AppsPage />} />
        <Route path="services" element={<ServicesPage />} />

        {/* 旧页面 - 通过 Legacy iframe 嵌入（待迁移） */}
        <Route path="instances" element={<LegacyPage view="instances" />} />
        <Route path="tasks" element={<LegacyPage view="tasks" />} />
        <Route path="configs" element={<LegacyPage view="configs" />} />

        {/* 新页面 - React 原生实现 */}
        <Route path="traces" element={<TracesPage />} />
        <Route path="metrics" element={<MetricsPage />} />
        <Route path="service-map" element={<ServiceMapPage />} />

        {/* 兜底 - 未匹配路由重定向到 Dashboard */}
        <Route path="*" element={<Navigate to="/dashboard" replace />} />
      </Route>
    </Routes>
  );
}

export default function App() {
  return (
    <BrowserRouter basename="/ui">
      <AuthProvider>
        <ToastProvider>
          <ProtectedRoutes />
        </ToastProvider>
      </AuthProvider>
    </BrowserRouter>
  );
}
