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
        // State - Arthas Management (新设计：实例列表 + Arthas 状态 + 任务)
        // ============================================================================
        arthasInstances: [],  // 所有在线实例，带 arthasStatus 字段
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
        instanceFilter: '',
        taskViewMode: 'tree', // 'tree' (层级树) or 'flat' (扁平列表)
        taskFilterOnlyFailed: false, // 只看异常
        taskSearchQuery: '', // 搜索关键词
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

            // 监听 terminalManager 事件
            this.setupTerminalEventListeners();

            // 初始加载当前视图的数据（确保页面刷新时数据被加载）
            if (this.authenticated && this.currentView) {
                this.onViewChange(this.currentView);
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
                console.log('[loadTasks] API response, tasks count:', res.tasks?.length);
                
                // Transform TaskInfo to flat structure for display
                // 兼容两种数据结构：
                // 1. 嵌套结构：{task: {task_id, ...}, status, agent_id, ...}
                // 2. 扁平结构：{task_id, task_type, status, ...}（task 为 null 或不存在）
                const rawTasks = (res.tasks || []).map((info, index) => {
                    // 兼容两种结构：如果有 task 字段则取 task，否则直接用 info
                    const task = info.task || {};
                    
                    // 字段取值优先级：task对象 > 顶层字段 > 默认值
                    const taskId = task.task_id || info.task_id || '';
                    const taskType = task.task_type_name || task.task_type || info.task_type_name || info.task_type || '';
                    const targetAgentId = info.agent_id || task.target_agent_id || info.target_agent_id || '';
                    const createdAt = info.created_at_millis || task.created_at_millis || 0;
                    
                    // 调试：找出空 task_id 的来源
                    if (!taskId) {
                        console.warn(`[loadTasks] Task at index ${index} has empty task_id:`, info);
                    }
                    
                    // Status priority: info.status (top-level) > info.result?.status
                    const statusNum = (typeof info.status === 'number') ? info.status : (info.result?.status ?? 0);
                    
                    return {
                        task_id: taskId,
                        task_type: taskType,
                        target_agent_id: targetAgentId,
                        app_id: info.app_id || '',
                        service_name: info.service_name || '',
                        status: this.taskStatusToString(statusNum),
                        created_at_millis: createdAt,
                        priority: task.priority,
                        timeout_millis: task.timeout_millis,
                        parameters: task.parameters,
                        _raw: info, // Keep raw data for detail view
                    };
                });
                
                // 过滤掉 task_id 为空的无效任务
                const validTasks = rawTasks.filter(task => task.task_id);
                console.log('[loadTasks] rawTasks count:', rawTasks.length, ', validTasks count:', validTasks.length);
                if (validTasks.length > 0) {
                    console.log('[loadTasks] first valid task:', validTasks[0]);
                }
                
                // Sort by created_at_millis descending (newest first)
                this.tasks = validTasks.sort((a, b) => (b.created_at_millis || 0) - (a.created_at_millis || 0));
                console.log('[loadTasks] this.tasks assigned, count:', this.tasks.length);
                
                // Build hierarchical tree structure: App -> Service -> Instance -> Task
                this.taskTreeData = this.buildTaskTree(this.tasks);
                console.log('[loadTasks] taskTreeData built, count:', this.taskTreeData.length);
                
                // Build grouped structure (legacy, for compatibility)
                this.groupedTasks = this.buildGroupedTasks(this.tasks);
            } catch (e) {
                console.error('[loadTasks] error:', e);
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

        /**
         * Build hierarchical tree structure: App -> Service -> Instance -> Task
         * 
         * 设计说明：
         * - App: 按 app_id 分组，无 app_id 的归入 "未分类"
         * - Service: 按 service_name 分组
         * - Instance: 按 target_agent_id 分组（作为实例标识）
         * - Task: 具体任务
         * 
         * 每个节点包含：
         * - id: 唯一标识
         * - name: 显示名称
         * - type: 'app' | 'service' | 'instance' | 'task'
         * - expanded: 是否展开
         * - stats: { total, running, failed, success, pending }
         * - children: 子节点数组
         * - lastUpdatedAt: 最近更新时间（用于排序）
         */
        buildTaskTree(tasks) {
            // 应用筛选
            let filteredTasks = tasks;
            
            // 只看异常筛选
            if (this.taskFilterOnlyFailed) {
                filteredTasks = tasks.filter(t => t.status === 'failed' || t.status === 'timeout');
            }
            
            // 搜索筛选
            if (this.taskSearchQuery && this.taskSearchQuery.trim()) {
                const query = this.taskSearchQuery.toLowerCase().trim();
                filteredTasks = filteredTasks.filter(t => 
                    (t.task_id && t.task_id.toLowerCase().includes(query)) ||
                    (t.app_id && t.app_id.toLowerCase().includes(query)) ||
                    (t.service_name && t.service_name.toLowerCase().includes(query)) ||
                    (t.target_agent_id && t.target_agent_id.toLowerCase().includes(query)) ||
                    (t.task_type && t.task_type.toLowerCase().includes(query))
                );
            }
            
            // 构建 App -> Service -> Instance -> Task 层级
            const appMap = new Map();
            
            for (const task of filteredTasks) {
                const appId = task.app_id || '_uncategorized_';
                const serviceName = task.service_name || '_unknown_service_';
                const instanceId = task.target_agent_id || '_global_';
                
                // 获取或创建 App 节点
                if (!appMap.has(appId)) {
                    appMap.set(appId, {
                        id: `app-${appId}`,
                        name: appId === '_uncategorized_' ? '未分类' : appId,
                        type: 'app',
                        expanded: false, // 默认折叠，有异常时自动展开
                        stats: { total: 0, running: 0, failed: 0, success: 0, pending: 0, timeout: 0 },
                        children: new Map(), // service_name -> serviceNode
                        lastUpdatedAt: 0,
                    });
                }
                const appNode = appMap.get(appId);
                
                // 获取或创建 Service 节点
                if (!appNode.children.has(serviceName)) {
                    appNode.children.set(serviceName, {
                        id: `svc-${appId}-${serviceName}`,
                        name: serviceName === '_unknown_service_' ? '未知服务' : serviceName,
                        type: 'service',
                        expanded: false,
                        stats: { total: 0, running: 0, failed: 0, success: 0, pending: 0, timeout: 0 },
                        children: new Map(), // instance_id -> instanceNode
                        lastUpdatedAt: 0,
                    });
                }
                const serviceNode = appNode.children.get(serviceName);
                
                // 获取或创建 Instance 节点
                if (!serviceNode.children.has(instanceId)) {
                    serviceNode.children.set(instanceId, {
                        id: `inst-${appId}-${serviceName}-${instanceId}`,
                        name: instanceId === '_global_' ? '全局任务' : instanceId,
                        fullId: instanceId === '_global_' ? '' : instanceId,
                        type: 'instance',
                        expanded: false,
                        stats: { total: 0, running: 0, failed: 0, success: 0, pending: 0, timeout: 0 },
                        tasks: [],
                        lastUpdatedAt: 0,
                    });
                }
                const instanceNode = serviceNode.children.get(instanceId);
                
                // 添加任务到 Instance
                instanceNode.tasks.push(task);
                
                // 更新统计
                const status = task.status || 'unknown';
                instanceNode.stats.total++;
                if (status === 'running') instanceNode.stats.running++;
                else if (status === 'failed') instanceNode.stats.failed++;
                else if (status === 'success') instanceNode.stats.success++;
                else if (status === 'pending') instanceNode.stats.pending++;
                else if (status === 'timeout') instanceNode.stats.timeout++;
                
                // 更新最近更新时间
                const taskTime = task.created_at_millis || 0;
                if (taskTime > instanceNode.lastUpdatedAt) instanceNode.lastUpdatedAt = taskTime;
            }
            
            // 转换 Map 为数组，并聚合统计
            const result = [];
            
            for (const [appId, appNode] of appMap) {
                const appChildren = [];
                
                for (const [serviceName, serviceNode] of appNode.children) {
                    const serviceChildren = [];
                    
                    for (const [instanceId, instanceNode] of serviceNode.children) {
                        // 按时间排序任务
                        instanceNode.tasks.sort((a, b) => (b.created_at_millis || 0) - (a.created_at_millis || 0));
                        
                        // 聚合到 Service
                        serviceNode.stats.total += instanceNode.stats.total;
                        serviceNode.stats.running += instanceNode.stats.running;
                        serviceNode.stats.failed += instanceNode.stats.failed;
                        serviceNode.stats.success += instanceNode.stats.success;
                        serviceNode.stats.pending += instanceNode.stats.pending;
                        serviceNode.stats.timeout += instanceNode.stats.timeout;
                        if (instanceNode.lastUpdatedAt > serviceNode.lastUpdatedAt) {
                            serviceNode.lastUpdatedAt = instanceNode.lastUpdatedAt;
                        }
                        
                        // Instance 有异常则自动展开
                        if (instanceNode.stats.failed > 0 || instanceNode.stats.timeout > 0) {
                            instanceNode.expanded = true;
                        }
                        
                        serviceChildren.push(instanceNode);
                    }
                    
                    // 按异常优先 + 时间排序 Instance
                    serviceChildren.sort((a, b) => {
                        const aFailed = a.stats.failed + a.stats.timeout;
                        const bFailed = b.stats.failed + b.stats.timeout;
                        if (aFailed !== bFailed) return bFailed - aFailed;
                        return b.lastUpdatedAt - a.lastUpdatedAt;
                    });
                    
                    serviceNode.children = serviceChildren;
                    
                    // 聚合到 App
                    appNode.stats.total += serviceNode.stats.total;
                    appNode.stats.running += serviceNode.stats.running;
                    appNode.stats.failed += serviceNode.stats.failed;
                    appNode.stats.success += serviceNode.stats.success;
                    appNode.stats.pending += serviceNode.stats.pending;
                    appNode.stats.timeout += serviceNode.stats.timeout;
                    if (serviceNode.lastUpdatedAt > appNode.lastUpdatedAt) {
                        appNode.lastUpdatedAt = serviceNode.lastUpdatedAt;
                    }
                    
                    // Service 有异常则自动展开
                    if (serviceNode.stats.failed > 0 || serviceNode.stats.timeout > 0) {
                        serviceNode.expanded = true;
                    }
                    
                    appChildren.push(serviceNode);
                }
                
                // 按异常优先 + 时间排序 Service
                appChildren.sort((a, b) => {
                    const aFailed = a.stats.failed + a.stats.timeout;
                    const bFailed = b.stats.failed + b.stats.timeout;
                    if (aFailed !== bFailed) return bFailed - aFailed;
                    return b.lastUpdatedAt - a.lastUpdatedAt;
                });
                
                appNode.children = appChildren;
                
                // App 有异常则自动展开
                if (appNode.stats.failed > 0 || appNode.stats.timeout > 0) {
                    appNode.expanded = true;
                }
                
                result.push(appNode);
            }
            
            // 按异常优先 + 时间排序 App
            result.sort((a, b) => {
                const aFailed = a.stats.failed + a.stats.timeout;
                const bFailed = b.stats.failed + b.stats.timeout;
                if (aFailed !== bFailed) return bFailed - aFailed;
                return b.lastUpdatedAt - a.lastUpdatedAt;
            });
            
            return result;
        },

        /**
         * 格式化短 ID（前4位...后4位）
         */
        formatShortId(id) {
            if (!id || id.length <= 12) return id || '-';
            return `${id.slice(0, 4)}...${id.slice(-4)}`;
        },

        /**
         * 格式化相对时间
         */
        formatRelativeTime(timestamp) {
            if (!timestamp) return '-';
            const diff = Date.now() - timestamp;
            if (diff < 60000) return '刚刚';
            if (diff < 3600000) return `${Math.floor(diff / 60000)}分钟前`;
            if (diff < 86400000) return `${Math.floor(diff / 3600000)}小时前`;
            return `${Math.floor(diff / 86400000)}天前`;
        },

        /**
         * 切换节点展开状态
         */
        toggleTreeNode(node) {
            node.expanded = !node.expanded;
        },

        /**
         * 展开所有节点
         */
        expandAllTreeNodes() {
            const expandAll = (nodes) => {
                for (const node of nodes) {
                    node.expanded = true;
                    if (node.children && Array.isArray(node.children)) {
                        expandAll(node.children);
                    }
                }
            };
            expandAll(this.taskTreeData);
        },

        /**
         * 折叠所有节点
         */
        collapseAllTreeNodes() {
            const collapseAll = (nodes) => {
                for (const node of nodes) {
                    node.expanded = false;
                    if (node.children && Array.isArray(node.children)) {
                        collapseAll(node.children);
                    }
                }
            };
            collapseAll(this.taskTreeData);
        },

        /**
         * 应用任务筛选（只看异常、搜索）
         */
        applyTaskFilter() {
            this.taskTreeData = this.buildTaskTree(this.tasks);
        },

        /**
         * 获取任务统计摘要
         */
        getTaskStats() {
            const stats = { total: 0, running: 0, failed: 0, success: 0, pending: 0, timeout: 0 };
            for (const task of this.tasks) {
                stats.total++;
                const status = task.status || 'unknown';
                if (status === 'running') stats.running++;
                else if (status === 'failed') stats.failed++;
                else if (status === 'success') stats.success++;
                else if (status === 'pending') stats.pending++;
                else if (status === 'timeout') stats.timeout++;
            }
            return stats;
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
        // Actions - Arthas
        // ============================================================================

        /**
         * 加载所有在线实例，并获取各实例的 Arthas 状态
         * 
         * 设计说明：
         * - 后端 ListConnectedAgents 返回已注册且健康的 agent 列表
         * - 如果 agent 在 tunnel 列表中，说明 Arthas 已连接，可以进行 terminal 操作
         * - 不再调用 getAgentArthasStatus，tunnel 连接即表示 Arthas 运行中
         */
        async loadArthasAgents() {
            if (this.loading.arthas) return;
            this.loading.arthas = true;
            try {
                // 1. 获取所有在线实例
                const instancesRes = await ApiService.getInstances('');
                const onlineInstances = (instancesRes.instances || []).filter(i => i.status?.state === 'online');

                // 2. 获取 tunnel 在线列表（用于判断 tunnel_ready）
                // 后端 ListConnectedAgents 返回的是已注册且健康的 agent
                // 字段：agent_id, app_id, service_name, ip, version, connected_at, last_ping_at
                let tunnelAgentsByAgentId = new Map();
                try {
                    const tunnelAgents = await ApiService.getArthasAgents();
                    for (const a of (tunnelAgents || [])) {
                        // 用 agent_id 作为 key 来匹配
                        if (a.agent_id) {
                            tunnelAgentsByAgentId.set(a.agent_id, a);
                        }
                    }
                } catch (e) {
                    // 接口不可用时忽略
                }

                // 3. 为每个实例构建 Arthas 状态
                const instancesWithStatus = onlineInstances.map((inst) => {
                    // 通过 agent_id 匹配 tunnel agent
                    const tunnelInfo = tunnelAgentsByAgentId.get(inst.agent_id);
                    
                    // tunnel 连接即表示 Arthas 运行中
                    const arthasStatus = {
                        state: tunnelInfo ? 'running' : 'stopped',
                        arthasVersion: tunnelInfo?.version || '',
                        tunnelReady: !!tunnelInfo,
                        // 保存 tunnel 的 agent_id，用于 connect
                        tunnelAgentId: tunnelInfo?.agent_id || '',
                    };

                    return {
                        ...inst,
                        arthasStatus,
                        // 操作状态
                        operating: false,
                    };
                });

                this.arthasInstances = instancesWithStatus;
                // 兼容旧的 arthasAgents（用于 Terminal 连接）
                this.arthasAgents = instancesWithStatus.filter(i => i.arthasStatus.tunnelReady);
            } catch (e) {
                this.handleError(e, 'Failed to load Arthas agents');
            } finally {
                this.loading.arthas = false;
            }
        },

        /**
         * 刷新单个实例的 Arthas 状态
         * 
         * 设计说明：
         * - 通过 tunnel 列表判断 agent 是否在线
         * - tunnel 连接即表示 Arthas 运行中，不再调用 getAgentArthasStatus
         */
        async refreshInstanceArthasStatus(agentId) {
            const inst = this.arthasInstances.find(i => i.agent_id === agentId);
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
                this.arthasAgents = this.arthasInstances.filter(i => i.arthasStatus.tunnelReady);
            } catch (e) {
                console.error('Failed to refresh Arthas status:', e);
            }
        },

        /**
         * Attach Arthas 到指定实例
         */
        async attachArthas(instance) {
            if (this.arthasTask.active) {
                this.showToast('Another task is running', 'warning');
                return;
            }

            instance.operating = true;
            this.arthasTask = {
                active: true,
                taskId: '',
                taskType: 'attach',
                targetAgentId: instance.agent_id,
                status: 'pending',
                message: 'Creating attach task...',
                startTime: Date.now(),
            };

            try {
                // 1. 创建 attach 任务
                const taskRes = await ApiService.createTask({
                    task_type_name: 'arthas_attach',
                    target_agent_id: instance.agent_id,
                    parameters: { action: 'attach' },
                    timeout_millis: 60000,
                });

                this.arthasTask.taskId = taskRes.task_id;
                this.arthasTask.message = `Task created: ${taskRes.task_id}`;

                // 2. 轮询任务状态
                await this.pollTaskStatus(taskRes.task_id, 'Arthas attach');

                // 3. 成功后刷新状态（带重试，等待 tunnel 连接）
                console.log('[Arthas] pollTaskStatus completed, status:', this.arthasTask.status);
                if (this.arthasTask.status === 'success') {
                    this.showToast('Arthas attached successfully, waiting for tunnel connection...', 'success');
                    console.log('[Arthas] Calling waitForTunnelConnection for agent:', instance.agent_id);
                    await this.waitForTunnelConnection(instance, 30); // 最多等待 30 秒
                    console.log('[Arthas] waitForTunnelConnection completed');
                }
            } catch (e) {
                this.arthasTask.status = 'failed';
                this.arthasTask.message = e.message || 'Attach failed';
                this.showToast(this.arthasTask.message, 'error');
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
            this.arthasTask = {
                active: true,
                taskId: '',
                taskType: 'detach',
                targetAgentId: instance.agent_id,
                status: 'pending',
                message: 'Creating detach task...',
                startTime: Date.now(),
            };

            try {
                // 1. 创建 detach 任务
                const taskRes = await ApiService.createTask({
                    task_type_name: 'arthas_detach',
                    target_agent_id: instance.agent_id,
                    parameters: { action: 'detach' },
                    timeout_millis: 30000,
                });

                this.arthasTask.taskId = taskRes.task_id;
                this.arthasTask.message = `Task created: ${taskRes.task_id}`;

                // 2. 轮询任务状态
                await this.pollTaskStatus(taskRes.task_id, 'Arthas detach');

                // 3. 成功后刷新状态
                if (this.arthasTask.status === 'success') {
                    this.showToast('Arthas detached successfully', 'success');
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
            
            console.log('[Arthas] waitForTunnelConnection started, agent_id:', instance.agent_id, 'maxRetries:', maxRetries);
            this.arthasTask.message = 'Waiting for tunnel connection...';
            
            for (let i = 0; i < maxRetries; i++) {
                await new Promise(r => setTimeout(r, pollInterval));
                
                try {
                    // 获取最新的 tunnel agents 列表
                    console.log('[Arthas] Fetching tunnel agents, attempt:', i + 1);
                    const tunnelAgents = await ApiService.getArthasAgents();
                    console.log('[Arthas] Got tunnel agents:', tunnelAgents);
                    
                    // 通过 agent_id 匹配（后端返回 snake_case 字段）
                    const tunnelInfo = (tunnelAgents || []).find(a => a.agent_id === instance.agent_id);
                    console.log('[Arthas] Looking for agent_id:', instance.agent_id, 'found:', tunnelInfo);
                    
                    if (tunnelInfo) {
                        // Tunnel 已连接，更新实例状态
                        const inst = this.arthasInstances.find(i => i.agent_id === instance.agent_id);
                        if (inst) {
                            inst.arthasStatus = {
                                state: 'running',
                                arthasVersion: tunnelInfo.version || '',
                                tunnelReady: true,
                                tunnelAgentId: tunnelInfo.agent_id,
                            };
                            
                            // 更新 arthasAgents
                            this.arthasAgents = this.arthasInstances.filter(i => i.arthasStatus?.tunnelReady);
                        }
                        
                        this.arthasTask.message = 'Tunnel connected successfully';
                        this.showToast('Arthas tunnel connected', 'success');
                        return true;
                    }
                    
                    this.arthasTask.message = `Waiting for tunnel connection... (${i + 1}/${maxRetries})`;
                } catch (e) {
                    console.warn('Failed to check tunnel status:', e);
                }
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
         * 前提：Arthas 已 running 且 tunnel 已就绪
         * 行为：直接建立 WebSocket 连接，使用 xterm.js 终端
         */
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
                    this.arthasSession.active = false;
                    this.arthasSession.ws = null;
                    
                    // Remove WebSocket binding from terminal
                    window.terminalManager.removeWebSocket(sessionId);
                    
                    // Write close message to terminal
                    const reason = event.reason ? `, reason: ${event.reason}` : '';
                    window.terminalManager.writeDataBySessionId(sessionId, 
                        `\r\n\x1b[33m[System] Connection closed (code: ${event.code}${reason})\x1b[0m\r\n`);
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
        formatUptime: Utils.formatUptime.bind(Utils),
        truncate: Utils.truncate.bind(Utils),

        copyToClipboard(text) {
            Utils.copyToClipboard(text);
            this.showToast('Copied to clipboard', 'success');
        },
    };
}
