/**
 * 登录页面 - API Key 认证
 *
 * 从旧版 Alpine.js 前端移植，保持一致的 UI 风格。
 */

import { useState } from 'react';
import { useAuth } from '@/contexts/AuthContext';

export default function LoginPage() {
  const { login, loginLoading, loginError } = useAuth();
  const [apiKeyInput, setApiKeyInput] = useState('');
  const [showApiKey, setShowApiKey] = useState(false);
  const [rememberApiKey, setRememberApiKey] = useState(true);

  const handleSubmit = async (e: React.FormEvent) => {
    e.preventDefault();
    await login(apiKeyInput, rememberApiKey);
  };

  return (
    <div className="min-h-screen flex items-center justify-center bg-gradient-to-br from-gray-900 to-gray-800">
      <div className="bg-white rounded-2xl shadow-2xl w-full max-w-md p-8 slide-in">
        {/* Logo & Title */}
        <div className="text-center mb-8">
          <div className="w-16 h-16 bg-primary-100 rounded-full flex items-center justify-center mx-auto mb-4">
            <i className="fas fa-satellite-dish text-primary-600 text-2xl" />
          </div>
          <h1 className="text-2xl font-bold text-gray-800">OTel Admin</h1>
          <p className="text-gray-500 mt-1">Control Plane Dashboard</p>
        </div>

        {/* Login Form */}
        <form onSubmit={handleSubmit}>
          <div className="space-y-4">
            <div>
              <label className="block text-sm font-medium text-gray-700 mb-2">
                API Key
              </label>
              <div className="relative">
                <input
                  type={showApiKey ? 'text' : 'password'}
                  value={apiKeyInput}
                  onChange={(e) => setApiKeyInput(e.target.value)}
                  required
                  className="w-full px-4 py-3 border border-gray-200 rounded-lg focus:ring-2 focus:ring-primary-500 focus:border-primary-500 pr-12"
                  placeholder="Enter your API Key"
                />
                <button
                  type="button"
                  onClick={() => setShowApiKey(!showApiKey)}
                  className="absolute right-3 top-1/2 -translate-y-1/2 text-gray-400 hover:text-gray-600"
                >
                  <i className={showApiKey ? 'fas fa-eye-slash' : 'fas fa-eye'} />
                </button>
              </div>
            </div>

            <div className="flex items-center gap-2">
              <input
                type="checkbox"
                id="rememberKey"
                checked={rememberApiKey}
                onChange={(e) => setRememberApiKey(e.target.checked)}
                className="w-4 h-4 text-primary-600 border-gray-300 rounded focus:ring-primary-500"
              />
              <label htmlFor="rememberKey" className="text-sm text-gray-600">
                Remember API Key
              </label>
            </div>
          </div>

          <button
            type="submit"
            disabled={loginLoading}
            className="w-full mt-6 px-4 py-3 bg-primary-600 text-white rounded-lg hover:bg-primary-700 transition flex items-center justify-center gap-2 disabled:opacity-50"
          >
            {loginLoading && <i className="fas fa-spinner fa-spin" />}
            <span>{loginLoading ? 'Authenticating...' : 'Login'}</span>
          </button>

          {loginError && (
            <p className="mt-4 text-center text-sm text-red-500">{loginError}</p>
          )}
        </form>

        <p className="mt-6 text-xs text-gray-400 text-center">
          API Key is configured in the collector config file
        </p>
      </div>
    </div>
  );
}
