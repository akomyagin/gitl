package cli

// True end-to-end tests for `gitl mcp`: the real compiled binary is launched
// as a child process and driven over its actual stdin/stdout, exactly the way
// an MCP host (Claude Code etc.) launches a local stdio server.
//
// What this file covers that mcp_test.go (handlers in isolation) and
// mcpserver tests (transport in isolation) cannot:
//   - the cobra command really wires os.Stdin/os.Stdout to Serve;
//   - stdout carries ONLY newline-framed JSON-RPC (every line the client reads
//     is parsed — a stray human-readable byte on stdout fails the test);
//   - warnings (the offline-mode notice) land on stderr, not the protocol
//     channel;
//   - config loading, tool registration, dispatch, handler execution and JSON
//     rendering all work together across a process boundary;
//   - the process exits cleanly (code 0) when the client closes stdin.
//
// The binary is built once per `go test` run (lazily, so unrelated -run
// filters skip the build) and removed in TestMain. Response decoding uses
// test-local wire structs, deliberately NOT the mcpserver types: if a
// server-side JSON tag drifts from the MCP wire format, these tests fail
// while type-sharing tests would not.
//
// Isolation: the server child process gets a synthetic environment — temp
// HOME/XDG dirs, no GITL_* variables at all — so no personal config or API
// key can ever leak in and every run is offline and deterministic.

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

// e2eTimeout bounds every read/wait on the child process so a server that
// wrongly goes silent fails the test instead of hanging it forever.
const e2eTimeout = 30 * time.Second

// offlineNotice is the stderr warning runReview prints when no API key is
// configured (review.go). The e2e asserts it reaches stderr — and, because
// every stdout line is JSON-parsed, that it never leaks into the protocol.
const offlineNotice = "no LLM API key configured"

// ---------------------------------------------------------------------------
// Binary build (once per test process, removed in TestMain)

var (
	gitlBinOnce sync.Once
	gitlBinDir  string // temp dir holding the built binary; removed in TestMain
	gitlBinPath string
	gitlBinErr  error
)

// buildGitlBinary compiles ./cmd/gitl once and returns the binary path.
// `go build` runs with the inherited environment on purpose: it needs the
// real GOCACHE/GOMODCACHE to stay offline and fast, and it reads no gitl
// config — full isolation applies to the server process, not the compiler.
func buildGitlBinary(t *testing.T) string {
	t.Helper()
	gitlBinOnce.Do(func() {
		root, err := moduleRoot()
		if err != nil {
			gitlBinErr = err
			return
		}
		dir, err := os.MkdirTemp("", "gitl-mcp-e2e-")
		if err != nil {
			gitlBinErr = err
			return
		}
		gitlBinDir = dir
		bin := filepath.Join(dir, "gitl-e2e")
		if runtime.GOOS == "windows" {
			bin += ".exe"
		}
		cmd := exec.Command("go", "build", "-o", bin, "./cmd/gitl")
		cmd.Dir = root
		if out, err := cmd.CombinedOutput(); err != nil {
			gitlBinErr = fmt.Errorf("go build ./cmd/gitl: %v\n%s", err, out)
			return
		}
		gitlBinPath = bin
	})
	if gitlBinErr != nil {
		t.Fatalf("building gitl binary for e2e: %v", gitlBinErr)
	}
	return gitlBinPath
}

// moduleRoot walks up from the test working directory to the go.mod dir.
func moduleRoot() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fmt.Errorf("go.mod not found above test working directory")
		}
		dir = parent
	}
}

// TestMain exists only to remove the shared e2e binary after the package's
// tests finish (a lazily built, test-scoped artifact has no other cleanup
// hook). It must not do anything else.
func TestMain(m *testing.M) {
	code := m.Run()
	if gitlBinDir != "" {
		os.RemoveAll(gitlBinDir)
	}
	os.Exit(code)
}

// ---------------------------------------------------------------------------
// Client-side wire types (independent of package mcpserver — see file comment)

// rpcResponse is one JSON-RPC 2.0 response line as a real client sees it.
type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  json.RawMessage `json:"result"`
	Error   *rpcError       `json:"error"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// toolCallResult is the result object of a tools/call response.
type toolCallResult struct {
	Content []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"content"`
	IsError bool `json:"isError"`
}

