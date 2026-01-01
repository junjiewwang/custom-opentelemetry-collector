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
