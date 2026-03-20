/**
 * 认证上下文 - 管理 API Key 认证状态
 *
 * 从旧版 Alpine.js 前端的认证逻辑移植而来。
 */

import { createContext, useContext, useState, useCallback, useEffect, type ReactNode } from 'react';
import { apiClient } from '@/api/client';

// ============================================================================
// 本地存储 Key（与旧版保持一致）
// ============================================================================

const STORAGE_KEY_API_KEY = 'otel_admin_api_key';
const STORAGE_KEY_REMEMBER = 'otel_admin_remember_key';

// ============================================================================
// Context 类型定义
// ============================================================================

interface AuthContextType {
  /** 是否已认证 */
  authenticated: boolean;
  /** 登录（验证 API Key） */
  login: (apiKey: string, remember: boolean) => Promise<void>;
  /** 登出 */
  logout: () => void;
  /** 登录加载状态 */
  loginLoading: boolean;
  /** 登录错误信息 */
  loginError: string;
}

const AuthContext = createContext<AuthContextType | null>(null);

// ============================================================================
// Provider 组件
// ============================================================================

export function AuthProvider({ children }: { children: ReactNode }) {
  const [authenticated, setAuthenticated] = useState(false);
  const [loginLoading, setLoginLoading] = useState(false);
  const [loginError, setLoginError] = useState('');

  // 启动时尝试从 localStorage 恢复 API Key
  useEffect(() => {
    const remember = localStorage.getItem(STORAGE_KEY_REMEMBER) === 'true';
    if (remember) {
      const savedKey = localStorage.getItem(STORAGE_KEY_API_KEY);
      if (savedKey) {
        apiClient.setApiKey(savedKey);
        // 验证 key 是否仍然有效
        apiClient.getDashboard()
          .then(() => {
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
      // 用 dashboard 接口验证 API Key 是否有效
      await apiClient.getDashboard();

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
    localStorage.removeItem(STORAGE_KEY_API_KEY);
    setAuthenticated(false);
  }, []);

  return (
    <AuthContext.Provider value={{ authenticated, login, logout, loginLoading, loginError }}>
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
