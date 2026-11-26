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
import {
  FIRST_FRAME_TIMEOUT_MS,
  RECONNECT_DELAY_MS,
  RELAY_BOUND_MESSAGE,
  decideWatchdogAction,
  isRelayReadyFrame,
  isServerStatusFrame,
} from './terminalReadiness';
import { apiClient } from '@/api/client';
import type { Instance, ApiError } from '@/types/api';

export interface TerminalPanelProps {
  /** 实例信息 */
  instance: Instance;
  /** 关闭面板回调 */
  onClose: () => void;
  /** 连接状态变化回调（用于刷新 Arthas 状态） */
  onStatusChange?: () => void;
}

// isArthasAPIJson returns true when data is a JSON payload from Arthas HTTP API,
// i.e. an initialization message that should NOT be rendered as terminal output.
// Such messages arrive when the agent flushes its internal pending-response queue
// over a newly opened openTunnel WebSocket before entering interactive TUI mode.
function isArthasAPIJson(data: string): boolean {
  if (data.length === 0 || data[0] !== '{') {
    return false;
  }
  try {
    JSON.parse(data);
    return true;
  } catch {
    return false;
  }
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
  const closeRefreshTimerRef = useRef<ReturnType<typeof setTimeout> | null>(null);
  const suppressCloseRefreshRef = useRef(false);
  const disposedRef = useRef(false);
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
  // 首帧看门狗：[+] 只表示通道绑定成功，不代表 Arthas 交互会话可输出。
  const firstFrameTimerRef = useRef<ReturnType<typeof setTimeout> | null>(null);
  const openTimeoutRef = useRef<ReturnType<typeof setTimeout> | null>(null);
  const firstFrameReceivedRef = useRef(false);
  const retryCountRef = useRef(0);
  // 连接/重连的同步状态守卫，避免 React state 异步导致的重连失败。
  const connectingRef = useRef(false);
  const connectedRef = useRef(false);
  // 自动重连期间只关闭 ws，不卸载面板、不展示误导性的关闭提示。
  const autoReconnectingRef = useRef(false);
  // 保存最新 connectWebSocket 引用，避免看门狗回调产生循环依赖。
  const connectWebSocketRef = useRef<() => void>(() => {});

  const serviceName = instance.service_name || 'unknown';
  const ip = instance.ip || instance.hostname || '';
  const tunnelAgentId = instance.arthas_tunnel_agent_id || '';

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
    convertEol: true,
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
    onReady: () => {
      // 初始尺寸在收到 [+] 后随首帧一起发送，避免过早触发 resize。
      connectWebSocket();
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

  // 组件卸载时彻底清理关闭回调和 WebSocket，避免旧面板异步回调污染当前页面
  useEffect(() => () => {
    disposedRef.current = true;
    if (closeRefreshTimerRef.current) {
      clearTimeout(closeRefreshTimerRef.current);
      closeRefreshTimerRef.current = null;
    }
    if (firstFrameTimerRef.current) {
      clearTimeout(firstFrameTimerRef.current);
      firstFrameTimerRef.current = null;
    }
    if (openTimeoutRef.current) {
      clearTimeout(openTimeoutRef.current);
      openTimeoutRef.current = null;
    }
    const ws = wsRef.current;
    if (ws) {
      ws.onopen = null;
      ws.onmessage = null;
      ws.onerror = null;
      ws.onclose = null;
      try { ws.close(); } catch { /* ignore */ }
      wsRef.current = null;
    }
  }, []);

  // ── WebSocket 连接 ──

  const clearFirstFrameTimer = useCallback(() => {
    if (firstFrameTimerRef.current) {
      clearTimeout(firstFrameTimerRef.current);
      firstFrameTimerRef.current = null;
    }
  }, []);

  const clearOpenTimeout = useCallback(() => {
    if (openTimeoutRef.current) {
      clearTimeout(openTimeoutRef.current);
      openTimeoutRef.current = null;
    }
  }, []);

  // 首帧看门狗超时：[+] 只代表通道绑定成功，不代表 Arthas 交互会话可输出。
  const handleFirstFrameTimeout = useCallback(() => {
    firstFrameTimerRef.current = null;
    if (disposedRef.current || firstFrameReceivedRef.current) return;

    if (decideWatchdogAction(retryCountRef.current) === 'give-up') {
      terminal.write('\r\n\x1b[31m[System] Arthas interactive session did not become ready in time. Close this terminal and retry, or restart the target application.\x1b[0m\r\n');
      setError('Arthas interactive session did not become ready');
      return;
    }

    retryCountRef.current += 1;
    terminal.write('\r\n\x1b[33m[System] Arthas did not respond, reconnecting...\x1b[0m\r\n');
    autoReconnectingRef.current = true;
    clearOpenTimeout();
    const old = wsRef.current;
    if (old) {
      old.onopen = null;
      old.onmessage = null;
      old.onerror = null;
      old.onclose = null;
      try { old.close(); } catch { /* ignore */ }
      wsRef.current = null;
    }
    connectedRef.current = false;
    connectingRef.current = false;
    relayReadyRef.current = false;
    setConnected(false);
    setTimeout(() => {
      if (disposedRef.current) return;
      autoReconnectingRef.current = false;
      connectWebSocketRef.current();
    }, RECONNECT_DELAY_MS);
  }, [terminal, clearOpenTimeout]);

  // 处理一帧下行文本。状态帧继续在终端里显示；真正的 Arthas 输出才算首帧。
  const handleIncomingFrame = useCallback((text: string) => {
    if (disposedRef.current) return;
    if (!relayReadyRef.current && isArthasAPIJson(text)) return;

    const statusFrame = isServerStatusFrame(text);
    const readyFrame = statusFrame && isRelayReadyFrame(text);

    // 修正"假就绪"：进度状态仍显示，但不再宣称 terminal is ready。
    terminal.write(readyFrame ? RELAY_BOUND_MESSAGE : text);

    if (!statusFrame && !firstFrameReceivedRef.current) {
      firstFrameReceivedRef.current = true;
      clearFirstFrameTimer();
    }

    if (!relayReadyRef.current && readyFrame) {
      relayReadyRef.current = true;
      setTimeout(() => {
        const dims = terminal.getProposedDimensions();
        if (dims) sendWS({ action: 'resize', cols: dims.cols, rows: dims.rows });
      }, 150);

      clearFirstFrameTimer();
      firstFrameTimerRef.current = setTimeout(handleFirstFrameTimeout, FIRST_FRAME_TIMEOUT_MS);
    }
  }, [terminal, sendWS, clearFirstFrameTimer, handleFirstFrameTimeout]);

  const connectWebSocket = useCallback(async (options?: { isRetry?: boolean }) => {
    if (connectingRef.current || connectedRef.current) return;
    connectingRef.current = true;
    setConnecting(true);
    setError('');

    const isRetry = options?.isRetry === true;
    if (!isRetry) {
      terminal.write('\x1b[32mArthas Terminal\x1b[0m\r\n');
      terminal.write(`\x1b[90mService: ${serviceName}\x1b[0m\r\n`);
      terminal.write(`\x1b[90mInstance: ${ip}\x1b[0m\r\n`);
    }

    try {
      // 重试时跳过 attach：Arthas 已在运行，重复 attach 只会多一次任务与等待。
      if (!isRetry && !instance.arthas_tunnel_agent_id) {
        terminal.write('\x1b[33m[System] Tunnel not ready, trying to attach Arthas...\x1b[0m\r\n');
        try {
          await apiClient.attachArthas(instance.agent_id);
          terminal.write('\x1b[32m[System] Attach request sent, waiting...\x1b[0m\r\n');
          await new Promise(r => setTimeout(r, 2000));
        } catch (attachErr) {
          terminal.write(`\x1b[31m[System] Attach failed: ${(attachErr as ApiError).message}\x1b[0m\r\n`);
          setError('Attach failed');
          setConnecting(false);
          connectingRef.current = false;
          return;
        }
      }

      // 重试不依赖旧的 tunnelAgentId prop，直接用实例 ID。
      const agentId = isRetry ? instance.agent_id : (tunnelAgentId || instance.agent_id);
      if (!agentId) {
        terminal.write('\x1b[31m[System] No agent ID available\x1b[0m\r\n');
        setError('No agent ID');
        setConnecting(false);
        connectingRef.current = false;
        return;
      }

      terminal.write('\x1b[90m[System] Authenticating...\x1b[0m\r\n');
      const tokenRes = await apiClient.generateWSToken();
      if (!tokenRes.token) throw new Error('Failed to obtain WebSocket token');

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
      relayReadyRef.current = false;
      firstFrameReceivedRef.current = false;
      clearFirstFrameTimer();

      ws.onopen = () => {
        connectingRef.current = false;
        connectedRef.current = true;
        setConnected(true);
        setConnecting(false);
        terminal.focus();
        onStatusChange?.();
      };

      ws.onmessage = (event) => {
        if (event.data instanceof ArrayBuffer) {
          handleIncomingFrame(new TextDecoder('utf-8').decode(new Uint8Array(event.data)));
        } else if (typeof event.data === 'string') {
          handleIncomingFrame(event.data);
        } else if (event.data instanceof Blob) {
          void event.data.arrayBuffer().then(buf => {
            handleIncomingFrame(new TextDecoder('utf-8').decode(new Uint8Array(buf)));
          });
        }
      };

      ws.onerror = () => {
        setError('WebSocket connection error');
        terminal.write('\r\n\x1b[31m[System] WebSocket error\x1b[0m\r\n');
      };

      ws.onclose = (event) => {
        connectedRef.current = false;
        connectingRef.current = false;
        setConnected(false);
        if (wsRef.current === ws) wsRef.current = null;
        clearFirstFrameTimer();
        clearOpenTimeout();

        // 自动重连时旧连接由看门狗主动关闭，不展示误导性的 Connection closed。
        if (autoReconnectingRef.current || suppressCloseRefreshRef.current || disposedRef.current) return;
        const reason = event.reason ? `, reason: ${event.reason}` : '';
        terminal.write(`\r\n\x1b[33m[System] Connection closed (code: ${event.code}${reason})\x1b[0m\r\n`);

        if (closeRefreshTimerRef.current) clearTimeout(closeRefreshTimerRef.current);
        closeRefreshTimerRef.current = setTimeout(() => {
          if (suppressCloseRefreshRef.current || disposedRef.current) return;
          onStatusChange?.();
          closeRefreshTimerRef.current = null;
        }, 500);
      };

      clearOpenTimeout();
      openTimeoutRef.current = setTimeout(() => {
        if (ws.readyState !== WebSocket.OPEN) {
          try { ws.close(); } catch { /* ignore */ }
          setError('Connection timeout');
          setConnecting(false);
          connectingRef.current = false;
          terminal.write('\r\n\x1b[31m[System] Connection timeout (15s)\x1b[0m\r\n');
        }
      }, 15000);

    } catch (e) {
      setError((e as Error).message || 'Connection failed');
      setConnecting(false);
      connectingRef.current = false;
      terminal.write(`\r\n\x1b[31m[System] Error: ${(e as Error).message}\x1b[0m\r\n`);
    }
  }, [instance, tunnelAgentId, serviceName, ip, terminal, sendWS, onStatusChange,
      handleIncomingFrame, clearFirstFrameTimer, clearOpenTimeout]);

  connectWebSocketRef.current = () => { void connectWebSocket({ isRetry: true }); };

  // ── 关闭 ──

  const handleClose = useCallback(() => {
    suppressCloseRefreshRef.current = true;
    if (closeRefreshTimerRef.current) {
      clearTimeout(closeRefreshTimerRef.current);
      closeRefreshTimerRef.current = null;
    }
    if (firstFrameTimerRef.current) {
      clearTimeout(firstFrameTimerRef.current);
      firstFrameTimerRef.current = null;
    }
    if (openTimeoutRef.current) {
      clearTimeout(openTimeoutRef.current);
      openTimeoutRef.current = null;
    }
    connectedRef.current = false;
    connectingRef.current = false;

    const ws = wsRef.current;
    if (ws) {
      ws.onopen = null;
      ws.onmessage = null;
      ws.onerror = null;
      ws.onclose = null;
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
