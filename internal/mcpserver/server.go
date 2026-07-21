package mcpserver

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
)

// ToolHandler executes one tool call. args is the raw "arguments" object from
// tools/call (may be nil/empty when the client sent none); the handler parses
// it against its own schema.
//
// Error contract (MCP 2025-06-18): a returned error is a TOOL EXECUTION
// failure — Serve converts it into a ToolsCallResult with isError=true so the
// model sees the error text and can adjust. Protocol-level failures (unknown
// tool, malformed params) never reach a handler; Serve answers those with
// JSON-RPC errors itself.
//
// The ctx passed to the handler is the Serve ctx: cancelling it (Ctrl-C /
// SIGTERM) cancels an in-flight tool run.
type ToolHandler func(ctx context.Context, args json.RawMessage) (*ToolsCallResult, error)

// Server is a single-client MCP stdio server with a static tool registry.
//
// Lifecycle: construct with New, register the toolset with RegisterTool, then
// call Serve exactly once. The registry is immutable after Serve starts
// (capabilities advertise listChanged:false), which is why RegisterTool needs
// no locking.
//
// Dispatch is deliberately sequential: MCP stdio is one pipe with one client,
// and gitl's tools (review/digest) are heavyweight one-at-a-time operations,
// so there is no per-request fan-out.
type Server struct {
	info     Implementation
	tools    []Tool // advertised in registration order
	handlers map[string]ToolHandler
}

// New builds a Server that identifies itself as name/version in the
// initialize handshake. No tools are registered yet.
func New(name, version string) *Server {
	return &Server{
		info:     Implementation{Name: name, Version: version},
		handlers: make(map[string]ToolHandler),
	}
}

// RegisterTool adds one tool to the registry. The tool is advertised by
// tools/list exactly as given (registration order preserved) and handler is
// invoked for tools/call with a matching name.
//
// Must be called before Serve; it is not safe to call concurrently with an
// active Serve. Like http.ServeMux.Handle, it panics on an empty name, a nil
// handler, or a duplicate registration — all are programming errors in the
// static wiring, not runtime conditions.
func (s *Server) RegisterTool(t Tool, handler ToolHandler) {
	if t.Name == "" {
		panic("mcpserver: RegisterTool with empty tool name")
	}
	if handler == nil {
		panic("mcpserver: RegisterTool with nil handler for " + t.Name)
	}
	if _, dup := s.handlers[t.Name]; dup {
		panic("mcpserver: duplicate tool registration: " + t.Name)
	}
	s.tools = append(s.tools, t)
	s.handlers[t.Name] = handler
}

// readResult carries one decoded frame or a read error from the reader
// goroutine to Serve's dispatch loop.
type readResult struct {
	msg *Message
	err error
}

// Serve reads newline-framed JSON-RPC messages from in and writes responses
// to out (os.Stdin/os.Stdout in production; pipes in tests) until the client
// closes the stream (EOF), the stream fails fatally, or ctx is cancelled.
//
// Cancellation: Reader.Read blocks and is not context-aware, so it runs in
// its own goroutine feeding a channel; Serve selects on ctx.Done() and
// returns nil promptly on Ctrl-C / SIGTERM instead of blocking on a client
// that has gone quiet without closing the pipe. The channel buffer of 1
// matters: if Serve has already returned and the reader was mid-Read, it can
// still deposit that one in-flight frame without blocking — an unbuffered
// channel would leak the goroutine in that race. The reader then blocks on
// the next Read and is reaped when the process exits.
func (s *Server) Serve(ctx context.Context, in io.Reader, out io.Writer) error {
	r := NewReader(in)
	w := NewWriter(out)

	frames := make(chan readResult, 1)
	go readFrames(r, frames)

	for {
		select {
		case <-ctx.Done():
			slog.Debug("mcp server: shutting down")
			return nil
		case fr, ok := <-frames:
			if !ok {
				slog.Debug("mcp server: client disconnected")
				return nil
			}
			if fr.err != nil {
				if errors.Is(fr.err, ErrStream) {
					// The scanner cannot recover (oversized frame or I/O
					// error) — the connection is effectively dead.
					return fr.err
				}
				// A single malformed line is not fatal: line boundaries are
				// intact, so answer with a Parse error (null id, JSON-RPC 2.0
				// §5) and resynchronize on the next line.
				slog.Warn("mcp server: malformed client message", "err", fr.err)
				if werr := w.Write(newError(nil, CodeParseError, fr.err.Error())); werr != nil {
					slog.Warn("mcp server: write failed", "err", werr)
					return nil
				}
				continue
			}
			reply := s.dispatch(ctx, fr.msg)
			if reply == nil {
				continue // notification or ignored message: nothing to write
			}
			if err := w.Write(reply); err != nil {
				// A write failure means the client pipe is gone — stop serving.
				slog.Warn("mcp server: write failed", "err", err)
				return nil
			}
		}
	}
}

// readFrames reads framed client messages and pushes them onto frames until
// the stream ends, then closes the channel. Per-line decode errors are
// forwarded (non-fatal, the stream stays framed); EOF closes the channel;
// a fatal stream error (ErrStream) is forwarded and then closes the channel,
// because every subsequent Read would fail identically.
func readFrames(r *Reader, frames chan<- readResult) {
	defer close(frames)
	for {
		msg, err := r.Read()
		if err != nil {
			if errors.Is(err, io.EOF) {
				return
			}
			frames <- readResult{err: err}
			if errors.Is(err, ErrStream) {
				return
			}
			continue
		}
		frames <- readResult{msg: msg}
	}
}