// ---------------------------------------------------------------------------
// MCP client session over a real child process

// lockedBuffer is a goroutine-safe bytes.Buffer: os/exec copies the child's
// stderr into it from an internal goroutine while the test may read it.
type lockedBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *lockedBuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *lockedBuffer) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

// mcpSession drives one `gitl mcp` child process as an MCP client.
type mcpSession struct {
	t       *testing.T
	cmd     *exec.Cmd
	stdin   io.WriteCloser
	lines   chan string // stdout lines; closed on child EOF
	scanErr chan error  // at most one scanner error
	stderr  *lockedBuffer
	nextID  int
	waited  bool // closeAndWait already reaped the process
}

// startMCPSession builds the binary (once), launches `gitl mcp` with dir as
// its working directory and a fully synthetic environment, and returns the
// client side of the session.
func startMCPSession(t *testing.T, dir string) *mcpSession {
	t.Helper()
	bin := buildGitlBinary(t)

	home := t.TempDir()
	cmd := exec.Command(bin, "mcp")
	cmd.Dir = dir
	// Environment built from scratch — NOT os.Environ() — so no GITL_* var,
	// API key, or personal config from the host can reach the server. PATH is
	// kept so the server can find git; HOME/XDG point at empty temp dirs so
	// os.UserConfigDir()/os.UserCacheDir() resolve inside the sandbox.
	cmd.Env = []string{
		"PATH=" + os.Getenv("PATH"),
		"HOME=" + home,
		"XDG_CONFIG_HOME=" + filepath.Join(home, ".config"),
		"XDG_CACHE_HOME=" + filepath.Join(home, ".cache"),
		"GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_CONFIG_SYSTEM=/dev/null",
	}

	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatalf("stdin pipe: %v", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatalf("stdout pipe: %v", err)
	}
	stderr := &lockedBuffer{}
	cmd.Stderr = stderr

	if err := cmd.Start(); err != nil {
		t.Fatalf("start %s mcp: %v", bin, err)
	}

	s := &mcpSession{
		t:       t,
		cmd:     cmd,
		stdin:   stdin,
		lines:   make(chan string, 16),
		scanErr: make(chan error, 1),
		stderr:  stderr,
	}

	// Reader goroutine: the only consumer of the child's stdout. Feeding a
	// channel keeps every recv timeout-able (a blocking Read is not).
	go func() {
		defer close(s.lines)
		sc := bufio.NewScanner(stdout)
		sc.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
		for sc.Scan() {
			s.lines <- sc.Text()
		}
		if err := sc.Err(); err != nil {
			s.scanErr <- err
		}
	}()

	t.Cleanup(func() {
		if s.waited {
			return
		}
		_ = s.stdin.Close()
		done := make(chan struct{})
		go func() {
			_ = s.cmd.Wait()
			close(done)
		}()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			_ = s.cmd.Process.Kill()
			<-done
		}
	})
	return s
}

// sendLine writes one raw line to the server's stdin.
func (s *mcpSession) sendLine(line string) {
	s.t.Helper()
	if _, err := io.WriteString(s.stdin, line+"\n"); err != nil {
		s.t.Fatalf("write to gitl mcp stdin: %v (stderr:\n%s)", err, s.stderr.String())
	}
}

// recvLine returns the next stdout line, or ok=false on child EOF. It fails
// the test on a scanner error or after e2eTimeout of silence.
func (s *mcpSession) recvLine() (string, bool) {
	s.t.Helper()
	select {
	case line, ok := <-s.lines:
		if !ok {
			select {
			case err := <-s.scanErr:
				s.t.Fatalf("reading gitl mcp stdout: %v", err)
			default:
			}
			return "", false
		}
		return line, true
	case <-time.After(e2eTimeout):
		s.t.Fatalf("timed out after %v waiting for a server response (stderr:\n%s)", e2eTimeout, s.stderr.String())
		return "", false
	}
}

