// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package arthastunnelext

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	"github.com/gorilla/websocket"
	"go.uber.org/zap"

	"go.opentelemetry.io/collector/custom/controlplane/model"
	"go.opentelemetry.io/collector/custom/extension/arthastunnelext/pending"
	"go.opentelemetry.io/collector/custom/extension/arthastunnelext/registry"
)

type wsIngress string

const (
	ingressAgentGateway wsIngress = "agentgateway"
	ingressAdmin        wsIngress = "admin"
	ingressInternal     wsIngress = "internal" // Internal cross-node proxy
)

type arthasURIMethod string

const (
	methodAgentRegister arthasURIMethod = "agentRegister"
	methodConnectArthas arthasURIMethod = "connectArthas"
	methodOpenTunnel    arthasURIMethod = "openTunnel"

	// server -> agent
	methodStartTunnel arthasURIMethod = "startTunnel"
)

type arthasURICompat struct {
	logger *zap.Logger
	cfg    *Config

	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup

	upgrader websocket.Upgrader

	// Local state (always used)
	mu     sync.Mutex
	agents map[string]*compatAgent

	// Pending store for managing pending connections
	// In local mode: LocalPendingStore
	// In distributed mode: RedisPendingStore (from DistributedManager)
	pendingStore pending.PendingStore

	// Distributed manager (optional, nil in local mode)
	distributed *DistributedManager

	// Task submitter (optional). When set, used by auto-detach to submit arthas_detach tasks.
	taskSubmitter interface {
		SubmitTaskForAgent(ctx context.Context, agentID string, task *model.Task) error
	}
}

type compatAgent struct {
	agentID       string
	appName       string
	arthasVersion string
	appID         string

	remoteAddr string

	conn      *websocket.Conn
	writeMu   sync.Mutex // protects concurrent writes to conn (ping vs startTunnel)
	closeOnce sync.Once

	connectedAt int64 // unix milli, set once at registration
	lastPongAt  int64 // unix milli, updated atomically on pong

	// Session activity tracking (all unix milli)
	lastActivityAt    int64 // updated on pending/tunnel lifecycle
	lastTunnelEndedAt int64 // updated when relay ends
	lastAutoDetachAt  int64 // updated when auto-detach task submitted

	// Counters
	activePendings int64 // number of connectArthas pending (waiting for openTunnel)
	activeTunnels  int64 // number of active relays
}

// safeWriteMessage writes a message to the agent connection with mutex protection.
func (a *compatAgent) safeWriteMessage(messageType int, data []byte) error {
	if a == nil || a.conn == nil {
		return errors.New("agent or connection is nil")
	}
	a.writeMu.Lock()
	defer a.writeMu.Unlock()
	return a.conn.WriteMessage(messageType, data)
}

// safeWriteControl writes a control message to the agent connection with mutex protection.
func (a *compatAgent) safeWriteControl(messageType int, data []byte, deadline time.Time) error {
	if a == nil || a.conn == nil {
		return errors.New("agent or connection is nil")
	}
	a.writeMu.Lock()
	defer a.writeMu.Unlock()
	return a.conn.WriteControl(messageType, data, deadline)
}

func (a *compatAgent) incPending(nowMillis int64) {
	if a == nil {
		return
	}
	atomic.AddInt64(&a.activePendings, 1)
	atomic.StoreInt64(&a.lastActivityAt, nowMillis)
}

func (a *compatAgent) decPending(nowMillis int64) {
	if a == nil {
		return
	}
	v := atomic.AddInt64(&a.activePendings, -1)
	if v < 0 {
		atomic.StoreInt64(&a.activePendings, 0)
	}
	atomic.StoreInt64(&a.lastActivityAt, nowMillis)
}

func (a *compatAgent) incTunnel(nowMillis int64) {
	if a == nil {
		return
	}
	atomic.AddInt64(&a.activeTunnels, 1)
	atomic.StoreInt64(&a.lastActivityAt, nowMillis)
}

func (a *compatAgent) decTunnel(nowMillis int64) {
	if a == nil {
		return
	}
	v := atomic.AddInt64(&a.activeTunnels, -1)
	if v < 0 {
		atomic.StoreInt64(&a.activeTunnels, 0)
	}
	atomic.StoreInt64(&a.lastTunnelEndedAt, nowMillis)
	atomic.StoreInt64(&a.lastActivityAt, nowMillis)
}

// ANSI color codes for terminal output.
const (
	ansiReset  = "\033[0m"
	ansiGreen  = "\033[32m"
	ansiYellow = "\033[33m"
	ansiRed    = "\033[31m"
	ansiCyan   = "\033[36m"
)

// Browser session status constants.
const (
	statusConnecting    = "connecting"
	statusWaitingTunnel = "waiting_tunnel"
	statusReady         = "ready"
	statusTimeout       = "timeout"
	statusError         = "error"
	statusClosed        = "closed"
)

// formatTerminalStatus formats a status message with ANSI colors for terminal display.
func formatTerminalStatus(status, message string) string {
	var color string
	var icon string
	switch status {
	case statusConnecting, statusWaitingTunnel:
		color = ansiCyan
		icon = "[*]"
	case statusReady:
		color = ansiGreen
		icon = "[+]"
	case statusTimeout:
		color = ansiYellow
		icon = "[!]"
	case statusError:
		color = ansiRed
		icon = "[-]"
	case statusClosed:
		color = ansiYellow
		icon = "[!]"
	default:
		color = ansiReset
		icon = "[.]"
	}
	return color + icon + " " + message + ansiReset + "\r\n"
}

// sendBrowserStatus sends a status message to the browser connection as raw terminal text.
func sendBrowserStatus(conn *websocket.Conn, sessionID, status, message string) error {
	_ = sessionID // sessionID reserved for future use
	if conn == nil {
		return errors.New("browser connection is nil")
	}
	return conn.WriteMessage(websocket.TextMessage, []byte(formatTerminalStatus(status, message)))
}

