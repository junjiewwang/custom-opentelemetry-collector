/**
 * SearchableSelect - 可复用的搜索下拉框组件
 * 
 * 通过可配置的属性前缀（prefix）支持在同一个 Alpine.js data 对象中
 * 创建多个独立的搜索下拉框实例，避免属性名冲突。
 * 
 * 支持特性:
 *   - 输入搜索过滤选项
 *   - 分组显示 (optgroup 效果)
 *   - 键盘导航 (↑ ↓ Enter Escape)
 *   - 高亮匹配文字
 *   - 点击外部自动关闭
 *   - 懒加载数据源
 *   - 允许自定义输入 (Custom 选项)
 *   - 多实例共存（通过前缀隔离）
 * 
 * @module components/searchable-select
 * 
 * @example
 * // 在 Alpine.js view 模块中使用:
 * import { SearchableSelect } from '../components/searchable-select.js';
 * 
 * export function myView() {
 *     return {
 *         ...SearchableSelect.create('taskType', { ... }),
 *         ...SearchableSelect.create('targetAgent', { ... }),
 *     };
 * }
 */

export const SearchableSelect = {
    /**
     * 创建带前缀的搜索下拉框实例（状态 + 方法）
     * 
     * @param {string} prefix - 属性前缀，用于在同一 data 对象中隔离多个实例
     * @param {Object} config - 组件配置
     * @param {Array<{value: string, label: string, group?: string, icon?: string}>} config.options - 选项列表
     * @param {string}   [config.value='']                       - 初始选中值
     * @param {string}   [config.placeholder='Search...']        - 占位文本
     * @param {string[]} [config.searchKeys=['label']]           - 参与搜索的字段名
     * @param {string}   [config.displayKey='label']             - 选项显示字段
     * @param {string}   [config.valueKey='value']               - 选项值字段
     * @param {string[]} [config.groups=[]]                      - 分组名列表（按此顺序展示）
     * @param {boolean}  [config.allowCustom=false]              - 是否允许自定义输入
     * @param {string}   [config.customLabel='-- Custom --']     - 自定义选项的显示文字
     * @param {string}   [config.emptyText='No matching results'] - 无结果提示
     * @returns {Object} 可展开到 Alpine.js data 中的状态和方法对象
     */
    create(prefix, config = {}) {
        const {
            options = [],
            value = '',
            placeholder = 'Search...',
            searchKeys = ['label'],
            displayKey = 'label',
            valueKey = 'value',
            groups = [],
            allowCustom = false,
            customLabel = '-- Custom --',
            emptyText = 'No matching results',
        } = config;

        // 构建前缀化的属性名
        const p = prefix;

        const result = {};

        // ── State ──────────────────────────────────────
        result[`${p}Options`]        = options;
        result[`${p}Value`]          = value;
        result[`${p}Search`]         = '';
        result[`${p}IsOpen`]         = false;
        result[`${p}HighlightIdx`]   = -1;
        result[`${p}IsCustomMode`]   = false;
        result[`${p}CustomValue`]    = '';
        result[`${p}LazyLoaded`]     = false;
        result[`${p}Loading`]        = false;

        // ── Config ─────────────────────────────────────
        result[`${p}Placeholder`]    = placeholder;
        result[`${p}SearchKeys`]     = searchKeys;
        result[`${p}DisplayKey`]     = displayKey;
        result[`${p}ValueKey`]       = valueKey;
        result[`${p}Groups`]         = groups;
        result[`${p}AllowCustom`]    = allowCustom;
        result[`${p}CustomLabel`]    = customLabel;
        result[`${p}EmptyText`]      = emptyText;
        result[`${p}LazyLoadFn`]     = null;  // 由外部赋值

        // ── Methods ────────────────────────────────────

        /**
         * 获取选中选项的显示文字
         */
        result[`${p}SelectedLabel`] = function() {
            if (this[`${p}IsCustomMode`]) {
                return this[`${p}CustomValue`] || '';
            }
            const vk = this[`${p}ValueKey`];
            const dk = this[`${p}DisplayKey`];
            const opt = this[`${p}Options`].find(o => o[vk] === this[`${p}Value`]);
            return opt ? opt[dk] : '';
        };

        /**
         * 获取经过搜索过滤后的选项列表
         */
        result[`${p}FilteredOptions`] = function() {
            const query = this[`${p}Search`].toLowerCase().trim();
            let filtered = this[`${p}Options`];
            if (query) {
                const keys = this[`${p}SearchKeys`];
                filtered = filtered.filter(opt =>
                    keys.some(key => {
                        const val = opt[key];
                        return val && String(val).toLowerCase().includes(query);
                    })
                );
            }
            return filtered;
        };

        /**
         * 获取按分组组织的过滤后选项
         */
        result[`${p}GroupedOptions`] = function() {
            const filtered = this[`${p}FilteredOptions`]();
            const groupNames = this[`${p}Groups`];

            if (groupNames.length === 0) {
                return [{ group: '', options: filtered }];
            }

            const groupMap = new Map();
            for (const g of groupNames) {
                groupMap.set(g, []);
            }
            for (const opt of filtered) {
                const g = opt.group || '';
                if (groupMap.has(g)) {
                    groupMap.get(g).push(opt);
                } else {
                    if (!groupMap.has('Other')) groupMap.set('Other', []);
                    groupMap.get('Other').push(opt);
                }
            }

            const groups = [];
            for (const [group, options] of groupMap) {
                if (options.length > 0) {
                    groups.push({ group, options });
                }
            }
            return groups;
        };

        /**
         * 获取扁平化的所有可选选项（含 custom 虚拟选项，用于键盘导航）
         */
        result[`${p}FlatOptions`] = function() {
            const flat = [];
            for (const g of this[`${p}GroupedOptions`]()) {
                for (const opt of g.options) {
                    flat.push(opt);
                }
            }
            if (this[`${p}AllowCustom`]) {
                const vk = this[`${p}ValueKey`];
                const dk = this[`${p}DisplayKey`];
                flat.push({ [vk]: '__custom__', [dk]: this[`${p}CustomLabel`], _isCustom: true });
            }
            return flat;
        };

        /**
         * 打开下拉面板（含懒加载）
         */
        result[`${p}Open`] = async function() {
            const lazyFn = this[`${p}LazyLoadFn`];
            if (lazyFn && !this[`${p}LazyLoaded`]) {
                this[`${p}Loading`] = true;
                try {
                    await lazyFn.call(this);
                    this[`${p}LazyLoaded`] = true;
                } catch (e) {
                    console.error(`[SearchableSelect:${p}] lazy load error:`, e);
                } finally {
                    this[`${p}Loading`] = false;
                }
            }
            this[`${p}IsOpen`] = true;
            this[`${p}Search`] = '';
            this[`${p}HighlightIdx`] = -1;
        };

        /**
         * 关闭下拉面板
         */
        result[`${p}Close`] = function() {
            this[`${p}IsOpen`] = false;
            this[`${p}Search`] = '';
            this[`${p}HighlightIdx`] = -1;
        };

        /**
         * 切换下拉面板开关
         */
        result[`${p}Toggle`] = async function() {
            if (this[`${p}IsOpen`]) {
                this[`${p}Close`]();
            } else {
                await this[`${p}Open`]();
            }
        };

        /**
         * 选中某个选项
         */
        result[`${p}Select`] = function(option) {
            const vk = this[`${p}ValueKey`];

            if (option._isCustom) {
                this[`${p}IsCustomMode`] = true;
                this[`${p}Value`] = '__custom__';
                this[`${p}CustomValue`] = '';
                this[`${p}Close`]();
                this.$nextTick(() => {
                    const input = this.$refs?.[`${p}CustomInput`];
                    if (input) input.focus();
                });
                return;
            }

            this[`${p}IsCustomMode`] = false;
            this[`${p}CustomValue`] = '';
            this[`${p}Value`] = option[vk];
            this[`${p}Close`]();
        };

        /**
         * 键盘导航处理
         */
        result[`${p}Keydown`] = function(event) {
            const flat = this[`${p}FlatOptions`]();
            if (flat.length === 0) return;

            switch (event.key) {
                case 'ArrowDown':
                    event.preventDefault();
                    if (!this[`${p}IsOpen`]) {
                        this[`${p}Open`]();
                        return;
                    }
                    this[`${p}HighlightIdx`] = Math.min(this[`${p}HighlightIdx`] + 1, flat.length - 1);
                    this[`_ss_scrollTo`](`${p}Dropdown`);
                    break;

                case 'ArrowUp':
                    event.preventDefault();
                    this[`${p}HighlightIdx`] = Math.max(this[`${p}HighlightIdx`] - 1, 0);
                    this[`_ss_scrollTo`](`${p}Dropdown`);
                    break;

                case 'Enter':
                    event.preventDefault();
                    if (this[`${p}IsOpen`] && this[`${p}HighlightIdx`] >= 0 && this[`${p}HighlightIdx`] < flat.length) {
                        this[`${p}Select`](flat[this[`${p}HighlightIdx`]]);
                    }
                    break;

                case 'Escape':
                    event.preventDefault();
                    this[`${p}Close`]();
                    break;
            }
        };

        /**
         * 更新选项列表（外部动态数据场景）
         */
        result[`${p}UpdateOptions`] = function(newOptions) {
            this[`${p}Options`] = newOptions;
        };

        /**
         * 获取当前值
         */
        result[`${p}GetValue`] = function() {
            if (this[`${p}IsCustomMode`]) return this[`${p}CustomValue`];
            return this[`${p}Value`];
        };

        /**
         * 设置当前值（外部调用）
         */
        result[`${p}SetValue`] = function(val) {
            const vk = this[`${p}ValueKey`];
            if (val === '__custom__' || (this[`${p}AllowCustom`] && !this[`${p}Options`].find(o => o[vk] === val))) {
                this[`${p}IsCustomMode`] = true;
                this[`${p}Value`] = '__custom__';
                this[`${p}CustomValue`] = val === '__custom__' ? '' : val;
            } else {
                this[`${p}IsCustomMode`] = false;
                this[`${p}CustomValue`] = '';
                this[`${p}Value`] = val;
            }
        };

        /**
         * 高亮匹配文字（返回安全 HTML）
         */
        result[`${p}Highlight`] = function(text) {
            if (!text) return '';
            const query = this[`${p}Search`].trim();
            if (!query) return _escapeHtml(text);

            const escaped = _escapeHtml(text);
            const regex = new RegExp(`(${_escapeRegex(query)})`, 'gi');
            return escaped.replace(regex, '<mark class="bg-yellow-200 text-yellow-900 rounded px-0.5">$1</mark>');
        };

        /**
         * 判断选项是否被选中
         */
        result[`${p}IsSelected`] = function(option) {
            const vk = this[`${p}ValueKey`];
            return !this[`${p}IsCustomMode`] && option[vk] === this[`${p}Value`];
        };

        /**
         * 判断选项是否被键盘高亮
         */
        result[`${p}IsHighlighted`] = function(option) {
            const flat = this[`${p}FlatOptions`]();
            const idx = this[`${p}HighlightIdx`];
            return idx >= 0 && idx < flat.length && flat[idx] === option;
        };

        // ── Shared Internal Helpers (只注册一次) ──────
        result['_ss_scrollTo'] = function(refName) {
            this.$nextTick(() => {
                const container = this.$refs?.[refName];
                const highlighted = container?.querySelector('[data-ss-highlighted="true"]');
                if (highlighted && container) {
                    highlighted.scrollIntoView({ block: 'nearest', behavior: 'smooth' });
                }
            });
        };

        return result;
    },
};

// ── Module-level helpers ───────────────────────────
function _escapeHtml(str) {
    const map = { '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' };
    return String(str).replace(/[&<>"']/g, ch => map[ch]);
}

function _escapeRegex(str) {
    return str.replace(/[.*+?^${}()|[\]\\]/g, '\\$&');
}
