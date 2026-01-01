/**
 * Storage Module - 本地存储管理
 * @module storage
 */

const KEYS = {
    API_KEY: 'otel_admin_api_key',
};

export const Storage = {
    /**
     * 获取保存的 API Key
     * @returns {string|null}
     */
    getApiKey() {
        return localStorage.getItem(KEYS.API_KEY);
    },

    /**
     * 保存 API Key
     * @param {string} key
     */
    setApiKey(key) {
        localStorage.setItem(KEYS.API_KEY, key);
    },

    /**
     * 移除 API Key
     */
    removeApiKey() {
        localStorage.removeItem(KEYS.API_KEY);
    },
};