// call sends one request and returns its decoded response, asserting the
// JSON-RPC envelope (version, echoed id) on the way.
func (s *mcpSession) call(method string, params any) rpcResponse {
	s.t.Helper()
	s.nextID++
	req := map[string]any{"jsonrpc": "2.0", "id": s.nextID, "method": method}
	if params != nil {
		req["params"] = params
	}
	b, err := json.Marshal(req)
	if err != nil {
		s.t.Fatalf("marshal request: %v", err)
	}
	s.sendLine(string(b))

	line, ok := s.recvLine()
	if !ok {
		s.t.Fatalf("server closed stdout while a %s response was pending (stderr:\n%s)", method, s.stderr.String())
	}
	var resp rpcResponse
	if err := json.Unmarshal([]byte(line), &resp); err != nil {
		s.t.Fatalf("stdout line is not valid JSON-RPC (%v) — non-protocol output on the protocol channel?\n%s", err, line)
	}
	if resp.JSONRPC != "2.0" {
		s.t.Fatalf("response jsonrpc = %q, want \"2.0\":\n%s", resp.JSONRPC, line)
	}
	if want := fmt.Sprintf("%d", s.nextID); string(resp.ID) != want {
		s.t.Fatalf("response id = %s, want %s:\n%s", resp.ID, want, line)
	}
	return resp
}

// notify sends a JSON-RPC notification (no id, no response expected).
func (s *mcpSession) notify(method string) {
	s.t.Helper()
	s.sendLine(fmt.Sprintf(`{"jsonrpc":"2.0","method":%q}`, method))
}

// handshake performs the client side of the MCP initialize sequence the way a
// real host does before any tools/* call.
func (s *mcpSession) handshake() {
	s.t.Helper()
	resp := s.call("initialize", map[string]any{
		"protocolVersion": "2025-06-18",
		"capabilities":    map[string]any{},
		"clientInfo":      map[string]any{"name": "gitl-e2e-test", "version": "0"},
	})
	if resp.Error != nil {
		s.t.Fatalf("initialize failed: %d %s", resp.Error.Code, resp.Error.Message)
	}
	s.notify("notifications/initialized")
}

// callTool performs tools/call and returns the decoded tool result, failing
// on any protocol-level (JSON-RPC) error.
func (s *mcpSession) callTool(name string, args any) toolCallResult {
	s.t.Helper()
	resp := s.call("tools/call", map[string]any{"name": name, "arguments": args})
	if resp.Error != nil {
		s.t.Fatalf("tools/call %s: unexpected protocol error %d: %s", name, resp.Error.Code, resp.Error.Message)
	}
	var res toolCallResult
	if err := json.Unmarshal(resp.Result, &res); err != nil {
		s.t.Fatalf("tools/call %s: unmarshal result: %v\n%s", name, err, resp.Result)
	}
	return res
}

// toolResultJSON asserts res is a successful single-text-block result whose
// text is valid JSON, and returns it decoded.
func (s *mcpSession) toolResultJSON(res toolCallResult) map[string]any {
	s.t.Helper()
	if res.IsError {
		s.t.Fatalf("unexpected isError tool result: %+v", res)
	}
	if len(res.Content) != 1 || res.Content[0].Type != "text" {
		s.t.Fatalf("expected exactly one text content block, got %+v", res.Content)
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(res.Content[0].Text), &m); err != nil {
		s.t.Fatalf("tool result text is not valid JSON: %v\n%s", err, res.Content[0].Text)
	}
	return m
}

// closeAndWait closes stdin (client hang-up), asserts the server writes
// nothing further, exits cleanly (code 0) within the timeout, and returns the
// captured stderr.
func (s *mcpSession) closeAndWait() string {
	s.t.Helper()
	if err := s.stdin.Close(); err != nil {
		s.t.Fatalf("close stdin: %v", err)
	}

	// Drain: anything after client EOF is a protocol violation; the channel
	// must close (child closed stdout by exiting).
	for {
		select {
		case line, ok := <-s.lines:
			if !ok {
				goto drained
			}
			s.t.Errorf("unexpected server output after client EOF: %s", line)
		case <-time.After(e2eTimeout):
			s.t.Fatalf("server stdout still open %v after client EOF (stderr:\n%s)", e2eTimeout, s.stderr.String())
		}
	}
drained:

	s.waited = true
	waitErr := make(chan error, 1)
	go func() { waitErr <- s.cmd.Wait() }()
	select {
	case err := <-waitErr:
		if err != nil {
			s.t.Errorf("gitl mcp exited with error on client EOF: %v (stderr:\n%s)", err, s.stderr.String())
		}
	case <-time.After(e2eTimeout):
		_ = s.cmd.Process.Kill()
		s.t.Fatalf("gitl mcp did not exit within %v of client EOF", e2eTimeout)
	}
	return s.stderr.String()
}