func newArthasURICompat(ctx context.Context, logger *zap.Logger, cfg *Config, distributed *DistributedManager, taskSubmitter interface {
	SubmitTaskForAgent(ctx context.Context, agentID string, task *model.Task) error
}) *arthasURICompat {
	baseCtx := ctx
	if baseCtx == nil {
		baseCtx = context.Background()
	}
	cctx, cancel := context.WithCancel(baseCtx)

	// Determine pending store
	var pendingStore pending.PendingStore
	if distributed != nil {
		// Use distributed pending store (RedisPendingStore)
		pendingStore = distributed.PendingStore()
	} else {
		// Use local pending store
		pendingStore = pending.NewLocalPendingStore(logger, "local", "")
	}

	s := &arthasURICompat{
		logger: logger,
		cfg:    cfg,
		ctx:    cctx,
		cancel: cancel,
		upgrader: websocket.Upgrader{
			ReadBufferSize:  4096,
			WriteBufferSize: 4096,
			CheckOrigin: func(r *http.Request) bool {
				return true
			},
		},
		agents:        make(map[string]*compatAgent),
		pendingStore:  pendingStore,
		distributed:   distributed,
		taskSubmitter: taskSubmitter,
	}

	s.startAutoDetachLoop()
	return s
}

func (s *arthasURICompat) shutdown(ctx context.Context) {
	if s.cancel != nil {
		s.cancel()
	}

	// Best-effort close all agent connections.
	s.mu.Lock()
	agents := make([]*compatAgent, 0, len(s.agents))
	for _, a := range s.agents {
		agents = append(agents, a)
	}
	s.mu.Unlock()

	for _, a := range agents {
		a.close()
	}

	// Close pending store (will close all pending channels)
	if s.pendingStore != nil {
		_ = s.pendingStore.Close()
	}

	done := make(chan struct{})
	go func() {
		s.wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		return
	case <-ctx.Done():
		return
	}
}

func (s *arthasURICompat) startAutoDetachLoop() {
	if s == nil || s.cfg == nil {
		return
	}
	cfg := s.cfg.AutoDetach
	if !cfg.Enabled {
		return
	}
	if s.taskSubmitter == nil {
		// Make it explicit: enabled in config but no task submitter wired.
		s.logger.Warn("auto_detach enabled but task submitter not available; auto_detach disabled")
		return
	}
	if cfg.SweepInterval <= 0 {
		s.logger.Warn("auto_detach disabled due to invalid sweep_interval", zap.Duration("sweep_interval", cfg.SweepInterval))
		return
	}

	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		ticker := time.NewTicker(cfg.SweepInterval)
		defer ticker.Stop()
		for {
			select {
			case <-s.ctx.Done():
				return
			case <-ticker.C:
				s.runAutoDetachSweep()
			}
		}
	}()

	s.logger.Info("auto_detach enabled",
		zap.Duration("idle_threshold", cfg.IdleThreshold),
		zap.Duration("min_register_age", cfg.MinRegisterAge),
		zap.Duration("cooldown", cfg.Cooldown),
		zap.Duration("sweep_interval", cfg.SweepInterval),
		zap.Bool("require_no_pending", cfg.RequireNoPending),
		zap.Int("max_tasks_per_sweep", cfg.MaxTasksPerSweep),
	)
}

func (s *arthasURICompat) runAutoDetachSweep() {
	if s == nil || s.cfg == nil || !s.cfg.AutoDetach.Enabled {
		return
	}
	cfg := s.cfg.AutoDetach

	// Snapshot local agents to avoid holding the mutex while submitting tasks.
	s.mu.Lock()
	agents := make([]*compatAgent, 0, len(s.agents))
	for _, a := range s.agents {
		agents = append(agents, a)
	}
	s.mu.Unlock()

	nowMillis := time.Now().UnixMilli()
	submitted := 0

	for _, a := range agents {
		if a == nil || a.conn == nil {
			continue
		}
		if submitted >= cfg.MaxTasksPerSweep {
			break
		}

		// Only consider healthy control connections.
		if s.isAgentTimeout(a) {
			continue
		}

		// Cold-start protection.
		if cfg.MinRegisterAge > 0 {
			if age := nowMillis - a.connectedAt; age < cfg.MinRegisterAge.Milliseconds() {
				continue
			}
		}

		if atomic.LoadInt64(&a.activeTunnels) > 0 {
			continue
		}
		if cfg.RequireNoPending && atomic.LoadInt64(&a.activePendings) > 0 {
			continue
		}

		lastAct := atomic.LoadInt64(&a.lastActivityAt)
		if lastAct <= 0 {
			lastAct = a.connectedAt
		}
		if idle := nowMillis - lastAct; idle < cfg.IdleThreshold.Milliseconds() {
			continue
		}

		if cfg.Cooldown > 0 {
			lastDetach := atomic.LoadInt64(&a.lastAutoDetachAt)
			if lastDetach > 0 && nowMillis-lastDetach < cfg.Cooldown.Milliseconds() {
				continue
			}
		}

		probeAgentID := strings.TrimSpace(a.agentID)
		if probeAgentID == "" {
			continue
		}

		if err := s.submitAutoDetachTask(probeAgentID, a, nowMillis, lastAct); err != nil {
			s.logger.Warn("auto_detach submit failed",
				zap.String("probe_agent_id", probeAgentID),
				zap.String("tunnel_agent_id", a.agentID),
				zap.Error(err),
			)
			continue
		}
		atomic.StoreInt64(&a.lastAutoDetachAt, nowMillis)
		submitted++
	}

	if submitted > 0 {
		s.logger.Info("auto_detach tasks submitted", zap.Int("count", submitted))
	}
}

func (s *arthasURICompat) submitAutoDetachTask(probeAgentID string, a *compatAgent, nowMillis int64, lastActMillis int64) error {
	if s == nil || s.taskSubmitter == nil || a == nil {
		return errors.New("nil submitter or agent")
	}

	params := map[string]any{
		"reason":          "idle_no_tunnel",
		"tunnel_agent_id": a.agentID,
		"idle_millis":     nowMillis - lastActMillis,
		"connected_at":    a.connectedAt,
		"last_pong_at":    atomic.LoadInt64(&a.lastPongAt),
		"app_id":          a.appID,
		"app_name":        a.appName,
		"remote_addr":     a.remoteAddr,
	}
	b, _ := json.Marshal(params)

	t := &model.Task{
		ID:              uuid.NewString(),
		TypeName:        "arthas_detach",
		TargetAgentID:   probeAgentID,
		TimeoutMillis:   int64(s.cfg.AutoDetach.TaskTimeout / time.Millisecond),
		CreatedAtMillis: nowMillis,
		ParametersJSON:  b,
	}

	// Bound task submission latency to avoid blocking the sweep loop.
	ctx, cancel := context.WithTimeout(s.ctx, 2*time.Second)
	defer cancel()
	return s.taskSubmitter.SubmitTaskForAgent(ctx, probeAgentID, t)
}

