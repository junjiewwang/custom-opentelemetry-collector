/**
 * Terminal Manager with xterm.js
 * Version: 20260105-v4 (last line visibility fix)
 */
console.log('[TerminalManager] Loading version 20260105-v4 (last line visibility fix)');

class TerminalManager {
    constructor() {
        this.terminals = new Map(); // key: sessionId, value: terminal object
        this.activeTerminal = null;
        this.commandHistory = new Map(); // key: sessionId, value: command array
        this.websockets = new Map(); // key: sessionId, value: WebSocket
        
        // Add global window resize handler
        let globalResizeTimeout;
        window.addEventListener('resize', () => {
            clearTimeout(globalResizeTimeout);
            globalResizeTimeout = setTimeout(() => {
                // Refit all visible terminals
                for (const [sessionId, terminal] of this.terminals.entries()) {
                    if (terminal.element && terminal.element.style.display !== 'none') {
                        this.fitAndNotify(sessionId);
                    }
                }
            }, 100);
        });
    }

    /**
     * Unified method to fit terminal and notify server about resize
     * Following official pattern: notify server first, then fit
     * @param {string} sessionId - Terminal session ID
     */
    fitAndNotify(sessionId) {
        const terminal = this.terminals.get(sessionId);
        if (!terminal) return;

        try {
            const dimensions = terminal.fitAddon.proposeDimensions();
            if (!dimensions) return;

            const { cols } = dimensions;
            // Reduce rows by 1 to ensure last line is fully visible
            // This compensates for padding and potential rounding errors
            const rows = Math.max(1, dimensions.rows - 1);

            // 1. Send resize message to server via WebSocket (if connected)
            const ws = this.websockets.get(sessionId);
            if (ws && ws.readyState === WebSocket.OPEN) {
                const resizeMessage = JSON.stringify({ action: 'resize', cols, rows });
                ws.send(resizeMessage);
                console.log(`[Terminal] Sent resize to server: ${cols}x${rows}`);
            }

            // 2. Resize terminal with adjusted dimensions
            terminal.xterm.resize(cols, rows);

            // 3. Scroll to bottom
            terminal.xterm.scrollToBottom();

            // 4. Also dispatch event for any external listeners
            const event = new CustomEvent('terminalResize', {
                detail: { sessionId, serviceName: terminal.serviceName, ip: terminal.ip, cols, rows }
            });
            document.dispatchEvent(event);
        } catch (error) {
            console.error(`[Terminal] Error fitting terminal ${sessionId}:`, error);
        }
    }