// riskLevel extracts risk.level from a decoded review artifact, asserting it
// is one of the documented values.
func riskLevel(t *testing.T, review map[string]any) string {
	t.Helper()
	risk, _ := review["risk"].(map[string]any)
	level, _ := risk["level"].(string)
	switch level {
	case "low", "medium", "high":
	default:
		t.Errorf("risk.level = %v, want low|medium|high", risk["level"])
	}
	return level
}

// ---------------------------------------------------------------------------
// Scenarios

func TestMCPE2EInitialize(t *testing.T) {
	// A plain temp dir, not a git repo: the server must start and handshake
	// regardless of where the host launched it.
	s := startMCPSession(t, t.TempDir())

	resp := s.call("initialize", map[string]any{
		"protocolVersion": "2025-06-18",
		"capabilities":    map[string]any{},
		"clientInfo":      map[string]any{"name": "gitl-e2e-test", "version": "0"},
	})
	if resp.Error != nil {
		t.Fatalf("initialize failed: %d %s", resp.Error.Code, resp.Error.Message)
	}

	var res struct {
		ProtocolVersion string `json:"protocolVersion"`
		Capabilities    struct {
			Tools *struct {
				ListChanged bool `json:"listChanged"`
			} `json:"tools"`
		} `json:"capabilities"`
		ServerInfo struct {
			Name    string `json:"name"`
			Version string `json:"version"`
		} `json:"serverInfo"`
	}
	if err := json.Unmarshal(resp.Result, &res); err != nil {
		t.Fatalf("unmarshal InitializeResult: %v\n%s", err, resp.Result)
	}
	if res.ProtocolVersion != "2025-06-18" {
		t.Errorf("protocolVersion = %q, want %q", res.ProtocolVersion, "2025-06-18")
	}
	if res.ServerInfo.Name != "gitl" {
		t.Errorf("serverInfo.name = %q, want %q", res.ServerInfo.Name, "gitl")
	}
	if res.ServerInfo.Version == "" {
		t.Error("serverInfo.version is empty")
	}
	if res.Capabilities.Tools == nil {
		t.Error("capabilities.tools missing — server must advertise the tools capability")
	} else if res.Capabilities.Tools.ListChanged {
		t.Error("capabilities.tools.listChanged = true, want false (static toolset)")
	}

	s.closeAndWait()
}

func TestMCPE2EToolsList(t *testing.T) {
	s := startMCPSession(t, t.TempDir())
	s.handshake()

	resp := s.call("tools/list", nil)
	if resp.Error != nil {
		t.Fatalf("tools/list failed: %d %s", resp.Error.Code, resp.Error.Message)
	}
	var res struct {
		Tools []struct {
			Name        string          `json:"name"`
			Description string          `json:"description"`
			InputSchema json.RawMessage `json:"inputSchema"`
		} `json:"tools"`
	}
	if err := json.Unmarshal(resp.Result, &res); err != nil {
		t.Fatalf("unmarshal ToolsListResult: %v\n%s", err, resp.Result)
	}
	if len(res.Tools) != 2 {
		t.Fatalf("tools/list returned %d tools, want 2: %+v", len(res.Tools), res.Tools)
	}
	wantNames := []string{"gitl_review", "gitl_digest"}
	for i, tool := range res.Tools {
		if tool.Name != wantNames[i] {
			t.Errorf("tools[%d].name = %q, want %q", i, tool.Name, wantNames[i])
		}
		if tool.Description == "" {
			t.Errorf("tools[%d] (%s) has an empty description", i, tool.Name)
		}
		var schema struct {
			Type       string                     `json:"type"`
			Properties map[string]json.RawMessage `json:"properties"`
		}
		if err := json.Unmarshal(tool.InputSchema, &schema); err != nil {
			t.Errorf("tools[%d] (%s) inputSchema is not valid JSON: %v", i, tool.Name, err)
			continue
		}
		if schema.Type != "object" || len(schema.Properties) == 0 {
			t.Errorf("tools[%d] (%s) inputSchema = %s, want a non-empty object schema", i, tool.Name, tool.InputSchema)
		}
	}

	s.closeAndWait()
}

