/**
 * Utils Module - 工具函数
 * @module utils
 */

export const Utils = {
    /**
     * 格式化日期（ISO 字符串）
     * @param {string} dateStr - ISO 日期字符串
     * @returns {string} 格式化后的日期
     */
    formatDate(dateStr) {
        if (!dateStr) return '-';
        const date = new Date(dateStr);
        if (isNaN(date.getTime())) return dateStr;
        return date.toLocaleString('zh-CN');
    },

    /**
     * 格式化 Unix 毫秒时间戳
     * @param {number} timestamp - Unix 毫秒时间戳
     * @returns {string} 格式化后的日期时间
     */
    formatTimestamp(timestamp) {
        if (!timestamp || timestamp === 0) return '-';
        const date = new Date(timestamp);
        if (isNaN(date.getTime())) return '-';
        return date.toLocaleString('zh-CN');
    },

    /**
     * 格式化运行时长
     * @param {number} startTime - 启动时间戳（毫秒）
     * @returns {string} 人类可读的时长
     */
    formatUptime(startTime) {
        if (!startTime || startTime === 0) return '-';
        const now = Date.now();
        const diff = now - startTime;
        
        if (diff < 0) return '-';
        
        const seconds = Math.floor(diff / 1000);
        const minutes = Math.floor(seconds / 60);
        const hours = Math.floor(minutes / 60);
        const days = Math.floor(hours / 24);
        
        if (days > 0) {
            return `${days}d ${hours % 24}h`;
        } else if (hours > 0) {
            return `${hours}h ${minutes % 60}m`;
        } else if (minutes > 0) {
            return `${minutes}m ${seconds % 60}s`;
        } else {
            return `${seconds}s`;
        }
    },

    /**
     * 复制文本到剪贴板
     * @param {string} text - 要复制的文本
     */
    copyToClipboard(text) {
        navigator.clipboard.writeText(text);
    },

    /**
     * 截断字符串
     * @param {string} str - 原始字符串
     * @param {number} len - 最大长度
     * @returns {string} 截断后的字符串
     */
    truncate(str, len = 20) {
        if (!str) return '-';
        return str.length > len ? str.substring(0, len) + '...' : str;
    },

    /**
     * 格式化相对时间（例如：刚刚 / 5分钟前 / 2小时前）
     * @param {number} timestamp - Unix 毫秒时间戳
     * @returns {string}
     */
    formatRelativeTime(timestamp) {
        if (!timestamp || timestamp === 0) return '-';
        const now = Date.now();
        const diff = now - timestamp;
        if (diff < 0) return '-';

        if (diff < 30 * 1000) return '刚刚';

        const minutes = Math.floor(diff / (60 * 1000));
        if (minutes < 60) return `${minutes}分钟前`;

        const hours = Math.floor(minutes / 60);
        if (hours < 24) return `${hours}小时前`;

        const days = Math.floor(hours / 24);
        if (days < 30) return `${days}天前`;

        return this.formatTimestamp(timestamp);
    },

    /**
     * 格式化短 ID
     * @param {string} id
     * @param {number} len
     * @returns {string}
     */
    formatShortId(id, len = 8) {
        if (!id) return '-';
        return id.length > len ? id.substring(0, len) : id;
    },

    /**
     * 将 base64 字符串解码为 UTF-8 文本（失败则返回空字符串）
     * @param {string} base64
     * @returns {string}
     */
    decodeBase64ToText(base64) {
        if (!base64 || typeof base64 !== 'string') return '';
        try {
            const binaryStr = atob(base64);
            const bytes = new Uint8Array(binaryStr.length);
            for (let i = 0; i < binaryStr.length; i++) {
                bytes[i] = binaryStr.charCodeAt(i);
            }
            return new TextDecoder('utf-8', { fatal: false }).decode(bytes);
        } catch (e) {
            return '';
        }
    },

    /**
     * 生成 UUID v4
     * @returns {string} UUID 字符串
     */
    generateUUID() {
        return 'xxxxxxxx-xxxx-4xxx-yxxx-xxxxxxxxxxxx'.replace(/[xy]/g, function(c) {
            const r = Math.random() * 16 | 0;
            const v = c === 'x' ? r : (r & 0x3 | 0x8);
            return v.toString(16);
        });
    },
};
