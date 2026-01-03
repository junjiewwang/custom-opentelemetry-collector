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
    { id: 'arthas', label: 'Arthas', icon: 'fas fa-terminal' },
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
        groupedTasks: [],
        arthasAgents: [],

        // ============================================================================
        // State - Arthas Terminal
        // ============================================================================
        arthasSession: {
            active: false,
            agentId: '',
            agentInfo: null,
            ws: null,
            sessionId: null,  // Terminal session ID
            output: '',
            inputCommand: '',
            connecting: false,
            error: '',
        },

        // ============================================================================
        // State - 筛选
        // ============================================================================
        instanceFilter: '',
        taskViewMode: 'grouped', // 'grouped' or 'flat'

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
        showCreateTaskModal: false,
        showSetTokenModal: false,
        detailTitle: '',
        detailData: null,

        // ============================================================================
        // State - 表单
        // ============================================================================
        newApp: { name: '', description: '' },
        newTask: {
            task_type: '',
            target_agent_id: '',
            timeout_millis: 60000,
            priority: 0,
            parameters_json: '',
        },
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
                arthas: () => this.loadArthasAgents(),
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
                
                // Transform TaskInfo to flat structure for display
                // TaskInfo: { task: {...}, status: number, agent_id, app_id, service_name, created_at_millis, result: {...} }
                const rawTasks = (res.tasks || []).map(info => {
                    // Status priority: info.status (top-level) > info.result?.status
                    const statusNum = (typeof info.status === 'number') ? info.status : (info.result?.status ?? 0);
                    return {
                        task_id: info.task?.task_id || '',
                        task_type: info.task?.task_type || '',
                        target_agent_id: info.agent_id || info.task?.target_agent_id || '',
                        app_id: info.app_id || '',
                        service_name: info.service_name || '',
                        status: this.taskStatusToString(statusNum),
                        created_at_millis: info.created_at_millis || info.task?.created_at_millis || 0,
                        priority: info.task?.priority,
                        timeout_millis: info.task?.timeout_millis,
                        parameters: info.task?.parameters,
                        _raw: info, // Keep raw data for detail view
                    };
                });
                
                // Sort by created_at_millis descending (newest first)
                this.tasks = rawTasks.sort((a, b) => (b.created_at_millis || 0) - (a.created_at_millis || 0));
                
                // Build grouped structure
                this.groupedTasks = this.buildGroupedTasks(this.tasks);
            } catch (e) {
                this.handleError(e, 'Failed to load tasks');
            } finally {
                this.loading.tasks = false;
            }
        },

        // Build hierarchical grouped structure: Agent -> Tasks (simplified)
        // Since app_id and service_name may be empty, we group primarily by agent
        buildGroupedTasks(tasks) {
            const agentMap = new Map();
            
            for (const task of tasks) {
                const agentId = task.target_agent_id || '_global_';
                
                // Get or create agent group
                if (!agentMap.has(agentId)) {
                    agentMap.set(agentId, {
                        agent_id: agentId === '_global_' ? '' : agentId,
                        // Use the first task's app_id/service_name as representative
                        app_id: task.app_id || '',
                        service_name: task.service_name || '',
                        expanded: true,
                        tasks: [],
                    });
                }
                const agentGroup = agentMap.get(agentId);
                agentGroup.tasks.push(task);
                
                // Update app_id/service_name if current is empty but task has value
                if (!agentGroup.app_id && task.app_id) {
                    agentGroup.app_id = task.app_id;
                }
                if (!agentGroup.service_name && task.service_name) {
                    agentGroup.service_name = task.service_name;
                }
            }
            
            // Convert Map to array
            const result = Array.from(agentMap.values());
            
            // Sort: global tasks first, then by agent_id
            result.sort((a, b) => {
                if (!a.agent_id && b.agent_id) return -1;
                if (a.agent_id && !b.agent_id) return 1;
                // Sort by most recent task time
                const aTime = a.tasks[0]?.created_at_millis || 0;
                const bTime = b.tasks[0]?.created_at_millis || 0;
                return bTime - aTime;
            });
            
            return result;
        },

        // Convert task status number to string (align with controlplanev1.TaskStatus)
        // 0=UNSPECIFIED, 1=SUCCESS, 2=FAILED, 3=TIMEOUT, 4=CANCELLED, 5=PENDING, 6=RUNNING
        taskStatusToString(status) {
            const statusMap = {
                0: 'unknown',
                1: 'success',
                2: 'failed',
                3: 'timeout',
                4: 'cancelled',
                5: 'pending',
                6: 'running',
            };
            return statusMap[status] || 'unknown';
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
            // Show raw TaskInfo data if available
            this.showDetail(`Task: ${task.task_id}`, task._raw || task);
        },

        async submitTask() {
            try {
                // 构建任务数据
                const taskData = {
                    task_type: this.newTask.task_type,
                    timeout_millis: this.newTask.timeout_millis || 60000,
                    priority: this.newTask.priority || 0,
                };

                // 可选字段
                if (this.newTask.target_agent_id) {
                    taskData.target_agent_id = this.newTask.target_agent_id;
                }

                // 解析 parameters JSON
                if (this.newTask.parameters_json && this.newTask.parameters_json.trim()) {
                    try {
                        taskData.parameters = JSON.parse(this.newTask.parameters_json);
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
        // Actions - Arthas
        // ============================================================================
        async loadArthasAgents() {
            if (this.loading.arthas) return;
            this.loading.arthas = true;
            try {
                // 加载所有在线实例作为可连接的 Arthas 目标
                const res = await ApiService.getInstances('');
                this.arthasAgents = (res.instances || []).filter(i => i.status?.state === 'online');
            } catch (e) {
                this.handleError(e, 'Failed to load Arthas agents');
            } finally {
                this.loading.arthas = false;
            }
        },

        async connectArthas(instance) {
            if (this.arthasSession.connecting) return;
            
            this.arthasSession.connecting = true;
            this.arthasSession.error = '';
            this.arthasSession.agentId = instance.agent_id;
            this.arthasSession.agentInfo = instance;
            this.arthasSession.output = '';

            try {
                // 1. 先下发连接 Arthas 的任务给探针
                this.arthasSession.output += `[System] Sending Arthas attach task to agent...\n`;
                
                const taskRes = await ApiService.createTask({
                    task_type: 'arthas_attach',
                    target_agent_id: instance.agent_id,
                    parameters: { action: 'attach' },
                    timeout_millis: 60000,
                });

                const taskId = taskRes.task_id;
                this.arthasSession.output += `[System] Task ID: ${taskId}\n`;

                // 2. 等待任务完成（轮询检查）
                let taskCompleted = false;
                let retries = 0;
                const maxRetries = 30; // 最多等待 30 秒

                while (!taskCompleted && retries < maxRetries) {
                    await new Promise(r => setTimeout(r, 1000));
                    try {
                        const taskStatus = await ApiService.getTask(taskId);
                        // taskStatus could be TaskInfo (has status number) or TaskResult (has status number)
                        const normalized = (typeof taskStatus?.status === 'number')
                            ? this.taskStatusToString(taskStatus.status)
                            : (taskStatus?.status || 'unknown');

                        if (normalized === 'success') {
                            taskCompleted = true;
                            this.arthasSession.output += `[System] Arthas attached successfully.\n`;
                        } else if (normalized === 'failed' || normalized === 'timeout' || normalized === 'cancelled') {
                            throw new Error(taskStatus.error_message || taskStatus.error || 'Arthas attach failed');
                        }
                    } catch (e) {
                        if (e.status === 404) {
                            // 任务可能还没创建完成
                        } else {
                            throw e;
                        }
                    }
                    retries++;
                }

                if (!taskCompleted) {
                    throw new Error('Arthas attach timeout');
                }

                // 3. 建立 WebSocket 连接
                this.arthasSession.output += `[System] Connecting to Arthas terminal...\n`;
                await this.connectArthasWebSocket(instance.agent_id);

            } catch (e) {
                this.arthasSession.error = e.message || 'Failed to connect Arthas';
                this.arthasSession.output += `[Error] ${this.arthasSession.error}\n`;
                this.showToast(this.arthasSession.error, 'error');
            } finally {
                this.arthasSession.connecting = false;
            }
        },

        async connectArthasWebSocket(agentId) {
            // Step 1: Get a short-lived WS token (secure, API key in header)
            const tokenResponse = await ApiService.request('POST', '/auth/ws-token', { purpose: 'arthas_terminal' });
            
            if (!tokenResponse.token) {
                throw new Error('Failed to obtain WebSocket token');
            }
            
            // Step 2: Connect WebSocket with the short-lived token (not API key)
            const protocol = window.location.protocol === 'https:' ? 'wss:' : 'ws:';
            const wsUrl = `${protocol}//${window.location.host}/api/v1/arthas/ws?agent_id=${agentId}&token=${tokenResponse.token}`;

            return new Promise((resolve, reject) => {
                const ws = new WebSocket(wsUrl);
                let sessionId = null;
                let terminalReady = false;

                ws.onopen = () => {
                    this.arthasSession.ws = ws;
                    this.arthasSession.output += `[System] WebSocket connected, opening terminal...\n`;
                    
                    // Send OPEN_TERMINAL message to establish terminal session
                    const openMsg = {
                        type: 'OPEN_TERMINAL',
                        payload: {
                            agentId: agentId,
                            cols: 120,
                            rows: 30
                        }
                    };
                    ws.send(JSON.stringify(openMsg));
                };

                ws.onmessage = (event) => {
                    try {
                        const msg = JSON.parse(event.data);
                        
                        switch (msg.type) {
                            case 'TERMINAL_READY':
                                sessionId = msg.payload.sessionId;
                                this.arthasSession.sessionId = sessionId;
                                this.arthasSession.active = true;
                                terminalReady = true;
                                this.arthasSession.output += `[System] Terminal ready (session: ${sessionId})\n`;
                                this.arthasSession.output += `[System] Type 'help' for available commands.\n\n`;
                                resolve();
                                break;
                                
                            case 'TERMINAL_OUTPUT':
                                // Decode base64 output
                                if (msg.payload && msg.payload.data) {
                                    try {
                                        const decoded = atob(msg.payload.data);
                                        this.arthasSession.output += decoded;
                                    } catch (e) {
                                        // If not base64, use as-is
                                        this.arthasSession.output += msg.payload.data;
                                    }
                                }
                                this.$nextTick(() => {
                                    const terminal = document.getElementById('arthas-terminal');
                                    if (terminal) terminal.scrollTop = terminal.scrollHeight;
                                });
                                break;
                                
                            case 'TERMINAL_CLOSED':
                                this.arthasSession.output += `\n[System] Terminal closed: ${msg.payload?.reason || 'unknown'}\n`;
                                this.arthasSession.active = false;
                                break;
                                
                            case 'ERROR':
                                const errMsg = msg.payload?.message || 'Unknown error';
                                this.arthasSession.output += `[Error] ${errMsg}\n`;
                                if (!terminalReady) {
                                    reject(new Error(errMsg));
                                }
                                break;
                                
                            default:
                                console.log('Unknown message type:', msg.type);
                        }
                    } catch (e) {
                        // Not JSON, treat as plain text output
                        this.arthasSession.output += event.data;
                        this.$nextTick(() => {
                            const terminal = document.getElementById('arthas-terminal');
                            if (terminal) terminal.scrollTop = terminal.scrollHeight;
                        });
                    }
                };

                ws.onerror = (error) => {
                    this.arthasSession.error = 'WebSocket connection error';
                    this.arthasSession.output += `[Error] WebSocket error\n`;
                    reject(new Error('WebSocket connection error'));
                };

                ws.onclose = (event) => {
                    this.arthasSession.active = false;
                    this.arthasSession.ws = null;
                    this.arthasSession.sessionId = null;
                    this.arthasSession.output += `\n[System] Connection closed (code: ${event.code})\n`;
                };

                // 设置连接超时
                setTimeout(() => {
                    if (!terminalReady) {
                        ws.close();
                        reject(new Error('Terminal session timeout'));
                    }
                }, 30000);
            });
        },

        sendArthasCommand() {
            if (!this.arthasSession.ws || !this.arthasSession.inputCommand.trim()) return;
            if (!this.arthasSession.sessionId) {
                this.arthasSession.output += `[Error] Terminal session not ready\n`;
                return;
            }

            const cmd = this.arthasSession.inputCommand.trim();
            this.arthasSession.output += `$ ${cmd}\n`;
            
            // Send INPUT message with session ID
            const inputMsg = {
                type: 'INPUT',
                payload: {
                    sessionId: this.arthasSession.sessionId,
                    data: cmd + '\n'
                }
            };
            this.arthasSession.ws.send(JSON.stringify(inputMsg));
            this.arthasSession.inputCommand = '';
        },

        disconnectArthas() {
            if (this.arthasSession.ws) {
                // Send CLOSE_TERMINAL message before closing
                if (this.arthasSession.sessionId) {
                    const closeMsg = {
                        type: 'CLOSE_TERMINAL',
                        payload: {
                            sessionId: this.arthasSession.sessionId
                        }
                    };
                    try {
                        this.arthasSession.ws.send(JSON.stringify(closeMsg));
                    } catch (e) {
                        // Ignore send errors on close
                    }
                }
                this.arthasSession.ws.close();
            }
            this.arthasSession.active = false;
            this.arthasSession.ws = null;
            this.arthasSession.sessionId = null;
            this.arthasSession.agentId = '';
            this.arthasSession.agentInfo = null;
            this.arthasSession.output = '';
            this.arthasSession.error = '';
        },

        clearArthasOutput() {
            this.arthasSession.output = '';
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
