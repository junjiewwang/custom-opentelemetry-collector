/**
 * API Service Module - 统一的 API 请求层
 * @module api
 */

class ApiServiceClass {
    constructor() {
        this.apiKey = '';
    }

    setApiKey(key) {
        this.apiKey = key;
    }

    getApiKey() {
        return this.apiKey;
    }

    async request(method, path, data = null) {
        const options = {
            method,
            headers: {
                'Content-Type': 'application/json',
                'X-API-Key': this.apiKey,
            },
        };
        if (data) options.body = JSON.stringify(data);

        const res = await fetch(`/api/v2${path}`, options);
        if (res.status === 401) {
            throw { status: 401, message: 'Unauthorized' };
        }
        if (!res.ok) {
            const err = await res.json().catch(() => ({}));
            throw { status: res.status, message: err.error || 'Request failed' };
        }
        return res.json();
    }

    // Dashboard
    getDashboard() {
        return this.request('GET', '/dashboard/overview');
    }

    // Apps
    getApps() {
        return this.request('GET', '/apps');
    }

    createApp(data) {
        return this.request('POST', '/apps', data);
    }

    deleteApp(id) {
        return this.request('DELETE', `/apps/${id}`);
    }

    regenerateToken(id) {
        return this.request('POST', `/apps/${id}/token`);
    }

    setToken(id, token) {
        return this.request('PUT', `/apps/${id}/token`, { token });
    }

    // Instances
    getInstances(status = '') {
        return this.request('GET', `/instances?status=${status}`);
    }

    getInstanceStats() {
        return this.request('GET', '/instances/stats');
    }

    unregisterAgent(id) {
        return this.request('POST', `/instances/${id}/kick`);
    }

    // Services
    getServices() {
        return this.request('GET', '/services');
    }

    // Tasks
    getTasks() {
        return this.request('GET', '/tasks');
    }

    getTask(id) {
        return this.request('GET', `/tasks/${id}`);
    }

    createTask(data) {
        return this.request('POST', '/tasks', data);
    }

    cancelTask(id) {
        return this.request('DELETE', `/tasks/${id}`);
    }

    // Arthas
    getArthasAgents() {
        return this.request('GET', '/arthas/agents');
    }

    // Config Management (Simplified: Service-level only)
    getAppServiceConfig(appId, serviceName) {
        return this.request('GET', `/apps/${appId}/config/services/${serviceName}`);
    }

    setAppServiceConfig(appId, serviceName, config) {
        return this.request('PUT', `/apps/${appId}/config/services/${serviceName}`, config);
    }

    deleteAppServiceConfig(appId, serviceName) {
        return this.request('DELETE', `/apps/${appId}/config/services/${serviceName}`);
    }
}

// 单例导出
export const ApiService = new ApiServiceClass();
