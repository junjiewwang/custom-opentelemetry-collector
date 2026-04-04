/**
 * SearchableSelect - 可复用的搜索下拉框组件
 *
 * 从旧版 Alpine.js SearchableSelect 完整移植到 React + TypeScript。
 *
 * 支持特性:
 *   - 输入搜索过滤选项
 *   - 分组显示 (optgroup 效果)
 *   - 键盘导航 (↑ ↓ Enter Escape)
 *   - 高亮匹配文字
 *   - 点击外部自动关闭
 *   - 懒加载数据源
 *   - 允许自定义输入 (Custom 选项)
 */

import { useState, useRef, useEffect, useCallback, useMemo } from 'react';

// ── 类型定义 ──────────────────────────────────────────

export interface SelectOption {
  value: string;
  label: string;
  group?: string;
  icon?: string;
  /** 允许附加任意字段，搜索时可匹配 */
  [key: string]: unknown;
}

interface OptionGroup {
  group: string;
  options: SelectOption[];
}

export interface SearchableSelectProps {
  /** 选项列表 */
  options: SelectOption[];
  /** 当前选中值 */
  value: string;
  /** 值变更回调 */
  onChange: (value: string) => void;
  /** 占位文本 */
  placeholder?: string;
  /** 参与搜索的字段名（默认 ['label']） */
  searchKeys?: string[];
  /** 分组名列表（按此顺序展示，不在列表中的归入 "Other"） */
  groups?: string[];
  /** 是否允许自定义输入 */
  allowCustom?: boolean;
  /** 自定义选项的显示文字 */
  customLabel?: string;
  /** 无结果提示 */
  emptyText?: string;
  /** 懒加载函数（首次展开时调用一次） */
  onLazyLoad?: () => Promise<void>;
  /** 是否禁用 */
  disabled?: boolean;
  /** 额外的 className */
  className?: string;
  /** 加载状态（外部控制） */
  loading?: boolean;
}

// ── 工具函数 ──────────────────────────────────────────

