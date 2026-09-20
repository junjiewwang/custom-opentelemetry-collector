/**
 * 应用根组件 - 路由配置
 */

import { lazy, Suspense } from 'react';
import { BrowserRouter, Routes, Route, Navigate } from 'react-router-dom';
import { AuthProvider, useAuth } from '@/contexts/AuthContext';
import { ToastProvider } from '@/contexts/ToastContext';
import { ConfirmProvider } from '@/components/ConfirmDialog';
import { SidebarProvider } from '@/contexts/SidebarContext';
import { ShellChromeProvider } from '@/contexts/ShellChromeContext';
import MainLayout from '@/layouts/MainLayout';
import LazyLoadFallback from '@/components/LazyLoadFallback';
import LoginPage from '@/pages/LoginPage';
import DashboardPage from '@/pages/DashboardPage';
import AppsPage from '@/pages/AppsPage';
import TenantsPage from '@/pages/TenantsPage';
import ServicesPage from '@/pages/ServicesPage';
import InstancesPage from '@/pages/InstancesPage';
import InstrumentationPage from '@/pages/InstrumentationPage';

// 懒加载：包含 ECharts 的页面 + TraceComparePage（减少主 chunk 体积）
const TracesPage = lazy(() => import('@/pages/TracesPage'));
const TraceComparePage = lazy(() => import('@/pages/TraceComparePage'));
const MetricsPage = lazy(() => import('@/pages/MetricsPage'));
const LogsPage = lazy(() => import('@/pages/LogsPage'));
const StorageAdminPage = lazy(() => import('@/pages/StorageAdminPage'));

/**
 * 受保护路由 - 未认证时重定向到登录页
 */
function ProtectedRoutes() {
  const { authenticated, role } = useAuth();

  if (!authenticated) {
    return <LoginPage />;
  }

  const isAdmin = role === 'admin';
  // tenant 角色落点到 Traces；admin 落点到 Dashboard
  const fallback = isAdmin ? '/dashboard' : '/traces';

  return (
    <Suspense fallback={<LazyLoadFallback />}>
      <Routes>
        <Route element={<MainLayout />}>
          {/* 默认重定向：admin → Dashboard，tenant → Traces */}
          <Route index element={<Navigate to={fallback} replace />} />

          {/* 管理页面 - 仅 admin 可见（tenant key 后端无权限，前端一并隐藏） */}
          {isAdmin && (
            <>
              <Route path="dashboard" element={<DashboardPage />} />
              <Route path="apps" element={<AppsPage />} />
              <Route path="tenants" element={<TenantsPage />} />
              <Route path="services" element={<ServicesPage />} />
              <Route path="instances" element={<InstancesPage />} />
              <Route path="instrumentation" element={<InstrumentationPage />} />
              {/* /configs 已整合进 ServicesPage Config Tab，旧书签兼容重定向 */}
              <Route path="configs" element={<Navigate to="/services" replace />} />
              <Route path="storage" element={<StorageAdminPage />} />
            </>
          )}

          {/* 可观测性页面 - 所有角色可见（后端按 tenant_id 隔离） */}
          <Route path="traces/compare" element={<TraceComparePage />} />
          <Route path="traces" element={<TracesPage />} />
          <Route path="metrics" element={<MetricsPage />} />
          <Route path="logs" element={<LogsPage />} />

          {/* 兜底 - 未匹配路由按角色重定向 */}
          <Route path="*" element={<Navigate to={fallback} replace />} />
        </Route>
      </Routes>
    </Suspense>
  );
}

export default function App() {
  return (
    <BrowserRouter basename="/ui">
      <AuthProvider>
        <ToastProvider>
          <ConfirmProvider>
            <ShellChromeProvider>
              <SidebarProvider>
                <ProtectedRoutes />
              </SidebarProvider>
            </ShellChromeProvider>
          </ConfirmProvider>
        </ToastProvider>
      </AuthProvider>
    </BrowserRouter>
  );
}
