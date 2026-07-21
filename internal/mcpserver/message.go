// Package mcpserver implements a minimal MCP (Model Context Protocol) server
// over stdio: JSON-RPC 2.0 message types, newline-delimited framing, and a
// single-client dispatch loop with a tool registry.
//
// Design decision: a thin hand-rolled JSON-RPC layer instead of an SDK, in
// line with the project's "manual net/http, no SDK" learning stance
// (docs/TECHNICAL_PLAN.md §5). gitl serves ONE static toolset to ONE stdio
// client (the way Claude Code and other MCP hosts launch local servers), so
// the full generality of an SDK — transports, sessions, proxying — is not
// needed. Params/Result and the message ID are kept as json.RawMessage so
// unknown shapes pass through without loss and IDs are echoed verbatim.
//
// Spec baseline: MCP 2025-06-18.
//   - Base JSON-RPC types: https://modelcontextprotocol.io/specification/2025-06-18/basic
//   - stdio transport:     https://modelcontextprotocol.io/specification/2025-06-18/basic/transports
package mcpserver

import (
	"bytes"
	"encoding/json"
	"fmt"
)

// jsonRPCVersion is the JSON-RPC version string mandated by MCP.
const jsonRPCVersion = "2.0"

// ProtocolVersion is the MCP spec revision this server targets.
//
// 2025-06-18 is the latest stable spec release: it removed JSON-RPC batching
// (which simplifies the codec — every frame is exactly one JSON object) and is
// the version negotiated by current MCP hosts. Version negotiation in
// handleInitialize follows the spec: if the client requests a version we do
// not speak, we answer with this one and the client decides whether to
// proceed or disconnect.
const ProtocolVersion = "2025-06-18"

// Standard JSON-RPC 2.0 error codes this server may emit.
const (
	CodeParseError     = -32700
	CodeInvalidRequest = -32600
	CodeMethodNotFound = -32601
	CodeInvalidParams  = -32602
	CodeInternalError  = -32603
)

// Method names handled by the server (MCP 2025-06-18).
const (
	MethodInitialize = "initialize"
	MethodToolsList  = "tools/list"
	MethodToolsCall  = "tools/call"

	// NotifInitialized is sent by the client after a successful initialize.
	// The server acknowledges it by silence (notifications get no response).
	NotifInitialized = "notifications/initialized"
)

// Message is a single MCP JSON-RPC 2.0 message in its most permissive form.
//
// One struct models request, response, and notification so the codec can
// decode any line without knowing its kind up front:
//
//   - ID present (non-null) + Method present → request
//   - ID present (non-null) + Result/Error   → response
//   - ID absent (null)      + Method present → notification
//
// Per MCP the ID MUST be a string or integer and MUST NOT be null; a
// null/absent ID therefore unambiguously marks a notification. ID, Params,
// Result are json.RawMessage so client IDs are echoed back byte-for-byte and
// unknown shapes pass through without loss.
type Message struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *Error          `json:"error,omitempty"`
}

// Error is a JSON-RPC 2.0 error object. Data is optional/free-form.
type Error struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data,omitempty"`
}

// Error implements the error interface so an *Error can be returned directly.
func (e *Error) Error() string {
	if e == nil {
		return "<nil mcpserver.Error>"
	}
	return fmt.Sprintf("jsonrpc error %d: %s", e.Code, e.Message)
}

// IsNotification reports whether the message carries no ID (or an explicit
// null ID) and therefore expects no response.
func (m *Message) IsNotification() bool {
	return isNullID(m.ID)
}

// IsResponse reports whether the message is a response (has a result or error).
func (m *Message) IsResponse() bool {
	return m.Result != nil || m.Error != nil
}

// IsRequest reports whether the message is a request: it has an ID and a
// method and is not a response.
func (m *Message) IsRequest() bool {
	return !isNullID(m.ID) && m.Method != "" && !m.IsResponse()
}

// isNullID reports whether a raw ID is absent or the JSON literal null. Any
// surrounding whitespace is tolerated.
func isNullID(raw json.RawMessage) bool {
	t := bytes.TrimSpace(raw)
	return len(t) == 0 || bytes.Equal(t, []byte("null"))
}

// newResult builds a success response echoing id and carrying result.
func newResult(id json.RawMessage, result json.RawMessage) *Message {
	return &Message{JSONRPC: jsonRPCVersion, ID: id, Result: result}
}

// newError builds an error response echoing id (null id for parse errors,
// per JSON-RPC 2.0 §5).
func newError(id json.RawMessage, code int, message string) *Message {
	if isNullID(id) {
		id = json.RawMessage("null")
	}
	return &Message{JSONRPC: jsonRPCVersion, ID: id, Error: &Error{Code: code, Message: message}}
}

// Implementation identifies a client or server (clientInfo / serverInfo).
type Implementation struct {
	Name    string `json:"name"`
	Title   string `json:"title,omitempty"`
	Version string `json:"version"`
}

// InitializeParams is the params object of an initialize request.
//
// Capabilities stays RawMessage: gitl offers no client-capability-dependent
// behavior (no sampling, no roots), so client capabilities are accepted but
// not interpreted.
type InitializeParams struct {
	ProtocolVersion string          `json:"protocolVersion"`
	Capabilities    json.RawMessage `json:"capabilities,omitempty"`
	ClientInfo      Implementation  `json:"clientInfo"`
}

// InitializeResult is the result of an initialize response.
type InitializeResult struct {
	ProtocolVersion string             `json:"protocolVersion"`
	Capabilities    ServerCapabilities `json:"capabilities"`
	ServerInfo      Implementation     `json:"serverInfo"`
	Instructions    string             `json:"instructions,omitempty"`
}

// ServerCapabilities advertises what this server can do. gitl exposes only
// tools — no resources, no prompts — so those keys are simply omitted (an
// omitted capability means "not supported" per spec).
type ServerCapabilities struct {
	Tools *ToolsCapability `json:"tools,omitempty"`
}

// ToolsCapability describes the tools capability. ListChanged is always false
// for gitl: the toolset is static for the lifetime of the process, so the
// server never pushes notifications/tools/list_changed.
type ToolsCapability struct {
	ListChanged bool `json:"listChanged"`
}

// Tool is one entry in a tools/list result. InputSchema is RawMessage so the
// registrar supplies the exact JSON Schema advertised to the client.
type Tool struct {
	Name        string          `json:"name"`
	Title       string          `json:"title,omitempty"`
	Description string          `json:"description,omitempty"`
	InputSchema json.RawMessage `json:"inputSchema"`
}

// ToolsListResult is the result of a tools/list response. Tools is always a
// JSON array, never null (callers must pass a non-nil slice).
type ToolsListResult struct {
	Tools []Tool `json:"tools"`
}

// ToolsCallParams is the params object of a tools/call request.
type ToolsCallParams struct {
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments,omitempty"`
}

// ContentBlock is one entry of a tools/call result's content array. gitl tools
// return text (rendered markdown/JSON artifacts), so only the "text" type is
// modeled.
type ContentBlock struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

// TextContent builds a single-text-block content slice — the common case for
// tool results and tool execution errors.
func TextContent(text string) []ContentBlock {
	return []ContentBlock{{Type: "text", Text: text}}
}

// ToolsCallResult is the result of a tools/call response. IsError=true marks
// a TOOL EXECUTION failure (the model should see the error text and may
// retry/adjust); protocol failures (unknown tool, malformed params) are
// JSON-RPC errors instead, per spec.
type ToolsCallResult struct {
	Content []ContentBlock `json:"content"`
	IsError bool           `json:"isError,omitempty"`
}
