/**
 * 应用根组件 - 路由配置
 */

import { BrowserRouter, Routes, Route, Navigate } from 'react-router-dom';
import { AuthProvider, useAuth } from '@/contexts/AuthContext';
import { ToastProvider } from '@/contexts/ToastContext';
import { ConfirmProvider } from '@/components/ConfirmDialog';
import { SidebarProvider } from '@/contexts/SidebarContext';
import MainLayout from '@/layouts/MainLayout';
import LoginPage from '@/pages/LoginPage';
import DashboardPage from '@/pages/DashboardPage';
import AppsPage from '@/pages/AppsPage';
import ServicesPage from '@/pages/ServicesPage';
import TracesPage from '@/pages/TracesPage';
import MetricsPage from '@/pages/MetricsPage';
import ServiceMapPage from '@/pages/ServiceMapPage';
import ConfigsPage from '@/pages/ConfigsPage';
import InstancesPage from '@/pages/InstancesPage';
import TasksPage from '@/pages/TasksPage';

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
        <Route path="instances" element={<InstancesPage />} />
        <Route path="tasks" element={<TasksPage />} />
        <Route path="configs" element={<ConfigsPage />} />

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
          <ConfirmProvider>
            <SidebarProvider>
              <ProtectedRoutes />
            </SidebarProvider>
          </ConfirmProvider>
        </ToastProvider>
      </AuthProvider>
    </BrowserRouter>
  );
}
