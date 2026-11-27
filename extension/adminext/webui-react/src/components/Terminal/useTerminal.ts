/**
 * useTerminal — xterm.js 的 React Hook 封装
 *
 * 功能：
 *   - 自动创建/销毁 Terminal 实例
 *   - FitAddon 自适应 + ResizeObserver
 *   - SearchAddon 搜索（findNext / findPrevious）
 *   - WebLinksAddon 链接可点击
 *   - 提供 write / clear / focus / resize / getProposedDimensions 方法
 *   - 支持 cols-1 / rows-1 修正（防止滚动条遮挡 + 最后一行截断）
 */

import { useRef, useEffect, useCallback } from 'react';
import { Terminal } from '@xterm/xterm';
import { FitAddon } from '@xterm/addon-fit';
import { SearchAddon } from '@xterm/addon-search';
import { WebLinksAddon } from '@xterm/addon-web-links';

// xterm.js CSS 需要在组件中全局导入一次
import '@xterm/xterm/css/xterm.css';

export interface UseTerminalOptions {
  /** 字体大小，默认 14 */
  fontSize?: number;
  /** 回滚行数，默认 10000 */
  scrollback?: number;
  /** 光标闪烁，默认 true */
  cursorBlink?: boolean;
  /** 是否将 \n 按 \r\n 语义处理，适合非 PTY 文本流场景 */
  convertEol?: boolean;
  /** 当 xterm 有用户输入时的回调（raw data） */
  onData?: (data: string) => void;
  /** 终端就绪回调（首次 fit 完成后） */
  onReady?: (cols: number, rows: number) => void;
  /** 终端尺寸变化时的回调 */
  onResize?: (cols: number, rows: number) => void;
}

export interface TerminalHandle {
  /** 写入数据到终端（支持 ANSI 序列） */
  write: (data: string) => void;
  /** 清屏 */
  clear: () => void;
  /** 聚焦 */
  focus: () => void;
  /** 失焦 */
  blur: () => void;
  /** 重新 fit（容器大小变化后手动调用） */
  fit: () => { cols: number; rows: number } | null;
  /** 获取当前应生效的尺寸（不实际 resize）；已含 FIT_SAFETY_MARGIN 修正，与 xterm.cols/rows 一致 */
  getProposedDimensions: () => { cols: number; rows: number } | null;
  /** 搜索相关 */
  findNext: (query: string, options?: SearchOptions) => boolean;
  findPrevious: (query: string, options?: SearchOptions) => boolean;
  /** 获取底层 xterm 实例（高级用途） */
  getXterm: () => Terminal | null;
}

export interface SearchOptions {
  caseSensitive?: boolean;
  regex?: boolean;
  wholeWord?: boolean;
  incremental?: boolean;
}

const DARK_THEME = {
  background: '#1e1e1e',
  foreground: '#d4d4d4',
  cursor: '#00ff00',
  cursorAccent: '#1e1e1e',
  selectionBackground: '#264f78',
  black: '#000000',
  red: '#cd3131',
  green: '#0dbc79',
  yellow: '#e5e510',
  blue: '#2472c8',
  magenta: '#bc3fbc',
  cyan: '#11a8cd',
  white: '#e5e5e5',
  brightBlack: '#666666',
  brightRed: '#f14c4c',
  brightGreen: '#23d18b',
  brightYellow: '#f5f543',
  brightBlue: '#3b8eea',
  brightMagenta: '#d670d6',
  brightCyan: '#29b8db',
  brightWhite: '#ffffff',
};

/**
 * 实际生效尺寸 = FitAddon 建议尺寸 - 安全余量。
 *
 * 为什么要减：@xterm/addon-fit@0.11 的 proposeDimensions() 取
 * getComputedStyle(parent).width/height；项目全局 box-sizing: border-box，
 * 且 xterm 容器带 px-2 pt-2 pb-2（8px padding），该值含 padding。FitAddon
 * 只减去 .xterm 自身 padding（实测 0px）并预留固定 14px overview ruler，
 * 于是高估约 1 列 / 1 行；字符单元实测 8.4024 × 16px，raw 行会超出可用高。
 *
 * 浏览器实测 5 种视口（800×600 / 1024×768 / 1280×720 / 1440×900 /
 * 1600×1000）：raw-1 在每一档都恰好抵消误差，既不溢出也不浪费。
 * 这是与 fontSize=14、容器 padding=8px、DPR=1、border-box 绑定的经验余量；
 * 修改字体/缩放/容器 padding 或升级 addon-fit 时必须重新实测，不要直接删掉。
 */
const FIT_SAFETY_MARGIN = 1;

