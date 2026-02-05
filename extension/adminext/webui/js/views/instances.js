/**
 * Instances View Module
 * @module views/instances
 */
import { ApiService } from '../api.js';
import { Utils } from '../utils.js';

export function instancesView() {
    return {
        // ============================================================================
        // State
        // ============================================================================
        instanceSearch: '',
        instanceFilter: 'all', // all, online, offline
        instanceDrawerOpen: false,
        selectedInstance: null,

        // ============================================================================
        // Logic
        // ============================================================================
        
        /**
         * 过滤后的实例列表
         */
        filteredInstances() {
            let list = this.instances || [];
            
            // 状态过滤
            if (this.instanceFilter === 'online') {
                list = list.filter(i => i.status?.state === 'online');
            } else if (this.instanceFilter === 'offline') {
                list = list.filter(i => i.status?.state === 'offline');
            }

            // 关键词搜索
            if (this.instanceSearch.trim()) {
                const q = this.instanceSearch.toLowerCase();
                list = list.filter(i => 
                    i.agent_id.toLowerCase().includes(q) ||
                    (i.hostname || '').toLowerCase().includes(q) ||
                    (i.service_name || '').toLowerCase().includes(q) ||
                    (i.ip || '').includes(q)
                );
            }

            return list;
        },

        /**
         * 显示详情抽屉
         */
        showInstanceDetails(inst) {
            this.selectedInstance = inst;
            this.instanceDrawerOpen = true;
        },

        /**
         * 下线实例
         */
        async unRegisterAgent(inst) {
            if (!confirm(`确定要从注册中心移除实例 ${inst.agent_id} 吗？\n如果该实例仍在运行，它可能会在下次心跳时重新注册。`)) return;
            try {
                await ApiService.unregisterAgent(inst.agent_id);
                this.showToast('实例已下线', 'success');
                this.instanceDrawerOpen = false;
                await this.loadInstances();
            } catch (e) {
                this.handleError(e, '下线实例失败');
            }
        },

        /**
         * 格式化时间
         */
        formatRelativeTime(ts) {
            if (!ts) return '-';
            // 如果是秒级时间戳，转为毫秒
            const timestamp = ts < 10000000000 ? ts * 1000 : ts;
            return Utils.formatRelativeTime(timestamp);
        },

        formatFullTime(ts) {
            if (!ts) return '-';
            const timestamp = ts < 10000000000 ? ts * 1000 : ts;
            return Utils.formatTimestamp(timestamp);
        },

        formatUptime(ts) {
            if (!ts) return '-';
            const timestamp = ts < 10000000000 ? ts * 1000 : ts;
            return Utils.formatUptime(timestamp);
        }
    };
}
