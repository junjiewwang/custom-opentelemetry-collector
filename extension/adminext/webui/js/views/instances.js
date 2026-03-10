/**
 * Instances View Module - 实例管理模块逻辑
 * @module views/instances
 * 
 * 功能：
 * - 左侧 App→Service 两级树形导航
 * - 右侧实例卡片列表，支持树节点联动筛选
 * - 可点击的统计卡片（全部/在线/离线/Arthas就绪/Arthas未注册）
 * - 丰富的实例详情抽屉
 */
import { ApiService } from '../api.js';
import { Utils } from '../utils.js';

export function instancesView() {
    return {
        // ============================================================================
        // State
        // ============================================================================
        instanceSearch: '',
        instanceFilter: 'all', // all, online, offline, arthas_ready, arthas_not_ready
        instanceDrawerOpen: false,
        selectedInstance: null,

        // Tree navigation
        instanceTreeData: [],
        selectedTreeNodeId: '', // svc-{appId}-{serviceName} or ''

        // ============================================================================
        // Tree Builder
        // ============================================================================

        /**
         * 构建 App → Service 两级实例树
         * 从 this.instances 数据构建，支持搜索过滤
         */
        buildInstanceTree(instances) {
            const filtered = this._applyInstanceFilters(instances);

            const appMap = new Map();
            for (const inst of filtered) {
                const appId = inst.app_id || '_global_';
                const serviceName = inst.service_name || '_unknown_';

                if (!appMap.has(appId)) {
                    appMap.set(appId, {
                        id: `app-${appId}`,
                        name: appId === '_global_' ? '全局' : appId,
                        type: 'app',
                        expanded: true,
                        count: 0,
                        children: new Map(),
                    });
                }
                const appNode = appMap.get(appId);

                if (!appNode.children.has(serviceName)) {
                    appNode.children.set(serviceName, {
                        id: `svc-${appId}-${serviceName}`,
                        name: serviceName === '_unknown_' ? '未知服务' : serviceName,
                        type: 'service',
                        appId: appId,
                        serviceName: serviceName,
                        count: 0,
                        onlineCount: 0,
                        offlineCount: 0,
                        arthasReadyCount: 0,
                        instances: [],
                    });
                }
                const svcNode = appNode.children.get(serviceName);
                svcNode.instances.push(inst);
                svcNode.count++;
                appNode.count++;

                if (inst.status?.state === 'online') {
                    svcNode.onlineCount++;
                } else {
                    svcNode.offlineCount++;
                }
                if (inst.arthasStatus?.tunnelReady) {
                    svcNode.arthasReadyCount++;
                }
            }

            // Convert Maps to sorted arrays
            const result = [];
            for (const [, appNode] of appMap) {
                const svcChildren = [];
                for (const [, svcNode] of appNode.children) {
                    // Sort instances: online first, then by last_heartbeat desc
                    svcNode.instances.sort((a, b) => {
                        const aOnline = a.status?.state === 'online' ? 1 : 0;
                        const bOnline = b.status?.state === 'online' ? 1 : 0;
                        if (aOnline !== bOnline) return bOnline - aOnline;
                        return (b.last_heartbeat || 0) - (a.last_heartbeat || 0);
                    });
                    svcChildren.push(svcNode);
                }
                // Sort services: by instance count desc
                svcChildren.sort((a, b) => b.count - a.count);
                appNode.children = svcChildren;
                result.push(appNode);
            }

            // Sort apps: by instance count desc
            result.sort((a, b) => b.count - a.count);
            return result;
        },

        // ============================================================================
        // Filters
        // ============================================================================

        /**
         * 应用搜索和状态过滤器
         */
        _applyInstanceFilters(instances) {
            let list = instances || [];

            // Status filter
            if (this.instanceFilter === 'online') {
                list = list.filter(i => i.status?.state === 'online');
            } else if (this.instanceFilter === 'offline') {
                list = list.filter(i => i.status?.state === 'offline');
            } else if (this.instanceFilter === 'arthas_ready') {
                list = list.filter(i => i.arthasStatus?.tunnelReady);
            } else if (this.instanceFilter === 'arthas_not_ready') {
                list = list.filter(i => !i.arthasStatus?.tunnelReady);
            }

            // Search filter
            if (this.instanceSearch && this.instanceSearch.trim()) {
                const q = this.instanceSearch.toLowerCase().trim();
                list = list.filter(i =>
                    (i.agent_id && i.agent_id.toLowerCase().includes(q)) ||
                    (i.hostname || '').toLowerCase().includes(q) ||
                    (i.service_name || '').toLowerCase().includes(q) ||
                    (i.ip || '').includes(q) ||
                    (i.app_id || '').toLowerCase().includes(q)
                );
            }

            return list;
        },

        /**
         * 过滤后的实例列表（供模板使用）
         * 综合考虑状态筛选、搜索、树节点选择
         */
        filteredInstances() {
            const filtered = this._applyInstanceFilters(this.instances || []);

            // If a tree node is selected, further filter by it
            if (this.selectedTreeNodeId) {
                return filtered.filter(inst => {
                    const appId = inst.app_id || '_global_';
                    const serviceName = inst.service_name || '_unknown_';
                    const svcId = `svc-${appId}-${serviceName}`;
                    return svcId === this.selectedTreeNodeId;
                });
            }

            return filtered;
        },

        /**
         * 触发过滤器更新（重建树）
         */
        applyInstanceFilter() {
            this.instanceTreeData = this.buildInstanceTree(this.instances || []);
        },

        // ============================================================================
        // Stats
        // ============================================================================

        /**
         * 获取实例统计数据
         */
        getInstanceStats() {
            const instances = this.instances || [];
            const stats = {
                all: instances.length,
                online: 0,
                offline: 0,
                arthas_ready: 0,
                arthas_not_ready: 0,
            };
            for (const inst of instances) {
                if (inst.status?.state === 'online') {
                    stats.online++;
                } else {
                    stats.offline++;
                }
                if (inst.arthasStatus?.tunnelReady) {
                    stats.arthas_ready++;
                } else {
                    stats.arthas_not_ready++;
                }
            }
            return stats;
        },

        // ============================================================================
        // Tree Interaction
        // ============================================================================

        /**
         * 展开/折叠树节点
         */
        toggleInstanceTreeNode(node) {
            node.expanded = !node.expanded;
        },

        /**
         * 选择服务节点（联动筛选右侧实例列表）
         */
        selectTreeServiceNode(svcNode) {
            if (this.selectedTreeNodeId === svcNode.id) {
                this.selectedTreeNodeId = '';
            } else {
                this.selectedTreeNodeId = svcNode.id;
            }
        },

        /**
         * 获取当前选中的树节点标签
         */
        getSelectedTreeNodeLabel() {
            if (!this.selectedTreeNodeId) return '';
            for (const app of this.instanceTreeData) {
                for (const svc of (app.children || [])) {
                    if (svc.id === this.selectedTreeNodeId) {
                        return `${app.name} / ${svc.name}`;
                    }
                }
            }
            return '';
        },

        // ============================================================================
        // Detail Drawer
        // ============================================================================

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
            if (!inst) return;
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

        // ============================================================================
        // Time Format Helpers
        // ============================================================================

        formatRelativeTime(ts) {
            if (!ts) return '-';
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
        },
    };
}
