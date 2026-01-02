// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package arthastunnelext

import (
	"crypto/rand"
	"encoding/hex"
	"time"
)

// MessageType represents the type of tunnel message.
type MessageType string

const (
	// Server -> Agent (control commands)
	MessageTypeArthasStart   MessageType = "ARTHAS_START"
	MessageTypeArthasStop    MessageType = "ARTHAS_STOP"
	MessageTypeTerminalOpen  MessageType = "TERMINAL_OPEN"
	MessageTypeTerminalInput MessageType = "TERMINAL_INPUT"
	MessageTypeTerminalResize MessageType = "TERMINAL_RESIZE"
	MessageTypeTerminalClose MessageType = "TERMINAL_CLOSE"
	MessageTypePing          MessageType = "PING"

	// Agent -> Server (responses/data)
	MessageTypeRegister         MessageType = "REGISTER"
	MessageTypeRegisterAck      MessageType = "REGISTER_ACK"
	MessageTypeArthasStatus     MessageType = "ARTHAS_STATUS"
	MessageTypeTerminalReady    MessageType = "TERMINAL_READY"
	MessageTypeTerminalRejected MessageType = "TERMINAL_REJECTED"
	MessageTypeTerminalClosed   MessageType = "TERMINAL_CLOSED"
	MessageTypePong             MessageType = "PONG"
	MessageTypeError            MessageType = "ERROR"
)

// TunnelMessage is the base message structure for tunnel communication.
type TunnelMessage struct {
	Type      MessageType `json:"type"`
	ID        string      `json:"id"`
	Timestamp int64       `json:"timestamp"`
}

// NewTunnelMessage creates a new tunnel message with generated ID and current timestamp.
func NewTunnelMessage(msgType MessageType) TunnelMessage {
	return TunnelMessage{
		Type:      msgType,
		ID:        generateMessageID(),
		Timestamp: time.Now().UnixMilli(),
	}
}

// generateMessageID generates a unique message ID.
func generateMessageID() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return "msg-" + hex.EncodeToString(b)
}

// ===== Register Messages =====

// RegisterMessage is sent by agent to register with the tunnel server.
type RegisterMessage struct {
	TunnelMessage
	Payload RegisterPayload `json:"payload"`
}

// RegisterPayload contains agent registration information.
type RegisterPayload struct {
	AgentID     string `json:"agentId"`
	ServiceName string `json:"serviceName,omitempty"`
	Version     string `json:"version,omitempty"`
	Hostname    string `json:"hostname,omitempty"`
	IP          string `json:"ip,omitempty"`
}

// RegisterAckMessage is sent by server to acknowledge registration.
type RegisterAckMessage struct {
	TunnelMessage
	Payload RegisterAckPayload `json:"payload"`
}

// RegisterAckPayload contains registration acknowledgement.
type RegisterAckPayload struct {
	Success bool   `json:"success"`
	Message string `json:"message,omitempty"`
}

// NewRegisterAckMessage creates a new register ack message.
func NewRegisterAckMessage(success bool, message string) *RegisterAckMessage {
	return &RegisterAckMessage{
		TunnelMessage: NewTunnelMessage(MessageTypeRegisterAck),
		Payload: RegisterAckPayload{
			Success: success,
			Message: message,
		},
	}
}

// ===== Arthas Control Messages =====

// ArthasStartMessage is sent by server to start Arthas on agent.
type ArthasStartMessage struct {
	TunnelMessage
	Payload *ArthasStartPayload `json:"payload,omitempty"`
}

// ArthasStartPayload contains Arthas start parameters.
type ArthasStartPayload struct {
	UserID string `json:"userId,omitempty"`
}

// NewArthasStartMessage creates a new Arthas start message.
func NewArthasStartMessage(userID string) *ArthasStartMessage {
	msg := &ArthasStartMessage{
		TunnelMessage: NewTunnelMessage(MessageTypeArthasStart),
	}
	if userID != "" {
		msg.Payload = &ArthasStartPayload{UserID: userID}
	}
	return msg
}

// ArthasStopMessage is sent by server to stop Arthas on agent.
type ArthasStopMessage struct {
	TunnelMessage
	Payload *ArthasStopPayload `json:"payload,omitempty"`
}

// ArthasStopPayload contains Arthas stop parameters.
type ArthasStopPayload struct {
	Reason string `json:"reason,omitempty"`
}

