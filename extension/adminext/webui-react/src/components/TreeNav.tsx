/**
 * TreeNav - 可复用的左侧树形导航组件
 *
 * 用于 Instances（App → Service 两级树）、Tasks（三级导航树）和 Configs（服务树）页面。
 * 支持：
 *   - 多级嵌套树结构
 *   - 节点展开/折叠
 *   - 节点选中 + 取消选中（toggle）
 *   - 搜索过滤
 *   - Badge 计数
 *   - 自定义节点图标和颜色
 *
 * @example
 * <TreeNav
 *   data={treeData}
 *   selectedId={selectedNodeId}
 *   onSelect={(node) => setSelectedNodeId(node.id)}
 *   searchable
 *   placeholder="Search services..."
 * />
 */

import { useState, useMemo, useCallback, type ReactNode } from 'react';

// ── 类型定义 ──────────────────────────────────────────

export interface TreeNode {
  /** 节点唯一标识 */
  id: string;
  /** 显示名称 */
  name: string;
  /** 图标 className（FontAwesome 格式） */
  icon?: string;
  /** 图标颜色 className（如 text-blue-500） */
  iconColor?: string;
  /** Badge 计数或文本 */
  badge?: number | string;
  /** Badge 颜色样式 */
  badgeColor?: string;
  /** 子节点 */
  children?: TreeNode[];
  /** 是否默认展开（首次渲染） */
  defaultExpanded?: boolean;
  /** 附加的额外数据（透传给 onSelect） */
  data?: unknown;
}

export interface TreeNavProps {
  /** 树数据 */
  data: TreeNode[];
  /** 当前选中的节点 ID */
  selectedId?: string;
  /** 节点选中回调。如果点击已选中的节点，传入 null 表示取消选中 */
  onSelect?: (node: TreeNode | null) => void;
  /** 是否显示搜索框 */
  searchable?: boolean;
  /** 搜索框占位文本 */
  placeholder?: string;
  /** 是否允许选择父节点（默认 false，仅可选叶子节点） */
  allowSelectParent?: boolean;
  /** 空状态提示 */
  emptyText?: string;
  /** 额外的 className */
  className?: string;
  /** 头部额外内容 */
  header?: ReactNode;
  /** 底部额外内容 */
  footer?: ReactNode;
}

// ── 组件实现 ──────────────────────────────────────────

export default function TreeNav({
  data,
  selectedId,
  onSelect,
  searchable = false,
  placeholder = 'Search...',
  allowSelectParent = false,
  emptyText = 'No items found',
  className = '',
  header,
  footer,
}: TreeNavProps) {
  const [search, setSearch] = useState('');
  const [expandedIds, setExpandedIds] = useState<Set<string>>(() => {
    // 默认展开标记了 defaultExpanded 的节点
    const expanded = new Set<string>();
    const collectDefaults = (nodes: TreeNode[]) => {
      for (const node of nodes) {
        if (node.defaultExpanded !== false && node.children && node.children.length > 0) {
          expanded.add(node.id);
        }
        if (node.children) {
          collectDefaults(node.children);
        }
      }
    };
    collectDefaults(data);
    return expanded;
  });

  // ── 搜索过滤 ──────────────────────────────────────

  const filteredData = useMemo(() => {
    const query = search.toLowerCase().trim();
    if (!query) return data;

    // 深度过滤：保留匹配的节点及其祖先
    const filterTree = (nodes: TreeNode[]): TreeNode[] => {
      const result: TreeNode[] = [];
      for (const node of nodes) {
        const nameMatch = node.name.toLowerCase().includes(query);
        const filteredChildren = node.children ? filterTree(node.children) : [];

        if (nameMatch || filteredChildren.length > 0) {
          result.push({
            ...node,
            children: filteredChildren.length > 0 ? filteredChildren : node.children,
            // 搜索时强制展开有匹配子节点的父节点
            defaultExpanded: true,
          });
        }
      }
      return result;
    };

    return filterTree(data);
  }, [data, search]);

  // ── 展开/折叠 ──────────────────────────────────────

  const toggleExpand = useCallback((nodeId: string) => {
    setExpandedIds(prev => {
      const next = new Set(prev);
      if (next.has(nodeId)) {
        next.delete(nodeId);
      } else {
        next.add(nodeId);
      }
      return next;
    });
  }, []);

  // ── 节点选中 ──────────────────────────────────────

  const handleSelect = useCallback((node: TreeNode) => {
    if (!onSelect) return;

    const hasChildren = node.children && node.children.length > 0;

    // 如果不允许选父节点且当前是父节点，只展开/折叠
    if (hasChildren && !allowSelectParent) {
      toggleExpand(node.id);
      return;
    }

    // 如果允许选父节点且当前有子节点，同时展开/折叠
    if (hasChildren && allowSelectParent) {
      toggleExpand(node.id);
    }

    // Toggle 选中状态
    if (selectedId === node.id) {
      onSelect(null);
    } else {
      onSelect(node);
    }
  }, [onSelect, selectedId, allowSelectParent, toggleExpand]);

  // ── 节点总数（用于判断空状态） ──────────────────────

  const totalNodes = useMemo(() => {
    const count = (nodes: TreeNode[]): number =>
      nodes.reduce((acc, n) => acc + 1 + (n.children ? count(n.children) : 0), 0);
    return count(filteredData);
  }, [filteredData]);

  // ── 渲染 ──────────────────────────────────────────

  return (
    <div className={`flex flex-col h-full ${className}`}>
      {/* 头部 */}
      {header && <div className="flex-shrink-0 px-3 py-2">{header}</div>}

      {/* 搜索框 */}
      {searchable && (
        <div className="flex-shrink-0 px-3 py-2">
          <div className="relative">
            <i className="fas fa-search absolute left-3 top-1/2 -translate-y-1/2 text-gray-400 text-xs" />
            <input
              type="text"
              value={search}
              onChange={e => setSearch(e.target.value)}
              placeholder={placeholder}
              className="w-full pl-8 pr-3 py-2 text-sm border border-gray-200 rounded-lg focus:ring-2 focus:ring-blue-500 focus:border-blue-500 bg-white"
            />
            {search && (
              <button
                onClick={() => setSearch('')}
                className="absolute right-2 top-1/2 -translate-y-1/2 text-gray-400 hover:text-gray-600"
              >
                <i className="fas fa-times text-xs" />
              </button>
            )}
          </div>
        </div>
      )}

      {/* 树内容（可滚动） */}
      <div className="flex-1 overflow-y-auto px-2 py-1">
        {totalNodes === 0 ? (
          <div className="px-4 py-8 text-center text-gray-400 text-sm">
            <i className="fas fa-folder-open text-2xl mb-2 block" />
            {emptyText}
          </div>
        ) : (
          <TreeNodeList
            nodes={filteredData}
            level={0}
            expandedIds={expandedIds}
            selectedId={selectedId}
            search={search}
            onToggleExpand={toggleExpand}
            onSelect={handleSelect}
          />
        )}
      </div>

      {/* 底部 */}
      {footer && <div className="flex-shrink-0 px-3 py-2 border-t border-gray-100">{footer}</div>}
    </div>
  );
}

