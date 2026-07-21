package mcpserver

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"testing"
	"time"
)

// testConn runs a real Server.Serve over io.Pipe pairs and lets a test act as
// the MCP client: write framed requests, read framed responses.
type testConn struct {
	t      *testing.T
	in     *io.PipeWriter // client → server
	out    *Reader        // server → client, decoded with the same codec
	cancel context.CancelFunc
	done   chan error

	waitOnce sync.Once
	waitErr  error
}

// startServer launches s.Serve in a goroutine and returns the client side.
func startServer(t *testing.T, s *Server) *testConn {
	t.Helper()
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan error, 1)
	go func() {
		err := s.Serve(ctx, inR, outW)
		outW.Close() // unblock a client stuck in recv after shutdown
		done <- err
	}()

	c := &testConn{t: t, in: inW, out: NewReader(outR), cancel: cancel, done: done}
	t.Cleanup(func() {
		cancel()
		inW.Close()
		c.wait()
	})
	return c
}

// send writes one raw line to the server.
func (c *testConn) send(line string) {
	c.t.Helper()
	if _, err := io.WriteString(c.in, line+"\n"); err != nil {
		c.t.Fatalf("send: %v", err)
	}
}

// recv reads the next response, failing the test after a timeout instead of
// hanging forever on a server that wrongly stays silent.
func (c *testConn) recv() *Message {
	c.t.Helper()
	type res struct {
		m   *Message
		err error
	}
	ch := make(chan res, 1)
	go func() {
		m, err := c.out.Read()
		ch <- res{m, err}
	}()
	select {
	case r := <-ch:
		if r.err != nil {
			c.t.Fatalf("recv: %v", r.err)
		}
		return r.m
	case <-time.After(5 * time.Second):
		c.t.Fatal("recv: timed out waiting for server response")
		return nil
	}
}

// wait blocks until Serve returns (at most once; later calls — including the
// Cleanup one — reuse the recorded result) and hands back its error.
func (c *testConn) wait() error {
	c.t.Helper()
	c.waitOnce.Do(func() {
		select {
		case c.waitErr = <-c.done:
		case <-time.After(5 * time.Second):
			c.t.Fatal("Serve did not return")
		}
	})
	return c.waitErr
}

func TestServeInitialize(t *testing.T) {
	c := startServer(t, New("gitl", "0.4.0"))
	c.send(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"test-client","version":"1.0"}}}`)

	m := c.recv()
	if m.Error != nil {
		t.Fatalf("initialize returned error: %v", m.Error)
	}
	if string(m.ID) != "1" {
		t.Fatalf("ID=%s want 1", m.ID)
	}

	var res InitializeResult
	if err := json.Unmarshal(m.Result, &res); err != nil {
		t.Fatalf("unmarshal result: %v", err)
	}
	if res.ProtocolVersion != ProtocolVersion {
		t.Errorf("protocolVersion=%q want %q", res.ProtocolVersion, ProtocolVersion)
	}
	if res.ServerInfo.Name != "gitl" || res.ServerInfo.Version != "0.4.0" {
		t.Errorf("serverInfo=%+v", res.ServerInfo)
	}
	if res.Capabilities.Tools == nil || res.Capabilities.Tools.ListChanged {
		t.Errorf("capabilities.tools=%+v want listChanged:false", res.Capabilities.Tools)
	}

	// The raw result must advertise ONLY tools — no resources/prompts keys.
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(m.Result, &raw); err != nil {
		t.Fatalf("unmarshal raw result: %v", err)
	}
	var caps map[string]json.RawMessage
	if err := json.Unmarshal(raw["capabilities"], &caps); err != nil {
		t.Fatalf("unmarshal capabilities: %v", err)
	}
	for _, forbidden := range []string{"resources", "prompts"} {
		if _, ok := caps[forbidden]; ok {
			t.Errorf("capabilities must not advertise %q", forbidden)
		}
	}
}

func TestServeInitializeVersionMismatchAnswersServerVersion(t *testing.T) {
	// Per spec, a server that does not support the requested version responds
	// with the one it does support; the client then decides.
	c := startServer(t, New("gitl", "0.4.0"))
	c.send(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05","clientInfo":{"name":"old","version":"0"}}}`)

	m := c.recv()
	var res InitializeResult
	if err := json.Unmarshal(m.Result, &res); err != nil {
		t.Fatalf("unmarshal result: %v", err)
	}
	if res.ProtocolVersion != ProtocolVersion {
		t.Fatalf("protocolVersion=%q want %q", res.ProtocolVersion, ProtocolVersion)
	}
}

