/**
 * TerminalPanel — Arthas 终端面板组件
 *
 * 功能：
 *   - 全屏覆盖式终端面板（fixed 定位）
 *   - 头部：Service/IP 信息 + Clear/Search/Close 按钮
 *   - VSCode 风格搜索框（Ctrl/Cmd+F 触发，Enter/Shift+Enter 导航）
 *   - WebSocket 连接管理（连接/断开/重连/超时保护）
 *   - 命令历史（↑↓ 键导航）
 *   - Arthas Attach → Connect 全流程
 */

import { useState, useCallback, useRef, useEffect } from 'react';
import useTerminal from './useTerminal';
import { apiClient } from '@/api/client';
import type { EnrichedInstance, ApiError } from '@/types/api';

export interface TerminalPanelProps {
  /** 实例信息 */
  instance: EnrichedInstance;
  /** 关闭面板回调 */
  onClose: () => void;
  /** 连接状态变化回调（用于刷新 Arthas 状态） */
  onStatusChange?: () => void;
}

// WS 消息协议（与旧版一致）
interface WSInputMessage {
  action: 'read' | 'resize';
  data?: string;
  cols?: number;
  rows?: number;
}

export default function TerminalPanel({ instance, onClose, onStatusChange }: TerminalPanelProps) {
  const containerRef = useRef<HTMLDivElement>(null);
  const wsRef = useRef<WebSocket | null>(null);
  const [connecting, setConnecting] = useState(false);
  const [connected, setConnected] = useState(false);
  const [error, setError] = useState('');

  // 搜索相关
  const [searchOpen, setSearchOpen] = useState(false);
  const [searchQuery, setSearchQuery] = useState('');
  const [searchCaseSensitive, setSearchCaseSensitive] = useState(false);
  const [searchRegex, setSearchRegex] = useState(false);
  const [searchStatus, setSearchStatus] = useState('');
  const searchInputRef = useRef<HTMLInputElement>(null);

  // 命令历史
  const historyRef = useRef<string[]>([]);
  const historyIdxRef = useRef(-1);
  const currentLineRef = useRef('');

  // 标记 relay 是否已就绪（后端发送 [+] ready 状态后为 true）
  const relayReadyRef = useRef(false);

  const serviceName = instance.service_name || 'unknown';
  const ip = instance.ip || instance.hostname || '';
  const tunnelAgentId = instance.arthasStatus?.tunnelAgentId || '';

  // ── WebSocket 数据发送 ──

  const sendWS = useCallback((msg: WSInputMessage) => {
    const ws = wsRef.current;
    if (ws && ws.readyState === WebSocket.OPEN) {
      ws.send(JSON.stringify(msg));
    }
  }, []);

  // ── xterm.js Hook ──

  const terminal = useTerminal(containerRef, {
    fontSize: 14,
    scrollback: 10000,
    cursorBlink: true,
    onData: (data: string) => {
      const code = data.charCodeAt(0);

      // Ctrl+L → 清屏（本地）
      if (code === 12) {
        terminal.clear();
        return;
      }

      // Ctrl+F → 搜索
      // 注意：Ctrl+F 由 xterm.attachCustomKeyEventHandler 处理，这里不需要

      // ↑ 键 → 命令历史
      if (data === '\x1b[A') {
        const hist = historyRef.current;
        if (hist.length > 0 && historyIdxRef.current < hist.length - 1) {
          historyIdxRef.current++;
          const cmd = hist[hist.length - 1 - historyIdxRef.current] ?? '';
          const clearCmd = '\x7f'.repeat(currentLineRef.current.length);
          currentLineRef.current = cmd;
          sendWS({ action: 'read', data: clearCmd + cmd });
        }
        return;
      }

      // ↓ 键 → 命令历史
      if (data === '\x1b[B') {
        const hist = historyRef.current;
        if (historyIdxRef.current > 0) {
          historyIdxRef.current--;
          const cmd = hist[hist.length - 1 - historyIdxRef.current] ?? '';
          const clearCmd = '\x7f'.repeat(currentLineRef.current.length);
          currentLineRef.current = cmd;
          sendWS({ action: 'read', data: clearCmd + cmd });
        } else if (historyIdxRef.current === 0) {
          historyIdxRef.current = -1;
          const clearCmd = '\x7f'.repeat(currentLineRef.current.length);
          currentLineRef.current = '';
          sendWS({ action: 'read', data: clearCmd });
        }
        return;
      }

      // 追踪当前行
      if (code === 13) {
        // Enter
        const cmd = currentLineRef.current.trim();
        if (cmd) historyRef.current.push(cmd);
        historyIdxRef.current = -1;
        currentLineRef.current = '';
      } else if (code === 127) {
        // Backspace
        if (currentLineRef.current.length > 0) {
          currentLineRef.current = currentLineRef.current.slice(0, -1);
        }
      } else if (code === 3) {
        // Ctrl+C
        currentLineRef.current = '';
      } else if (code >= 32 && code < 127) {
        currentLineRef.current += data;
      }

      // 发送到 server
      sendWS({ action: 'read', data });
    },
    onReady: (cols, rows) => {
      // 连接 WebSocket 时用的初始尺寸
      connectWebSocket(cols, rows);
    },
    onResize: (cols, rows) => {
      sendWS({ action: 'resize', cols, rows });
    },
  });

  // ── Ctrl+F 拦截（需要 xterm 实例就绪后注册） ──

  useEffect(() => {
    const xterm = terminal.getXterm();
    if (!xterm) return;
    const handler = xterm.attachCustomKeyEventHandler((e: KeyboardEvent) => {
      if ((e.ctrlKey || e.metaKey) && e.key === 'f') {
        e.preventDefault();
        setSearchOpen(true);
        setTimeout(() => searchInputRef.current?.focus(), 50);
        return false;
      }
      return true;
    });
    return () => { handler; };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [terminal.getXterm()]);

  // ── WebSocket 连接 ──

  const connectWebSocket = useCallback(async (_initCols?: number, _initRows?: number) => {
    if (connecting || connected) return;
    setConnecting(true);
    setError('');

    // 欢迎消息
    terminal.write('\x1b[32mArthas Terminal\x1b[0m\r\n');
    terminal.write(`\x1b[90mService: ${serviceName}\x1b[0m\r\n`);
    terminal.write(`\x1b[90mInstance: ${ip}\x1b[0m\r\n`);

    try {
      // 如果 tunnel 未就绪，先尝试 attach
      if (!instance.arthasStatus?.tunnelReady) {
        terminal.write('\x1b[33m[System] Tunnel not ready, trying to attach Arthas...\x1b[0m\r\n');
        try {
          await apiClient.attachArthas(instance.agent_id);
          terminal.write('\x1b[32m[System] Attach request sent, waiting...\x1b[0m\r\n');
          // 等待 tunnel 就绪
          await new Promise(r => setTimeout(r, 2000));
        } catch (attachErr) {
          terminal.write(`\x1b[31m[System] Attach failed: ${(attachErr as ApiError).message}\x1b[0m\r\n`);
          setError('Attach failed');
          setConnecting(false);
          return;
        }
      }

      const agentId = tunnelAgentId || instance.agent_id;
      if (!agentId) {
        terminal.write('\x1b[31m[System] No agent ID available\x1b[0m\r\n');
        setError('No agent ID');
        setConnecting(false);
        return;
      }

      // 获取 WS Token
      terminal.write('\x1b[90m[System] Authenticating...\x1b[0m\r\n');
      const tokenRes = await apiClient.generateWSToken();
      if (!tokenRes.token) throw new Error('Failed to obtain WebSocket token');

      // 建立 WebSocket 连接
      const protocol = window.location.protocol === 'https:' ? 'wss:' : 'ws:';
      const qs = new URLSearchParams({
        method: 'connectArthas',
        id: agentId,
        token: tokenRes.token,
        agent_id: agentId,
      });
      const wsUrl = `${protocol}//${window.location.host}/api/v2/arthas/ws?${qs.toString()}`;

      terminal.write('\x1b[90m[System] Connecting to Arthas...\x1b[0m\r\n');

      const ws = new WebSocket(wsUrl);
      ws.binaryType = 'arraybuffer';
      wsRef.current = ws;

      ws.onopen = () => {
        setConnected(true);
        setConnecting(false);
        relayReadyRef.current = false;
        // 注意：此时后端还在执行 connectArthas 流程（查找 agent → startTunnel → 等待 openTunnel），
        // 尚未进入 relay 模式，所以不能在这里发送 resize，否则 Arthas 收不到。
        // resize 会在检测到后端 ready 状态消息后发送。
        terminal.focus();
        onStatusChange?.();
      };

      ws.onmessage = (event) => {
        let text = '';
        if (event.data instanceof ArrayBuffer) {
          text = new TextDecoder('utf-8').decode(new Uint8Array(event.data));
        } else if (typeof event.data === 'string') {
          text = event.data;
        } else if (event.data instanceof Blob) {
          event.data.arrayBuffer().then(buf => {
            const decoded = new TextDecoder('utf-8').decode(new Uint8Array(buf));
            terminal.write(decoded);
            // Blob 类型也需要检测 ready 状态
            if (!relayReadyRef.current && decoded.includes('[+]')) {
              relayReadyRef.current = true;
              setTimeout(() => {
                const dims = terminal.getProposedDimensions();
                if (dims) {
                  sendWS({ action: 'resize', cols: dims.cols, rows: dims.rows });
                }
              }, 100);
            }
          });
          return;
        }

        terminal.write(text);

        // 检测后端发送的 ready 状态消息（"[+] Connected successfully, terminal is ready"）
        // 此时后端已进入 relayWebSocketPair 模式，可以安全发送 resize 触发 Arthas banner
        if (!relayReadyRef.current && text.includes('[+]')) {
          relayReadyRef.current = true;
          // 短暂延迟确保 relay 完全就绪
          setTimeout(() => {
            const dims = terminal.getProposedDimensions();
            if (dims) {
              sendWS({ action: 'resize', cols: dims.cols, rows: dims.rows });
            }
          }, 100);
        }
      };

      ws.onerror = () => {
        setError('WebSocket connection error');
        terminal.write('\r\n\x1b[31m[System] WebSocket error\x1b[0m\r\n');
      };

      ws.onclose = (event) => {
        setConnected(false);
        wsRef.current = null;
        const reason = event.reason ? `, reason: ${event.reason}` : '';
        terminal.write(`\r\n\x1b[33m[System] Connection closed (code: ${event.code}${reason})\x1b[0m\r\n`);
        // 延迟刷新实例状态
        setTimeout(() => onStatusChange?.(), 500);
      };

      // 超时保护
      setTimeout(() => {
        if (ws.readyState !== WebSocket.OPEN) {
          try { ws.close(); } catch { /* ignore */ }
          setError('Connection timeout');
          setConnecting(false);
          terminal.write('\r\n\x1b[31m[System] Connection timeout (15s)\x1b[0m\r\n');
        }
      }, 15000);

    } catch (e) {
      setError((e as Error).message || 'Connection failed');
      setConnecting(false);
      terminal.write(`\r\n\x1b[31m[System] Error: ${(e as Error).message}\x1b[0m\r\n`);
    }
  }, [connecting, connected, instance, tunnelAgentId, serviceName, ip, terminal, sendWS, onStatusChange]);

  // ── 关闭 ──

  const handleClose = useCallback(() => {
    const ws = wsRef.current;
    if (ws) {
      try { ws.close(); } catch { /* ignore */ }
      wsRef.current = null;
    }
    onClose();
  }, [onClose]);

  // ── 搜索 ──

  const doSearch = useCallback((direction: 'next' | 'prev') => {
    if (!searchQuery) return;
    const opts = { caseSensitive: searchCaseSensitive, regex: searchRegex };
    const found = direction === 'next'
      ? terminal.findNext(searchQuery, opts)
      : terminal.findPrevious(searchQuery, opts);
    setSearchStatus(found ? 'Match found' : 'No match');
  }, [searchQuery, searchCaseSensitive, searchRegex, terminal]);

  useEffect(() => {
    if (searchQuery) doSearch('next');
    else setSearchStatus('');
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [searchQuery, searchCaseSensitive, searchRegex]);

  // 搜索框键盘
  const handleSearchKeyDown = useCallback((e: React.KeyboardEvent) => {
    if (e.key === 'Enter') {
      e.preventDefault();
      doSearch(e.shiftKey ? 'prev' : 'next');
    } else if (e.key === 'Escape') {
      e.preventDefault();
      setSearchOpen(false);
      terminal.focus();
    }
  }, [doSearch, terminal]);

  // ── 渲染 ──

  return (
    <div className="fixed inset-4 z-[80] bg-gray-900 rounded-xl shadow-2xl flex flex-col overflow-hidden border border-gray-700/50">
      {/* 头部 */}
      <div className="bg-gray-800 px-5 py-3 flex items-center justify-between border-b border-gray-700 flex-shrink-0">
        <div className="flex items-center gap-3">
          <div className={`w-2.5 h-2.5 rounded-full flex-shrink-0 ${
            connected ? 'bg-green-400 animate-pulse' : connecting ? 'bg-yellow-400 animate-pulse' : 'bg-gray-500'
          }`} />
          <div className="text-sm">
            <span className="text-blue-400 font-medium">{serviceName}</span>
            <span className="text-gray-500 mx-2">@</span>
            <span className="text-green-400 font-medium">{ip}</span>
          </div>
          {error && (
            <span className="text-[10px] text-red-400 bg-red-900/30 px-2 py-0.5 rounded">{error}</span>
          )}
        </div>
        <div className="flex items-center gap-1">
          {!connected && !connecting && (
            <button onClick={() => connectWebSocket()}
              className="px-3 py-1.5 text-xs text-gray-300 hover:text-white hover:bg-gray-700 rounded transition flex items-center gap-1.5">
              <i className="fas fa-plug text-[10px]" /> Reconnect
            </button>
          )}
          <button onClick={() => terminal.clear()}
            className="px-3 py-1.5 text-xs text-gray-300 hover:text-white hover:bg-gray-700 rounded transition flex items-center gap-1.5">
            <i className="fas fa-eraser text-[10px]" /> Clear
          </button>
          <button onClick={() => { setSearchOpen(!searchOpen); setTimeout(() => searchInputRef.current?.focus(), 50); }}
            className="px-3 py-1.5 text-xs text-gray-300 hover:text-white hover:bg-gray-700 rounded transition flex items-center gap-1.5"
            title="Ctrl+F">
            <i className="fas fa-search text-[10px]" /> Search
          </button>
          <button onClick={handleClose}
            className="px-3 py-1.5 text-xs text-gray-300 hover:text-white hover:bg-red-600/80 rounded transition flex items-center gap-1.5">
            <i className="fas fa-times text-[10px]" /> Close
          </button>
        </div>
      </div>

      {/* VSCode 风格搜索框 */}
      {searchOpen && (
        <div className="bg-gray-800 border-b border-gray-700 px-5 py-2 flex items-center gap-2 flex-shrink-0">
          <i className="fas fa-search text-gray-500 text-xs" />
          <input ref={searchInputRef} type="text" value={searchQuery}
            onChange={e => setSearchQuery(e.target.value)}
            onKeyDown={handleSearchKeyDown}
            placeholder="Search..."
            className="flex-1 bg-gray-900 text-gray-200 text-xs px-3 py-1.5 rounded border border-gray-600 focus:border-blue-500 focus:outline-none font-mono"
            spellCheck={false} autoComplete="off" />
          <span className={`text-[10px] font-medium min-w-[60px] text-center ${
            searchStatus === 'No match' ? 'text-red-400' : searchStatus ? 'text-green-400' : 'text-gray-500'
          }`}>{searchStatus}</span>
          <button onClick={() => doSearch('prev')} className="text-gray-400 hover:text-white p-1 transition" title="Previous (Shift+Enter)">
            <i className="fas fa-chevron-up text-xs" />
          </button>
          <button onClick={() => doSearch('next')} className="text-gray-400 hover:text-white p-1 transition" title="Next (Enter)">
            <i className="fas fa-chevron-down text-xs" />
          </button>
          <label className="flex items-center gap-1 cursor-pointer select-none" title="Case Sensitive">
            <input type="checkbox" checked={searchCaseSensitive} onChange={e => setSearchCaseSensitive(e.target.checked)}
              className="w-3 h-3 rounded border-gray-600 text-blue-500 focus:ring-blue-500 bg-gray-800" />
            <span className="text-[10px] text-gray-400 font-bold">Aa</span>
          </label>
          <label className="flex items-center gap-1 cursor-pointer select-none" title="Regular Expression">
            <input type="checkbox" checked={searchRegex} onChange={e => setSearchRegex(e.target.checked)}
              className="w-3 h-3 rounded border-gray-600 text-blue-500 focus:ring-blue-500 bg-gray-800" />
            <span className="text-[10px] text-gray-400 font-bold">.*</span>
          </label>
          <button onClick={() => { setSearchOpen(false); terminal.focus(); }}
            className="text-gray-400 hover:text-white p-1 transition" title="Close (Esc)">
            <i className="fas fa-times text-xs" />
          </button>
        </div>
      )}

      {/* xterm.js 容器 */}
      <div ref={containerRef} className="flex-1 px-2 pt-2 pb-2" style={{ overflow: 'hidden' }} />
    </div>
  );
}