func (a *compatAgent) close() {
	if a == nil {
		return
	}
	a.closeOnce.Do(func() {
		if a.conn != nil {
			_ = a.conn.Close()
		}
	})
}

// HandleInternalConnectArthas is the dedicated entry point for internal cross-node
// connectArthas proxy requests. Unlike handleWS, it does not rely on the "method"
// query parameter for dispatch - the caller (extension.handleInternalConnect) has
// already determined this is a connectArthas request based on the URL path.
//
// This separation ensures:
// - Internal protocol is decoupled from external method-based dispatch
// - Clear single responsibility: internal proxy vs external WS protocol
// - Easier evolution of internal behavior without affecting external API
func (s *arthasURICompat) HandleInternalConnectArthas(w http.ResponseWriter, r *http.Request, agentID string) {
	remoteAddr := r.RemoteAddr
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		remoteAddr = xff
	}

	logger := s.logger.With(
		zap.String("ingress", string(ingressInternal)),
		zap.String("method", "connectArthas"),
		zap.String("remote_addr", remoteAddr),
		zap.String("agent_id", agentID),
	)

	logger.Debug("Internal connectArthas WebSocket upgrade attempt")

	conn, err := s.upgrader.Upgrade(w, r, nil)
	if err != nil {
		logger.Error("Failed to upgrade internal WebSocket", zap.Error(err))
		return
	}

	logger.Info("Internal connectArthas WebSocket connection established")

	// Build context for handleConnectArthas
	// We inject the agentID into query so handleConnectArthas can read it uniformly
	query := r.URL.Query()
	query.Set("id", agentID)

	ctx := &compatConnContext{
		ingress:    ingressInternal,
		conn:       conn,
		request:    r,
		query:      query,
		remoteAddr: remoteAddr,
		logger:     logger,
	}

	// Run the connectArthas flow directly (no dispatch needed)
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		s.handleConnectArthas(ctx)
	}()
}

func (s *arthasURICompat) handleWS(ing wsIngress, w http.ResponseWriter, r *http.Request) {
	remoteAddr := r.RemoteAddr
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		remoteAddr = xff
	}

	s.logger.Debug("WebSocket connection attempt",
		zap.String("ingress", string(ing)),
		zap.String("remote_addr", remoteAddr),
		zap.String("request_uri", r.RequestURI),
		zap.String("user_agent", r.UserAgent()),
	)

	conn, err := s.upgrader.Upgrade(w, r, nil)
	if err != nil {
		s.logger.Error("Failed to upgrade WebSocket",
			zap.String("ingress", string(ing)),
			zap.String("remote_addr", remoteAddr),
			zap.String("request_uri", r.RequestURI),
			zap.Error(err),
		)
		return
	}

	method := strings.TrimSpace(r.URL.Query().Get("method"))
	if method == "" {
		s.logger.Warn("WebSocket missing method parameter",
			zap.String("ingress", string(ing)),
			zap.String("remote_addr", remoteAddr),
			zap.String("request_uri", r.RequestURI),
		)
		_ = writeClose(conn, 2000, "missing method")
		_ = conn.Close()
		return
	}

	m := normalizeMethod(method)

	// Enforce ingress allowlist by default.
	if s.cfg.StrictIngressMethodAllowlist {
		if !isMethodAllowed(ing, m) {
			s.logger.Warn("WebSocket method not allowed",
				zap.String("ingress", string(ing)),
				zap.String("method", string(m)),
				zap.String("remote_addr", remoteAddr),
			)
			_ = writeClose(conn, 2000, "method not allowed")
			_ = conn.Close()
			return
		}
	}

	s.logger.Info("WebSocket connection established",
		zap.String("ingress", string(ing)),
		zap.String("method", string(m)),
		zap.String("remote_addr", remoteAddr),
		zap.String("query", r.URL.RawQuery),
	)

	ctx := &compatConnContext{
		ingress:    ing,
		conn:       conn,
		request:    r,
		query:      r.URL.Query(),
		remoteAddr: remoteAddr,
		logger: s.logger.With(
			zap.String("ingress", string(ing)),
			zap.String("method", string(m)),
			zap.String("remote_addr", remoteAddr),
		),
	}

	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		s.dispatch(ctx, m)
	}()
}

type compatConnContext struct {
	ingress    wsIngress
	conn       *websocket.Conn
	request    *http.Request
	query      url.Values
	remoteAddr string
	logger     *zap.Logger
}

func normalizeMethod(method string) arthasURIMethod {
	m := strings.TrimSpace(method)
	m = strings.TrimPrefix(m, "/")
	m = strings.TrimSpace(m)
	// accept case-insensitive inputs
	lm := strings.ToLower(m)
	switch lm {
	case strings.ToLower(string(methodAgentRegister)):
		return methodAgentRegister
	case strings.ToLower(string(methodConnectArthas)):
		return methodConnectArthas
	case strings.ToLower(string(methodOpenTunnel)):
		return methodOpenTunnel
	default:
		// preserve original for error message
		return arthasURIMethod(m)
	}
}

func isMethodAllowed(ing wsIngress, m arthasURIMethod) bool {
	switch ing {
	case ingressAgentGateway:
		return m == methodAgentRegister || m == methodOpenTunnel
	case ingressAdmin:
		return m == methodConnectArthas
	case ingressInternal:
		// Internal proxy only allows connectArthas (token already validated at extension layer)
		return m == methodConnectArthas
	default:
		return false
	}
}

func (s *arthasURICompat) dispatch(ctx *compatConnContext, m arthasURIMethod) {
	switch m {
	case methodAgentRegister:
		s.handleAgentRegister(ctx)
	case methodConnectArthas:
		s.handleConnectArthas(ctx)
	case methodOpenTunnel:
		s.handleOpenTunnel(ctx)
	default:
		ctx.logger.Warn("Unsupported method")
		_ = writeClose(ctx.conn, 2000, "unsupported method")
		_ = ctx.conn.Close()
	}
}