func TestServeToolsListEmpty(t *testing.T) {
	c := startServer(t, New("gitl", "0.4.0"))
	c.send(`{"jsonrpc":"2.0","id":5,"method":"tools/list"}`)

	m := c.recv()
	if m.Error != nil {
		t.Fatalf("tools/list returned error: %v", m.Error)
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(m.Result, &raw); err != nil {
		t.Fatalf("unmarshal result: %v", err)
	}
	// Empty registry must render as "tools":[] — an array, never null.
	if got := strings.TrimSpace(string(raw["tools"])); got != "[]" {
		t.Fatalf("tools=%s want []", got)
	}
}

func TestServeToolsCallUnknownTool(t *testing.T) {
	c := startServer(t, New("gitl", "0.4.0"))
	c.send(`{"jsonrpc":"2.0","id":6,"method":"tools/call","params":{"name":"nope","arguments":{}}}`)

	m := c.recv()
	if m.Error == nil {
		t.Fatalf("expected error, got result %s", m.Result)
	}
	if m.Error.Code != CodeInvalidParams {
		t.Errorf("code=%d want %d", m.Error.Code, CodeInvalidParams)
	}
	if !strings.Contains(m.Error.Message, "unknown tool: nope") {
		t.Errorf("message=%q", m.Error.Message)
	}
}

func TestServeUnknownMethod(t *testing.T) {
	c := startServer(t, New("gitl", "0.4.0"))
	c.send(`{"jsonrpc":"2.0","id":7,"method":"resources/list"}`)

	m := c.recv()
	if m.Error == nil || m.Error.Code != CodeMethodNotFound {
		t.Fatalf("expected -32601, got %+v", m.Error)
	}
	if string(m.ID) != "7" {
		t.Errorf("ID=%s want 7", m.ID)
	}
}

func TestServeParseErrorThenResync(t *testing.T) {
	c := startServer(t, New("gitl", "0.4.0"))
	c.send(`{"jsonrpc":"2.0",garbage`)
	c.send(`{"jsonrpc":"2.0","id":8,"method":"tools/list"}`)

	// First response: Parse error with a null id (JSON-RPC 2.0 §5).
	m := c.recv()
	if m.Error == nil || m.Error.Code != CodeParseError {
		t.Fatalf("expected -32700 for garbage line, got %+v", m.Error)
	}
	if string(m.ID) != "null" {
		t.Errorf("parse error ID=%s want null", m.ID)
	}

	// Second: the server resynchronized and serves the next request normally.
	m = c.recv()
	if m.Error != nil {
		t.Fatalf("post-resync request failed: %v", m.Error)
	}
	if string(m.ID) != "8" {
		t.Errorf("ID=%s want 8", m.ID)
	}
}

func TestServeNotificationGetsNoResponse(t *testing.T) {
	c := startServer(t, New("gitl", "0.4.0"))
	c.send(`{"jsonrpc":"2.0","method":"notifications/initialized"}`)
	c.send(`{"jsonrpc":"2.0","id":9,"method":"tools/list"}`)

	// The first (and only) response must belong to the request, proving the
	// notification was acknowledged by silence.
	m := c.recv()
	if string(m.ID) != "9" {
		t.Fatalf("first response ID=%s want 9 (notification must not be answered)", m.ID)
	}
}

func TestServeContextCancelStopsServe(t *testing.T) {
	c := startServer(t, New("gitl", "0.4.0"))
	// Handshake proves the loop is alive before cancelling.
	c.send(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","clientInfo":{"name":"t","version":"0"}}}`)
	c.recv()

	c.cancel() // SIGTERM analogue: the input pipe stays open, only ctx fires
	if err := c.wait(); err != nil {
		t.Fatalf("Serve returned %v on ctx cancel, want nil", err)
	}
}

func TestServeClientEOFStopsServe(t *testing.T) {
	c := startServer(t, New("gitl", "0.4.0"))
	c.in.Close() // client hangs up
	if err := c.wait(); err != nil {
		t.Fatalf("Serve returned %v on EOF, want nil", err)
	}
}

func TestServeFatalStreamError(t *testing.T) {
	// An input stream that fails with a real I/O error (not EOF) is fatal:
	// Serve returns an ErrStream-wrapped error instead of spinning.
	s := New("gitl", "0.4.0")
	var out strings.Builder
	err := s.Serve(context.Background(), failingReader{err: errors.New("boom")}, &out)
	if !errors.Is(err, ErrStream) {
		t.Fatalf("Serve=%v want ErrStream", err)
	}
}

func TestRegisterToolListAndCall(t *testing.T) {
	// The registration MECHANISM under test with a toy tool; the real gitl
	// tools (gitl_review / gitl_digest) arrive in the next PR.
	s := New("gitl", "0.4.0")
	schema := json.RawMessage(`{"type":"object","properties":{"msg":{"type":"string"}}}`)
	s.RegisterTool(Tool{Name: "echo", Description: "echo msg back", InputSchema: schema},
		func(_ context.Context, args json.RawMessage) (*ToolsCallResult, error) {
			var p struct {
				Msg string `json:"msg"`
			}
			if err := json.Unmarshal(args, &p); err != nil {
				return nil, err
			}
			return &ToolsCallResult{Content: TextContent(p.Msg)}, nil
		})
	s.RegisterTool(Tool{Name: "boom", InputSchema: json.RawMessage(`{}`)},
		func(context.Context, json.RawMessage) (*ToolsCallResult, error) {
			return nil, fmt.Errorf("kaboom: tool failed")
		})

	c := startServer(t, s)

	// tools/list advertises both, in registration order, with the schema.
	c.send(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`)
	var list ToolsListResult
	if err := json.Unmarshal(c.recv().Result, &list); err != nil {
		t.Fatalf("unmarshal tools/list: %v", err)
	}
	if len(list.Tools) != 2 || list.Tools[0].Name != "echo" || list.Tools[1].Name != "boom" {
		t.Fatalf("tools=%+v", list.Tools)
	}
	if string(list.Tools[0].InputSchema) != string(schema) {
		t.Errorf("inputSchema not advertised verbatim: %s", list.Tools[0].InputSchema)
	}

	// tools/call routes to the handler and returns its content.
	c.send(`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"echo","arguments":{"msg":"hi"}}}`)
	var res ToolsCallResult
	if err := json.Unmarshal(c.recv().Result, &res); err != nil {
		t.Fatalf("unmarshal tools/call: %v", err)
	}
	if res.IsError || len(res.Content) != 1 || res.Content[0].Text != "hi" {
		t.Fatalf("result=%+v", res)
	}

	// A handler error becomes isError:true (tool execution failure, not a
	// JSON-RPC error).
	c.send(`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"boom","arguments":{}}}`)
	m := c.recv()
	if m.Error != nil {
		t.Fatalf("handler error must not be a protocol error: %v", m.Error)
	}
	if err := json.Unmarshal(m.Result, &res); err != nil {
		t.Fatalf("unmarshal tools/call: %v", err)
	}
	if !res.IsError || len(res.Content) != 1 || !strings.Contains(res.Content[0].Text, "kaboom") {
		t.Fatalf("result=%+v want isError with kaboom text", res)
	}
}

func TestRegisterToolPanicsOnMisuse(t *testing.T) {
	noop := func(context.Context, json.RawMessage) (*ToolsCallResult, error) {
		return &ToolsCallResult{}, nil
	}
	tests := []struct {
		name string
		do   func(s *Server)
	}{
		{"empty name", func(s *Server) { s.RegisterTool(Tool{}, noop) }},
		{"nil handler", func(s *Server) { s.RegisterTool(Tool{Name: "x"}, nil) }},
		{"duplicate", func(s *Server) {
			s.RegisterTool(Tool{Name: "x"}, noop)
			s.RegisterTool(Tool{Name: "x"}, noop)
		}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			defer func() {
				if recover() == nil {
					t.Fatal("expected panic")
				}
			}()
			tc.do(New("gitl", "0.4.0"))
		})
	}
}

func TestServeToolsCallMissingName(t *testing.T) {
	c := startServer(t, New("gitl", "0.4.0"))
	c.send(`{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"arguments":{}}}`)
	m := c.recv()
	if m.Error == nil || m.Error.Code != CodeInvalidParams {
		t.Fatalf("expected -32602 for missing tool name, got %+v", m.Error)
	}
}

func TestServeToolHandlerPanicIsRecovered(t *testing.T) {
	// A real handler (RunReviewCore/RunDigestCore in a future PR) shells out
	// to git and parses LLM output — a nil deref or out-of-range slice there
	// must surface as a tool error, not crash the whole MCP process.
	s := New("gitl", "0.4.0")
	s.RegisterTool(Tool{Name: "boom", InputSchema: json.RawMessage(`{}`)},
		func(context.Context, json.RawMessage) (*ToolsCallResult, error) {
			panic("boom")
		})

	c := startServer(t, s)
	c.send(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"boom"}}`)
	m := c.recv()
	var res ToolsCallResult
	if err := json.Unmarshal(m.Result, &res); err != nil {
		t.Fatalf("unmarshal tools/call: %v", err)
	}
	if !res.IsError {
		t.Fatalf("result=%+v want isError after handler panic", res)
	}

	// Serve must still be alive: a second, well-behaved call on the same
	// connection should succeed normally.
	s.RegisterTool(Tool{Name: "ok", InputSchema: json.RawMessage(`{}`)},
		func(context.Context, json.RawMessage) (*ToolsCallResult, error) {
			return &ToolsCallResult{Content: TextContent("fine")}, nil
		})
	c.send(`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"ok"}}`)
	m2 := c.recv()
	var res2 ToolsCallResult
	if err := json.Unmarshal(m2.Result, &res2); err != nil {
		t.Fatalf("unmarshal second tools/call: %v", err)
	}
	if res2.IsError {
		t.Fatalf("second call result=%+v, want success after prior panic was recovered", res2)
	}
}

