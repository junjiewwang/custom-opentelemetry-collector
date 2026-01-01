/**
 * Main Entry Point - 应用入口
 * @module main
 */

import { adminApp } from './app.js';

// 将 adminApp 注册到全局，供 Alpine.js 使用
window.adminApp = adminApp;

// 使用 Alpine 的 alpine:init 事件注册 data 组件
document.addEventListener('alpine:init', () => {
    Alpine.data('adminApp', adminApp);
    console.log('[OTel Admin] Alpine component registered');
});

// Tailwind 配置（需要在 tailwindcss 加载后立即配置）
if (window.tailwind) {
    window.tailwind.config = {
        theme: {
            extend: {
                colors: {
                    primary: {
                        50: '#eff6ff',
                        100: '#dbeafe',
                        200: '#bfdbfe',
                        300: '#93c5fd',
                        400: '#60a5fa',
                        500: '#3b82f6',
                        600: '#2563eb',
                        700: '#1d4ed8',
                        800: '#1e40af',
                        900: '#1e3a8a',
                    },
                    success: {
                        500: '#22c55e',
                        600: '#16a34a',
                    },
                    warning: {
                        500: '#f59e0b',
                        600: '#d97706',
                    },
                    danger: {
                        500: '#ef4444',
                        600: '#dc2626',
                    },
                },
            },
        },
    };
}

console.log('[OTel Admin] WebUI module loaded');
