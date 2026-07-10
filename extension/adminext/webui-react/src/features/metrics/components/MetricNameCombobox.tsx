/**
 * MetricNameCombobox — 可搜索的 Metric 名称选择器
 *
 * 功能：
 * - 模糊搜索：输入即实时过滤下拉列表
 * - 键盘导航：↑↓ 移动高亮，Enter 选择，Esc 关闭
 * - 匹配高亮：下拉项中匹配部分用蓝色标注
 * - 点击外部关闭
 *
 * 替代原生 <datalist>，解决补全体验差的问题。
 */

import { useState, useRef, useEffect, useMemo, useCallback } from 'react';

interface MetricNameComboboxProps {
  value: string;
  onChange: (value: string) => void;
  onSelect: () => void; // trigger query on Enter/click
  names: string[];
  placeholder?: string;
  className?: string;
  disabled?: boolean;
}

/** HTML-escapes text and wraps matching substring in a highlight span */
function highlightMatch(text: string, query: string): string {
  if (!query) return escapeHtml(text);
  const escaped = escapeHtml(text);
  const escapedQuery = escapeHtml(query);
  // Case-insensitive match, preserve original casing in display
  const regex = new RegExp(`(${escapedQuery.replace(/[.*+?^${}()|[\]\\]/g, '\\$&')})`, 'gi');
  return escaped.replace(regex, '<mark class="bg-blue-100 text-blue-700 rounded-sm px-0.5">$1</mark>');
}

function escapeHtml(str: string): string {
  return str.replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;').replace(/"/g, '&quot;');
}

export default function MetricNameCombobox({
  value,
  onChange,
  onSelect,
  names,
  placeholder = 'Search metrics...',
  className = '',
  disabled = false,
}: MetricNameComboboxProps) {
  const [isOpen, setIsOpen] = useState(false);
  const [highlightIndex, setHighlightIndex] = useState(0);
  const containerRef = useRef<HTMLDivElement>(null);
  const inputRef = useRef<HTMLInputElement>(null);
  const listRef = useRef<HTMLUListElement>(null);

  // Filtered + highlighted suggestions (limit to 50 for performance)
  const filtered = useMemo(() => {
    if (!value) return names.slice(0, 50);
    const q = value.toLowerCase();
    return names.filter(n => n.toLowerCase().includes(q)).slice(0, 50);
  }, [value, names]);

  // Reset highlight when filter changes
  useEffect(() => {
    setHighlightIndex(0);
  }, [value]);

  // Scroll highlighted item into view
  useEffect(() => {
    if (listRef.current) {
      const item = listRef.current.children[highlightIndex] as HTMLElement | undefined;
      item?.scrollIntoView({ block: 'nearest' });
    }
  }, [highlightIndex]);

  // Close on outside click
  useEffect(() => {
    if (!isOpen) return;
    const handler = (e: MouseEvent) => {
      if (containerRef.current && !containerRef.current.contains(e.target as Node)) {
        setIsOpen(false);
      }
    };
    document.addEventListener('mousedown', handler);
    return () => document.removeEventListener('mousedown', handler);
  }, [isOpen]);

  // Keyboard handlers
  const handleKeyDown = useCallback((e: React.KeyboardEvent) => {
    if (!isOpen && (e.key === 'ArrowDown' || e.key === 'ArrowUp')) {
      setIsOpen(true);
      return;
    }
    switch (e.key) {
      case 'ArrowDown':
        e.preventDefault();
        setHighlightIndex(i => Math.min(i + 1, filtered.length - 1));
        break;
      case 'ArrowUp':
        e.preventDefault();
        setHighlightIndex(i => Math.max(i - 1, 0));
        break;
      case 'Enter':
        e.preventDefault();
        if (isOpen && filtered.length > 0) {
          const selected = filtered[highlightIndex];
          if (selected) {
            onChange(selected);
            setIsOpen(false);
          }
        }
        onSelect();
        break;
      case 'Escape':
        setIsOpen(false);
        break;
    }
  }, [isOpen, filtered, highlightIndex, onChange, onSelect]);

  const handleSelect = (name: string) => {
    onChange(name);
    setIsOpen(false);
    inputRef.current?.focus();
  };

  return (
    <div ref={containerRef} className={`relative ${className}`}>
      <input
        ref={inputRef}
        type="text"
        value={value}
        onChange={(e) => {
          onChange(e.target.value);
          if (!isOpen) setIsOpen(true);
        }}
        onFocus={() => setIsOpen(true)}
        onKeyDown={handleKeyDown}
        placeholder={placeholder}
        disabled={disabled}
        className="w-full px-4 py-2 border border-gray-200 rounded-lg text-sm font-mono focus:ring-2 focus:ring-primary-500 focus:border-primary-500"
      />

      {/* Dropdown */}
      {isOpen && filtered.length > 0 && (
        <ul
          ref={listRef}
          className="absolute top-full left-0 right-0 mt-1 bg-white border border-gray-200 rounded-lg shadow-lg z-50 max-h-60 overflow-y-auto"
        >
          {filtered.map((name, i) => (
            <li
              key={name}
              onClick={() => handleSelect(name)}
              onMouseEnter={() => setHighlightIndex(i)}
              className={`px-4 py-2 text-sm font-mono cursor-pointer truncate ${
                i === highlightIndex
                  ? 'bg-primary-50 text-primary-700'
                  : 'text-gray-700 hover:bg-gray-50'
              }`}
              // eslint-disable-next-line react/no-danger
              dangerouslySetInnerHTML={{ __html: highlightMatch(name, value) }}
            />
          ))}
        </ul>
      )}

      {/* No results */}
      {isOpen && value && filtered.length === 0 && (
        <div className="absolute top-full left-0 right-0 mt-1 bg-white border border-gray-200 rounded-lg shadow-lg z-50 p-4 text-center text-sm text-gray-400">
          No matching metrics
        </div>
      )}
    </div>
  );
}