// NewArthasStopMessage creates a new Arthas stop message.
func NewArthasStopMessage(reason string) *ArthasStopMessage {
	msg := &ArthasStopMessage{
		TunnelMessage: NewTunnelMessage(MessageTypeArthasStop),
	}
	if reason != "" {
		msg.Payload = &ArthasStopPayload{Reason: reason}
	}
	return msg
}

// ArthasStatusMessage is sent by agent to report Arthas status.
type ArthasStatusMessage struct {
	TunnelMessage
	Payload ArthasStatusPayload `json:"payload"`
}

// ArthasStatusPayload contains Arthas status information.
type ArthasStatusPayload struct {
	State          string `json:"state"`
	ArthasVersion  string `json:"arthasVersion,omitempty"`
	ActiveSessions int    `json:"activeSessions"`
	MaxSessions    int    `json:"maxSessions"`
	UptimeMs       int64  `json:"uptimeMs"`
}

// ===== Terminal Messages =====

// TerminalOpenMessage is sent by server to open a terminal session.
type TerminalOpenMessage struct {
	TunnelMessage
	Payload TerminalOpenPayload `json:"payload"`
}

// TerminalOpenPayload contains terminal open parameters.
type TerminalOpenPayload struct {
	RequestID string `json:"requestId"`
	UserID    string `json:"userId,omitempty"`
	Cols      int    `json:"cols"`
	Rows      int    `json:"rows"`
}

// NewTerminalOpenMessage creates a new terminal open message.
func NewTerminalOpenMessage(requestID, userID string, cols, rows int) *TerminalOpenMessage {
	return &TerminalOpenMessage{
		TunnelMessage: NewTunnelMessage(MessageTypeTerminalOpen),
		Payload: TerminalOpenPayload{
			RequestID: requestID,
			UserID:    userID,
			Cols:      cols,
			Rows:      rows,
		},
	}
}

// TerminalReadyMessage is sent by agent when terminal is ready.
type TerminalReadyMessage struct {
	TunnelMessage
	Payload TerminalReadyPayload `json:"payload"`
}

// TerminalReadyPayload contains terminal ready information.
type TerminalReadyPayload struct {
	SessionID       string `json:"sessionId"`
	RequestID       string `json:"requestId"`
	ArthasVersion   string `json:"arthasVersion,omitempty"`
	CurrentSessions int    `json:"currentSessions"`
	MaxSessions     int    `json:"maxSessions"`
}

// TerminalRejectedMessage is sent by agent when terminal cannot be opened.
type TerminalRejectedMessage struct {
	TunnelMessage
	Payload TerminalRejectedPayload `json:"payload"`
}

// TerminalRejectedPayload contains rejection information.
type TerminalRejectedPayload struct {
	RequestID string `json:"requestId"`
	Reason    string `json:"reason"`
	Message   string `json:"message,omitempty"`
}

// TerminalInputMessage is sent by server to send input to terminal.
type TerminalInputMessage struct {
	TunnelMessage
	Payload TerminalInputPayload `json:"payload"`
}

// TerminalInputPayload contains terminal input data.
type TerminalInputPayload struct {
	SessionID string `json:"sessionId"`
	Data      string `json:"data"`
}

// NewTerminalInputMessage creates a new terminal input message.
func NewTerminalInputMessage(sessionID, data string) *TerminalInputMessage {
	return &TerminalInputMessage{
		TunnelMessage: NewTunnelMessage(MessageTypeTerminalInput),
		Payload: TerminalInputPayload{
			SessionID: sessionID,
			Data:      data,
		},
	}
}

// TerminalResizeMessage is sent by server to resize terminal.
type TerminalResizeMessage struct {
	TunnelMessage
	Payload TerminalResizePayload `json:"payload"`
}

// TerminalResizePayload contains terminal resize parameters.
type TerminalResizePayload struct {
	SessionID string `json:"sessionId"`
	Cols      int    `json:"cols"`
	Rows      int    `json:"rows"`
}

// NewTerminalResizeMessage creates a new terminal resize message.
func NewTerminalResizeMessage(sessionID string, cols, rows int) *TerminalResizeMessage {
	return &TerminalResizeMessage{
		TunnelMessage: NewTunnelMessage(MessageTypeTerminalResize),
		Payload: TerminalResizePayload{
			SessionID: sessionID,
			Cols:      cols,
			Rows:      rows,
		},
	}
}

// TerminalCloseMessage is sent by server to close terminal.
type TerminalCloseMessage struct {
	TunnelMessage
	Payload TerminalClosePayload `json:"payload"`
}