func (s *arthasURICompat) handleAgentRegister(ctx *compatConnContext) {
	agentID, appName, arthasVersion := s.resolveAgentRegisterParams(ctx.query)
	appID := ctx.request.Header.Get("X-App-ID")

	remoteAddr := ctx.remoteAddr
	if ra := ctx.request.Header.Get("X-Forwarded-For"); ra != "" {
		remoteAddr = ra
	}

	now := time.Now().UnixMilli()
	a := &compatAgent{
		agentID:        agentID,
		appName:        appName,
		arthasVersion:  arthasVersion,
		appID:          appID,
		remoteAddr:     remoteAddr,
		conn:           ctx.conn,
		connectedAt:    now,
		lastPongAt:     now,
		lastActivityAt: now,
	}

	// Replace old agent connection if exists.
	s.mu.Lock()
	if old, ok := s.agents[agentID]; ok {
		ctx.logger.Info("Replacing existing agentRegister connection",
			zap.String("agent_id", agentID),
		)
		old.close()
	}
	s.agents[agentID] = a
	s.mu.Unlock()

	// Register in distributed registry if enabled
	if s.distributed != nil {
		ip := remoteAddr
		if idx := strings.LastIndex(remoteAddr, ":"); idx > 0 {
			ip = remoteAddr[:idx]
		}
		agentInfo := &registry.AgentInfo{
			AgentID:       agentID,
			AppID:         appID,
			AppName:       appName,
			ArthasVersion: arthasVersion,
			IP:            ip,
			RemoteAddr:    remoteAddr,
			NodeID:        s.distributed.NodeID(),
			NodeAddr:      s.distributed.NodeAddr(),
			ConnectedAt:   now,
			LastPongAt:    now,
		}
		if err := s.distributed.Registry().Register(s.ctx, agentInfo); err != nil {
			ctx.logger.Warn("Failed to register agent in distributed registry",
				zap.String("agent_id", agentID),
				zap.Error(err),
			)
		}
	}

	ctx.logger.Info("Agent registered",
		zap.String("agent_id", agentID),
		zap.String("app_name", appName),
		zap.String("arthas_version", arthasVersion),
		zap.String("app_id", appID),
		zap.String("remote_addr", remoteAddr),
	)

	// Send response frame: response:/?method=agentRegister&id=xxx
	vals := url.Values{}
	vals.Set("method", string(methodAgentRegister))
	vals.Set("id", agentID)
	resp := buildResponseFrame(vals)
	_ = ctx.conn.WriteMessage(websocket.TextMessage, []byte(resp))

	// Keep the control connection alive and detect disconnect.
	s.runAgentControlLoops(ctx, a)

	// Cleanup on disconnect.
	s.mu.Lock()
	if cur, ok := s.agents[agentID]; ok && cur == a {
		delete(s.agents, agentID)
		ctx.logger.Info("Agent disconnected and removed from registry",
			zap.String("agent_id", agentID),
			zap.Duration("connection_duration", time.Since(time.UnixMilli(a.connectedAt))),
		)
	}
	s.mu.Unlock()

	// Unregister from distributed registry
	if s.distributed != nil {
		if err := s.distributed.Registry().Unregister(s.ctx, agentID); err != nil {
			ctx.logger.Warn("Failed to unregister agent from distributed registry",
				zap.String("agent_id", agentID),
				zap.Error(err),
			)
		}
	}
}

func (s *arthasURICompat) resolveAgentRegisterParams(q url.Values) (agentID string, appName string, arthasVersion string) {
	appName = q.Get("appName")
	arthasVersion = q.Get("arthasVersion")

	id := q.Get("id")
	if id != "" {
		return id, appName, arthasVersion
	}

	randID := randomUpperAlphaNum(20)
	if appName != "" {
		return appName + "_" + randID, appName, arthasVersion
	}
	return randID, appName, arthasVersion
}

func (s *arthasURICompat) runAgentControlLoops(ctx *compatConnContext, a *compatAgent) {
	// Read loop to process control frames (and to detect disconnect).
	// We don't implement httpProxy in this phase; frames are ignored.
	readDone := make(chan struct{})

	// Use livenessTimeout for ReadDeadline (unified with ListAgents filter).
	livenessTimeout := s.livenessTimeout()

	// Configure pong handler to track liveness (atomic update).
	_ = ctx.conn.SetReadDeadline(time.Now().Add(livenessTimeout))
	ctx.conn.SetPongHandler(func(string) error {
		pongTime := time.Now()
		atomic.StoreInt64(&a.lastPongAt, pongTime.UnixMilli())
		_ = ctx.conn.SetReadDeadline(pongTime.Add(livenessTimeout))

		// Record pong for batched Redis update in distributed mode
		if s.distributed != nil {
			s.distributed.RecordPong(a.agentID, pongTime)
		}
		return nil
	})

	// Send first ping immediately to reduce initial window without pong update.
	_ = a.safeWriteControl(websocket.PingMessage, nil, time.Now().Add(10*time.Second))

	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		defer close(readDone)
		for {
			_, _, err := ctx.conn.ReadMessage()
			if err != nil {
				return
			}
		}
	}()

	// Ping loop.
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		t := time.NewTicker(s.pingInterval())
		defer t.Stop()
		for {
			select {
			case <-s.ctx.Done():
				return
			case <-readDone:
				return
			case <-t.C:
				// Use safeWriteControl to protect against concurrent writes.
				_ = a.safeWriteControl(websocket.PingMessage, nil, time.Now().Add(10*time.Second))
			}
		}
	}()

	<-readDone
}

