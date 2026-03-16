package relay

import (
	"encoding/json"
	"log"
)

// Message types exchanged over the relay WebSocket tunnel.
const (
	TypeAuth               = "auth"
	TypeAuthOK             = "auth_ok"
	TypeClientAuth         = "client_auth"
	TypeClientAuthResult   = "client_auth_result"
	TypeClientReAuth       = "client_reauth"
	TypeClientReAuthResult = "client_reauth_result"

	TypeRequest  = "request"
	TypeResponse = "response"

	TypeStreamOpen   = "stream_open"
	TypeStreamData   = "stream_data"
	TypeStreamInput  = "stream_input"  // base64-encoded stdin bytes
	TypeStreamResize = "stream_resize" // {"rows":N,"cols":N}
	TypeStreamClose  = "stream_close"

	TypePing  = "ping"
	TypePong  = "pong"
	TypeError = "error"

	TypeNotification = "notification"
	TypeRegisterAPNs = "register_apns" // device token from LAN/Tailscale clients bypassing the relay WebSocket
)

type Envelope struct {
	Type      string          `json:"type"`
	RequestID string          `json:"request_id,omitempty"` // correlates request/response and stream frames
	ClientID  string          `json:"client_id,omitempty"`  // relay-assigned iOS client identifier
	Payload   json.RawMessage `json:"payload,omitempty"`
}

type AuthPayload struct {
	AgentID string `json:"agent_id"`
	Token   string `json:"token"`          // relay token (agent) or JWT (client)
	Type    string `json:"type,omitempty"` // "jwt" (default) or "api_key" (re-pair)
}

type ClientAuthPayload struct {
	JWT string `json:"jwt"`
}

type ClientAuthResultPayload struct {
	Accepted   bool   `json:"accepted"`
	DeviceID   string `json:"device_id,omitempty"`
	DeviceName string `json:"device_name,omitempty"`
}

type ClientReAuthPayload struct {
	APIKey     string `json:"api_key"`
	DeviceID   string `json:"device_id"`
	DeviceName string `json:"device_name"`
}

type ClientReAuthResultPayload struct {
	Accepted bool   `json:"accepted"`
	JWT      string `json:"jwt,omitempty"`
}

type RequestPayload struct {
	Method  string            `json:"method"`
	Path    string            `json:"path"`
	Headers map[string]string `json:"headers,omitempty"`
	Body    string            `json:"body,omitempty"`
}

type ResponsePayload struct {
	StatusCode int               `json:"status_code"`
	Headers    map[string]string `json:"headers,omitempty"`
	Body       string            `json:"body,omitempty"`
}

type StreamPayload struct {
	Data string `json:"data"`
}

type ResizePayload struct {
	Rows int `json:"rows"`
	Cols int `json:"cols"`
}

type ErrorPayload struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type NotificationPayload struct {
	ContainerID   string `json:"container_id"`
	ContainerName string `json:"container_name"`
	Action        string `json:"action"` // "start" | "stop" | "die" | "kill"
	AgentName     string `json:"agent_name,omitempty"`
}

type RegisterAPNsPayload struct {
	DeviceID    string `json:"device_id"`
	DeviceToken string `json:"device_token"`
	Environment string `json:"environment"` // "development" | "production"
}

func MustMarshal(v any) json.RawMessage {
	data, err := json.Marshal(v)
	if err != nil {
		log.Printf("[relay] failed to marshal payload: %v", err)
		return json.RawMessage(`{}`)
	}
	return data
}