function escapeHtml(str: string): string {
  const map: Record<string, string> = { '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' };
  return String(str).replace(/[&<>"']/g, ch => map[ch] || ch);
}

function escapeRegex(str: string): string {
  return str.replace(/[.*+?^${}()|[\]\\]/g, '\\$&');
}

// ── 组件实现 ──────────────────────────────────────────

export default function SearchableSelect({
  options,
  value,
  onChange,
  placeholder = 'Search...',
  searchKeys = ['label'],
  groups = [],
  allowCustom = false,
  customLabel = '-- Custom --',
  emptyText = 'No matching results',
  onLazyLoad,
  disabled = false,
  className = '',
  loading: externalLoading = false,
}: SearchableSelectProps) {
  const [isOpen, setIsOpen] = useState(false);
  const [search, setSearch] = useState('');
  const [highlightIdx, setHighlightIdx] = useState(-1);
  const [isCustomMode, setIsCustomMode] = useState(false);
  const [customValue, setCustomValue] = useState('');
  const [lazyLoaded, setLazyLoaded] = useState(false);
  const [internalLoading, setInternalLoading] = useState(false);

  const containerRef = useRef<HTMLDivElement>(null);
  const dropdownRef = useRef<HTMLDivElement>(null);
  const searchInputRef = useRef<HTMLInputElement>(null);
  const customInputRef = useRef<HTMLInputElement>(null);

  const isLoading = externalLoading || internalLoading;

  // ── 搜索过滤 ──────────────────────────────────────

  const filteredOptions = useMemo(() => {
    const query = search.toLowerCase().trim();
    if (!query) return options;
    return options.filter(opt =>
      searchKeys.some(key => {
        const val = opt[key];
        return val != null && String(val).toLowerCase().includes(query);
      })
    );
  }, [options, search, searchKeys]);

  // ── 分组 ──────────────────────────────────────────

  const groupedOptions = useMemo<OptionGroup[]>(() => {
    if (groups.length === 0) {
      return [{ group: '', options: filteredOptions }];
    }

    const groupMap = new Map<string, SelectOption[]>();
    for (const g of groups) {
      groupMap.set(g, []);
    }
    for (const opt of filteredOptions) {
      const g = opt.group || '';
      if (groupMap.has(g)) {
        groupMap.get(g)!.push(opt);
      } else {
        if (!groupMap.has('Other')) groupMap.set('Other', []);
        groupMap.get('Other')!.push(opt);
      }
    }

    const result: OptionGroup[] = [];
    for (const [group, opts] of groupMap) {
      if (opts.length > 0) {
        result.push({ group, options: opts });
      }
    }
    return result;
  }, [filteredOptions, groups]);

  // ── 扁平化选项列表（含 custom 虚拟选项，用于键盘导航） ──

  const flatOptions = useMemo(() => {
    const flat: (SelectOption & { _isCustom?: boolean })[] = [];
    for (const g of groupedOptions) {
      for (const opt of g.options) {
        flat.push(opt);
      }
    }
    if (allowCustom) {
      flat.push({ value: '__custom__', label: customLabel, _isCustom: true });
    }
    return flat;
  }, [groupedOptions, allowCustom, customLabel]);

  // ── 获取选中选项的显示文字 ──────────────────────────

  const selectedLabel = useMemo(() => {
    if (isCustomMode) return customValue || '';
    const opt = options.find(o => o.value === value);
    return opt ? (opt.icon ? `${opt.icon} ${opt.label}` : opt.label) : '';
  }, [options, value, isCustomMode, customValue]);

  // ── 打开/关闭 ──────────────────────────────────────

  const open = useCallback(async () => {
    if (disabled) return;

    // 懒加载
    if (onLazyLoad && !lazyLoaded) {
      setInternalLoading(true);
      try {
        await onLazyLoad();
        setLazyLoaded(true);
      } catch (e) {
        console.error('[SearchableSelect] lazy load error:', e);
      } finally {
        setInternalLoading(false);
      }
    }

    setIsOpen(true);
    setSearch('');
    setHighlightIdx(-1);

    // 聚焦搜索框
    requestAnimationFrame(() => {
      searchInputRef.current?.focus();
    });
  }, [disabled, onLazyLoad, lazyLoaded]);

  const close = useCallback(() => {
    setIsOpen(false);
    setSearch('');
    setHighlightIdx(-1);
  }, []);

  const toggle = useCallback(async () => {
    if (isOpen) {
      close();
    } else {
      await open();
    }
  }, [isOpen, close, open]);

  // ── 选中 ──────────────────────────────────────────

  const selectOption = useCallback((option: SelectOption & { _isCustom?: boolean }) => {
    if (option._isCustom) {
      setIsCustomMode(true);
      setCustomValue('');
      onChange('__custom__');
      close();
      requestAnimationFrame(() => {
        customInputRef.current?.focus();
      });
      return;
    }

    setIsCustomMode(false);
    setCustomValue('');
    onChange(option.value);
    close();
  }, [onChange, close]);

  // ── 自定义输入值变更 ──────────────────────────────

  const handleCustomValueChange = useCallback((val: string) => {
    setCustomValue(val);
    onChange(val);
  }, [onChange]);

  // ── 退出自定义模式 ──────────────────────────────────

  const exitCustomMode = useCallback(() => {
    setIsCustomMode(false);
    setCustomValue('');
  }, []);

  // ── 键盘导航 ──────────────────────────────────────

  const handleKeyDown = useCallback((event: React.KeyboardEvent) => {
    if (flatOptions.length === 0) return;

    switch (event.key) {
      case 'ArrowDown':
        event.preventDefault();
        if (!isOpen) {
          open();
          return;
        }
        setHighlightIdx(prev => Math.min(prev + 1, flatOptions.length - 1));
        break;

      case 'ArrowUp':
        event.preventDefault();
        setHighlightIdx(prev => Math.max(prev - 1, 0));
        break;

      case 'Enter':
        event.preventDefault();
        if (isOpen && highlightIdx >= 0 && highlightIdx < flatOptions.length) {
          const opt = flatOptions[highlightIdx];
          if (opt) selectOption(opt);
        }
        break;

      case 'Escape':
        event.preventDefault();
        close();
        break;
    }
  }, [flatOptions, isOpen, highlightIdx, open, close, selectOption]);

  // ── 自动滚动高亮项 ──────────────────────────────────

  useEffect(() => {
    if (highlightIdx >= 0 && dropdownRef.current) {
      const highlighted = dropdownRef.current.querySelector('[data-highlighted="true"]');
      if (highlighted) {
        highlighted.scrollIntoView({ block: 'nearest', behavior: 'smooth' });
      }
    }
  }, [highlightIdx]);

  // ── 点击外部关闭 ──────────────────────────────────

  useEffect(() => {
    if (!isOpen) return;

    const handleClickOutside = (e: MouseEvent) => {
      if (containerRef.current && !containerRef.current.contains(e.target as Node)) {
        close();
      }
    };
    document.addEventListener('mousedown', handleClickOutside);
    return () => document.removeEventListener('mousedown', handleClickOutside);
  }, [isOpen, close]);

  // ── 高亮匹配文字 ──────────────────────────────────

  const highlightText = useCallback((text: string): string => {
    if (!text) return '';
    const query = search.trim();
    if (!query) return escapeHtml(text);

    const escaped = escapeHtml(text);
    const regex = new RegExp(`(${escapeRegex(query)})`, 'gi');
    return escaped.replace(regex, '<mark class="bg-yellow-200 text-yellow-900 rounded px-0.5">$1</mark>');
  }, [search]);

  // ── 渲染 ──────────────────────────────────────────

  return (
    <div ref={containerRef} className={`relative ${className}`}>
      {/* 自定义输入模式 */}
      {isCustomMode ? (
        <div className="flex items-center gap-2">
          <input
            ref={customInputRef}
            type="text"
            value={customValue}
            onChange={e => handleCustomValueChange(e.target.value)}
            placeholder="Enter custom value..."
            className="flex-1 px-4 py-2.5 border border-gray-200 rounded-lg focus:ring-2 focus:ring-blue-500 focus:border-blue-500 text-sm"
          />
          <button
            type="button"
            onClick={exitCustomMode}
            className="px-3 py-2.5 text-gray-400 hover:text-gray-600 border border-gray-200 rounded-lg hover:bg-gray-50 transition"
            title="Back to selection"
          >
            <i className="fas fa-times" />
          </button>
        </div>
      ) : (
        /* 选择器按钮 */
        <button
          type="button"
          onClick={toggle}
          disabled={disabled}
          onKeyDown={handleKeyDown}
          title={selectedLabel || undefined}
          className={`w-full flex items-center justify-between px-4 py-2.5 border rounded-lg transition text-left text-sm ${
            disabled
              ? 'bg-gray-100 text-gray-400 cursor-not-allowed border-gray-200'
              : isOpen
                ? 'border-blue-500 ring-2 ring-blue-500 bg-white'
                : 'border-gray-200 bg-white hover:border-gray-300'
          }`}
        >
          <span className={`truncate ${selectedLabel ? 'text-gray-800' : 'text-gray-400'}`}>
            {selectedLabel || placeholder}
          </span>
          <div className="flex items-center gap-1">
            {isLoading && (
              <i className="fas fa-spinner fa-spin text-gray-400 text-xs" />
            )}
            <i className={`fas fa-chevron-down text-gray-400 text-xs transition-transform ${isOpen ? 'rotate-180' : ''}`} />
          </div>
        </button>
      )}

      {/* 下拉面板 */}
      {isOpen && (
        <div className="absolute z-50 mt-1 min-w-full w-max max-w-[360px] bg-white border border-gray-200 rounded-lg shadow-lg overflow-hidden fade-in">
          {/* 搜索框 */}
          <div className="p-2 border-b border-gray-100">
            <div className="relative">
              <i className="fas fa-search absolute left-3 top-1/2 -translate-y-1/2 text-gray-400 text-xs" />
              <input
                ref={searchInputRef}
                type="text"
                value={search}
                onChange={e => {
                  setSearch(e.target.value);
                  setHighlightIdx(-1);
                }}
                onKeyDown={handleKeyDown}
                placeholder={placeholder}
                className="w-full pl-8 pr-3 py-2 text-sm border border-gray-200 rounded-md focus:ring-2 focus:ring-blue-500 focus:border-blue-500"
              />
            </div>
          </div>

          {/* 选项列表 */}
          <div ref={dropdownRef} className="max-h-60 overflow-y-auto">
            {isLoading ? (
              <div className="px-4 py-6 text-center text-gray-400 text-sm">
                <i className="fas fa-spinner fa-spin mr-2" />
                Loading...
              </div>
            ) : flatOptions.length === 0 ? (
              <div className="px-4 py-6 text-center text-gray-400 text-sm">
                {emptyText}
              </div>
            ) : (
              <>
                {groupedOptions.map((group, gi) => (
                  <div key={gi}>
                    {/* 分组标题 */}
                    {group.group && (
                      <div className="px-3 py-1.5 text-xs font-semibold text-gray-400 uppercase tracking-wider bg-gray-50 sticky top-0">
                        {group.group}
                      </div>
                    )}
                    {/* 选项 */}
                    {group.options.map((opt) => {
                      const flatIdx = flatOptions.indexOf(opt);
                      const isSelected = !isCustomMode && opt.value === value;
                      const isHighlighted = flatIdx === highlightIdx;

                      return (
                        <button
                          key={opt.value}
                          type="button"
                          data-highlighted={isHighlighted ? 'true' : 'false'}
                          onClick={() => selectOption(opt)}
                          className={`w-full flex items-center gap-2 px-4 py-2 text-sm text-left transition ${
                            isHighlighted
                              ? 'bg-blue-50 text-blue-700'
                              : isSelected
                                ? 'bg-blue-50 text-blue-600'
                                : 'text-gray-700 hover:bg-gray-50'
                          }`}
                        >
                          {opt.icon && <span>{opt.icon}</span>}
                          <span
                            className="flex-1"
                            dangerouslySetInnerHTML={{ __html: highlightText(opt.label) }}
                          />
                          {isSelected && (
                            <i className="fas fa-check text-blue-500 text-xs" />
                          )}
                        </button>
                      );
                    })}
                  </div>
                ))}

                {/* Custom 选项 */}
                {allowCustom && (
                  <button
                    type="button"
                    data-highlighted={highlightIdx === flatOptions.length - 1 ? 'true' : 'false'}
                    onClick={() => selectOption({ value: '__custom__', label: customLabel, _isCustom: true })}
                    className={`w-full flex items-center gap-2 px-4 py-2 text-sm text-left border-t border-gray-100 transition ${
                      highlightIdx === flatOptions.length - 1
                        ? 'bg-blue-50 text-blue-700'
                        : 'text-gray-500 hover:bg-gray-50'
                    }`}
                  >
                    <i className="fas fa-keyboard text-xs" />
                    <span>{customLabel}</span>
                  </button>
                )}
              </>
            )}
          </div>
        </div>
      )}
    </div>
  );
}
