/**
 * Tasks View Module - 任务管理模块逻辑
 * @module views/tasks
 */

import { ApiService } from '../api.js';
import { Utils } from '../utils.js';

export function tasksView() {
    return {
        // ============================================================================
        // State
        // ============================================================================
        tasks: [],
        taskTreeData: [],
        taskViewMode: 'tree',
        taskStatusFilter: 'all',
        taskSearchQuery: '',

        // Task Detail Drawer
        selectedTask: null,
        taskDrawerOpen: false,
        _taskDetailReqSeq: 0,
        _taskDetailCloseTimer: null,

        showCreateTaskModal: false,
        newTask: {
            task_type_preset: 'arthas_attach',
            task_type_custom: '',
            target_agent_id: '',
            timeout_millis: 60000,
            priority: 0,
            parameters_json: '',
        },

        // ============================================================================
        // Actions
        // ============================================================================
        async loadTasks() {
            if (this.loading.tasks) return;
            this.loading.tasks = true;
            try {
                const res = await ApiService.getTasks();
                
                const rawTasks = (res.tasks || []).map((info, index) => {
                    const task = info.task || {};
                    const taskId = task.task_id || info.task_id || '';
                    const taskType = task.task_type_name || task.task_type || info.task_type_name || info.task_type || '';
                    const targetAgentId = info.agent_id || task.target_agent_id || info.target_agent_id || '';
                    const createdAt = info.created_at_millis || task.created_at_millis || 0;
                    const statusNum = (typeof info.status === 'number') ? info.status : (info.result?.status ?? 0);
                    
                    let parameters = task.parameters_json;
                    if (typeof parameters === 'string') {
                        try {
                            parameters = JSON.parse(parameters);
                        } catch (e) {
                            // keep raw string for debug
                            parameters = { raw: parameters };
                        }
                    }
                    if (parameters == null) parameters = {};

                    const rawResult = info.result || null;

                    return {
                        task_id: taskId,
                        task_type: taskType,
                        target_agent_id: targetAgentId,
                        app_id: info.app_id || '',
                        service_name: info.service_name || '',
                        status: this.taskStatusToString(statusNum),
                        created_at_millis: createdAt,
                        priority: task.priority_num ?? task.priority ?? 0,
                        timeout_millis: task.timeout_millis,
                        parameters,
                        _raw: info,
                        _result: this.normalizeTaskResult(rawResult),
                        _detailLoading: false,
                        _detailError: '',
                    };
                });
                
                const validTasks = rawTasks.filter(task => task.task_id);
                this.tasks = validTasks.sort((a, b) => (b.created_at_millis || 0) - (a.created_at_millis || 0));
                this.taskTreeData = this.buildTaskTree(this.tasks);
            } catch (e) {
                this.handleError(e, 'Failed to load tasks');
            } finally {
                this.loading.tasks = false;
            }
        },

        buildTaskTree(tasks) {
            let filteredTasks = tasks;
            
            if (this.taskStatusFilter !== 'all') {
                filteredTasks = filteredTasks.filter(t => t.status === this.taskStatusFilter);
            }
            
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
            
            const appMap = new Map();
            for (const task of filteredTasks) {
                const appId = task.app_id || '_uncategorized_';
                const serviceName = task.service_name || '_unknown_service_';
                const instanceId = task.target_agent_id || '_global_';
                
                if (!appMap.has(appId)) {
                    appMap.set(appId, {
                        id: `app-${appId}`,
                        name: appId === '_uncategorized_' ? '未分类' : appId,
                        type: 'app',
                        expanded: false,
                        stats: { total: 0, running: 0, failed: 0, success: 0, pending: 0, timeout: 0 },
                        children: new Map(),
                        lastUpdatedAt: 0,
                    });
                }
                const appNode = appMap.get(appId);
                
                if (!appNode.children.has(serviceName)) {
                    appNode.children.set(serviceName, {
                        id: `svc-${appId}-${serviceName}`,
                        name: serviceName === '_unknown_service_' ? '未知服务' : serviceName,
                        type: 'service',
                        expanded: false,
                        stats: { total: 0, running: 0, failed: 0, success: 0, pending: 0, timeout: 0 },
                        children: new Map(),
                        lastUpdatedAt: 0,
                    });
                }
                const serviceNode = appNode.children.get(serviceName);
                
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
                // 标记任务所属实例，方便服务维度的排序与展示
                task._instance_label = instanceNode.fullId || 'Global';
                task._instance_id = instanceNode.fullId || '';
                instanceNode.tasks.push(task);
                
                const status = task.status || 'unknown';
                instanceNode.stats.total++;
                if (status === 'running') instanceNode.stats.running++;
                else if (status === 'failed') instanceNode.stats.failed++;
                else if (status === 'success') instanceNode.stats.success++;
                else if (status === 'pending') instanceNode.stats.pending++;
                else if (status === 'timeout') instanceNode.stats.timeout++;
                
                const taskTime = task.created_at_millis || 0;
                if (taskTime > instanceNode.lastUpdatedAt) instanceNode.lastUpdatedAt = taskTime;
            }
            
            const result = [];
            for (const [appId, appNode] of appMap) {
                const appChildren = [];
                for (const [serviceName, serviceNode] of appNode.children) {
                    const serviceChildren = [];
                    for (const [instanceId, instanceNode] of serviceNode.children) {
                        instanceNode.tasks.sort((a, b) => (b.created_at_millis || 0) - (a.created_at_millis || 0));
                        serviceNode.stats.total += instanceNode.stats.total;
                        serviceNode.stats.running += instanceNode.stats.running;
                        serviceNode.stats.failed += instanceNode.stats.failed;
                        serviceNode.stats.success += instanceNode.stats.success;
                        serviceNode.stats.pending += instanceNode.stats.pending;
                        serviceNode.stats.timeout += instanceNode.stats.timeout;
                        if (instanceNode.lastUpdatedAt > serviceNode.lastUpdatedAt) serviceNode.lastUpdatedAt = instanceNode.lastUpdatedAt;
                        if (instanceNode.stats.failed > 0 || instanceNode.stats.timeout > 0) instanceNode.expanded = true;
                        serviceChildren.push(instanceNode);
                    }
                    // 最新优先（同时间再按异常优先，便于快速定位最新任务）
                    serviceChildren.sort((a, b) => {
                        const timeDiff = b.lastUpdatedAt - a.lastUpdatedAt;
                        if (timeDiff !== 0) return timeDiff;
                        const aFailed = a.stats.failed + a.stats.timeout;
                        const bFailed = b.stats.failed + b.stats.timeout;
                        return bFailed - aFailed;
                    });
                    serviceNode.children = serviceChildren;
                    // Service 维度的任务列表：按创建时间从新到旧
                    const serviceTasks = [];
                    for (const inst of serviceChildren) {
                        for (const t of inst.tasks) {
                            serviceTasks.push(t);
                        }
                    }
                    serviceTasks.sort((a, b) => (b.created_at_millis || 0) - (a.created_at_millis || 0));
                    serviceNode.allTasks = serviceTasks;

                    appNode.stats.total += serviceNode.stats.total;
                    appNode.stats.running += serviceNode.stats.running;
                    appNode.stats.failed += serviceNode.stats.failed;
                    appNode.stats.success += serviceNode.stats.success;
                    appNode.stats.pending += serviceNode.stats.pending;
                    appNode.stats.timeout += serviceNode.stats.timeout;
                    if (serviceNode.lastUpdatedAt > appNode.lastUpdatedAt) appNode.lastUpdatedAt = serviceNode.lastUpdatedAt;
                    if (serviceNode.stats.failed > 0 || serviceNode.stats.timeout > 0) serviceNode.expanded = true;
                    appChildren.push(serviceNode);
                }
                // 最新优先（同时间再按异常优先）
                appChildren.sort((a, b) => {
                    const timeDiff = b.lastUpdatedAt - a.lastUpdatedAt;
                    if (timeDiff !== 0) return timeDiff;
                    const aFailed = a.stats.failed + a.stats.timeout;
                    const bFailed = b.stats.failed + b.stats.timeout;
                    return bFailed - aFailed;
                });
                appNode.children = appChildren;
                if (appNode.stats.failed > 0 || appNode.stats.timeout > 0) appNode.expanded = true;
                result.push(appNode);
            }
            
            // 最新优先（同时间再按异常优先）
            result.sort((a, b) => {
                const timeDiff = b.lastUpdatedAt - a.lastUpdatedAt;
                if (timeDiff !== 0) return timeDiff;
                const aFailed = a.stats.failed + a.stats.timeout;
                const bFailed = b.stats.failed + b.stats.timeout;
                return bFailed - aFailed;
            });
            return result;
        },

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

        async submitTask() {
            try {
                const taskType = this.newTask.task_type_preset === 'custom' 
                    ? this.newTask.task_type_custom 
                    : this.newTask.task_type_preset;

                if (!taskType) {
                    this.showToast('Please specify task type', 'error');
                    return;
                }

                const taskData = {
                    task_type_name: taskType,
                    timeout_millis: this.newTask.timeout_millis || 60000,
                    priority_num: this.newTask.priority || 0,
                };
                if (this.newTask.target_agent_id) taskData.target_agent_id = this.newTask.target_agent_id;
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
                this.newTask = { 
                    task_type_preset: 'arthas_attach', 
                    task_type_custom: '', 
                    target_agent_id: '', 
                    timeout_millis: 60000, 
                    priority: 0, 
                    parameters_json: '' 
                };
                await this.loadTasks();
            } catch (e) {
                this.handleError(e, 'Failed to create task');
            }
        },

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

        taskStatusToString(status) {
            const statusMap = {
                0: 'unknown', 1: 'pending', 2: 'running', 3: 'success', 
                4: 'failed', 5: 'timeout', 6: 'cancelled', 7: 'failed'
            };
            return statusMap[status] || 'unknown';
        },

        applyTaskFilter() {
            this.taskTreeData = this.buildTaskTree(this.tasks);
        },

        closeTaskDrawer() {
            // 关闭只控制展示状态，延迟清理 selectedTask，避免过渡期间闪烁
            this.taskDrawerOpen = false;

            if (this._taskDetailCloseTimer) {
                clearTimeout(this._taskDetailCloseTimer);
                this._taskDetailCloseTimer = null;
            }

            const closeDelay = 320; // 与 tasks.html 的 x-transition duration 对齐
            this._taskDetailCloseTimer = setTimeout(() => {
                if (!this.taskDrawerOpen) {
                    this.selectedTask = null;
                }
                this._taskDetailCloseTimer = null;
            }, closeDelay);
        },

        async openTaskDetail(task) {
            if (!task || !task.task_id) return;

            // 打开抽屉：先取消任何“延迟清理”，再展示
            if (this._taskDetailCloseTimer) {
                clearTimeout(this._taskDetailCloseTimer);
                this._taskDetailCloseTimer = null;
            }
            this.taskDrawerOpen = true;

            const reqSeq = ++this._taskDetailReqSeq;

            // Optimistic open with list data
            this.selectedTask = {
                ...task,
                _detailLoading: true,
                _detailError: '',
            };

            try {
                const res = await ApiService.getTask(task.task_id);

                // /tasks/{id} may return either TaskInfoV2 {task,status,result,...}
                // or TaskResult {task_id,status,...} when only result exists.
                const isTaskInfo = res && typeof res === 'object' && res.task && (res.status !== undefined);
                const rawInfo = isTaskInfo ? res : null;
                const rawResult = isTaskInfo ? (res.result || null) : res;

                const statusNum = (typeof (rawInfo?.status ?? rawResult?.status) === 'number') ? (rawInfo?.status ?? rawResult?.status) : null;
                const statusStr = (statusNum != null) ? this.taskStatusToString(statusNum) : (task.status || 'unknown');

                // refresh params if server has newer task payload
                let parameters = this.selectedTask.parameters;
                if (rawInfo?.task?.parameters_json !== undefined) {
                    parameters = rawInfo.task.parameters_json;
                    if (typeof parameters === 'string') {
                        try { parameters = JSON.parse(parameters); } catch (e) { parameters = { raw: parameters }; }
                    }
                    if (parameters == null) parameters = {};
                }

                // 如果用户已关闭/切换了抽屉，丢弃过期响应，避免 UI 瞬态跳变
                if (!this.taskDrawerOpen) return;
                if (!this.selectedTask || this.selectedTask.task_id !== task.task_id) return;
                if (reqSeq !== this._taskDetailReqSeq) return;

                this.selectedTask = {
                    ...this.selectedTask,
                    status: statusStr,
                    parameters,
                    _raw: rawInfo || this.selectedTask._raw,
                    _result: this.normalizeTaskResult(rawResult),
                    _detailLoading: false,
                    _detailError: '',
                };
            } catch (e) {
                if (!this.taskDrawerOpen) return;
                if (!this.selectedTask || this.selectedTask.task_id !== task.task_id) return;
                if (reqSeq !== this._taskDetailReqSeq) return;

                this.selectedTask = {
                    ...this.selectedTask,
                    _detailLoading: false,
                    _detailError: e?.message || 'Failed to load task detail',
                };
            }
        },

        getResultSummaryEntries(resultJSON) {
            if (!resultJSON || typeof resultJSON !== 'object' || Array.isArray(resultJSON)) return [];

            // 让关键字段靠前（尤其是 arthas_attach/detach 的结果）
            const preferred = ['tunnel_ready', 'arthas_state', 'state', 'message', 'output', 'url'];
            const entries = Object.entries(resultJSON);

            if (entries.length === 0) return [];

            const scored = entries.map(([k, v]) => {
                const idx = preferred.indexOf(k);
                const score = (idx >= 0) ? idx : 1000;
                return { key: k, value: v, score };
            });

            scored.sort((a, b) => {
                if (a.score !== b.score) return a.score - b.score;
                return a.key.localeCompare(b.key);
            });

            return scored.slice(0, 12).map(({ key, value }) => ({
                key,
                valueText: (typeof value === 'string') ? value : JSON.stringify(value),
            }));
        },

        normalizeTaskResult(result) {
            if (!result || typeof result !== 'object') return null;

            // Handle different naming conventions (json_raw_message might be camelCase or PascalCase in some environments)
            const rawJSON = result.result_json ?? result.ResultJSON ?? result.result ?? result.Result;
            
            let resultJSONObj = null;
            let resultJSONPretty = '';
            
            if (rawJSON !== undefined && rawJSON !== null && rawJSON !== '') {
                try {
                    if (typeof rawJSON === 'string') {
                        try {
                            resultJSONObj = JSON.parse(rawJSON);
                            resultJSONPretty = JSON.stringify(resultJSONObj, null, 2);
                        } catch (e) {
                            // Not a valid JSON string, keep as is
                            resultJSONObj = null;
                            resultJSONPretty = rawJSON;
                        }
                    } else {
                        resultJSONObj = rawJSON;
                        resultJSONPretty = JSON.stringify(rawJSON, null, 2);
                    }
                } catch (e) {
                    resultJSONObj = null;
                    resultJSONPretty = '';
                }
            }

            const rawData = result.result_data ?? result.ResultData ?? result.data ?? '';
            const resultDataBase64 = (typeof rawData === 'string') ? rawData : '';
            let resultDataText = '';
            if (resultDataBase64) {
                resultDataText = Utils.decodeBase64ToText(resultDataBase64);
                if (resultDataText && resultDataText.length > 20000) {
                    resultDataText = resultDataText.slice(0, 20000) + '\n... (truncated)';
                }
            }

            const startedAt = result.started_at_millis || result.StartedAtMillis || 0;
            const completedAt = result.completed_at_millis || result.CompletedAtMillis || 0;
            let execMillis = (typeof result.execution_time_millis === 'number') ? result.execution_time_millis : 
                             (typeof result.ExecutionTimeMillis === 'number') ? result.ExecutionTimeMillis : 0;
            
            // 补算逻辑：如果后端没给总耗时，但有起止时间，则计算差值
            if (!execMillis && startedAt > 0 && completedAt > 0 && completedAt >= startedAt) {
                execMillis = completedAt - startedAt;
            }

            return {
                status: result.status ?? result.Status,
                error_code: result.error_code || result.ErrorCode || '',
                error_message: result.error_message || result.ErrorMessage || '',
                started_at_millis: startedAt,
                completed_at_millis: completedAt,
                execution_time_millis: execMillis,
                has_execution_info: (typeof result.execution_time_millis === 'number') || 
                                   (typeof result.ExecutionTimeMillis === 'number') || 
                                   (startedAt > 0 && completedAt > 0),
                result_json_obj: resultJSONObj,
                result_json_pretty: resultJSONPretty,
                result_summary: this.getResultSummaryEntries(resultJSONObj),
                result_data_base64: resultDataBase64,
                result_data_text: resultDataText,
                result_data_type: result.result_data_type || result.ResultDataType || '',
                compression: result.compression ?? result.Compression,
                original_size: result.original_size ?? result.OriginalSize,
                compressed_size: result.compressed_size ?? result.CompressedSize,
                _raw: result,
            };
        },
    };
}