func (s *arthasURICompat) handleConnectArthas(ctx *compatConnContext) {
	agentID := ctx.query.Get("id")

	ctx.logger.Info("[connectArthas] Received browser connect request",
		zap.String("requested_agent_id", agentID),
		zap.String("full_query", ctx.request.URL.RawQuery),
	)

	if agentID == "" {
		ctx.logger.Warn("[connectArthas] Missing agent id in request")
		_ = sendBrowserStatus(ctx.conn, "", statusError, "Missing agent ID parameter")
		_ = ctx.conn.Close()
		return
	}

	// Generate sessionID immediately for browser.
	sessionID := randomUpperAlphaNum(20)

	// Step 1: Check if agent is online (local first, then distributed registry)
	var agent *compatAgent
	var agentNodeAddr string
	var isLocalAgent bool

	s.mu.Lock()
	agent = s.agents[agentID]
	registeredAgentIDs := make([]string, 0, len(s.agents))
	for id := range s.agents {
		registeredAgentIDs = append(registeredAgentIDs, id)
	}
	s.mu.Unlock()

	isLocalAgent = agent != nil && agent.conn != nil

	// In distributed mode, check if agent is on another node
	if !isLocalAgent && s.distributed != nil {
		var err error
		agentNodeAddr, err = s.distributed.GetAgentNodeAddr(s.ctx, agentID)
		if err != nil {
			ctx.logger.Warn("[connectArthas] Failed to lookup agent in distributed registry",
				zap.String("agent_id", agentID),
				zap.Error(err),
			)
		}
	}

	ctx.logger.Info("[connectArthas] Looking up agent",
		zap.String("requested_agent_id", agentID),
		zap.String("session_id", sessionID),
		zap.Strings("local_agents", registeredAgentIDs),
		zap.Bool("local_agent_found", isLocalAgent),
		zap.String("remote_node_addr", agentNodeAddr),
	)

	// If agent not found anywhere, return error
	if !isLocalAgent && agentNodeAddr == "" {
		ctx.logger.Warn("[connectArthas] Agent not online",
			zap.String("requested_agent_id", agentID),
		)
		_ = sendBrowserStatus(ctx.conn, sessionID, statusError,
			"Agent is offline. Please ensure the target application has started Arthas and connected to the server.")
		_ = ctx.conn.Close()
		return
	}

	// If agent is on another node, proxy the request
	if !isLocalAgent && agentNodeAddr != "" {
		ctx.logger.Info("[connectArthas] Agent is on another node, proxying request",
			zap.String("agent_id", agentID),
			zap.String("session_id", sessionID),
			zap.String("target_node", agentNodeAddr),
		)
		s.proxyConnectArthas(ctx, agentID, sessionID, agentNodeAddr)
		return
	}

	// Agent is local, proceed with normal flow
	ctx.logger.Info("[connectArthas] Agent found locally",
		zap.String("agent_id", agentID),
		zap.String("session_id", sessionID),
		zap.String("agent_app_name", agent.appName),
		zap.String("agent_arthas_version", agent.arthasVersion),
		zap.String("agent_remote_addr", agent.remoteAddr),
		zap.Time("agent_connected_at", time.UnixMilli(agent.connectedAt)),
	)

	// Step 2: Send initial status to browser immediately.
	if err := sendBrowserStatus(ctx.conn, sessionID, statusConnecting, "Connecting to agent..."); err != nil {
		ctx.logger.Error("[connectArthas] Failed to send initial status to browser",
			zap.String("session_id", sessionID),
			zap.Error(err),
		)
		_ = ctx.conn.Close()
		return
	}

	clientConnID := randomUpperAlphaNum(20)
	createdAt := time.Now()

	// Step 3: Register pending in the unified pending store.
	pendingInfo := &pending.PendingInfo{
		ClientConnID: clientConnID,
		SessionID:    sessionID,
		AgentID:      agentID,
		CreatedAt:    createdAt,
	}
	if err := s.pendingStore.CreateWithBrowserConn(s.ctx, pendingInfo, ctx.conn); err != nil {
		ctx.logger.Error("[connectArthas] Failed to register pending connection",
			zap.String("client_connection_id", clientConnID),
			zap.Error(err),
		)
		_ = sendBrowserStatus(ctx.conn, sessionID, statusError, "Server busy, please try again later")
		_ = ctx.conn.Close()
		return
	}

	pendingCounted := true
	agent.incPending(time.Now().UnixMilli())
	defer func() {
		if pendingCounted {
			agent.decPending(time.Now().UnixMilli())
		}
	}()

	ctx.logger.Info("[connectArthas] Registered pending connection",
		zap.String("agent_id", agentID),
		zap.String("session_id", sessionID),
		zap.String("client_connection_id", clientConnID),
	)

	// Step 4: Send startTunnel to agent via the existing agentRegister connection.
	vals := url.Values{}
	vals.Set("method", string(methodStartTunnel))
	vals.Set("id", agentID)
	vals.Set("clientConnectionId", clientConnID)
	startTunnel := buildResponseFrame(vals)

	ctx.logger.Info("[connectArthas] Sending startTunnel to agent",
		zap.String("agent_id", agentID),
		zap.String("session_id", sessionID),
		zap.String("client_connection_id", clientConnID),
		zap.String("startTunnel_message", startTunnel),
	)

	// Use safeWriteMessage to protect against concurrent writes (ping vs startTunnel).
	if err := agent.safeWriteMessage(websocket.TextMessage, []byte(startTunnel)); err != nil {
		ctx.logger.Error("[connectArthas] Failed to send startTunnel to agent",
			zap.String("agent_id", agentID),
			zap.String("session_id", sessionID),
			zap.String("client_connection_id", clientConnID),
			zap.Error(err),
		)
		_ = sendBrowserStatus(ctx.conn, sessionID, statusError, "Failed to notify agent, please try again later")
		s.cleanupPending(clientConnID, ctx.conn, "startTunnel send failed")
		return
	}

	// Step 5: Send waiting status to browser.
	if err := sendBrowserStatus(ctx.conn, sessionID, statusWaitingTunnel, "Waiting for agent to establish tunnel..."); err != nil {
		ctx.logger.Warn("[connectArthas] Failed to send waiting status to browser",
			zap.String("session_id", sessionID),
			zap.Error(err),
		)
		// Continue anyway, browser might still receive tunnel.
	}

	ctx.logger.Info("[connectArthas] startTunnel sent successfully, waiting for agent openTunnel",
		zap.String("agent_id", agentID),
		zap.String("session_id", sessionID),
		zap.String("client_connection_id", clientConnID),
		zap.Duration("timeout", s.connectTimeout()),
	)

	// Step 6: Wait for openTunnel from agent using unified pending store.
	timeout := s.connectTimeout()
	tunnelConn, err := s.pendingStore.WaitForTunnel(s.ctx, clientConnID, timeout)
	if err != nil {
		// Determine the error type
		if err == context.DeadlineExceeded {
			ctx.logger.Error("[connectArthas] Timeout waiting for agent openTunnel",
				zap.String("agent_id", agentID),
				zap.String("session_id", sessionID),
				zap.String("client_connection_id", clientConnID),
				zap.Duration("timeout", timeout),
			)
			_ = sendBrowserStatus(ctx.conn, sessionID, statusTimeout, "Timeout waiting for agent response, please check network or retry")
		} else if err == context.Canceled {
			ctx.logger.Warn("[connectArthas] Server shutdown while waiting for openTunnel",
				zap.String("agent_id", agentID),
				zap.String("session_id", sessionID),
				zap.String("client_connection_id", clientConnID),
			)
			_ = sendBrowserStatus(ctx.conn, sessionID, statusClosed, "Server is under maintenance, please try again later")
		} else {
			ctx.logger.Error("[connectArthas] Error waiting for openTunnel",
				zap.String("agent_id", agentID),
				zap.String("session_id", sessionID),
				zap.String("client_connection_id", clientConnID),
				zap.Error(err),
			)
			_ = sendBrowserStatus(ctx.conn, sessionID, statusError, "Failed to establish tunnel")
		}
		s.cleanupPending(clientConnID, ctx.conn, "wait failed")
		return
	}

	// Success: pending delivered, remove pending record and start relay.
	pendingCounted = false
	nowMillis := time.Now().UnixMilli()
	agent.decPending(nowMillis)
	// Don't close browser conn, it's used for relay.
	s.cleanupPending(clientConnID, nil, "")

	ctx.logger.Info("[connectArthas] openTunnel received, tunnel bound successfully",
		zap.String("agent_id", agentID),
		zap.String("session_id", sessionID),
		zap.String("client_connection_id", clientConnID),
		zap.String("tunnel_remote_addr", tunnelConn.RemoteAddr().String()),
	)

	// Step 7: Send ready status to browser.
	if err := sendBrowserStatus(ctx.conn, sessionID, statusReady, "Connected successfully, terminal is ready"); err != nil {
		ctx.logger.Warn("[connectArthas] Failed to send ready status to browser",
			zap.String("session_id", sessionID),
			zap.Error(err),
		)
		// Continue anyway, relay should still work.
	}

	ctx.logger.Info("[connectArthas] Starting WebSocket relay",
		zap.String("agent_id", agentID),
		zap.String("session_id", sessionID),
		zap.String("client_connection_id", clientConnID),
		zap.String("browser_addr", ctx.remoteAddr),
		zap.String("agent_tunnel_addr", tunnelConn.RemoteAddr().String()),
	)

	agent.incTunnel(nowMillis)
	relayWebSocketPair(s.ctx, ctx.logger, ctx.conn, tunnelConn)
	agent.decTunnel(time.Now().UnixMilli())
}

