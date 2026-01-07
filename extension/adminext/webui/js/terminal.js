/**
 * Terminal Manager with xterm.js
 * Version: 20260106-v6 (VSCode-style search box)
 * 
 * Changes:
 * - VSCode-style search box with real-time search, next/prev navigation
 * - Keyboard shortcuts: Ctrl/Cmd+F to open, Esc to close, Enter/Shift+Enter to navigate
 * - Case-sensitive and regex options
 * - Match count display (progressive counting)
 */
console.log('[TerminalManager] Loading version 20260106-v6 (VSCode-style search box)');

class TerminalManager {
    constructor() {
        this.terminals = new Map(); // key: sessionId, value: terminal object
        this.activeTerminal = null;
        this.commandHistory = new Map(); // key: sessionId, value: command array
        this.websockets = new Map(); // key: sessionId, value: WebSocket
        this.searchStates = new Map(); // key: sessionId, value: search state object
        
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

            // Reduce cols by 1 to prevent right-side scrollbar from occluding text
            // This ensures the rightmost character is never hidden behind the scrollbar
            const cols = Math.max(1, dimensions.cols - 1);
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
                    <button class="clear-terminal-btn px-3 py-1 text-sm text-gray-300 hover:text-white hover:bg-gray-700 rounded transition-colors">
                        清屏
                    </button>
                    <button class="search-terminal-btn px-3 py-1 text-sm text-gray-300 hover:text-white hover:bg-gray-700 rounded transition-colors flex items-center space-x-1" title="Ctrl+F">
                        <svg class="w-4 h-4" fill="none" stroke="currentColor" viewBox="0 0 24 24">
                            <path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M21 21l-6-6m2-5a7 7 0 11-14 0 7 7 0 0114 0z"></path>
                        </svg>
                        <span>搜索</span>
                    </button>
                    <button class="close-terminal-btn px-3 py-1 text-sm text-gray-300 hover:text-white hover:bg-gray-700 rounded transition-colors">
                        关闭
                    </button>
                </div>
            </div>
            
            <!-- VSCode-style Search Box -->
            <div class="terminal-search-box" data-session-id="${sessionId}">
                <div class="search-input-row">
                    <svg class="search-icon" fill="none" stroke="currentColor" viewBox="0 0 24 24">
                        <path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M21 21l-6-6m2-5a7 7 0 11-14 0 7 7 0 0114 0z"></path>
                    </svg>
                    <input type="text" class="search-input" placeholder="搜索..." spellcheck="false" autocomplete="off">
                    <span class="match-count"></span>
                    <button class="search-nav-btn search-prev-btn" title="上一个 (Shift+Enter)">
                        <svg fill="none" stroke="currentColor" viewBox="0 0 24 24">
                            <path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M5 15l7-7 7 7"></path>
                        </svg>
                    </button>
                    <button class="search-nav-btn search-next-btn" title="下一个 (Enter)">
                        <svg fill="none" stroke="currentColor" viewBox="0 0 24 24">
                            <path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M19 9l-7 7-7-7"></path>
                        </svg>
                    </button>
                    <button class="search-close-btn" title="关闭 (Esc)">
                        <svg fill="none" stroke="currentColor" viewBox="0 0 24 24">
                            <path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M6 18L18 6M6 6l12 12"></path>
                        </svg>
                    </button>
                </div>
                <div class="search-options-row">
                    <label class="search-option" title="区分大小写 (Alt+C)">
                        <input type="checkbox" class="search-case-sensitive">
                        <span>Aa</span>
                    </label>
                    <label class="search-option" title="正则表达式 (Alt+R)">
                        <input type="checkbox" class="search-regex">
                        <span>.*</span>
                    </label>
                    <label class="search-option" title="全词匹配 (Alt+W)">
                        <input type="checkbox" class="search-whole-word">
                        <span>\\b</span>
                    </label>
                    <span class="search-error"></span>
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
            // First fit with adjusted cols and rows (WebSocket not ready yet, so no server notification)
            const dimensions = fitAddon.proposeDimensions();
            if (dimensions) {
                // cols - 1: prevent scrollbar occlusion
                // rows - 1: ensure last line visibility
                const cols = Math.max(1, dimensions.cols - 1);
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

        // Initialize search state
        this.searchStates.set(sessionId, {
            isOpen: false,
            query: '',
            currentIndex: 0,
            totalMatches: 0,
            options: {
                caseSensitive: false,
                regex: false,
                wholeWord: false
            },
            error: null
        });

        return terminal;
    }

    /**
     * Bind terminal event handlers
     */
    bindTerminalEvents(terminal) {
        const sessionId = terminal.sessionId;

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
            this.openSearchBox(sessionId);
        });

        // Bind search box events
        this.bindSearchBoxEvents(terminal);

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

    // ==================== Search Box Methods ====================

