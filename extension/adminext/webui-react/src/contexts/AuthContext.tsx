/**
 * 认证上下文 - 管理 API Key 认证状态 + 角色（admin / tenant）
 *
 * 从旧版 Alpine.js 前端的认证逻辑移植而来，并增加多租户角色区分：
 * - super / operator key → role = "admin"（全量管理 UI）
 * - tenant key       → role = "tenant"（仅可观测性 UI，租户隔离）
 */

import { createContext, useContext, useState, useCallback, useEffect, type ReactNode } from 'react';
import { apiClient, type AuthMe } from '@/api/client';

// ============================================================================
// 本地存储 Key（与旧版保持一致）
// ============================================================================

const STORAGE_KEY_API_KEY = 'otel_admin_api_key';
const STORAGE_KEY_REMEMBER = 'otel_admin_remember_key';

// ============================================================================
// 类型定义
// ============================================================================

export type Role = 'admin' | 'tenant';

interface AuthContextType {
  /** 是否已认证 */
  authenticated: boolean;
  /** 当前角色：admin（super/operator）或 tenant（tk_ key） */
  role: Role;
  /** 当前租户 ID（仅 tenant 角色非空） */
  tenantID: string;
  /** 是否正在扮演其它租户（admin/operator 视角切换） */
  impersonating: boolean;
  /** 扮演的目标租户 ID（仅 impersonating 时非空） */
  impersonatedTenantID: string;
  /** 登录（验证 API Key 并解析角色） */
  login: (apiKey: string, remember: boolean) => Promise<void>;
  /** 登出 */
  logout: () => void;
  /** 扮演某租户（传空串 = 退出扮演回到全局视角） */
  impersonate: (tenantID: string) => Promise<void>;
  /** 登录加载状态 */
  loginLoading: boolean;
  /** 登录错误信息 */
  loginError: string;
}

const AuthContext = createContext<AuthContextType | null>(null);

// ============================================================================
// Helpers
// ============================================================================

/** 将后端 key_type 映射为前端角色：只有 tenant key 才是受限角色。 */
function roleFromAuthMe(me: AuthMe): Role {
  return me.key_type === 'tenant' ? 'tenant' : 'admin';
}

// ============================================================================
// Provider 组件
// ============================================================================

export function AuthProvider({ children }: { children: ReactNode }) {
  const [authenticated, setAuthenticated] = useState(false);
  const [role, setRole] = useState<Role>('admin');
  const [tenantID, setTenantID] = useState('');
  const [impersonating, setImpersonating] = useState(false);
  const [impersonatedTenantID, setImpersonatedTenantID] = useState('');
  const [loginLoading, setLoginLoading] = useState(false);
  const [loginError, setLoginError] = useState('');

  // 应用 /auth/me 返回的身份：key_type → role，impersonating → 扮演状态。
  // 注意：扮演时后端把生效租户放到 tenant_id 上（actor 本身是 admin），所以
  // 扮演态下 tenantID 置空、impersonatedTenantID 取 tenant_id。
  const applyAuthMe = useCallback((me: AuthMe) => {
    setRole(roleFromAuthMe(me));
    setTenantID(me.impersonating ? '' : (me.tenant_id || ''));
    setImpersonating(me.impersonating || false);
    setImpersonatedTenantID(me.impersonating ? (me.tenant_id || '') : '');
  }, []);

  // 启动时尝试从 localStorage 恢复 API Key
  useEffect(() => {
    const remember = localStorage.getItem(STORAGE_KEY_REMEMBER) === 'true';
    if (remember) {
      const savedKey = localStorage.getItem(STORAGE_KEY_API_KEY);
      if (savedKey) {
        apiClient.setApiKey(savedKey);
        // 用 /auth/me 验证 key 是否有效 + 解析角色
        apiClient.getAuthMe()
          .then((me) => {
            applyAuthMe(me);
            setAuthenticated(true);
          })
          .catch(() => {
            // key 无效，清除
            localStorage.removeItem(STORAGE_KEY_API_KEY);
            apiClient.setApiKey('');
          });
      }
    }
  }, []);

  const login = useCallback(async (apiKey: string, remember: boolean) => {
    setLoginLoading(true);
    setLoginError('');

    try {
      apiClient.setApiKey(apiKey);
      // 用 /auth/me 验证 API Key 是否有效并解析角色（替代旧的 getDashboard，
      // 后者是 admin-only，会导致 tenant key 登录失败）
      const me = await apiClient.getAuthMe();

      applyAuthMe(me);

      // 保存到 localStorage
      if (remember) {
        localStorage.setItem(STORAGE_KEY_API_KEY, apiKey);
        localStorage.setItem(STORAGE_KEY_REMEMBER, 'true');
      } else {
        localStorage.removeItem(STORAGE_KEY_API_KEY);
        localStorage.setItem(STORAGE_KEY_REMEMBER, 'false');
      }

      setAuthenticated(true);
    } catch (err: unknown) {
      const apiErr = err as { status?: number; message?: string };
      if (apiErr.status === 401) {
        setLoginError('Invalid API Key');
      } else {
        setLoginError(apiErr.message || 'Connection failed');
      }
      apiClient.setApiKey('');
    } finally {
      setLoginLoading(false);
    }
  }, []);

  const logout = useCallback(() => {
    apiClient.setApiKey('');
    apiClient.clearImpersonateTenant();
    localStorage.removeItem(STORAGE_KEY_API_KEY);
    setAuthenticated(false);
    setRole('admin');
    setTenantID('');
    setImpersonating(false);
    setImpersonatedTenantID('');
  }, []);

  // 扮演（admin/operator）：设置 X-Tenant-Id 头并重取 /auth/me 确认；空串 = 退出扮演。
  const impersonate = useCallback(async (targetTenantID: string) => {
    apiClient.setImpersonateTenant(targetTenantID);
    try {
      const me = await apiClient.getAuthMe();
      applyAuthMe(me);
    } catch (err) {
      apiClient.clearImpersonateTenant();
      setImpersonating(false);
      setImpersonatedTenantID('');
      throw err;
    }
  }, [applyAuthMe]);

  return (
    <AuthContext.Provider value={{ authenticated, role, tenantID, impersonating, impersonatedTenantID, login, logout, impersonate, loginLoading, loginError }}>
      {children}
    </AuthContext.Provider>
  );
}

// ============================================================================
// Hook
// ============================================================================

export function useAuth(): AuthContextType {
  const context = useContext(AuthContext);
  if (!context) {
    throw new Error('useAuth must be used within an AuthProvider');
  }
  return context;
}