func TestServeToolHandlerObservesCtxCancel(t *testing.T) {
	// A cancelled Serve ctx reaches an in-flight handler, so long tool runs
	// (a real review) abort on SIGTERM instead of outliving the server loop.
	started := make(chan struct{})
	finished := make(chan error, 1)
	s := New("gitl", "0.4.0")
	s.RegisterTool(Tool{Name: "slow", InputSchema: json.RawMessage(`{}`)},
		func(ctx context.Context, _ json.RawMessage) (*ToolsCallResult, error) {
			close(started)
			<-ctx.Done()
			finished <- ctx.Err()
			return nil, ctx.Err()
		})

	c := startServer(t, s)
	c.send(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"slow"}}`)

	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("handler never started")
	}
	c.cancel()
	select {
	case err := <-finished:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("handler ctx err=%v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("handler did not observe ctx cancel")
	}
	// Drain the isError reply so Serve's pending pipe write completes and the
	// loop reaches its ctx.Done() case.
	m := c.recv()
	var res ToolsCallResult
	if err := json.Unmarshal(m.Result, &res); err != nil {
		t.Fatalf("unmarshal tools/call: %v", err)
	}
	if !res.IsError {
		t.Fatalf("result=%+v want isError for cancelled tool", res)
	}
	if err := c.wait(); err != nil {
		t.Fatalf("Serve returned %v, want nil", err)
	}
}