    /**
     * Bind search box event handlers
     */
    bindSearchBoxEvents(terminal) {
        const sessionId = terminal.sessionId;
        const searchBox = terminal.element.querySelector('.terminal-search-box');
        const searchInput = searchBox.querySelector('.search-input');
        const prevBtn = searchBox.querySelector('.search-prev-btn');
        const nextBtn = searchBox.querySelector('.search-next-btn');
        const closeBtn = searchBox.querySelector('.search-close-btn');
        const caseSensitiveCheckbox = searchBox.querySelector('.search-case-sensitive');
        const regexCheckbox = searchBox.querySelector('.search-regex');
        const wholeWordCheckbox = searchBox.querySelector('.search-whole-word');

        // Debounce timer for real-time search
        let searchDebounceTimer;

        // Input event - real-time search with debounce
        searchInput.addEventListener('input', () => {
            clearTimeout(searchDebounceTimer);
            searchDebounceTimer = setTimeout(() => {
                this.performSearch(sessionId, searchInput.value);
            }, 150);
        });

        // Keyboard events in search input
        searchInput.addEventListener('keydown', (e) => {
            if (e.key === 'Enter') {
                e.preventDefault();
                if (e.shiftKey) {
                    this.searchPrevious(sessionId);
                } else {
                    this.searchNext(sessionId);
                }
            } else if (e.key === 'Escape') {
                e.preventDefault();
                this.closeSearchBox(sessionId);
            } else if (e.key === 'c' && e.altKey) {
                e.preventDefault();
                caseSensitiveCheckbox.checked = !caseSensitiveCheckbox.checked;
                caseSensitiveCheckbox.dispatchEvent(new Event('change'));
            } else if (e.key === 'r' && e.altKey) {
                e.preventDefault();
                regexCheckbox.checked = !regexCheckbox.checked;
                regexCheckbox.dispatchEvent(new Event('change'));
            } else if (e.key === 'w' && e.altKey) {
                e.preventDefault();
                wholeWordCheckbox.checked = !wholeWordCheckbox.checked;
                wholeWordCheckbox.dispatchEvent(new Event('change'));
            }
        });

        // Navigation buttons
        prevBtn.addEventListener('click', () => this.searchPrevious(sessionId));
        nextBtn.addEventListener('click', () => this.searchNext(sessionId));
        closeBtn.addEventListener('click', () => this.closeSearchBox(sessionId));

        // Option checkboxes - re-search when changed
        const optionChangeHandler = () => {
            const state = this.searchStates.get(sessionId);
            if (state) {
                state.options.caseSensitive = caseSensitiveCheckbox.checked;
                state.options.regex = regexCheckbox.checked;
                state.options.wholeWord = wholeWordCheckbox.checked;
                // Re-search with new options
                if (state.query) {
                    this.performSearch(sessionId, state.query);
                }
            }
        };
        caseSensitiveCheckbox.addEventListener('change', optionChangeHandler);
        regexCheckbox.addEventListener('change', optionChangeHandler);
        wholeWordCheckbox.addEventListener('change', optionChangeHandler);

        // Global keyboard shortcut (Ctrl/Cmd + F)
        terminal.element.addEventListener('keydown', (e) => {
            if ((e.ctrlKey || e.metaKey) && e.key === 'f') {
                e.preventDefault();
                e.stopPropagation();
                this.openSearchBox(sessionId);
            }
        });

        // Also capture Ctrl+F on xterm
        terminal.xterm.attachCustomKeyEventHandler((e) => {
            if ((e.ctrlKey || e.metaKey) && e.key === 'f') {
                e.preventDefault();
                this.openSearchBox(sessionId);
                return false; // Prevent xterm from handling
            }
            return true;
        });
    }

    /**
     * Open search box
     */
    openSearchBox(sessionId) {
        const terminal = this.terminals.get(sessionId);
        const state = this.searchStates.get(sessionId);
        if (!terminal || !state) return;

        const searchBox = terminal.element.querySelector('.terminal-search-box');
        const searchInput = searchBox.querySelector('.search-input');

        // If already open, just focus input
        if (state.isOpen) {
            searchInput.focus();
            searchInput.select();
            return;
        }

        state.isOpen = true;
        
        // 1. First blur terminal to prevent it from stealing focus
        terminal.xterm.blur();
        
        // 2. Make search box visible
        searchBox.classList.add('visible');
        
        // 3. Focus after transition completes (more reliable)
        const focusInput = () => {
            searchInput.focus();
            searchInput.select();
            console.log('[Terminal] Search box opened, input focused');
        };

        // Listen for transition end as primary method
        const onTransitionEnd = (e) => {
            if (e.propertyName === 'visibility' || e.propertyName === 'transform') {
                searchBox.removeEventListener('transitionend', onTransitionEnd);
                focusInput();
            }
        };
        searchBox.addEventListener('transitionend', onTransitionEnd);

        // Fallback: force focus after a short delay in case transitionend doesn't fire
        setTimeout(() => {
            searchBox.removeEventListener('transitionend', onTransitionEnd);
            if (document.activeElement !== searchInput) {
                focusInput();
            }
        }, 250);
    }