// ── 递归节点列表 ──────────────────────────────────────

interface TreeNodeListProps {
  nodes: TreeNode[];
  level: number;
  expandedIds: Set<string>;
  selectedId?: string;
  search: string;
  onToggleExpand: (id: string) => void;
  onSelect: (node: TreeNode) => void;
}

function TreeNodeList({
  nodes,
  level,
  expandedIds,
  selectedId,
  search,
  onToggleExpand,
  onSelect,
}: TreeNodeListProps) {
  return (
    <ul className="space-y-0.5">
      {nodes.map(node => (
        <TreeNodeItem
          key={node.id}
          node={node}
          level={level}
          expandedIds={expandedIds}
          selectedId={selectedId}
          search={search}
          onToggleExpand={onToggleExpand}
          onSelect={onSelect}
        />
      ))}
    </ul>
  );
}

// ── 单个节点 ──────────────────────────────────────────

interface TreeNodeItemProps {
  node: TreeNode;
  level: number;
  expandedIds: Set<string>;
  selectedId?: string;
  search: string;
  onToggleExpand: (id: string) => void;
  onSelect: (node: TreeNode) => void;
}

function TreeNodeItem({
  node,
  level,
  expandedIds,
  selectedId,
  search,
  onToggleExpand,
  onSelect,
}: TreeNodeItemProps) {
  const hasChildren = node.children && node.children.length > 0;
  const isExpanded = expandedIds.has(node.id) || (search.trim() !== '' && node.defaultExpanded);
  const isSelected = node.id === selectedId;

  return (
    <li>
      <button
        type="button"
        onClick={() => onSelect(node)}
        className={`w-full flex items-center gap-2 px-3 py-2 rounded-lg text-sm transition-all duration-150 ${
          isSelected
            ? 'bg-blue-50 text-blue-700 font-medium'
            : 'text-gray-700 hover:bg-gray-100'
        }`}
        style={{ paddingLeft: `${12 + level * 16}px` }}
      >
        {/* 展开/折叠箭头（用 span + role=button 避免 button 嵌套 button 的 HTML 合法性问题） */}
        {hasChildren ? (
          <span
            role="button"
            tabIndex={0}
            onClick={(e) => {
              e.stopPropagation();
              onToggleExpand(node.id);
            }}
            onKeyDown={(e) => {
              if (e.key === 'Enter' || e.key === ' ') {
                e.preventDefault();
                e.stopPropagation();
                onToggleExpand(node.id);
              }
            }}
            className="w-4 h-4 flex items-center justify-center text-gray-400 hover:text-gray-600 flex-shrink-0 cursor-pointer"
          >
            <i className={`fas fa-chevron-right text-[10px] transition-transform ${isExpanded ? 'rotate-90' : ''}`} />
          </span>
        ) : (
          <span className="w-4 flex-shrink-0" />
        )}

        {/* 图标 */}
        {node.icon && (
          <i className={`${node.icon} text-xs ${node.iconColor || 'text-gray-400'} flex-shrink-0`} />
        )}

        {/* 名称 */}
        <span className="flex-1 truncate text-left">
          {highlightName(node.name, search)}
        </span>

        {/* Badge */}
        {node.badge !== undefined && (
          <span className={`text-xs px-1.5 py-0.5 rounded-full flex-shrink-0 ${
            node.badgeColor || 'bg-gray-100 text-gray-500'
          }`}>
            {node.badge}
          </span>
        )}
      </button>

      {/* 子节点 */}
      {hasChildren && isExpanded && (
        <TreeNodeList
          nodes={node.children!}
          level={level + 1}
          expandedIds={expandedIds}
          selectedId={selectedId}
          search={search}
          onToggleExpand={onToggleExpand}
          onSelect={onSelect}
        />
      )}
    </li>
  );
}

// ── 搜索高亮辅助函数 ──────────────────────────────────

function highlightName(name: string, search: string): ReactNode {
  if (!search.trim()) return name;

  const query = search.toLowerCase().trim();
  const idx = name.toLowerCase().indexOf(query);
  if (idx === -1) return name;

  return (
    <>
      {name.slice(0, idx)}
      <mark className="bg-yellow-200 text-yellow-900 rounded px-0.5">{name.slice(idx, idx + query.length)}</mark>
      {name.slice(idx + query.length)}
    </>
  );
}