// proxyConnectArthas proxies the connectArthas request to another node.
func (s *arthasURICompat) proxyConnectArthas(ctx *compatConnContext, agentID, sessionID, targetNodeAddr string) {
	if s.distributed == nil || s.distributed.Proxy() == nil {
		ctx.logger.Error("[connectArthas] Distributed proxy not available")
		_ = sendBrowserStatus(ctx.conn, sessionID, statusError, "Internal error: distributed proxy not available")
		_ = ctx.conn.Close()
		return
	}

	// Send status to browser
	if err := sendBrowserStatus(ctx.conn, sessionID, statusConnecting, "Connecting to agent (cross-node)..."); err != nil {
		ctx.logger.Warn("[connectArthas] Failed to send connecting status",
			zap.Error(err),
		)
	}

	// Proxy the WebSocket connection to the target node
	err := s.distributed.Proxy().ProxyConnectArthas(s.ctx, targetNodeAddr, ctx.conn, agentID)
	if err != nil {
		ctx.logger.Error("[connectArthas] Failed to proxy to target node",
			zap.String("agent_id", agentID),
			zap.String("target_node", targetNodeAddr),
			zap.Error(err),
		)
		_ = sendBrowserStatus(ctx.conn, sessionID, statusError, "Failed to connect to agent node")
		_ = ctx.conn.Close()
	}
}

// cleanupPending removes a pending connection from the pending store and optionally closes the browser connection.
func (s *arthasURICompat) cleanupPending(clientConnID string, browserConn *websocket.Conn, reason string) {
	// Delete from pending store
	_ = s.pendingStore.Delete(s.ctx, clientConnID)

	// Close browser connection if provided
	if browserConn != nil && reason != "" {
		_ = writeClose(browserConn, 2000, reason)
		_ = browserConn.Close()
	}
}