    /**
     * Close search box
     */
    closeSearchBox(sessionId) {
        const terminal = this.terminals.get(sessionId);
        const state = this.searchStates.get(sessionId);
        if (!terminal || !state) return;

        const searchBox = terminal.element.querySelector('.terminal-search-box');

        state.isOpen = false;
        searchBox.classList.remove('visible');

        // Clear search highlights (optional - comment out to keep highlights)
        // terminal.searchAddon.clearDecorations();

        // Return focus to terminal
        terminal.xterm.focus();
    }

    /**
     * Perform search with current query and options
     */
    performSearch(sessionId, query) {
        const terminal = this.terminals.get(sessionId);
        const state = this.searchStates.get(sessionId);
        if (!terminal || !state) return;

        const searchBox = terminal.element.querySelector('.terminal-search-box');
        const matchCountEl = searchBox.querySelector('.match-count');
        const searchInput = searchBox.querySelector('.search-input');
        const errorEl = searchBox.querySelector('.search-error');

        state.query = query;
        state.error = null;
        errorEl.textContent = '';
        searchInput.classList.remove('search-input-error');

        if (!query) {
            matchCountEl.textContent = '';
            matchCountEl.className = 'match-count';
            state.currentIndex = 0;
            state.totalMatches = 0;
            return;
        }

        // Validate regex if regex mode is enabled
        if (state.options.regex) {
            try {
                new RegExp(query);
            } catch (e) {
                state.error = '无效的正则表达式';
                errorEl.textContent = state.error;
                searchInput.classList.add('search-input-error');
                matchCountEl.textContent = '';
                return;
            }
        }

        // Build search options
        const searchOptions = {
            caseSensitive: state.options.caseSensitive,
            regex: state.options.regex,
            wholeWord: state.options.wholeWord,
            incremental: false
        };

        try {
            // Perform search - findNext returns boolean indicating if match found
            const found = terminal.searchAddon.findNext(query, searchOptions);
            
            if (found) {
                // Count matches asynchronously
                this.countMatches(sessionId, query, searchOptions);
            } else {
                state.currentIndex = 0;
                state.totalMatches = 0;
                matchCountEl.textContent = '无匹配';
                matchCountEl.className = 'match-count no-match';
            }
        } catch (e) {
            console.error('[Terminal] Search error:', e);
            state.error = '搜索出错';
            errorEl.textContent = state.error;
        }
    }

    /**
     * Count total matches (progressive counting to avoid blocking UI)
     */
    countMatches(sessionId, query, options) {
        const terminal = this.terminals.get(sessionId);
        const state = this.searchStates.get(sessionId);
        if (!terminal || !state) return;

        const searchBox = terminal.element.querySelector('.terminal-search-box');
        const matchCountEl = searchBox.querySelector('.match-count');

        // For simplicity, just show "有匹配" without exact count
        // Full counting would require iterating through buffer which can be slow
        state.totalMatches = -1; // Unknown
        state.currentIndex = 1;
        matchCountEl.textContent = '有匹配';
        matchCountEl.className = 'match-count has-match';
    }

    /**
     * Search next match
     */
    searchNext(sessionId) {
        const terminal = this.terminals.get(sessionId);
        const state = this.searchStates.get(sessionId);
        if (!terminal || !state || !state.query) return;

        const searchBox = terminal.element.querySelector('.terminal-search-box');
        const matchCountEl = searchBox.querySelector('.match-count');

        const searchOptions = {
            caseSensitive: state.options.caseSensitive,
            regex: state.options.regex,
            wholeWord: state.options.wholeWord,
            incremental: false
        };

        try {
            const found = terminal.searchAddon.findNext(state.query, searchOptions);
            if (!found) {
                // Wrap around - search from beginning
                matchCountEl.textContent = '已到底部';
                matchCountEl.className = 'match-count wrap-around';
            }
        } catch (e) {
            console.error('[Terminal] Search next error:', e);
        }
    }

    /**
     * Search previous match
     */
    searchPrevious(sessionId) {
        const terminal = this.terminals.get(sessionId);
        const state = this.searchStates.get(sessionId);
        if (!terminal || !state || !state.query) return;

        const searchBox = terminal.element.querySelector('.terminal-search-box');
        const matchCountEl = searchBox.querySelector('.match-count');

        const searchOptions = {
            caseSensitive: state.options.caseSensitive,
            regex: state.options.regex,
            wholeWord: state.options.wholeWord,
            incremental: false
        };

        try {
            const found = terminal.searchAddon.findPrevious(state.query, searchOptions);
            if (!found) {
                // Wrap around - search from end
                matchCountEl.textContent = '已到顶部';
                matchCountEl.className = 'match-count wrap-around';
            }
        } catch (e) {
            console.error('[Terminal] Search previous error:', e);
        }
    }
}

// Create singleton instance
const terminalManager = new TerminalManager();

export default terminalManager;

export {
    TerminalManager,
    terminalManager
};