func TestMCPE2EReviewOfflineRange(t *testing.T) {
	dir := setupRepo(t, false)
	s := startMCPSession(t, dir)
	s.handshake()

	m := s.toolResultJSON(s.callTool("gitl_review", map[string]any{"range": "HEAD~1..HEAD"}))

	if m["schema_version"] != float64(1) {
		t.Errorf("schema_version = %v, want 1", m["schema_version"])
	}
	if m["range"] != "HEAD~1..HEAD" {
		t.Errorf("range = %v, want HEAD~1..HEAD", m["range"])
	}
	if m["offline"] != true {
		t.Error("offline = false, want true (no API key in the server environment)")
	}
	riskLevel(t, m)
	risk, _ := m["risk"].(map[string]any)
	if risk["heuristic"] != true {
		t.Errorf("risk.heuristic = %v, want true (offline heuristic scoring)", risk["heuristic"])
	}
	if body, _ := m["review_markdown"].(string); body == "" {
		t.Error("review_markdown is empty, want the offline review body")
	}

	stderr := s.closeAndWait()
	// The offline notice must arrive on stderr; stdout purity is enforced by
	// recvLine parsing every protocol line as JSON.
	if !strings.Contains(stderr, offlineNotice) {
		t.Errorf("stderr should carry the offline-mode notice %q, got:\n%s", offlineNotice, stderr)
	}
}

func TestMCPE2EDigestOfflineDefault(t *testing.T) {
	dir := setupRepo(t, false)
	s := startMCPSession(t, dir)
	s.handshake()

	m := s.toolResultJSON(s.callTool("gitl_digest", map[string]any{}))

	if m["days"] != float64(defaultDigestDays) {
		t.Errorf("days = %v, want default %d", m["days"], defaultDigestDays)
	}
	repos, _ := m["repos"].([]any)
	if len(repos) != 1 {
		t.Fatalf("repos length = %d, want 1 (server cwd only)", len(repos))
	}
	repo, _ := repos[0].(map[string]any)
	if repo["path"] != "." {
		t.Errorf("repos[0].path = %v, want %q (the server's working directory)", repo["path"], ".")
	}
	if repo["ok"] != true {
		t.Fatalf("repos[0].ok = %v, want true (error: %v)", repo["ok"], repo["error"])
	}
	stats, _ := repo["stats"].(map[string]any)
	if stats["commits"] != float64(2) {
		t.Errorf("stats.commits = %v, want 2 (setupRepo makes two commits)", stats["commits"])
	}
	byAuthor, _ := repo["by_author"].([]any)
	if len(byAuthor) != 1 {
		t.Fatalf("by_author length = %d, want 1", len(byAuthor))
	}
	author, _ := byAuthor[0].(map[string]any)
	if author["author"] != "Test Author" || author["commits"] != float64(2) {
		t.Errorf("by_author[0] = %v, want Test Author with 2 commits", author)
	}
	overall, _ := m["overall"].(map[string]any)
	if overall["repos_ok"] != float64(1) || overall["combined_commits"] != float64(2) {
		t.Errorf("overall = %v, want repos_ok=1 combined_commits=2", overall)
	}

	s.closeAndWait()
}