func (s *arthasURICompat) handleOpenTunnel(ctx *compatConnContext) {
	clientConnID := firstNonEmpty(
		ctx.query.Get("clientConnectionId"),
		ctx.query.Get("client_connection_id"),
	)

	ctx.logger.Info("[openTunnel] Received agent openTunnel request",
		zap.String("client_connection_id", clientConnID),
		zap.String("full_query", ctx.request.URL.RawQuery),
	)

	if clientConnID == "" {
		ctx.logger.Warn("[openTunnel] Missing clientConnectionId in request")
		_ = writeClose(ctx.conn, 2000, "missing clientConnectionId")
		_ = ctx.conn.Close()
		return
	}

	// Step 1: Try to deliver to local pending store first
	isLocal := s.pendingStore.IsLocal(clientConnID)

	ctx.logger.Info("[openTunnel] Looking up pending connection",
		zap.String("client_connection_id", clientConnID),
		zap.Bool("is_local", isLocal),
	)

	if isLocal {
		// Deliver tunnel to local pending
		err := s.pendingStore.DeliverTunnel(clientConnID, ctx.conn)
		if err == nil {
			ctx.logger.Info("[openTunnel] Tunnel delivered successfully (local)",
				zap.String("client_connection_id", clientConnID),
				zap.String("tunnel_remote_addr", ctx.remoteAddr),
			)
			// Connection ownership transferred to the pending handler
			return
		}

		if err == pending.ErrTunnelAlreadyDelivered {
			ctx.logger.Warn("[openTunnel] Tunnel already bound (duplicate openTunnel?)",
				zap.String("client_connection_id", clientConnID),
			)
			_ = writeClose(ctx.conn, 2000, "tunnel already bound")
			_ = ctx.conn.Close()
			return
		}

		// Other errors (e.g., pending not found) - fall through to distributed check
		ctx.logger.Debug("[openTunnel] Local delivery failed, checking distributed store",
			zap.String("client_connection_id", clientConnID),
			zap.Error(err),
		)
	}

	// Step 2: In distributed mode, check if pending is on another node
	if s.distributed != nil {
		pendingInfo, err := s.pendingStore.Get(s.ctx, clientConnID)
		if err != nil {
			ctx.logger.Warn("[openTunnel] Failed to lookup pending in store",
				zap.String("client_connection_id", clientConnID),
				zap.Error(err),
			)
		}

		if pendingInfo != nil && pendingInfo.NodeID != s.distributed.NodeID() {
			// Pending is on another node, proxy the tunnel
			ctx.logger.Info("[openTunnel] Pending is on another node, proxying tunnel",
				zap.String("client_connection_id", clientConnID),
				zap.String("target_node_id", pendingInfo.NodeID),
				zap.String("target_node_addr", pendingInfo.NodeAddr),
			)
			s.proxyOpenTunnel(ctx, clientConnID, pendingInfo.NodeAddr)
			return
		}
	}

	// Not found anywhere
	ctx.logger.Warn("[openTunnel] No pending connection found",
		zap.String("client_connection_id", clientConnID),
	)
	_ = writeClose(ctx.conn, 2000, "no pending connection")
	_ = ctx.conn.Close()
}

// proxyOpenTunnel proxies the openTunnel request to another node.
func (s *arthasURICompat) proxyOpenTunnel(ctx *compatConnContext, clientConnID, targetNodeAddr string) {
	if s.distributed == nil || s.distributed.Proxy() == nil {
		ctx.logger.Error("[openTunnel] Distributed proxy not available")
		_ = writeClose(ctx.conn, 2000, "internal error")
		_ = ctx.conn.Close()
		return
	}

	err := s.distributed.Proxy().ProxyOpenTunnel(s.ctx, targetNodeAddr, ctx.conn, clientConnID)
	if err != nil {
		ctx.logger.Error("[openTunnel] Failed to proxy to target node",
			zap.String("client_connection_id", clientConnID),
			zap.String("target_node", targetNodeAddr),
			zap.Error(err),
		)
		_ = writeClose(ctx.conn, 2000, "proxy failed")
		_ = ctx.conn.Close()
	}
}

func (s *arthasURICompat) pingInterval() time.Duration {
	if s.cfg.PingInterval > 0 {
		return s.cfg.PingInterval
	}
	return 20 * time.Second
}

func (s *arthasURICompat) pongTimeout() time.Duration {
	if s.cfg.PongTimeout > 0 {
		return s.cfg.PongTimeout
	}
	return 60 * time.Second
}

func (s *arthasURICompat) livenessGrace() time.Duration {
	if s.cfg.LivenessGrace > 0 {
		return s.cfg.LivenessGrace
	}
	return 30 * time.Second
}

// livenessTimeout returns the unified timeout for both ReadDeadline and ListAgents filter.
// livenessTimeout = pongTimeout + livenessGrace
func (s *arthasURICompat) livenessTimeout() time.Duration {
	return s.pongTimeout() + s.livenessGrace()
}

// ListAgents returns a snapshot of all connected and healthy agents.
// In distributed mode, returns agents from all nodes via Redis.
func (s *arthasURICompat) ListAgents() []*ConnectedAgent {
	// In distributed mode, use the registry to get all agents
	if s.distributed != nil {
		agentInfos, err := s.distributed.Registry().List(s.ctx)
		if err != nil {
			s.logger.Warn("Failed to list agents from distributed registry, falling back to local",
				zap.Error(err),
			)
			// Fall through to local
		} else {
			agents := make([]*ConnectedAgent, 0, len(agentInfos))
			for _, info := range agentInfos {
				agents = append(agents, &ConnectedAgent{
					AgentID:     info.AgentID,
					AppID:       info.AppID,
					ServiceName: info.AppName,
					IP:          info.IP,
					Version:     info.ArthasVersion,
					ConnectedAt: time.UnixMilli(info.ConnectedAt),
					LastPingAt:  time.UnixMilli(info.LastPongAt),
				})
			}
			return agents
		}
	}

	// Local mode or fallback
	s.mu.Lock()
	defer s.mu.Unlock()

	agents := make([]*ConnectedAgent, 0, len(s.agents))
	for _, a := range s.agents {
		if a == nil || a.conn == nil {
			continue
		}
		// Filter out timed-out agents.
		if s.isAgentTimeout(a) {
			continue
		}
		agents = append(agents, a.toConnectedAgent())
	}
	return agents
}

// IsAgentOnline checks if agent is registered and healthy (lastPingAt not timeout).
// In distributed mode, checks both local and Redis registry.
func (s *arthasURICompat) IsAgentOnline(agentID string) bool {
	// Check local first
	s.mu.Lock()
	a := s.agents[agentID]
	s.mu.Unlock()

	if a != nil && a.conn != nil && !s.isAgentTimeout(a) {
		return true
	}

	// In distributed mode, check Redis registry
	if s.distributed != nil {
		info, err := s.distributed.Registry().Get(s.ctx, agentID)
		if err != nil {
			s.logger.Warn("Failed to check agent in distributed registry",
				zap.String("agent_id", agentID),
				zap.Error(err),
			)
			return false
		}
		return info != nil
	}

	return false
}

// isAgentTimeout checks if agent's lastPongAt exceeds livenessTimeout.
// Uses atomic read to avoid data race with pong handler.
func (s *arthasURICompat) isAgentTimeout(a *compatAgent) bool {
	if a == nil {
		return true
	}
	lastPong := atomic.LoadInt64(&a.lastPongAt)
	return time.Since(time.UnixMilli(lastPong)) > s.livenessTimeout()
}