// dispatch routes one decoded client message. It returns the response to
// write, or nil when the message expects none (notifications, and responses —
// this server never sends client-bound requests, so any response is stray and
// ignored).
func (s *Server) dispatch(ctx context.Context, m *Message) *Message {
	switch {
	case m.IsResponse():
		return nil
	case m.IsNotification():
		// notifications/initialized and anything else: acknowledged by
		// silence, per JSON-RPC (notifications MUST NOT be answered).
		return nil
	case m.IsRequest():
		return s.dispatchRequest(ctx, m)
	default:
		// Has an id but neither method nor result/error.
		return newError(m.ID, CodeInvalidRequest, "invalid request")
	}
}

// dispatchRequest routes one request by method.
func (s *Server) dispatchRequest(ctx context.Context, m *Message) *Message {
	slog.Debug("mcp server: request", "method", m.Method)
	switch m.Method {
	case MethodInitialize:
		return s.handleInitialize(m)
	case MethodToolsList:
		return s.handleToolsList(m)
	case MethodToolsCall:
		return s.handleToolsCall(ctx, m)
	default:
		return newError(m.ID, CodeMethodNotFound, fmt.Sprintf("method not found: %s", m.Method))
	}
}

// handleInitialize answers the MCP handshake. Version negotiation per spec:
// this server speaks exactly one protocol revision, so it always responds
// with ProtocolVersion — if that differs from what the client requested, the
// client decides whether to proceed or disconnect.
func (s *Server) handleInitialize(m *Message) *Message {
	var params InitializeParams
	if len(m.Params) > 0 {
		if err := json.Unmarshal(m.Params, &params); err != nil {
			return newError(m.ID, CodeInvalidParams, "invalid initialize params: "+err.Error())
		}
	}
	if params.ProtocolVersion != "" && params.ProtocolVersion != ProtocolVersion {
		slog.Debug("mcp server: protocol version mismatch", "client", params.ProtocolVersion, "server", ProtocolVersion)
	}
	return s.resultMessage(m.ID, InitializeResult{
		ProtocolVersion: ProtocolVersion,
		Capabilities: ServerCapabilities{
			// Tools only; resources/prompts are omitted (= unsupported).
			// listChanged:false — the toolset is static for the process
			// lifetime, the server never pushes list_changed.
			Tools: &ToolsCapability{ListChanged: false},
		},
		ServerInfo: s.info,
	})
}

// handleToolsList advertises the registered toolset. No pagination: the
// toolset is small and static, so the cursor protocol is not implemented.
func (s *Server) handleToolsList(m *Message) *Message {
	tools := s.tools
	if tools == nil {
		tools = []Tool{} // "tools":[] — never null
	}
	return s.resultMessage(m.ID, ToolsListResult{Tools: tools})
}

// handleToolsCall parses the call params, looks up the handler, and converts
// its outcome per the MCP error contract (see ToolHandler).
func (s *Server) handleToolsCall(ctx context.Context, m *Message) *Message {
	var params ToolsCallParams
	if err := json.Unmarshal(m.Params, &params); err != nil {
		return newError(m.ID, CodeInvalidParams, "invalid tools/call params: "+err.Error())
	}
	if params.Name == "" {
		return newError(m.ID, CodeInvalidParams, "tools/call: missing tool name")
	}
	handler, ok := s.handlers[params.Name]
	if !ok {
		return newError(m.ID, CodeInvalidParams, "unknown tool: "+params.Name)
	}

	res, err := s.callHandler(ctx, handler, params.Arguments)
	if err != nil {
		// Tool execution failure: surfaced to the model, not as a protocol
		// error. Never include secrets in error text — handlers own that, but
		// the transport at least never logs arguments.
		res = &ToolsCallResult{Content: TextContent(err.Error()), IsError: true}
	}
	if res == nil {
		res = &ToolsCallResult{}
	}
	if res.Content == nil {
		res.Content = []ContentBlock{} // "content":[] — never null
	}
	return s.resultMessage(m.ID, *res)
}

// callHandler runs handler and recovers a panic into a tool-execution error
// instead of crashing Serve. Real handlers (a future PR wraps RunReviewCore/
// RunDigestCore, which shell out to git and parse LLM output) can panic on a
// nil deref or out-of-range slice; per the ToolHandler contract that must
// surface to the caller as an error, not take down the whole MCP process.
func (s *Server) callHandler(ctx context.Context, handler ToolHandler, args json.RawMessage) (res *ToolsCallResult, err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("tool handler panicked: %v", r)
		}
	}()
	return handler(ctx, args)
}

// resultMessage marshals v as the result of a success response. A marshal
// failure over our own plain structs is a programming error, but it degrades
// to a JSON-RPC internal error instead of a panic to keep the server alive.
func (s *Server) resultMessage(id json.RawMessage, v any) *Message {
	b, err := json.Marshal(v)
	if err != nil {
		slog.Warn("mcp server: marshal result", "err", err)
		return newError(id, CodeInternalError, "internal error: marshal result")
	}
	return newResult(id, b)
}