    /**
     * Create terminal window with xterm.js
     */
    createTerminal(sessionId, serviceName, ip) {
        // Check if terminal already exists for this session
        if (this.terminals.has(sessionId)) {
            this.showTerminalBySessionId(sessionId);
            return this.terminals.get(sessionId);
        }

        // Create terminal container element
        const terminalElement = document.createElement('div');
        terminalElement.className = 'terminal-window fixed inset-4 bg-gray-900 rounded-lg shadow-2xl flex flex-col z-50';
        terminalElement.dataset.sessionId = sessionId;
        terminalElement.dataset.service = serviceName;
        terminalElement.dataset.ip = ip;
        
        terminalElement.innerHTML = `
            <div class="terminal-header bg-gray-800 px-4 py-3 rounded-t-lg flex items-center justify-between border-b border-gray-700">
                <div class="flex items-center space-x-3">
                    <div class="text-white font-medium">
                        <span class="text-blue-400">${serviceName}</span>
                        <span class="text-gray-400 mx-2">@</span>
                        <span class="text-green-400">${ip}</span>
                    </div>
                </div>
                <div class="flex items-center space-x-2">
                    <button class="minimize-terminal-btn px-3 py-1 text-sm text-gray-300 hover:text-white hover:bg-gray-700 rounded transition-colors flex items-center space-x-1">
                        <svg class="w-4 h-4" fill="none" stroke="currentColor" viewBox="0 0 24 24">
                            <path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M15 19l-7-7 7-7"></path>
                        </svg>
                        <span>返回主页</span>
                    </button>
                    <button class="clear-terminal-btn px-3 py-1 text-sm text-gray-300 hover:text-white hover:bg-gray-700 rounded transition-colors">
                        清屏
                    </button>
                    <button class="search-terminal-btn px-3 py-1 text-sm text-gray-300 hover:text-white hover:bg-gray-700 rounded transition-colors">
                        搜索
                    </button>
                    <button class="close-terminal-btn px-3 py-1 text-sm text-gray-300 hover:text-white hover:bg-gray-700 rounded transition-colors">
                        关闭
                    </button>
                </div>
            </div>
            
            <div class="xterm-container flex-1 px-4 pt-4 pb-4" style="overflow: hidden;"></div>
        `;

        // Initialize xterm.js
        const xterm = new Terminal({
            cursorBlink: true,
            cursorStyle: 'block',
            fontSize: 14,
            fontFamily: 'Consolas, Monaco, "Courier New", monospace',
            theme: {
                background: '#1e1e1e',
                foreground: '#d4d4d4',
                cursor: '#00ff00',
                cursorAccent: '#1e1e1e',
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
                brightWhite: '#ffffff'
            },
            allowTransparency: false,
            scrollback: 10000,
            tabStopWidth: 4
        });

        // Load addons
        const fitAddon = new FitAddon.FitAddon();
        const webLinksAddon = new WebLinksAddon.WebLinksAddon();
        const searchAddon = new SearchAddon.SearchAddon();

        xterm.loadAddon(fitAddon);
        xterm.loadAddon(webLinksAddon);
        xterm.loadAddon(searchAddon);

        // Open terminal in container
        const xtermContainer = terminalElement.querySelector('.xterm-container');
        xterm.open(xtermContainer);

        // Create terminal object first (needed for fitAndNotify)
        const terminal = {
            element: terminalElement,
            sessionId,
            serviceName,
            ip,
            xterm,
            fitAddon,
            searchAddon,
            resizeObserver: null, // will be set below
            currentLine: '',
            historyIndex: -1
        };

        // Store terminal with sessionId as key (needed for fitAndNotify)
        this.terminals.set(sessionId, terminal);

        // Initial fit - use requestAnimationFrame for better timing
        requestAnimationFrame(() => {
            // First fit with adjusted rows (WebSocket not ready yet, so no server notification)
            const dimensions = fitAddon.proposeDimensions();
            if (dimensions) {
                const cols = dimensions.cols;
                const rows = Math.max(1, dimensions.rows - 1);
                xterm.resize(cols, rows);
                xterm.scrollToBottom();
                console.log(`[Terminal] Initial fit: ${cols}x${rows}`);
            }
        });

        // Handle container resize with ResizeObserver
        let resizeTimeout;
        const resizeObserver = new ResizeObserver(() => {
            clearTimeout(resizeTimeout);
            resizeTimeout = setTimeout(() => {
                this.fitAndNotify(sessionId);
            }, 100);
        });
        resizeObserver.observe(xtermContainer);
        terminal.resizeObserver = resizeObserver;

        // Write welcome message
        xterm.writeln('\x1b[32mArthas Terminal Connected\x1b[0m');
        xterm.writeln(`\x1b[90mService: ${serviceName}\x1b[0m`);
        xterm.writeln(`\x1b[90mInstance: ${ip}\x1b[0m`);
        xterm.writeln(`\x1b[90mType 'help' for available commands\x1b[0m`);
        xterm.writeln('');

        // Bind event handlers
        this.bindTerminalEvents(terminal);
        
        // Add to container
        const container = document.getElementById('terminalContainer');
        container.appendChild(terminalElement);
        container.classList.remove('hidden');

        // Set as active terminal
        this.activeTerminal = sessionId;

        // Focus terminal
        xterm.focus();

        // Initialize command history
        if (!this.commandHistory.has(sessionId)) {
            this.commandHistory.set(sessionId, []);
        }

        return terminal;
    }

