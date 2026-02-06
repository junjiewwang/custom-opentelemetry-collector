/**
 * Main App Module - Alpine.js 应用主逻辑
 * @module app
 */

import { ApiService } from './api.js';
import { Utils } from './utils.js';
import { Storage } from './storage.js';
import { tasksView } from './views/tasks.js';
import { instancesView } from './views/instances.js';

/**
 * 组件加载器 - 负责异步加载 HTML 模板
 */
const ComponentLoader = {
    cache: {},
    async load(name) {
        if (this.cache[name]) return this.cache[name];
        const response = await fetch(`views/${name}.html?v=${Date.now()}`);
        if (!response.ok) throw new Error(`Failed to load template: ${name}`);
        const html = await response.text();
        this.cache[name] = html;
        return html;
    }
};

/**
 * 菜单配置
 */
const MENU_ITEMS = [
    { id: 'dashboard', label: 'Dashboard', icon: 'fas fa-chart-pie' },
    { id: 'apps', label: 'Applications', icon: 'fas fa-cube' },
    { id: 'instances', label: 'Instances', icon: 'fas fa-server' },
    { id: 'services', label: 'Services', icon: 'fas fa-sitemap' },
    { id: 'tasks', label: 'Tasks', icon: 'fas fa-tasks' },
    { id: 'configs', label: 'Configs', icon: 'fas fa-cog' },
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
        // Mixin - Modules
        // ============================================================================
        ...tasksView(),
        ...instancesView(),

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
        viewTemplates: {}, // 存储已加载的 HTML 模板内容

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
        services: [],
        arthasAgents: [],

        // ============================================================================
        // State - Arthas Management (集成在实例列表中)
        // ============================================================================
        
        // ============================================================================
        // State - Config Management
        // ============================================================================
        configTree: [],
        selectedConfigNode: null,
        editingConfig: {
            appId: '',
            serviceName: '',
            loading: false,
            saving: false,
            content: '', // JSON string
            originalContent: '',
            isDirty: false,
            error: '',
            version: '',
            reference: null, // Full template from server
            missingFields: [], // Fields present in reference but missing in content
            showHint: false, // Whether to show the hint banner
            hintType: '' // 'template' (empty config) or 'missing' (missing fields)
        },
        configSearchQuery: '',
        arthasTask: {
            active: false,       // 是否有任务正在执行
            taskId: '',
            taskType: '',        // 'attach' | 'detach'
            targetAgentId: '',
            status: '',          // pending/running/success/failed/timeout
            message: '',
            startTime: 0,
        },
        arthasSession: {
            active: false,
            agentId: '',
            agentInfo: null,
            ws: null,
            sessionId: null,  // Terminal session ID for terminalManager
            connecting: false,
            error: '',
        },

        // ============================================================================
        // State - 筛选
        // ============================================================================
        taskTreeData: [], // 层级树数据: App -> Service -> Instance -> Task

        // ============================================================================
        // State - 加载状态
        // ============================================================================
        loading: {
            dashboard: false,
            apps: false,
            instances: false,
            services: false,
            tasks: false,
            arthas: false,
        },

        // ============================================================================
        // State - 弹窗
        // ============================================================================
        showCreateAppModal: false,
        showDetailModal: false,
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
            // 预加载任务模块模板（如果当前是任务视图或即将进入）
            if (this.currentView === 'tasks') {
                await this.ensureTemplate('tasks');
            }

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

            // 监听 terminalManager 事件
            this.setupTerminalEventListeners();

            // 初始加载当前视图的数据（确保页面刷新时数据被加载）
            if (this.authenticated && this.currentView) {
                this.onViewChange(this.currentView);
            }
        },

        /**
         * 确保指定视图的模板已加载
         * @param {string} viewName 
         */
        async ensureTemplate(viewName) {
            if (this.viewTemplates[viewName]) return;
            try {
                this.viewTemplates[viewName] = await ComponentLoader.load(viewName);
            } catch (e) {
                console.error(`Failed to load template: ${viewName}`, e);
                this.showToast(`Failed to load view: ${viewName}`, 'error');
            }
        },

        /**
         * 设置 terminalManager 事件监听
         */
        setupTerminalEventListeners() {
            // 防止重复注册
            if (this._terminalEventsSetup) {
                console.log('[App] Terminal event listeners already setup, skipping');
                return;
            }
            this._terminalEventsSetup = true;
            
            console.log('[App] Setting up terminal event listeners');
            // 终端关闭事件 - 清理 arthasSession 状态
            document.addEventListener('terminalClosed', (event) => {
                const { sessionId } = event.detail;
                if (this.arthasSession.sessionId === sessionId) {
                    // Close WebSocket if still open
                    if (this.arthasSession.ws) {
                        try {
                            this.arthasSession.ws.close();
                        } catch (e) {}
                    }
                    // Reset state
                    this.arthasSession.active = false;
                    this.arthasSession.ws = null;
                    this.arthasSession.sessionId = null;
                    this.arthasSession.agentId = '';
                    this.arthasSession.agentInfo = null;
                    this.arthasSession.error = '';
                }
            });

            // 终端最小化事件 - 保持连接但隐藏 UI
            document.addEventListener('terminalMinimized', (event) => {
                const { sessionId } = event.detail;
                if (this.arthasSession.sessionId === sessionId) {
                    // 保持 active 状态，只是隐藏了 UI
                    // 用户可以通过点击 Connect 按钮重新显示
                }
            });

            // 终端 resize 事件 - 可选：发送 resize 命令到服务端
            document.addEventListener('terminalResize', (event) => {
                const { sessionId, cols, rows } = event.detail;
                if (this.arthasSession.sessionId === sessionId && this.arthasSession.ws) {
                    // Arthas 支持 resize，可以发送 resize 命令
                    // 但当前透明 relay 模式下，这需要特殊处理
                    // 暂时不实现
                }
            });

            // 终端输入事件 - 将 JSON 格式的数据通过 WebSocket 发送到服务端
            document.addEventListener('terminalRawData', (event) => {
                const { sessionId, data } = event.detail;
                console.log('[App] Received terminalRawData event, sessionId:', sessionId, 'data:', data);
                console.log('[App] Current arthasSession:', this.arthasSession.sessionId, 'ws:', !!this.arthasSession.ws);
                if (this.arthasSession.sessionId === sessionId && this.arthasSession.ws) {
                    if (this.arthasSession.ws.readyState === WebSocket.OPEN) {
                        console.log('[App] Sending data via WebSocket:', data);
                        this.arthasSession.ws.send(data);
                    } else {
                        console.log('[App] WebSocket not open, readyState:', this.arthasSession.ws.readyState);
                    }
                } else {
                    console.log('[App] Session mismatch or no ws');
                }
            });
        },

        async onViewChange(view) {
            if (!this.authenticated) return;

            // 确保模板已加载
            await this.ensureTemplate(view);

            const loaders = {
                dashboard: () => this.loadDashboard(),
                apps: () => this.loadApps(),
                instances: () => this.loadInstances(),
                services: () => this.loadServices(),
                tasks: () => this.loadTasks(),
                configs: () => this.loadConfigData(),
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
                // Fetch all instances and arthas agents in parallel
                const [instancesRes, arthasRes] = await Promise.allSettled([
                    ApiService.getInstances(''),
                    ApiService.getArthasAgents()
                ]);

                const instancesList = instancesRes.status === 'fulfilled' ? (instancesRes.value.instances || []) : [];
                const tunnelAgents = arthasRes.status === 'fulfilled' ? (arthasRes.value || []) : [];
                
                // Map tunnel agents by agent_id
                const tunnelMap = new Map();
                tunnelAgents.forEach(a => {
                    if (a.agent_id) tunnelMap.set(a.agent_id, a);
                });

                // Enrich instances with arthas status
                this.instances = instancesList.map(inst => {
                    const tunnelInfo = tunnelMap.get(inst.agent_id);
                    return {
                        ...inst,
                        arthasStatus: {
                            state: tunnelInfo ? 'running' : 'stopped',
                            arthasVersion: tunnelInfo?.version || '',
                            tunnelReady: !!tunnelInfo,
                            tunnelAgentId: tunnelInfo?.agent_id || '',
                        },
                        operating: false
                    };
                });
                
                // 更新 arthasAgents (保持兼容)
                this.arthasAgents = this.instances.filter(i => i.arthasStatus?.tunnelReady);
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
        // Replaced by instancesView module

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
            // Show raw TaskInfo data if available
            this.showDetail(`Task: ${task.task_id}`, task._raw || task);
        },

        async submitTask() {
            try {
                // 构建任务数据
                const taskData = {
                    task_type_name: this.newTask.task_type,
                    timeout_millis: this.newTask.timeout_millis || 60000,
                    priority_num: this.newTask.priority || 0,
                };

                // 可选字段
                if (this.newTask.target_agent_id) {
                    taskData.target_agent_id = this.newTask.target_agent_id;
                }

                // 解析 parameters JSON
                if (this.newTask.parameters_json && this.newTask.parameters_json.trim()) {
                    try {
                        taskData.parameters_json = JSON.parse(this.newTask.parameters_json);
                    } catch (parseErr) {
                        this.showToast('Invalid JSON in parameters field', 'error');
                        return;
                    }
                }

                await ApiService.createTask(taskData);
                this.showToast('Task created successfully', 'success');
                this.showCreateTaskModal = false;
                
                // 重置表单
                this.newTask = {
                    task_type: '',
                    target_agent_id: '',
                    timeout_millis: 60000,
                    priority: 0,
                    parameters_json: '',
                };
                
                await this.loadTasks();
            } catch (e) {
                this.handleError(e, 'Failed to create task');
            }
        },

        // ============================================================================
        // Actions - Config Management
        // ============================================================================
        
        /**
         * 加载配置管理页面的基础数据（App 列表和实例列表）
         */
        async loadConfigData() {
            if (this.loading.configs) return;
            this.loading.configs = true;
            try {
                // 同时加载 Apps 和 Instances 来构建树
                const [appsRes, instancesRes] = await Promise.all([
                    ApiService.getApps(),
                    ApiService.getInstances('all')
                ]);
                
                const apps = appsRes.apps || [];
                const instances = instancesRes.instances || [];
                
                // 构建配置树结构: App -> Service -> Instances
                this.configTree = apps.map(app => {
                    const appInstances = instances.filter(inst => inst.app_id === app.id);
                    
                    // 按服务分组
                    const serviceMap = new Map();
                    appInstances.forEach(inst => {
                        const svcName = inst.service_name || '_unknown_';
                        if (!serviceMap.has(svcName)) {
                            serviceMap.set(svcName, []);
                        }
                        serviceMap.get(svcName).push(inst);
                    });

                    const services = Array.from(serviceMap.entries()).map(([name, svcInstances]) => ({
                        id: `svc-${app.id}-${name}`,
                        name: name === '_unknown_' ? 'Unknown Service' : name,
                        serviceName: name,
                        appId: app.id,
                        type: 'service',
                        expanded: false,
                        instances: svcInstances.map(inst => ({
                            id: inst.agent_id,
                            name: inst.hostname || inst.ip || inst.agent_id.substring(0, 8),
                            type: 'instance',
                            appId: app.id,
                            serviceName: name,
                            agentId: inst.agent_id,
                            status: inst.status?.state || 'offline'
                        }))
                    }));

                    return {
                        id: app.id,
                        name: app.name,
                        type: 'app',
                        expanded: true,
                        services: services
                    };
                });
            } catch (e) {
                this.handleError(e, 'Failed to load config tree');
            } finally {
                this.loading.configs = false;
            }
        },

        /**
         * 选择一个配置节点（App, Service 或 Instance）
         */
        async selectConfigNode(node) {
            if (this.editingConfig.isDirty) {
                if (!confirm('You have unsaved changes. Discard them?')) return;
            }

            this.selectedConfigNode = node;
            this.editingConfig.error = '';
            this.editingConfig.loading = true;
            this.editingConfig.isDirty = false;
            this.editingConfig.version = '';

            try {
                let configRes;
                if (node.type === 'app') {
                    this.editingConfig.isAppDefault = true;
                    this.editingConfig.isServiceLevel = false;
                    this.editingConfig.appId = node.id;
                    this.editingConfig.serviceName = '';
                    this.editingConfig.agentId = '';
                    configRes = await ApiService.getAppDefaultConfig(node.id);
                } else if (node.type === 'service') {
                    this.editingConfig.isAppDefault = false;
                    this.editingConfig.isServiceLevel = true;
                    this.editingConfig.appId = node.appId;
                    this.editingConfig.serviceName = node.serviceName;
                    this.editingConfig.agentId = '';
                    configRes = await ApiService.getAppServiceConfig(node.appId, node.serviceName);
                } else {
                    this.editingConfig.isAppDefault = false;
                    this.editingConfig.isServiceLevel = false;
                    this.editingConfig.appId = node.appId;
                    this.editingConfig.serviceName = node.serviceName;
                    this.editingConfig.agentId = node.agentId;
                    configRes = await ApiService.getAppInstanceConfig(node.appId, node.agentId, node.serviceName);
                }

                // V2 API returns { config, reference }
                const responseData = configRes || {};
                const fullConfig = responseData.config || responseData || {};
                const reference = responseData.reference || null;
                
                this.editingConfig.reference = reference;
                
                // 提取元数据
                this.editingConfig.version = fullConfig.version || '';
                this.editingConfig.updatedAt = fullConfig.updated_at || 0;
                
                // 提取业务配置（过滤掉元数据字段，避免干扰用户编辑）
                const businessConfig = { ...fullConfig };
                delete businessConfig.version;
                delete businessConfig.updated_at;
                delete businessConfig.etag;
                
                const jsonStr = JSON.stringify(businessConfig, null, 2);
                this.editingConfig.content = jsonStr;
                this.editingConfig.originalContent = jsonStr;

                // Check for hints
                this.updateConfigHints(businessConfig, reference);
            } catch (e) {
                // 如果是 404，说明没有配置，显示空对象
                if (e.status === 404) {
                    this.editingConfig.content = '{}';
                    this.editingConfig.originalContent = '{}';
                    this.editingConfig.version = 'none';
                } else {
                    this.handleError(e, 'Failed to load configuration');
                    this.editingConfig.error = e.message;
                }
            } finally {
                this.editingConfig.loading = false;
            }
        },

        /**
         * 更新配置提示（模板推荐或缺失字段提醒）
         */
        updateConfigHints(current, reference) {
            if (!reference) {
                this.editingConfig.showHint = false;
                return;
            }

            // Case 1: Empty or skeleton config (version "0" or empty object)
            const isEmpty = Object.keys(current).length === 0 || this.editingConfig.version === "0";
            if (isEmpty) {
                this.editingConfig.showHint = true;
                this.editingConfig.hintType = 'template';
                this.editingConfig.missingFields = [];
                return;
            }

            // Case 2: Check for missing top-level fields compared to reference
            const missing = [];
            for (const key in reference) {
                if (key === 'version' || key === 'updated_at' || key === 'etag') continue;
                if (!(key in current)) {
                    missing.push(key);
                }
            }

            if (missing.length > 0) {
                this.editingConfig.showHint = true;
                this.editingConfig.hintType = 'missing';
                this.editingConfig.missingFields = missing;
            } else {
                this.editingConfig.showHint = false;
            }
        },

        /**
         * 应用推荐模板
         */
        applyConfigTemplate() {
            if (!this.editingConfig.reference) return;
            if (this.editingConfig.content !== '{}' && !confirm('Overwriting existing configuration with template. Continue?')) return;
            
            const template = { ...this.editingConfig.reference };
            delete template.version;
            delete template.updated_at;
            delete template.etag;
            
            this.editingConfig.content = JSON.stringify(template, null, 2);
            this.checkConfigDirty();
            this.editingConfig.showHint = false;
            this.showToast('Template applied. Remember to save changes.', 'info');
        },

        /**
         * 补全缺失字段
         */
        fillMissingConfigFields() {
            if (!this.editingConfig.reference || this.editingConfig.missingFields.length === 0) return;
            
            try {
                const current = JSON.parse(this.editingConfig.content);
                this.editingConfig.missingFields.forEach(field => {
                    if (this.editingConfig.reference[field] !== undefined) {
                        current[field] = this.editingConfig.reference[field];
                    }
                });
                
                this.editingConfig.content = JSON.stringify(current, null, 2);
                this.checkConfigDirty();
                this.editingConfig.showHint = false;
                this.showToast('Missing fields added from template.', 'success');
            } catch (e) {
                this.showToast('Error parsing current JSON. Fix it before filling fields.', 'error');
            }
        },

        /**
         * 检查配置是否已修改
         */
        checkConfigDirty() {
            this.editingConfig.isDirty = this.editingConfig.content !== this.editingConfig.originalContent;
        },

        /**
         * 保存配置
         */
        async saveConfig() {
            if (this.editingConfig.saving) return;
            
            let configData;
            try {
                configData = JSON.parse(this.editingConfig.content);
            } catch (e) {
                this.showToast('Invalid JSON format', 'error');
                return;
            }

            this.editingConfig.saving = true;
            try {
                await ApiService.setAppServiceConfig(this.editingConfig.appId, this.editingConfig.serviceName, configData);
                
                this.editingConfig.originalContent = this.editingConfig.content;
                this.editingConfig.isDirty = false;
                this.showToast('Configuration saved successfully', 'success');
            } catch (e) {
                this.handleError(e, 'Failed to save configuration');
            } finally {
                this.editingConfig.saving = false;
            }
        },

        /**
         * 重置当前编辑的配置
         */
        resetConfigEditor() {
            if (!confirm('Reset changes to original?')) return;
            this.editingConfig.content = this.editingConfig.originalContent;
            this.editingConfig.isDirty = false;
        },

        /**
         * 删除服务配置
         */
        async deleteOverrideConfig() {
            if (!confirm(`Delete configuration for service "${this.editingConfig.serviceName}"?`)) return;

            this.editingConfig.saving = true;
            try {
                await ApiService.deleteAppServiceConfig(this.editingConfig.appId, this.editingConfig.serviceName);
                this.showToast(`Configuration for service "${this.editingConfig.serviceName}" deleted`, 'success');
                // 重新加载以获取空配置
                await this.selectConfigNode(this.selectedConfigNode);
            } catch (e) {
                this.handleError(e, `Failed to delete service configuration`);
            } finally {
                this.editingConfig.saving = false;
            }
        },

        // ============================================================================
        // Actions - Arthas
        // ============================================================================

        /**
         * 刷新单个实例的 Arthas 状态
         * 
         * 设计说明：
         * - 通过 tunnel 列表判断 agent 是否在线
         * - tunnel 连接即表示 Arthas 运行中，不再调用 getAgentArthasStatus
         */
        async refreshInstanceArthasStatus(agentId) {
            const inst = this.instances.find(i => i.agent_id === agentId);
            if (!inst) return;

            try {
                // 获取 tunnel 状态，通过 agent_id 匹配
                let tunnelInfo = null;
                try {
                    const tunnelAgents = await ApiService.getArthasAgents();
                    tunnelInfo = (tunnelAgents || []).find(a => a.agent_id === agentId);
                } catch (e) {}

                if (tunnelInfo) {
                    // tunnel 已连接，Arthas 运行中
                    inst.arthasStatus = {
                        state: 'running',
                        arthasVersion: tunnelInfo.version || '',
                        tunnelReady: true,
                        tunnelAgentId: tunnelInfo.agent_id,
                    };
                } else {
                    // tunnel 未连接
                    inst.arthasStatus = {
                        state: 'stopped',
                        arthasVersion: inst.arthasStatus?.arthasVersion || '',
                        tunnelReady: false,
                        tunnelAgentId: '',
                    };
                }

                // 更新 arthasAgents
                this.arthasAgents = this.instances.filter(i => i.arthasStatus?.tunnelReady);
            } catch (e) {
                console.error('Failed to refresh Arthas status:', e);
            }
        },

        /**
         * Attach Arthas 到指定实例
         * @returns {Promise<boolean>} 是否成功 attach 并连接 tunnel
         */
        async attachArthas(instance) {
            if (this.arthasTask.active) {
                this.showToast('Another task is running', 'warning');
                return false;
            }

            instance.operating = true;
            const shortId = instance.agent_id.substring(0, 8);
            this.arthasTask = {
                active: true,
                taskId: '',
                taskType: 'attach',
                targetAgentId: instance.agent_id,
                status: 'pending',
                message: `Creating attach task for ${shortId}...`,
                startTime: Date.now(),
            };

            try {
                // 1. 创建 attach 任务
                const taskRes = await ApiService.createTask({
                    task_type_name: 'arthas_attach',
                    target_agent_id: instance.agent_id,
                    parameters_json: { action: 'attach' },
                    timeout_millis: 60000,
                });

                this.arthasTask.taskId = taskRes.task_id;
                this.arthasTask.message = `Task created: ${taskRes.task_id.substring(0, 8)}`;

                // 2. 轮询任务状态
                await this.pollTaskStatus(taskRes.task_id, `Arthas Attach [${shortId}]`);

                // 3. 成功后刷新状态（带重试，等待 tunnel 连接）
                if (this.arthasTask.status === 'success') {
                    this.showToast(`Arthas attached to ${shortId} successfully, waiting for tunnel...`, 'success');
                    return await this.waitForTunnelConnection(instance, 30);
                }
                return false;
            } catch (e) {
                this.arthasTask.status = 'failed';
                this.arthasTask.message = e.message || 'Attach failed';
                this.showToast(this.arthasTask.message, 'error');
                return false;
            } finally {
                instance.operating = false;
            }
        },

        /**
         * Detach Arthas 从指定实例
         */
        async detachArthas(instance) {
            if (this.arthasTask.active) {
                this.showToast('Another task is running', 'warning');
                return;
            }

            instance.operating = true;
            const shortId = instance.agent_id.substring(0, 8);
            this.arthasTask = {
                active: true,
                taskId: '',
                taskType: 'detach',
                targetAgentId: instance.agent_id,
                status: 'pending',
                message: `Creating detach task for ${shortId}...`,
                startTime: Date.now(),
            };

            try {
                // 1. 创建 detach 任务
                const taskRes = await ApiService.createTask({
                    task_type_name: 'arthas_detach',
                    target_agent_id: instance.agent_id,
                    parameters_json: { action: 'detach' },
                    timeout_millis: 30000,
                });

                this.arthasTask.taskId = taskRes.task_id;
                this.arthasTask.message = `Task created: ${taskRes.task_id.substring(0, 8)}`;

                // 2. 轮询任务状态
                await this.pollTaskStatus(taskRes.task_id, `Arthas Detach [${shortId}]`);

                // 3. 成功后刷新状态
                if (this.arthasTask.status === 'success') {
                    this.showToast(`Arthas detached from ${shortId} successfully`, 'success');
                    await new Promise(r => setTimeout(r, 1000));
                    await this.refreshInstanceArthasStatus(instance.agent_id);
                }
            } catch (e) {
                this.arthasTask.status = 'failed';
                this.arthasTask.message = e.message || 'Detach failed';
                this.showToast(this.arthasTask.message, 'error');
            } finally {
                instance.operating = false;
            }
        },

        /**
         * 轮询任务状态
         */
        async pollTaskStatus(taskId, taskName) {
            const maxRetries = 60; // 最多等待 60 秒
            let retries = 0;

            while (retries < maxRetries) {
                await new Promise(r => setTimeout(r, 1000));
                retries++;

                try {
                    const taskInfo = await ApiService.getTask(taskId);
                    const statusNum = (typeof taskInfo?.status === 'number') ? taskInfo.status : 0;
                    const statusStr = this.taskStatusToString(statusNum);

                    this.arthasTask.status = statusStr;
                    this.arthasTask.message = `${taskName}: ${statusStr} (${retries}s)`;

                    if (statusStr === 'success') {
                        this.arthasTask.message = `${taskName} completed successfully`;
                        this.arthasTask.active = false;
                        return;
                    } else if (statusStr === 'failed' || statusStr === 'timeout' || statusStr === 'cancelled') {
                        const errMsg = taskInfo.error_message || taskInfo.error || `${taskName} ${statusStr}`;
                        this.arthasTask.message = errMsg;
                        this.arthasTask.active = false;
                        throw new Error(errMsg);
                    }
                } catch (e) {
                    if (e.status === 404) {
                        // 任务可能还没创建完成，继续等待
                        this.arthasTask.message = `Waiting for task... (${retries}s)`;
                    } else {
                        throw e;
                    }
                }
            }

            // 超时
            this.arthasTask.status = 'timeout';
            this.arthasTask.message = `${taskName} timeout after ${maxRetries}s`;
            this.arthasTask.active = false;
            throw new Error(this.arthasTask.message);
        },

        /**
         * 等待 Tunnel 连接成功（带重试）
         * Arthas attach 成功后，需要等待 Arthas 连接到 tunnel server
         * 
         * 设计说明：
         * - 后端返回的字段是 agent_id, version 等（snake_case）
         * - tunnel 连接即表示 Arthas 运行中
         */
        async waitForTunnelConnection(instance, maxWaitSeconds = 30) {
            const pollInterval = 2000; // 每 2 秒检查一次
            const maxRetries = Math.ceil(maxWaitSeconds * 1000 / pollInterval);

            this.arthasTask.message = 'Waiting for tunnel connection...';

            for (let i = 0; i < maxRetries; i++) {
                await new Promise(r => setTimeout(r, pollInterval));

                let tunnelAgents;
                try {
                    tunnelAgents = await ApiService.getArthasAgents();
                } catch (e) {
                    // 网络/服务端错误：继续等待，但不要中断流程
                    this.arthasTask.message = `Waiting for tunnel connection... (${i + 1}/${maxRetries})`;
                    continue;
                }

                // 通过 agent_id 匹配（后端返回 snake_case 字段）
                const tunnelInfo = (tunnelAgents || []).find(a => a.agent_id === instance.agent_id);
                if (!tunnelInfo) {
                    this.arthasTask.message = `Waiting for tunnel connection... (${i + 1}/${maxRetries})`;
                    continue;
                }

                // tunnel 已连接：更新当前 instance（避免引用漂移）
                instance.arthasStatus = {
                    state: 'running',
                    arthasVersion: tunnelInfo.version || '',
                    tunnelReady: true,
                    tunnelAgentId: tunnelInfo.agent_id,
                };

                // 同步更新全局 instances 列表（如果存在）
                const inst = (this.instances || []).find(x => x.agent_id === instance.agent_id);
                if (inst) {
                    inst.arthasStatus = { ...instance.arthasStatus };
                }

                // 更新 arthasAgents
                this.arthasAgents = (this.instances || []).filter(x => x.arthasStatus?.tunnelReady);

                this.arthasTask.message = 'Tunnel connected successfully';
                this.showToast('Arthas tunnel connected', 'success');
                return true;
            }

            // 超时但不报错，只是提示用户手动刷新
            this.arthasTask.message = 'Tunnel connection timeout. Please click Refresh to check status.';
            this.showToast('Tunnel connection timeout. Please refresh manually.', 'warning');
            return false;
        },

        /**
         * 关闭任务状态面板
         */
        closeTaskPanel() {
            if (!this.arthasTask.active) {
                this.arthasTask = {
                    active: false,
                    taskId: '',
                    taskType: '',
                    targetAgentId: '',
                    status: '',
                    message: '',
                    startTime: 0,
                };
            }
        },

        /**
         * 连接 Arthas Terminal
         * 逻辑：如果未注册则先 attach，注册后连接；如果已注册直接连接
         */
        async openArthas(instance) {
            if (instance.operating || this.arthasSession.connecting) return;

            // 如果已经连接了 tunnel，直接 connect
            if (instance.arthasStatus?.tunnelReady) {
                return this.connectArthas(instance);
            }

            // 否则，先尝试 attach
            try {
                const shortId = instance.agent_id.substring(0, 8);
                this.showToast(`Attaching Arthas to ${shortId}...`, 'info');
                const attached = await this.attachArthas(instance);
                
                if (attached && instance.arthasStatus?.tunnelReady) {
                    // 等待一小会儿确保状态同步
                    await new Promise(r => setTimeout(r, 500));
                    return this.connectArthas(instance);
                }
            } catch (e) {
                console.error('[Arthas] Auto-attach and connect failed:', e);
                // Error toast is already shown by attachArthas or pollTaskStatus
            }
        },

        async connectArthas(instance) {
            if (this.arthasSession.connecting) return;
            
            // 前置检查：tunnel 必须就绪
            if (!instance.arthasStatus?.tunnelReady) {
                this.showToast('Tunnel not ready. Please attach Arthas first.', 'warning');
                return;
            }

            // 使用 tunnel 的 agentId（Arthas 注册的 ID），而不是 OTel agent ID
            const tunnelAgentId = instance.arthasStatus?.tunnelAgentId;
            if (!tunnelAgentId) {
                this.showToast('Tunnel agent ID not found. Please refresh and try again.', 'error');
                return;
            }

            this.arthasSession.connecting = true;
            this.arthasSession.error = '';
            this.arthasSession.agentId = tunnelAgentId; // 使用 tunnel agentId
            this.arthasSession.agentInfo = instance;

            try {
                // 直接建立 WebSocket 连接（不再创建 attach 任务）
                await this.connectArthasWebSocket(tunnelAgentId, instance);
            } catch (e) {
                this.arthasSession.error = e.message || 'Failed to connect Arthas';
                this.showToast(this.arthasSession.error, 'error');
            } finally {
                this.arthasSession.connecting = false;
            }
        },

        async connectArthasWebSocket(agentId, instance) {
            // Step 1: Get a short-lived WS token (secure, API key in header)
            const tokenResponse = await ApiService.request('POST', '/auth/ws-token', { purpose: 'arthas_terminal' });

            if (!tokenResponse.token) {
                throw new Error('Failed to obtain WebSocket token');
            }

            // Step 2: Generate sessionId for terminalManager
            const sessionId = `arthas-${agentId}-${Date.now()}`;
            this.arthasSession.sessionId = sessionId;

            // Step 3: Connect WebSocket with the short-lived token (not API key)
            const protocol = window.location.protocol === 'https:' ? 'wss:' : 'ws:';
            const qs = new URLSearchParams({
                method: 'connectArthas',
                id: agentId,
                token: tokenResponse.token,
                agent_id: agentId,
            });
            const wsUrl = `${protocol}//${window.location.host}/api/v2/arthas/ws?${qs.toString()}`;

            return new Promise((resolve, reject) => {
                const ws = new WebSocket(wsUrl);
                ws.binaryType = 'arraybuffer';

                ws.onopen = () => {
                    this.arthasSession.ws = ws;
                    this.arthasSession.active = true;

                    // Create terminal UI using terminalManager
                    const serviceName = instance.service_name || 'unknown';
                    const ip = instance.ip || '';
                    window.terminalManager.createTerminal(sessionId, serviceName, ip);
                    
                    // Bind WebSocket to terminal
                    window.terminalManager.setWebSocket(sessionId, ws);

                    resolve();
                };

                ws.onmessage = (event) => {
                    // Write data to terminal (transparent relay)
                    if (event.data instanceof ArrayBuffer) {
                        const text = new TextDecoder('utf-8').decode(new Uint8Array(event.data));
                        window.terminalManager.writeDataBySessionId(sessionId, text);
                    } else if (typeof event.data === 'string') {
                        window.terminalManager.writeDataBySessionId(sessionId, event.data);
                    } else if (event.data instanceof Blob) {
                        event.data.arrayBuffer().then(buf => {
                            const text = new TextDecoder('utf-8').decode(new Uint8Array(buf));
                            window.terminalManager.writeDataBySessionId(sessionId, text);
                        });
                    }
                };

                ws.onerror = () => {
                    this.arthasSession.error = 'WebSocket connection error';
                    reject(new Error('WebSocket connection error'));
                };

                ws.onclose = (event) => {
                    const closedAgentId = this.arthasSession.agentInfo?.agent_id;
                    this.arthasSession.active = false;
                    this.arthasSession.ws = null;
                    
                    // Remove WebSocket binding from terminal
                    window.terminalManager.removeWebSocket(sessionId);
                    
                    // Write close message to terminal
                    const reason = event.reason ? `, reason: ${event.reason}` : '';
                    window.terminalManager.writeDataBySessionId(sessionId, 
                        `\r\n\x1b[33m[System] Connection closed (code: ${event.code}${reason})\x1b[0m\r\n`);

                    // 同步刷新实例状态，确保 UI 上的圆点及时变灰/变绿
                    if (closedAgentId) {
                        setTimeout(() => this.refreshInstanceArthasStatus(closedAgentId), 500);
                    }
                };

                // Connection timeout protection
                setTimeout(() => {
                    if (ws.readyState !== WebSocket.OPEN) {
                        try { ws.close(); } catch (e) {}
                        reject(new Error('WebSocket connect timeout'));
                    }
                }, 15000);
            });
        },

        disconnectArthas() {
            // Close WebSocket
            if (this.arthasSession.ws) {
                try {
                    this.arthasSession.ws.close();
                } catch (e) {
                    // ignore
                }
            }
            
            // Close terminal UI
            if (this.arthasSession.sessionId) {
                window.terminalManager.closeTerminalBySessionId(this.arthasSession.sessionId);
            }
            
            // Reset state
            this.arthasSession.active = false;
            this.arthasSession.ws = null;
            this.arthasSession.sessionId = null;
            this.arthasSession.agentId = '';
            this.arthasSession.agentInfo = null;
            this.arthasSession.error = '';
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
        formatRelativeTime: Utils.formatRelativeTime.bind(Utils),
        formatShortId: Utils.formatShortId.bind(Utils),
        formatUptime: Utils.formatUptime.bind(Utils),
        truncate: Utils.truncate.bind(Utils),

        copyToClipboard(text) {
            Utils.copyToClipboard(text);
            this.showToast('Copied to clipboard', 'success');
        },

        // ============================================================================
        // Status Helpers (For UI)
        // ============================================================================
        getStatusClass(status) {
            switch(status) {
                case 'running': return "bg-blue-100 text-blue-700";
                case 'pending': return "bg-yellow-100 text-yellow-700";
                case 'success': return "bg-green-100 text-green-700";
                case 'failed': return "bg-red-100 text-red-700";
                case 'timeout': return "bg-orange-100 text-orange-700";
                default: return "bg-gray-100 text-gray-700";
            }
        },

        getStatusIcon(status) {
            switch(status) {
                case 'running': return "fas fa-spinner fa-spin";
                case 'success': return "fas fa-check";
                case 'failed': return "fas fa-times";
                case 'timeout': return "fas fa-clock";
                default: return "fas fa-question";
            }
        },

        getStatusIconClass(status) {
            switch(status) {
                case 'running': return "bg-blue-50 text-blue-600";
                case 'success': return "bg-green-50 text-green-600";
                case 'failed': return "bg-red-50 text-red-600";
                case 'timeout': return "bg-orange-50 text-orange-600";
                default: return "bg-gray-50 text-gray-600";
            }
        }
    };
}
