package relay

import "encoding/json"

// Message types exchanged over the relay WebSocket tunnel.
const (
	// Authentication
	TypeAuth             = "auth"               // Agent/Client → Relay: authenticate
	TypeAuthOK           = "auth_ok"             // Relay → Agent/Client: auth succeeded
	TypeClientAuth       = "client_auth"         // Relay → Agent: verify this iOS client's JWT
	TypeClientAuthResult = "client_auth_result"  // Agent → Relay: accept/reject client
	TypeClientReAuth     = "client_reauth"       // Relay → Agent: re-pair request (API key auth, expired JWT)
	TypeClientReAuthResult = "client_reauth_result" // Agent → Relay: new JWT for re-paired client

	// Request/Response (REST-style over tunnel)
	TypeRequest  = "request"  // Client → Agent (via Relay): HTTP request
	TypeResponse = "response" // Agent → Client (via Relay): HTTP response

	// Streaming (logs/stats/exec over tunnel)
	TypeStreamOpen   = "stream_open"   // Client → Agent: open a stream
	TypeStreamData   = "stream_data"   // Agent → Client: one frame of output
	TypeStreamInput  = "stream_input"  // Client → Agent: stdin bytes (exec only, base64)
	TypeStreamResize = "stream_resize" // Client → Agent: PTY resize {"rows":N,"cols":N}
	TypeStreamClose  = "stream_close"  // Either side: close a stream

	// Heartbeat
	TypePing  = "ping"
	TypePong  = "pong"

	// Error
	TypeError = "error"

	// Push notifications — agent sends container events to relay for APNs dispatch,
	// and forwards device tokens registered by LAN/Tailscale iOS clients.
	TypeNotification = "notification"
	TypeRegisterAPNs = "register_apns"
)

// Envelope is the top-level message on every WebSocket frame through the relay.
type Envelope struct {
	Type      string          `json:"type"`
	RequestID string          `json:"request_id,omitempty"` // Correlates request/response pairs and stream frames
	ClientID  string          `json:"client_id,omitempty"`  // Identifies which iOS client (assigned by relay)
	Payload   json.RawMessage `json:"payload,omitempty"`
}

// AuthPayload is sent by agents and clients to authenticate with the relay.
type AuthPayload struct {
	AgentID string `json:"agent_id"`
	Token   string `json:"token"`             // Relay token (agent) or JWT (client)
	Type    string `json:"type,omitempty"`     // "jwt" (default) or "api_key" (re-pair)
}

// ClientAuthPayload is forwarded from relay to agent for local JWT validation.
type ClientAuthPayload struct {
	JWT string `json:"jwt"`
}

// ClientAuthResultPayload is the agent's response to a client auth challenge.
type ClientAuthResultPayload struct {
	Accepted   bool   `json:"accepted"`
	DeviceID   string `json:"device_id,omitempty"`
	DeviceName string `json:"device_name,omitempty"`
}

// ClientReAuthPayload is forwarded from relay to agent for API key re-pair.
type ClientReAuthPayload struct {
	APIKey     string `json:"api_key"`
	DeviceID   string `json:"device_id"`
	DeviceName string `json:"device_name"`
}

// ClientReAuthResultPayload is the agent's response with a fresh JWT.
type ClientReAuthResultPayload struct {
	Accepted bool   `json:"accepted"`
	JWT      string `json:"jwt,omitempty"` // Fresh JWT if accepted
}

// RequestPayload represents an HTTP request forwarded through the relay.
type RequestPayload struct {
	Method  string            `json:"method"`
	Path    string            `json:"path"`
	Headers map[string]string `json:"headers,omitempty"`
	Body    string            `json:"body,omitempty"`
}

// ResponsePayload represents an HTTP response sent back through the relay.
type ResponsePayload struct {
	StatusCode int               `json:"status_code"`
	Headers    map[string]string `json:"headers,omitempty"`
	Body       string            `json:"body,omitempty"`
}

// StreamPayload carries one frame of streaming data (a log line or stats snapshot).
type StreamPayload struct {
	Data string `json:"data"`
}

// ResizePayload carries a PTY resize request.
type ResizePayload struct {
	Rows int `json:"rows"`
	Cols int `json:"cols"`
}

// ErrorPayload carries error details.
type ErrorPayload struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// NotificationPayload is sent by the agent to the relay when a container event occurs.
// The relay dispatches APNs pushes to all registered devices for this agent.
type NotificationPayload struct {
	ContainerID   string `json:"container_id"`
	ContainerName string `json:"container_name"`
	Action        string `json:"action"` // "start" | "stop" | "die" | "kill"
	AgentName     string `json:"agent_name,omitempty"`
}

// RegisterAPNsPayload is sent by the agent to forward a device token that was
// registered by a LAN or Tailscale iOS client via the agent's HTTP API.
type RegisterAPNsPayload struct {
	DeviceID    string `json:"device_id"`
	DeviceToken string `json:"device_token"`
	Environment string `json:"environment"` // "development" | "production"
}

// MustMarshal marshals v to JSON, panicking on error (for internal use only).
func MustMarshal(v interface{}) json.RawMessage {
	data, err := json.Marshal(v)
	if err != nil {
		panic("relay: failed to marshal payload: " + err.Error())
	}
	return data
}