    /**
     * Bind terminal event handlers
     */
    bindTerminalEvents(terminal) {
        const sessionId = terminal.sessionId;

        // Minimize button (return to main page)
        const minimizeButton = terminal.element.querySelector('.minimize-terminal-btn');
        minimizeButton.addEventListener('click', () => {
            this.minimizeTerminalBySessionId(sessionId);
        });

        // Close buttons - with confirmation
        const closeButtons = terminal.element.querySelectorAll('.close-terminal-btn');
        closeButtons.forEach(btn => {
            btn.addEventListener('click', () => {
                // Show confirmation dialog
                if (confirm(`确定要关闭与 ${terminal.serviceName} (${terminal.ip}) 的连接吗？\n\n这将断开Arthas连接并关闭终端窗口。`)) {
                    this.closeTerminalBySessionId(sessionId);
                }
            });
        });

        // Clear button
        const clearButton = terminal.element.querySelector('.clear-terminal-btn');
        clearButton.addEventListener('click', () => {
            this.clearTerminalBySessionId(sessionId);
        });

        // Search button
        const searchButton = terminal.element.querySelector('.search-terminal-btn');
        searchButton.addEventListener('click', () => {
            const searchTerm = prompt('搜索内容:');
            if (searchTerm) {
                terminal.searchAddon.findNext(searchTerm);
            }
        });

        // Handle terminal input
        // Server-side echo mode: all input is sent to server, server handles all display
        terminal.xterm.onData(data => {
            const code = data.charCodeAt(0);

            // Handle Ctrl+L (clear screen) - local only
            if (code === 12) {
                this.clearTerminalBySessionId(sessionId);
                return;
            }
            
            // Handle Arrow Up (history) - local only
            if (data === '\x1b[A') {
                const history = this.commandHistory.get(sessionId) || [];
                if (history.length > 0 && terminal.historyIndex < history.length - 1) {
                    terminal.historyIndex++;
                    const historyCommand = history[history.length - 1 - terminal.historyIndex];
                    
                    // Send backspaces to clear current line, then send history command
                    const clearCmd = '\x7f'.repeat(terminal.currentLine.length);
                    terminal.currentLine = historyCommand;
                    
                    // Send clear + history command to server via JSON
                    const jsonMessage = JSON.stringify({
                        action: "read",
                        data: clearCmd + historyCommand
                    });
                    const event = new CustomEvent('terminalRawData', {
                        detail: { sessionId: sessionId, serviceName: terminal.serviceName, ip: terminal.ip, data: jsonMessage }
                    });
                    document.dispatchEvent(event);
                }
                return;
            }
            
            // Handle Arrow Down (history) - local only
            if (data === '\x1b[B') {
                const history = this.commandHistory.get(sessionId) || [];
                if (terminal.historyIndex > 0) {
                    terminal.historyIndex--;
                    const historyCommand = history[history.length - 1 - terminal.historyIndex];
                    
                    // Send backspaces to clear current line, then send history command
                    const clearCmd = '\x7f'.repeat(terminal.currentLine.length);
                    terminal.currentLine = historyCommand;
                    
                    const jsonMessage = JSON.stringify({
                        action: "read",
                        data: clearCmd + historyCommand
                    });
                    const event = new CustomEvent('terminalRawData', {
                        detail: { sessionId: sessionId, serviceName: terminal.serviceName, ip: terminal.ip, data: jsonMessage }
                    });
                    document.dispatchEvent(event);
                } else if (terminal.historyIndex === 0) {
                    terminal.historyIndex = -1;
                    
                    // Clear current line
                    const clearCmd = '\x7f'.repeat(terminal.currentLine.length);
                    terminal.currentLine = '';
                    
                    const jsonMessage = JSON.stringify({
                        action: "read",
                        data: clearCmd
                    });
                    const event = new CustomEvent('terminalRawData', {
                        detail: { sessionId: sessionId, serviceName: terminal.serviceName, ip: terminal.ip, data: jsonMessage }
                    });
                    document.dispatchEvent(event);
                }
                return;
            }
            
            // Track current line for history
            if (code === 13) {
                // Enter key - add to history
                const command = terminal.currentLine.trim();
                if (command) {
                    const history = this.commandHistory.get(sessionId) || [];
                    history.push(command);
                    this.commandHistory.set(sessionId, history);
                }
                terminal.historyIndex = -1;
                terminal.currentLine = '';
            } else if (code === 127) {
                // Backspace
                if (terminal.currentLine.length > 0) {
                    terminal.currentLine = terminal.currentLine.slice(0, -1);
                }
            } else if (code === 3) {
                // Ctrl+C
                terminal.currentLine = '';
            } else if (code >= 32 && code < 127) {
                // Regular character
                terminal.currentLine += data;
            }
            
            // Send all input to server via JSON format (server handles echo)
            const jsonMessage = JSON.stringify({
                action: "read",
                data: data
            });
            console.log('[Terminal] Sending JSON message:', jsonMessage);
            const event = new CustomEvent('terminalRawData', {
                detail: { sessionId: sessionId, serviceName: terminal.serviceName, ip: terminal.ip, data: jsonMessage }
            });
            document.dispatchEvent(event);
        });

        // Handle terminal key events
        terminal.xterm.onKey(({ key, domEvent }) => {
            // Prevent default for some keys
            if (domEvent.ctrlKey || domEvent.altKey) {
                domEvent.preventDefault();
            }
        });
    }

