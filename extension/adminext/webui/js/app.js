/**
 * Main App Module - Alpine.js 应用主逻辑
 * @module app
 */

import { ApiService } from './api.js';
import { Utils } from './utils.js';
import { Storage } from './storage.js';

/**
 * 菜单配置
 */
const MENU_ITEMS = [
    { id: 'dashboard', label: 'Dashboard', icon: 'fas fa-chart-pie' },
    { id: 'apps', label: 'Applications', icon: 'fas fa-cube' },
    { id: 'instances', label: 'Instances', icon: 'fas fa-server' },
    { id: 'services', label: 'Services', icon: 'fas fa-sitemap' },
    { id: 'tasks', label: 'Tasks', icon: 'fas fa-tasks' },
];

/**
 * 自动刷新间隔（毫秒）
 */
const AUTO_REFRESH_INTERVAL = 30000;

/**
 * Toast 显示时长（毫秒）
 */
const TOAST_DURATION = 3000;

/**
 * Token 最大长度（与后端 MaxTokenLength 保持一致）
 */
const MAX_TOKEN_LENGTH = 64;

/**
 * 创建 Admin App 实例
 * @returns {Object} Alpine.js data object
 */
export function adminApp() {
    return {
        // ============================================================================
        // Constants (exposed to template)
        // ============================================================================
        MAX_TOKEN_LENGTH,

        // ============================================================================
        // State - 认证
        // ============================================================================
        authenticated: false,
        apiKey: '',
        apiKeyInput: '',
        showApiKey: false,
        rememberApiKey: true,
        loginLoading: false,
        loginError: '',

        // ============================================================================
        // State - 导航
        // ============================================================================
        currentView: 'dashboard',
        menuItems: MENU_ITEMS,

        // ============================================================================
        // State - 连接状态
        // ============================================================================
        connected: true,

        // ============================================================================
        // State - 数据
        // ============================================================================
        dashboard: {},
        apps: [],
        instances: [],
        instanceStats: {},
        services: [],
        tasks: [],

        // ============================================================================
        // State - 筛选
        // ============================================================================
        instanceFilter: '',

        // ============================================================================
        // State - 加载状态
        // ============================================================================
        loading: {
            dashboard: false,
            apps: false,
            instances: false,
            services: false,
            tasks: false,
        },

        // ============================================================================
        // State - 弹窗
        // ============================================================================
        showCreateAppModal: false,
        showDetailModal: false,
        showCreateTaskModal: false,
        showSetTokenModal: false,
        detailTitle: '',
        detailData: null,

        // ============================================================================
        // State - 表单
        // ============================================================================
        newApp: { name: '', description: '' },
        setTokenApp: null,
        customToken: '',

        // ============================================================================
        // State - Toast
        // ============================================================================
        toast: { show: false, message: '', type: 'info' },

        // ============================================================================
        // Lifecycle
        // ============================================================================
        async init() {
            // 尝试自动登录
            const savedKey = Storage.getApiKey();
            if (savedKey) {
                this.apiKeyInput = savedKey;
                await this.login(true);
            }

            // 监听视图变化自动加载数据
            this.$watch('currentView', (view) => this.onViewChange(view));

            // 自动刷新定时器
            setInterval(() => this.autoRefresh(), AUTO_REFRESH_INTERVAL);
        },

        onViewChange(view) {
            if (!this.authenticated) return;

            const loaders = {
                dashboard: () => this.loadDashboard(),
                apps: () => this.loadApps(),
                instances: () => this.loadInstances(),
                services: () => this.loadServices(),
                tasks: () => this.loadTasks(),
            };

            if (loaders[view]) loaders[view]();
        },

        autoRefresh() {
            if (!this.authenticated) return;
            if (this.currentView === 'dashboard') this.loadDashboard();
            if (this.currentView === 'instances') this.loadInstances();
        },

        // ============================================================================
        // Auth
        // ============================================================================
        async login(silent = false) {
            if (!silent) this.loginLoading = true;
            this.loginError = '';

            try {
                ApiService.setApiKey(this.apiKeyInput);
                await ApiService.getDashboard();

                this.apiKey = this.apiKeyInput;
                this.authenticated = true;

                if (this.rememberApiKey) {
                    Storage.setApiKey(this.apiKey);
                }

                // 加载初始数据
                await this.loadDashboard();
                await this.loadApps();
            } catch (e) {
                if (!silent) {
                    this.loginError = e.status === 401 ? 'Invalid API Key' : e.message;
                }
                Storage.removeApiKey();
            } finally {
                this.loginLoading = false;
            }
        },

        logout() {
            this.authenticated = false;
            this.apiKey = '';
            this.apiKeyInput = '';
            ApiService.setApiKey('');
            Storage.removeApiKey();

            // 清空数据
            this.dashboard = {};
            this.apps = [];
            this.instances = [];
            this.services = [];
            this.tasks = [];
            this.instanceStats = {};
        },

        // ============================================================================
        // Data Loaders
        // ============================================================================
        async loadDashboard() {
            if (this.loading.dashboard) return;
            this.loading.dashboard = true;
            try {
                this.dashboard = await ApiService.getDashboard();
                this.connected = true;
            } catch (e) {
                this.handleError(e, 'Failed to load dashboard');
            } finally {
                this.loading.dashboard = false;
            }
        },

        async loadApps() {
            if (this.loading.apps) return;
            this.loading.apps = true;
            try {
                const res = await ApiService.getApps();
                this.apps = res.apps || [];
            } catch (e) {
                this.handleError(e, 'Failed to load apps');
            } finally {
                this.loading.apps = false;
            }
        },

        async loadInstances() {
            if (this.loading.instances) return;
            this.loading.instances = true;
            try {
                const [instancesRes, statsRes] = await Promise.all([
                    ApiService.getInstances(this.instanceFilter),
                    ApiService.getInstanceStats(),
                ]);
                this.instances = instancesRes.instances || [];
                this.instanceStats = statsRes;
            } catch (e) {
                this.handleError(e, 'Failed to load instances');
            } finally {
                this.loading.instances = false;
            }
        },

        async loadServices() {
            if (this.loading.services) return;
            this.loading.services = true;
            try {
                const res = await ApiService.getServices();
                this.services = res.services || [];
            } catch (e) {
                this.handleError(e, 'Failed to load services');
            } finally {
                this.loading.services = false;
            }
        },

        async loadTasks() {
            if (this.loading.tasks) return;
            this.loading.tasks = true;
            try {
                const res = await ApiService.getTasks();
                this.tasks = res.tasks || [];
            } catch (e) {
                this.handleError(e, 'Failed to load tasks');
            } finally {
                this.loading.tasks = false;
            }
        },

        // ============================================================================
        // Actions - Apps
        // ============================================================================
        async createApp() {
            try {
                await ApiService.createApp(this.newApp);
                this.showToast('Application created successfully', 'success');
                this.showCreateAppModal = false;
                this.newApp = { name: '', description: '' };
                await this.loadApps();
            } catch (e) {
                this.handleError(e, 'Failed to create app');
            }
        },

        openSetTokenModal(app) {
            this.setTokenApp = app;
            this.customToken = '';
            this.showSetTokenModal = true;
        },

        async setCustomToken() {
            if (!this.setTokenApp || !this.customToken) return;
            try {
                await ApiService.setToken(this.setTokenApp.id, this.customToken);
                this.showToast('Token updated successfully', 'success');
                this.showSetTokenModal = false;
                this.setTokenApp = null;
                this.customToken = '';
                await this.loadApps();
            } catch (e) {
                this.handleError(e, 'Failed to set token');
            }
        },

        async regenerateTokenInModal() {
            if (!this.setTokenApp) return;
            if (!confirm(`Generate a new random token for "${this.setTokenApp.name}"? This will invalidate the current token.`)) return;
            try {
                await ApiService.regenerateToken(this.setTokenApp.id);
                this.showToast('Token regenerated successfully', 'success');
                this.showSetTokenModal = false;
                this.setTokenApp = null;
                this.customToken = '';
                await this.loadApps();
            } catch (e) {
                this.handleError(e, 'Failed to regenerate token');
            }
        },

        async confirmDeleteApp(app) {
            if (!confirm(`Delete "${app.name}"? This action cannot be undone.`)) return;
            try {
                await ApiService.deleteApp(app.id);
                this.showToast('Application deleted successfully', 'success');
                await this.loadApps();
            } catch (e) {
                this.handleError(e, 'Failed to delete app');
            }
        },

        viewAppDetail(app) {
            this.showDetail(`App: ${app.name}`, app);
        },

        editApp(app) {
            this.showToast('Edit feature coming soon', 'info');
        },

        // ============================================================================
        // Actions - Instances
        // ============================================================================
        async kickInstance(instance) {
            if (!confirm(`Kick instance "${Utils.truncate(instance.agent_id)}"?`)) return;
            try {
                await ApiService.kickInstance(instance.agent_id);
                this.showToast('Instance kicked successfully', 'success');
                await this.loadInstances();
            } catch (e) {
                this.handleError(e, 'Failed to kick instance');
            }
        },

        viewInstanceDetail(instance) {
            this.showDetail(`Instance: ${Utils.truncate(instance.agent_id)}`, instance);
        },

        // ============================================================================
        // Actions - Services
        // ============================================================================
        viewServiceInstances(service) {
            this.currentView = 'instances';
        },

        // ============================================================================
        // Actions - Tasks
        // ============================================================================
        async cancelTask(task) {
            if (!confirm(`Cancel task "${task.task_id}"?`)) return;
            try {
                await ApiService.cancelTask(task.task_id);
                this.showToast('Task cancelled successfully', 'success');
                await this.loadTasks();
            } catch (e) {
                this.handleError(e, 'Failed to cancel task');
            }
        },

        viewTaskDetail(task) {
            this.showDetail(`Task: ${task.task_id}`, task);
        },

        // ============================================================================
        // UI Helpers
        // ============================================================================
        showDetail(title, data) {
            this.detailTitle = title;
            this.detailData = data;
            this.showDetailModal = true;
        },

        showToast(message, type = 'info') {
            this.toast = { show: true, message, type };
            setTimeout(() => { this.toast.show = false; }, TOAST_DURATION);
        },

        handleError(e, defaultMsg) {
            if (e.status === 401) {
                this.logout();
                this.showToast('Session expired, please login again', 'error');
            } else {
                this.connected = false;
                this.showToast(e.message || defaultMsg, 'error');
            }
        },

        // ============================================================================
        // Utility Proxies
        // ============================================================================
        formatDate: Utils.formatDate.bind(Utils),
        formatTimestamp: Utils.formatTimestamp.bind(Utils),
        formatUptime: Utils.formatUptime.bind(Utils),
        truncate: Utils.truncate.bind(Utils),

        copyToClipboard(text) {
            Utils.copyToClipboard(text);
            this.showToast('Copied to clipboard', 'success');
        },
    };
}