// TerminalClosePayload contains terminal close parameters.
type TerminalClosePayload struct {
	SessionID string `json:"sessionId"`
}

// NewTerminalCloseMessage creates a new terminal close message.
func NewTerminalCloseMessage(sessionID string) *TerminalCloseMessage {
	return &TerminalCloseMessage{
		TunnelMessage: NewTunnelMessage(MessageTypeTerminalClose),
		Payload: TerminalClosePayload{
			SessionID: sessionID,
		},
	}
}

// TerminalClosedMessage is sent by agent when terminal is closed.
type TerminalClosedMessage struct {
	TunnelMessage
	Payload TerminalClosedPayload `json:"payload"`
}

// TerminalClosedPayload contains terminal closed information.
type TerminalClosedPayload struct {
	SessionID string `json:"sessionId"`
	Reason    string `json:"reason,omitempty"`
}

// ===== Ping/Pong Messages =====

// PingMessage is sent by server to check agent connectivity.
type PingMessage struct {
	TunnelMessage
}

// NewPingMessage creates a new ping message.
func NewPingMessage() *PingMessage {
	return &PingMessage{
		TunnelMessage: NewTunnelMessage(MessageTypePing),
	}
}

// PongMessage is sent by agent in response to ping.
type PongMessage struct {
	TunnelMessage
}

// ===== Error Message =====

// ErrorMessage is sent to report errors.
type ErrorMessage struct {
	TunnelMessage
	Payload ErrorPayload `json:"payload"`
}

// ErrorPayload contains error information.
type ErrorPayload struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Details string `json:"details,omitempty"`
}

// NewErrorMessage creates a new error message.
func NewErrorMessage(code, message, details string) *ErrorMessage {
	return &ErrorMessage{
		TunnelMessage: NewTunnelMessage(MessageTypeError),
		Payload: ErrorPayload{
			Code:    code,
			Message: message,
			Details: details,
		},
	}
}

// ===== Browser Messages =====

// BrowserMessageType represents browser-side message types.
type BrowserMessageType string

const (
	BrowserMessageTypeOpenTerminal  BrowserMessageType = "OPEN_TERMINAL"
	BrowserMessageTypeInput         BrowserMessageType = "INPUT"
	BrowserMessageTypeResize        BrowserMessageType = "RESIZE"
	BrowserMessageTypeCloseTerminal BrowserMessageType = "CLOSE_TERMINAL"

	BrowserMessageTypeTerminalReady  BrowserMessageType = "TERMINAL_READY"
	BrowserMessageTypeTerminalOutput BrowserMessageType = "TERMINAL_OUTPUT"
	BrowserMessageTypeTerminalClosed BrowserMessageType = "TERMINAL_CLOSED"
	BrowserMessageTypeError          BrowserMessageType = "ERROR"
)

// BrowserMessage is the base message structure for browser communication.
type BrowserMessage struct {
	Type    BrowserMessageType `json:"type"`
	Payload any                `json:"payload,omitempty"`
}

// BrowserOpenTerminalPayload contains parameters for opening a terminal.
type BrowserOpenTerminalPayload struct {
	AgentID string `json:"agentId"`
	Cols    int    `json:"cols"`
	Rows    int    `json:"rows"`
}

// BrowserInputPayload contains terminal input from browser.
type BrowserInputPayload struct {
	SessionID string `json:"sessionId"`
	Data      string `json:"data"`
}

// BrowserResizePayload contains terminal resize from browser.
type BrowserResizePayload struct {
	SessionID string `json:"sessionId"`
	Cols      int    `json:"cols"`
	Rows      int    `json:"rows"`
}

// BrowserCloseTerminalPayload contains parameters for closing a terminal.
type BrowserCloseTerminalPayload struct {
	SessionID string `json:"sessionId"`
}

// BrowserTerminalReadyPayload is sent to browser when terminal is ready.
type BrowserTerminalReadyPayload struct {
	SessionID string `json:"sessionId"`
	AgentID   string `json:"agentId"`
}

// BrowserTerminalOutputPayload is sent to browser with terminal output.
type BrowserTerminalOutputPayload struct {
	SessionID string `json:"sessionId"`
	Data      string `json:"data"` // base64 encoded
}

// BrowserTerminalClosedPayload is sent to browser when terminal is closed.
type BrowserTerminalClosedPayload struct {
	SessionID string `json:"sessionId"`
	Reason    string `json:"reason,omitempty"`
}

// BrowserErrorPayload is sent to browser on error.
type BrowserErrorPayload struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}