function adjustDimensions(raw: { cols: number; rows: number }) {
  return {
    cols: Math.max(1, raw.cols - FIT_SAFETY_MARGIN),
    rows: Math.max(1, raw.rows - FIT_SAFETY_MARGIN),
  };
}

export default function useTerminal(
  containerRef: React.RefObject<HTMLDivElement | null>,
  options: UseTerminalOptions = {},
): TerminalHandle {
  const xtermRef = useRef<Terminal | null>(null);
  const fitRef = useRef<FitAddon | null>(null);
  const searchRef = useRef<SearchAddon | null>(null);
  const onDataRef = useRef(options.onData);
  const onReadyRef = useRef(options.onReady);
  const onResizeRef = useRef(options.onResize);

  // 保持回调引用最新
  onDataRef.current = options.onData;
  onReadyRef.current = options.onReady;
  onResizeRef.current = options.onResize;

  useEffect(() => {
    const container = containerRef.current;
    if (!container) return;

    // 创建 xterm 实例
    const xterm = new Terminal({
      cursorBlink: options.cursorBlink ?? true,
      cursorStyle: 'block',
      fontSize: options.fontSize ?? 14,
      fontFamily: 'Consolas, Monaco, "Courier New", monospace',
      theme: DARK_THEME,
      allowTransparency: false,
      scrollback: options.scrollback ?? 10000,
      convertEol: options.convertEol ?? false,
      tabStopWidth: 4,
    });

    const fitAddon = new FitAddon();
    const searchAddon = new SearchAddon();
    const webLinksAddon = new WebLinksAddon();

    xterm.loadAddon(fitAddon);
    xterm.loadAddon(searchAddon);
    xterm.loadAddon(webLinksAddon);

    xterm.open(container);
    xtermRef.current = xterm;
    fitRef.current = fitAddon;
    searchRef.current = searchAddon;

    // 初始 fit
    requestAnimationFrame(() => {
      const raw = fitAddon.proposeDimensions();
      if (raw) {
        const { cols, rows } = adjustDimensions(raw);
        xterm.resize(cols, rows);
        xterm.scrollToBottom();
        onReadyRef.current?.(cols, rows);
      }
    });

    // 用户输入回调
    const dataDisp = xterm.onData(data => {
      onDataRef.current?.(data);
    });

    // ResizeObserver 自适应
    let resizeTimer: ReturnType<typeof setTimeout>;
    const resizeObserver = new ResizeObserver(() => {
      clearTimeout(resizeTimer);
      resizeTimer = setTimeout(() => {
        const raw = fitAddon.proposeDimensions();
        if (!raw) return;
        const { cols, rows } = adjustDimensions(raw);
        xterm.resize(cols, rows);
        xterm.scrollToBottom();
        onResizeRef.current?.(cols, rows);
      }, 100);
    });
    resizeObserver.observe(container);

    // 清理
    return () => {
      clearTimeout(resizeTimer);
      resizeObserver.disconnect();
      dataDisp.dispose();
      xterm.dispose();
      xtermRef.current = null;
      fitRef.current = null;
      searchRef.current = null;
    };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [containerRef]);

  // ── 暴露方法 ──

  const write = useCallback((data: string) => {
    xtermRef.current?.write(data);
  }, []);

  const clear = useCallback(() => {
    const xterm = xtermRef.current;
    if (!xterm) return;
    xterm.clear();
    xterm.writeln('\x1b[32mTerminal Cleared\x1b[0m');
  }, []);

  const focus = useCallback(() => {
    xtermRef.current?.focus();
  }, []);

  const blur = useCallback(() => {
    xtermRef.current?.blur();
  }, []);

  const fit = useCallback((): { cols: number; rows: number } | null => {
    const raw = fitRef.current?.proposeDimensions();
    if (!raw || !xtermRef.current) return null;
    const adjusted = adjustDimensions(raw);
    xtermRef.current.resize(adjusted.cols, adjusted.rows);
    xtermRef.current.scrollToBottom();
    return adjusted;
  }, []);

  const getProposedDimensions = useCallback((): { cols: number; rows: number } | null => {
    const raw = fitRef.current?.proposeDimensions();
    if (!raw) return null;
    return adjustDimensions(raw);
  }, []);

  const findNext = useCallback((query: string, opts?: SearchOptions): boolean => {
    return searchRef.current?.findNext(query, opts) ?? false;
  }, []);

  const findPrevious = useCallback((query: string, opts?: SearchOptions): boolean => {
    return searchRef.current?.findPrevious(query, opts) ?? false;
  }, []);

  const getXterm = useCallback(() => xtermRef.current, []);

  return {
    write,
    clear,
    focus,
    blur,
    fit,
    getProposedDimensions,
    findNext,
    findPrevious,
    getXterm,
  };
}