    /**
     * Write data to terminal by sessionId (for WebSocket messages)
     */
    writeDataBySessionId(sessionId, data) {
        const terminal = this.terminals.get(sessionId);
        
        if (!terminal) return;

        // Write raw data to xterm (preserves ANSI codes)
        terminal.xterm.write(data, () => {
            // Scroll to bottom after writing data
            try {
                terminal.xterm.scrollToBottom();
            } catch (error) {
                console.error('Error scrolling to bottom:', error);
            }
        });
    }

    /**
     * Write data to terminal (legacy method - finds terminal by serviceName and ip)
     */
    writeData(serviceName, ip, data) {
        // Find terminal by serviceName and ip
        for (const terminal of this.terminals.values()) {
            if (terminal.serviceName === serviceName && terminal.ip === ip) {
                terminal.xterm.write(data, () => {
                    // Scroll to bottom after writing data
                    try {
                        terminal.xterm.scrollToBottom();
                    } catch (error) {
                        console.error('Error scrolling to bottom:', error);
                    }
                });
                return;
            }
        }
    }

    /**
     * Clear terminal by sessionId
     */
    clearTerminalBySessionId(sessionId) {
        const terminal = this.terminals.get(sessionId);
        
        if (!terminal) return;

        terminal.xterm.clear();
        terminal.xterm.writeln('\x1b[32mTerminal Cleared\x1b[0m');
        terminal.xterm.write('\x1b[32m$\x1b[0m ');
        terminal.currentLine = '';
    }

    /**
     * Clear terminal (legacy method)
     */
    clearTerminal(serviceName, ip) {
        for (const [sessionId, terminal] of this.terminals.entries()) {
            if (terminal.serviceName === serviceName && terminal.ip === ip) {
                this.clearTerminalBySessionId(sessionId);
                return;
            }
        }
    }