// toConnectedAgent converts compatAgent to the public ConnectedAgent struct.
// Uses atomic read for lastPongAt to avoid data race.
func (a *compatAgent) toConnectedAgent() *ConnectedAgent {
	if a == nil {
		return nil
	}

	// Parse IP from remoteAddr ("ip:port" -> "ip").
	ip := a.remoteAddr
	if idx := strings.LastIndex(a.remoteAddr, ":"); idx > 0 {
		ip = a.remoteAddr[:idx]
	}

	return &ConnectedAgent{
		AgentID:     a.agentID,
		AppID:       a.appID,
		ServiceName: a.appName,
		IP:          ip,
		Version:     a.arthasVersion,
		ConnectedAt: time.UnixMilli(a.connectedAt),
		LastPingAt:  time.UnixMilli(atomic.LoadInt64(&a.lastPongAt)),
	}
}

func (s *arthasURICompat) connectTimeout() time.Duration {
	if s.cfg.CompatConnectTimeout > 0 {
		return s.cfg.CompatConnectTimeout
	}
	return 20 * time.Second
}

func randomUpperAlphaNum(n int) string {
	// Generate random bytes, hex-encode and uppercase, then slice.
	b := make([]byte, (n+1)/2)
	_, _ = rand.Read(b)
	s := strings.ToUpper(hex.EncodeToString(b))
	if len(s) >= n {
		return s[:n]
	}
	return s
}

func firstNonEmpty(vs ...string) string {
	for _, v := range vs {
		if v != "" {
			return v
		}
	}
	return ""
}

func buildResponseFrame(vals url.Values) string {
	// Official format: response:/?k=v
	return "response:/?" + vals.Encode()
}

func writeClose(conn *websocket.Conn, code int, reason string) error {
	if conn == nil {
		return errors.New("nil conn")
	}
	deadline := time.Now().Add(2 * time.Second)
	msg := websocket.FormatCloseMessage(code, reason)
	_ = conn.WriteControl(websocket.CloseMessage, msg, deadline)
	return nil
}

// connectToAgentProgrammatic establishes a programmatic tunnel connection to an agent.
// This is the internal implementation of ConnectToAgent, used by MCP Extension.
//
// Unlike handleConnectArthas (which operates over a browser WebSocket), this method:
//   - Does NOT need a browser WebSocket connection
//   - Uses the pending store's Create (not CreateWithBrowserConn) to register a pending
//   - Returns the raw tunnel WebSocket connection wrapped in an ArthasSession
//   - Only supports local agents (distributed proxy is a future enhancement)
func (s *arthasURICompat) connectToAgentProgrammatic(ctx context.Context, agentID string, logger *zap.Logger) (*ArthasSession, error) {
	logger.Info("[connectToAgent] Programmatic connect request",
		zap.String("agent_id", agentID),
	)

	if agentID == "" {
		return nil, fmt.Errorf("agent_id is required")
	}

	// Step 1: Find the agent locally
	s.mu.Lock()
	agent := s.agents[agentID]
	s.mu.Unlock()

	if agent == nil || agent.conn == nil {
		// In distributed mode, the agent might be on another node.
		// For now, only support local agents. Distributed support can be added later
		// by implementing a similar proxy mechanism as proxyConnectArthas.
		if s.distributed != nil {
			nodeAddr, err := s.distributed.GetAgentNodeAddr(s.ctx, agentID)
			if err == nil && nodeAddr != "" {
				return nil, fmt.Errorf("agent '%s' is on another node (%s); programmatic cross-node connection is not yet supported", agentID, nodeAddr)
			}
		}
		return nil, fmt.Errorf("agent '%s' is not connected to tunnel", agentID)
	}

	// Check if agent is healthy
	if s.isAgentTimeout(agent) {
		return nil, fmt.Errorf("agent '%s' tunnel connection is unhealthy (timed out)", agentID)
	}

	// Step 2: Generate IDs
	sessionID := randomUpperAlphaNum(20)
	clientConnID := randomUpperAlphaNum(20)

	// Step 3: Register pending (without browser connection)
	pendingInfo := &pending.PendingInfo{
		ClientConnID: clientConnID,
		SessionID:    sessionID,
		AgentID:      agentID,
		CreatedAt:    time.Now(),
	}
	if err := s.pendingStore.Create(s.ctx, pendingInfo); err != nil {
		return nil, fmt.Errorf("failed to register pending connection: %w", err)
	}

	agent.incPending(time.Now().UnixMilli())

	// Ensure cleanup on error
	cleanedUp := false
	defer func() {
		if !cleanedUp {
			agent.decPending(time.Now().UnixMilli())
			_ = s.pendingStore.Delete(s.ctx, clientConnID)
		}
	}()

	// Step 4: Send startTunnel to agent
	vals := url.Values{}
	vals.Set("method", string(methodStartTunnel))
	vals.Set("id", agentID)
	vals.Set("clientConnectionId", clientConnID)
	startTunnelMsg := buildResponseFrame(vals)

	logger.Info("[connectToAgent] Sending startTunnel to agent",
		zap.String("agent_id", agentID),
		zap.String("session_id", sessionID),
		zap.String("client_connection_id", clientConnID),
	)

	if err := agent.safeWriteMessage(websocket.TextMessage, []byte(startTunnelMsg)); err != nil {
		return nil, fmt.Errorf("failed to send startTunnel to agent: %w", err)
	}

	// Step 5: Wait for openTunnel with timeout
	timeout := s.connectTimeout()
	tunnelConn, err := s.pendingStore.WaitForTunnel(ctx, clientConnID, timeout)
	if err != nil {
		if err == context.DeadlineExceeded {
			return nil, fmt.Errorf("timeout waiting for agent '%s' to establish tunnel (waited %v)", agentID, timeout)
		}
		return nil, fmt.Errorf("failed waiting for tunnel: %w", err)
	}

	// Success: mark cleanup as done, transfer ownership
	cleanedUp = true
	nowMillis := time.Now().UnixMilli()
	agent.decPending(nowMillis)
	_ = s.pendingStore.Delete(s.ctx, clientConnID)

	logger.Info("[connectToAgent] Tunnel established successfully",
		zap.String("agent_id", agentID),
		zap.String("session_id", sessionID),
		zap.String("tunnel_remote_addr", tunnelConn.RemoteAddr().String()),
	)

	// Step 6: Wrap in ArthasSession and return
	session := newArthasSession(tunnelConn, agentID, sessionID, logger)
	return session, nil
}