// TestMCPE2EReviewInvalidArgs: invalid tool arguments must come back as a
// TOOL EXECUTION error (isError:true with readable text) — not a JSON-RPC
// error and not a server crash. All cases run over one session, proving the
// server keeps serving after each rejected call.
func TestMCPE2EReviewInvalidArgs(t *testing.T) {
	dir := setupRepo(t, false)
	s := startMCPSession(t, dir)
	s.handshake()

	tests := []struct {
		name     string
		args     map[string]any
		wantText string
	}{
		{"no mode selected", map[string]any{}, "provide a revision range"},
		{"range and staged", map[string]any{"range": "HEAD~1..HEAD", "staged": true}, "cannot combine"},
		{"range and pr", map[string]any{"range": "HEAD~1..HEAD", "pr": 1}, "mutually exclusive"},
		{"unknown argument", map[string]any{"revrange": "HEAD~1..HEAD"}, "invalid tool arguments"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			res := s.callTool("gitl_review", tt.args)
			if !res.IsError {
				t.Fatalf("expected isError:true, got %+v", res)
			}
			if len(res.Content) != 1 || res.Content[0].Type != "text" {
				t.Fatalf("error result should carry one text block, got %+v", res.Content)
			}
			if !strings.Contains(res.Content[0].Text, tt.wantText) {
				t.Errorf("error text %q should contain %q", res.Content[0].Text, tt.wantText)
			}
		})
	}

	s.closeAndWait()
}

// TestMCPE2EUnknownTool: calling a tool that is not registered is a
// protocol-level failure — JSON-RPC error -32602 (invalid params, per
// mcpserver.CodeInvalidParams) — and must not kill the session.
func TestMCPE2EUnknownTool(t *testing.T) {
	s := startMCPSession(t, t.TempDir())
	s.handshake()

	resp := s.call("tools/call", map[string]any{"name": "does_not_exist", "arguments": map[string]any{}})
	if resp.Error == nil {
		t.Fatalf("expected a JSON-RPC error for an unknown tool, got result: %s", resp.Result)
	}
	if resp.Error.Code != -32602 {
		t.Errorf("error code = %d, want -32602", resp.Error.Code)
	}
	if !strings.Contains(resp.Error.Message, "unknown tool: does_not_exist") {
		t.Errorf("error message = %q, want it to name the unknown tool", resp.Error.Message)
	}

	// The server survives the protocol error and keeps serving.
	if resp := s.call("tools/list", nil); resp.Error != nil {
		t.Errorf("tools/list after unknown-tool error failed: %d %s", resp.Error.Code, resp.Error.Message)
	}

	s.closeAndWait()
}

// TestMCPE2EFullSession drives a realistic multi-call session over one
// process: initialize → initialized → tools/list → review → digest → review
// again. The repeated review must succeed with an identical risk verdict
// (the offline heuristic is deterministic), proving no state from earlier
// calls corrupts later ones and the server stays healthy throughout.
func TestMCPE2EFullSession(t *testing.T) {
	dir := setupRepo(t, false)
	s := startMCPSession(t, dir)
	s.handshake()

	if resp := s.call("tools/list", nil); resp.Error != nil {
		t.Fatalf("tools/list failed: %d %s", resp.Error.Code, resp.Error.Message)
	}

	first := s.toolResultJSON(s.callTool("gitl_review", map[string]any{"range": "HEAD~1..HEAD"}))
	firstLevel := riskLevel(t, first)

	digest := s.toolResultJSON(s.callTool("gitl_digest", map[string]any{"days": 3}))
	if digest["days"] != float64(3) {
		t.Errorf("digest days = %v, want 3", digest["days"])
	}
	overall, _ := digest["overall"].(map[string]any)
	if overall["combined_commits"] != float64(2) {
		t.Errorf("digest overall.combined_commits = %v, want 2", overall["combined_commits"])
	}

	second := s.toolResultJSON(s.callTool("gitl_review", map[string]any{"range": "HEAD~1..HEAD"}))
	secondLevel := riskLevel(t, second)
	if secondLevel != firstLevel {
		t.Errorf("repeated review risk.level = %q, want %q (deterministic offline heuristic)", secondLevel, firstLevel)
	}
	if second["review_markdown"] != first["review_markdown"] {
		t.Error("repeated review produced a different review_markdown — offline review is not deterministic across calls in one session")
	}

	stderr := s.closeAndWait()
	if !strings.Contains(stderr, offlineNotice) {
		t.Errorf("stderr should carry the offline-mode notice %q, got:\n%s", offlineNotice, stderr)
	}
}
