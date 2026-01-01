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

        const res = await fetch(`/api/v1${path}`, options);
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

    // Instances
    getInstances(status = '') {
        return this.request('GET', `/instances?status=${status}`);
    }

    getInstanceStats() {
        return this.request('GET', '/instances/stats');
    }

    kickInstance(id) {
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

    createTask(data) {
        return this.request('POST', '/tasks', data);
    }

    cancelTask(id) {
        return this.request('DELETE', `/tasks/${id}`);
    }
}

// 单例导出
export const ApiService = new ApiServiceClass();