    /**
     * Close terminal by sessionId
     */
    closeTerminalBySessionId(sessionId) {
        const terminal = this.terminals.get(sessionId);
        
        if (!terminal) return;

        // Remove WebSocket binding
        this.websockets.delete(sessionId);

        // Dispose xterm instance
        terminal.xterm.dispose();
        
        // Disconnect resize observer
        terminal.resizeObserver.disconnect();

        // Remove element
        terminal.element.remove();
        
        // Remove from map
        this.terminals.delete(sessionId);

        // Hide container if no terminals
        if (this.terminals.size === 0) {
            const container = document.getElementById('terminalContainer');
            container.classList.add('hidden');
        }

        // Trigger close event
        const event = new CustomEvent('terminalClosed', {
            detail: { sessionId: sessionId, serviceName: terminal.serviceName, ip: terminal.ip }
        });
        document.dispatchEvent(event);
    }

    /**
     * Close terminal (legacy method)
     */
    closeTerminal(serviceName, ip) {
        // Find and close all terminals for this serviceName and ip
        const terminalsToClose = [];
        for (const [sessionId, terminal] of this.terminals.entries()) {
            if (terminal.serviceName === serviceName && terminal.ip === ip) {
                terminalsToClose.push(sessionId);
            }
        }
        
        terminalsToClose.forEach(sessionId => {
            this.closeTerminalBySessionId(sessionId);
        });
    }

    /**
     * Show terminal by sessionId
     */
    showTerminalBySessionId(sessionId) {
        const terminal = this.terminals.get(sessionId);
        
        if (!terminal) return;

        terminal.element.style.display = 'flex';
        this.activeTerminal = sessionId;
        terminal.xterm.focus();
        
        // Refit terminal and notify server
        requestAnimationFrame(() => {
            this.fitAndNotify(sessionId);
        });
    }

    /**
     * Show terminal (legacy method)
     */
    showTerminal(serviceName, ip) {
        // Find first matching terminal
        for (const [sessionId, terminal] of this.terminals.entries()) {
            if (terminal.serviceName === serviceName && terminal.ip === ip) {
                this.showTerminalBySessionId(sessionId);
                return;
            }
        }
    }

    /**
     * Hide terminal by sessionId
     */
    hideTerminalBySessionId(sessionId) {
        const terminal = this.terminals.get(sessionId);
        
        if (!terminal) return;

        terminal.element.style.display = 'none';
    }

    /**
     * Hide terminal (legacy method)
     */
    hideTerminal(serviceName, ip) {
        for (const [sessionId, terminal] of this.terminals.entries()) {
            if (terminal.serviceName === serviceName && terminal.ip === ip) {
                this.hideTerminalBySessionId(sessionId);
                return;
            }
        }
    }

    /**
     * Check if terminal exists by sessionId
     */
    hasTerminalBySessionId(sessionId) {
        return this.terminals.has(sessionId);
    }

    /**
     * Check if terminal exists (legacy method)
     */
    hasTerminal(serviceName, ip) {
        for (const terminal of this.terminals.values()) {
            if (terminal.serviceName === serviceName && terminal.ip === ip) {
                return true;
            }
        }
        return false;
    }

    /**
     * Get terminal by sessionId
     */
    getTerminalBySessionId(sessionId) {
        return this.terminals.get(sessionId);
    }

    /**
     * Get terminal (legacy method)
     */
    getTerminal(serviceName, ip) {
        for (const terminal of this.terminals.values()) {
            if (terminal.serviceName === serviceName && terminal.ip === ip) {
                return terminal;
            }
        }
        return null;
    }

    /**
     * Close all terminals
     */
    closeAllTerminals() {
        const sessionIds = Array.from(this.terminals.keys());
        
        sessionIds.forEach(sessionId => {
            this.closeTerminalBySessionId(sessionId);
        });
    }

    /**
     * Resize terminal by sessionId
     */
    resizeTerminalBySessionId(sessionId) {
        this.fitAndNotify(sessionId);
    }

    /**
     * Resize terminal (legacy method)
     */
    resizeTerminal(serviceName, ip) {
        for (const [sessionId, terminal] of this.terminals.entries()) {
            if (terminal.serviceName === serviceName && terminal.ip === ip) {
                this.resizeTerminalBySessionId(sessionId);
                return;
            }
        }
    }

    /**
     * Focus terminal by sessionId
     */
    focusTerminalBySessionId(sessionId) {
        const terminal = this.terminals.get(sessionId);
        
        if (!terminal) return;

        terminal.xterm.focus();
    }

    /**
     * Focus terminal (legacy method)
     */
    focusTerminal(serviceName, ip) {
        for (const [sessionId, terminal] of this.terminals.entries()) {
            if (terminal.serviceName === serviceName && terminal.ip === ip) {
                this.focusTerminalBySessionId(sessionId);
                return;
            }
        }
    }

    /**
     * Minimize terminal by sessionId (hide but keep connection)
     */
    minimizeTerminalBySessionId(sessionId) {
        const terminal = this.terminals.get(sessionId);
        
        if (!terminal) return;

        // Hide terminal window
        terminal.element.style.display = 'none';

        // Dispatch event to update session state
        const event = new CustomEvent('terminalMinimized', {
            detail: { sessionId: sessionId, serviceName: terminal.serviceName, ip: terminal.ip }
        });
        document.dispatchEvent(event);
    }

    /**
     * Minimize terminal (legacy method)
     */
    minimizeTerminal(serviceName, ip) {
        for (const [sessionId, terminal] of this.terminals.entries()) {
            if (terminal.serviceName === serviceName && terminal.ip === ip) {
                this.minimizeTerminalBySessionId(sessionId);
                return;
            }
        }
    }

    /**
     * Notify server about terminal resize by sessionId
     */
    notifyTerminalResizeBySessionId(sessionId, fitAddon) {
        const terminal = this.terminals.get(sessionId);
        if (!terminal) return;

        const dimensions = fitAddon.proposeDimensions();
        if (!dimensions) return;

        const { cols, rows } = dimensions;
        
        // Dispatch custom event for WebSocket to handle
        const event = new CustomEvent('terminalResize', {
            detail: { 
                sessionId: sessionId,
                serviceName: terminal.serviceName, 
                ip: terminal.ip, 
                cols, 
                rows 
            }
        });
        document.dispatchEvent(event);
    }

    /**
     * Notify server about terminal resize (legacy method)
     */
    notifyTerminalResize(serviceName, ip, fitAddon) {
        for (const [sessionId, terminal] of this.terminals.entries()) {
            if (terminal.serviceName === serviceName && terminal.ip === ip) {
                this.notifyTerminalResizeBySessionId(sessionId, fitAddon);
                return;
            }
        }
    }

    /**
     * Set WebSocket for terminal by sessionId
     * Also sends initial terminal size to server
     * @param {string} sessionId - Terminal session ID
     * @param {WebSocket} ws - WebSocket instance
     */
    setWebSocket(sessionId, ws) {
        this.websockets.set(sessionId, ws);
        
        // Send initial terminal size once WebSocket is ready
        // Use a small delay to ensure WebSocket is fully open
        if (ws.readyState === WebSocket.OPEN) {
            this.fitAndNotify(sessionId);
        } else {
            ws.addEventListener('open', () => {
                this.fitAndNotify(sessionId);
            }, { once: true });
        }
    }

    /**
     * Remove WebSocket binding for terminal by sessionId
     * @param {string} sessionId - Terminal session ID
     */
    removeWebSocket(sessionId) {
        this.websockets.delete(sessionId);
    }

    /**
     * Get WebSocket for terminal by sessionId
     * @param {string} sessionId - Terminal session ID
     * @returns {WebSocket|undefined}
     */
    getWebSocket(sessionId) {
        return this.websockets.get(sessionId);
    }
}

// Create singleton instance
const terminalManager = new TerminalManager();

export default terminalManager;

export {
    TerminalManager,
    terminalManager
};